package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/service"
)

func TestParameterTranslationChannelFlow(t *testing.T) {
	const marker = "PARAMETER_TRANSLATION_FLOW_CHILD"
	if os.Getenv(marker) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestParameterTranslationChannelFlow$", "-test.timeout=40s")
		cmd.Env = append(os.Environ(), marker+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated custom flow: %v\n%s", err, output)
		}
		return
	}
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: ":memory:", AILogDir: t.TempDir()}
	blockProtocolNetwork(t)
	db, err := repository.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := db.DB()
	connection.SetMaxOpenConns(1)
	defer connection.Close()
	user := model.User{ID: "custom-user", Username: "custom-user", Role: model.UserRoleAdmin, Status: model.UserStatusActive, Credits: 100}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"image", "audio", "video"} {
		t.Run(kind, func(t *testing.T) {
			source := `{models:{"custom-model":{POST:{url:"/custom",body:{text:prompt,model}},result:"output.url"}}}`
			if kind == "video" {
				source = `{poll:{taskId:"id",GET:{url:"/custom/{taskId}"},status:"state",success:["done"],failure:["failed"]},models:{"custom-model":{POST:{url:"/custom",body:{text:prompt,model}},result:"output.url"}}}`
			}
			channel := model.ModelChannel{ID: "custom-channel", Name: "custom", Protocol: "apimart", BaseURL: "https://upstream.invalid/v1", APIKey: "secret-key", Models: []string{"custom-model"}, Enabled: true, Weight: 1, ParameterTranslation: source}
			if _, err := repository.SaveSettings(model.Settings{Private: model.PrivateSetting{Channels: []model.ModelChannel{channel}}, Public: model.PublicSetting{ModelChannel: model.PublicModelChannelSetting{ModelCosts: []model.ModelCost{{Model: "custom-model", Credits: 2}}}}}, "fixture"); err != nil {
				t.Fatal(err)
			}
			public, err := service.PublicSettings()
			encoded, _ := json.Marshal(public)
			if err != nil || strings.Contains(string(encoded), source) || strings.Contains(string(encoded), channel.APIKey) || len(public.ModelChannel.Channels[0].ParameterTranslationModels) != 1 {
				t.Fatalf("public metadata leaked or missing: %s %v", encoded, err)
			}
			calls := 0
			protocolMockHTTP(t, func(request *http.Request) (*http.Response, error) {
				calls++
				path := "/v1/custom"
				payload := `{"output":{"url":"https://media.invalid/` + kind + `"}}`
				if kind == "video" {
					if calls == 1 {
						payload = `{"id":"job"}`
					} else {
						path += "/job"
						payload = `{"state":"done","output":{"url":"https://media.invalid/video"}}`
					}
				}
				if request.URL.Path != path || request.Header.Get("Authorization") != "Bearer secret-key" {
					t.Fatalf("custom route/auth: %s %v", request.URL, request.Header)
				}
				if calls == 1 {
					body, _ := io.ReadAll(request.Body)
					assertProtocolJSONValue(t, json.RawMessage(body), `{"model":"custom-model","text":"hello"}`)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
			})
			endpoint := map[string]string{"image": "/images/generations", "audio": "/audio/speech", "video": "/videos"}[kind]
			body := `{"model":"custom-model","seconds":5,"_parameterTranslation":{"kind":"` + kind + `","variables":{"prompt":"hello","seconds":5}}}`
			request := httptest.NewRequest("POST", endpoint, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Model-Channel-ID", channel.ID)
			request = request.WithContext(service.WithUser(request.Context(), model.PublicUser(user)))
			writer := httptest.NewRecorder()
			if kind == "video" {
				AIVideos(writer, request)
			} else {
				proxyAIRequest(writer, request, endpoint)
			}
			if writer.Code != 200 || strings.Contains(writer.Body.String(), `"code":1`) {
				t.Fatalf("response=%s", writer.Body)
			}
			if kind == "video" {
				var envelope struct {
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				_ = json.Unmarshal(writer.Body.Bytes(), &envelope)
				task, found, err := repository.GetVideoTask(envelope.Data.ID)
				if err != nil || !found || task.ParameterTranslationSnapshot == "" || task.Credits != 10 || task.Seconds != "5" {
					t.Fatalf("snapshot missing: %v", err)
				}
				channel.ParameterTranslation = ""
				_, _ = repository.SaveSettings(model.Settings{Private: model.PrivateSetting{Channels: []model.ModelChannel{channel}}}, "changed")
				update, err := pollVideoTaskFromUpstream(context.Background(), task)
				if err != nil || update.VideoURL != "https://media.invalid/video" {
					t.Fatalf("snapshot poll=%+v err=%v", update, err)
				}
				if encoded, _ := json.Marshal(service.VideoTaskResponse(task)); strings.Contains(string(encoded), "source") && strings.Contains(string(encoded), "models") {
					t.Fatalf("private snapshot leaked: %s", encoded)
				}
				failed := service.VideoTaskPollUpdate{Status: "failed", Error: "provider rejected"}
				if err := service.UpdateVideoTaskFromPoll(task, failed); err != nil {
					t.Fatal(err)
				}
				if err := service.UpdateVideoTaskFromPoll(task, failed); err != nil {
					t.Fatal(err)
				}
				var refunds int64
				if err := db.Model(&model.CreditLog{}).Where("type = ?", model.CreditLogTypeAIRefund).Count(&refunds).Error; err != nil || refunds != 1 {
					t.Fatalf("refunds=%d err=%v", refunds, err)
				}
			} else if !strings.Contains(writer.Body.String(), "https://media.invalid/"+kind) {
				t.Fatalf("media result=%s", writer.Body)
			}
			if calls != map[string]int{"image": 1, "audio": 1, "video": 2}[kind] {
				t.Fatalf("unexpected resubmission: %d", calls)
			}
		})
	}
	t.Run("personal-channel", func(t *testing.T) {
		ctx := service.WithUser(context.Background(), model.PublicUser(user))
		raw := json.RawMessage(`{"localChannels":[{"id":"personal","protocol":"openai","baseUrl":"https://upstream.invalid/v1","apiKey":"personal-key","models":["personal-model"]}],"channelTranslations":[{"channelId":"personal","parameterTranslation":"{models:{'personal-model':{POST:{url:'/personal',body:{prompt}},result:'url'}}}"}]}`)
		if _, err := service.SaveCurrentUserModelConfig(ctx, raw); err != nil {
			t.Fatal(err)
		}
		calls := 0
		protocolMockHTTP(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path != "/v1/personal" || r.Header.Get("Authorization") != "Bearer personal-key" {
				t.Fatalf("personal route/auth: %s %v", r.URL, r.Header)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"url":"https://media.invalid/personal.png"}`))}, nil
		})
		r := httptest.NewRequest("POST", "/images/generations", strings.NewReader(`{"model":"personal-model","_parameterTranslation":{"kind":"image","variables":{"prompt":"personal prompt"}}}`)).WithContext(ctx)
		r.Header.Set("X-User-Model-Channel-ID", "personal")
		w := httptest.NewRecorder()
		proxyAIRequest(w, r, "/images/generations")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "https://media.invalid/personal.png") || calls != 1 {
			t.Fatalf("personal response %s calls=%d", w.Body, calls)
		}
		if _, err := service.SaveCurrentUserModelConfig(ctx, json.RawMessage(`{"localChannels":[{"id":"personal","protocol":"openai","baseUrl":"https://upstream.invalid/v1","apiKey":"personal-key","models":["personal-model"]}]}`)); err != nil {
			t.Fatal(err)
		}
		preserved, err := service.SelectUserLocalModelChannelForModel(user.ID, "personal-model", "personal")
		if err != nil || preserved.ParameterTranslation == "" {
			t.Fatalf("omitted source lost: %v", err)
		}
		if _, err := service.SaveCurrentUserModelConfig(ctx, json.RawMessage(`{"localChannels":[{"id":"personal","protocol":"openai","baseUrl":"https://upstream.invalid/v1","apiKey":"personal-key","models":["personal-model"]}],"channelTranslations":[]}`)); err != nil {
			t.Fatal(err)
		}
		channel, err := service.SelectUserLocalModelChannelForModel(user.ID, "personal-model", "personal")
		if err != nil || channel.ParameterTranslation != "" {
			t.Fatalf("personal clear failed: %v %s", err, channel.ParameterTranslation)
		}
	})
}
