package service

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/model"
)

func TestParameterTranslationStaticMatch(t *testing.T) {
	source := `{ models: { native: {}, image: { POST: { url: "/generate", body: { prompt } }, result: r => r.url }, video: { POST: { url: (() => { throw new Error("unused"); })() } } } }`
	names, err := ParameterTranslationModels(source)
	if err != nil || !reflect.DeepEqual(names, []string{"image", "video"}) {
		t.Fatalf("names=%v err=%v", names, err)
	}
	channel := model.ModelChannel{BaseURL: "https://example.test/v1", ParameterTranslation: source}
	for _, name := range []string{"native", "unmatched"} {
		translation, err := NewParameterTranslation(channel, name, nil)
		if err != nil || translation != nil {
			t.Fatalf("default %s: %v %v", name, translation, err)
		}
	}
	translation, err := NewParameterTranslation(channel, "image", map[string]any{"prompt": "hello"})
	if err != nil || translation == nil {
		t.Fatalf("selected model evaluated unrelated rule: %v", err)
	}
	for _, source := range []string{`{models:{a:{POST:{},GET:{}}}}`, `{models:{[model]:{POST:{}}}}`, `{models:{a:{POST:{}},a:{POST:{}}}}`} {
		if _, err := ParameterTranslationModels(source); err == nil {
			t.Fatalf("accepted invalid source %s", source)
		}
	}
}

func TestParameterTranslationWireFormats(t *testing.T) {
	for _, format := range []string{"json", "urlencoded", "formData", "raw"} {
		t.Run(format, func(t *testing.T) {
			body := `{text:prompt,flag:false,count:0,items:["a","b"],missing:resolution}`
			if format == "raw" {
				body = `prompt`
			}
			translation, err := NewParameterTranslation(model.ModelChannel{BaseURL: "https://example.test/v1?fixed=base", APIKey: "key with & space", ParameterTranslation: `{models:{sample:{POST:{url:"/generate?items=old",format:"` + format + `",params:{items:["a","b"]},body:` + body + `},result:"url"}}}`}, "sample", map[string]any{"prompt": "a&b"})
			if err != nil {
				t.Fatal(err)
			}
			request, _, err := translation.request(context.Background(), false, "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(request.URL.Query()["items"], []string{"a", "b"}) || request.URL.Query().Get("fixed") != "base" {
				t.Fatalf("query=%s", request.URL)
			}
			payload, _ := io.ReadAll(request.Body)
			switch format {
			case "raw":
				if string(payload) != "a&b" {
					t.Fatalf("raw=%s", payload)
				}
			case "json":
				var fields map[string]any
				_ = json.Unmarshal(payload, &fields)
				if fields["flag"] != false || fields["count"] != float64(0) || fields["text"] != "a&b" {
					t.Fatalf("JSON=%s", payload)
				}
			case "urlencoded", "formData":
				values := url.Values{}
				if format == "urlencoded" {
					values, _ = url.ParseQuery(string(payload))
				} else {
					_, params, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
					form, err := multipart.NewReader(strings.NewReader(string(payload)), params["boundary"]).ReadForm(1024)
					if err != nil {
						t.Fatal(err)
					}
					defer form.RemoveAll()
					values = form.Value
				}
				if values.Get("text") != "a&b" || values.Get("flag") != "false" || values.Get("count") != "0" || len(values["items"]) != 2 || values.Has("missing") {
					t.Fatalf("form=%v", values)
				}
			}
		})
	}
	for _, responseType := range []string{"json", "text", "blob", "arraybuffer"} {
		t.Run(responseType, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if responseType == "json" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"url":"https://example.test/media"}`)
				} else if responseType == "text" {
					_, _ = io.WriteString(w, "https://example.test/media")
				} else {
					w.Header().Set("Content-Type", "audio/wav")
					_, _ = io.WriteString(w, "fixture audio")
				}
			}))
			defer upstream.Close()
			result := `result:"url",`
			if responseType != "json" {
				result = ""
			}
			translation, err := NewParameterTranslation(model.ModelChannel{BaseURL: upstream.URL, ParameterTranslation: `{models:{sample:{` + result + `POST:{url:"/media",responseType:"` + responseType + `"}}}}`}, "sample", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := translation.Send(context.Background(), "audio", false, "")
			if err != nil || response.Status != "completed" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			if responseType == "blob" || responseType == "arraybuffer" {
				if string(response.Body) != "fixture audio" || response.ContentType != "audio/wav" {
					t.Fatalf("binary=%+v", response)
				}
			} else if len(response.URLs) != 1 {
				t.Fatalf("URLs=%v", response.URLs)
			}
		})
	}
}

func TestParameterTranslationExpressionTimeout(t *testing.T) {
	_, err := NewParameterTranslation(model.ModelChannel{BaseURL: "https://example.test", ParameterTranslation: `{models:{sample:{POST:{url:"/generate",body:(()=>{while(true){}})()}}}}`}, "sample", nil)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expression did not time out: %v", err)
	}
}

func TestParameterTranslationEmptyBinaryFails(t *testing.T) {
	if urls := ParameterTranslationMediaURLs("", "image", "image/png", 0); len(urls) != 0 {
		t.Fatalf("empty media URL accepted: %v", urls)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	translation, err := NewParameterTranslation(model.ModelChannel{BaseURL: upstream.URL, ParameterTranslation: `{models:{sample:{POST:{url:"/audio",responseType:"blob"}}}}`}, "sample", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := translation.Send(context.Background(), "audio", false, ""); err == nil {
		t.Fatal("empty binary response was accepted")
	}
}

func TestParameterTranslationRequestAndSelectors(t *testing.T) {
	channel := model.ModelChannel{BaseURL: "https://example.test/v1", APIKey: `key"&/?`, ParameterTranslation: `{
		headers: { Authorization: "Bearer {channelKey}" },
		models: { sample: { POST: { url: "/generate?fixed=1", headers: { "X-API-Key": "{channelKey}" }, params: { q: "a&b" }, body: {
			model, prompt, missing: resolution, enabled: false, count: 0, empty: [], value: null,
			...(firstFrame || lastFrame ? { frames: [{url:firstFrame,role:"first"},{url:lastFrame,role:"last"}].filter(f => f.url) } : { images, videos, audios })
		} }, result: r => r.items.map(item => item.url) } }
	}`}
	translation, err := NewParameterTranslation(channel, "sample", map[string]any{"prompt": "hello", "firstFrame": "https://example.test/frame.png", "images": []string{"https://example.test/ref.png"}})
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := translation.request(context.Background(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.Path != "/v1/generate" || request.URL.Query().Get("q") != "a&b" || request.URL.Query().Get("fixed") != "1" {
		t.Fatalf("URL=%s", request.URL)
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("X-API-Key") != channel.APIKey {
		t.Fatalf("auth=%v", request.Header)
	}
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["missing"]; ok {
		t.Fatal("undefined was sent")
	}
	if _, ok := body["images"]; ok {
		t.Fatal("ordinary references leaked into frame request")
	}
	if body["enabled"] != false || body["count"] != float64(0) || len(body["empty"].([]any)) != 0 {
		t.Fatalf("falsy values lost: %v", body)
	}
	if value, present := body["value"]; !present || value != nil {
		t.Fatal("null was omitted")
	}
	value, err := translation.selectValue(parameterTranslationValue(translation.rule, "result"), map[string]any{"items": []any{map[string]any{"url": "https://example.test/result.png"}}})
	if err != nil || !reflect.DeepEqual(value, []any{"https://example.test/result.png"}) {
		t.Fatalf("selector=%v err=%v", value, err)
	}
}

func TestParameterTranslationPollingAllTasks(t *testing.T) {
	queries := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			_, _ = io.WriteString(w, `{"data":[{"id":"a"},{"id":"b"},{"id":"a"}]}`)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		queries = append(queries, id)
		_, _ = io.WriteString(w, `{"state":2,"progress":100,"output":{"url":"https://example.test/`+id+`.png"}}`)
	}))
	defer upstream.Close()
	channel := model.ModelChannel{BaseURL: upstream.URL + "/v1", ParameterTranslation: `{
		poll: { taskId: r => r.data.map(item => item.id), GET: {url:"/tasks/{taskId}"}, status:"state", success:[2], failure:[3], progress:"progress" },
		models: { sample: { POST:{url:"/generate",body:{model,prompt}},result:"output.url" } }
	}`}
	translation, err := NewParameterTranslation(channel, "sample", map[string]any{"prompt": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := translation.Execute(context.Background(), "image", nil)
	if err != nil || result.Status != "completed" || !reflect.DeepEqual(queries, []string{"a", "b"}) || len(result.URLs) != 2 {
		t.Fatalf("result=%+v queries=%v err=%v", result, queries, err)
	}
}

func TestParameterTranslationFailureNeverResubmits(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"message":"bad input"}}`)
	}))
	defer upstream.Close()
	translation, err := NewParameterTranslation(model.ModelChannel{BaseURL: upstream.URL, ParameterTranslation: `{models:{sample:{POST:{url:"/generate"},result:"url"}}}`}, "sample", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = translation.Execute(context.Background(), "image", nil)
	if err == nil || !strings.Contains(err.Error(), "bad input") || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
