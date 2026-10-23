package desktopui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/chaptermedia"
)

func generationMP3() []byte {
	data := make([]byte, 417)
	copy(data, []byte{0xff, 0xfb, 0x90, 0x00})
	return data
}
func setGenerationConnection(t *testing.T, s *Server, kind, base string) {
	t.Helper()
	draft := mediaConnectionDraft(kind)
	draft["baseURL"] = base + "/v1"
	draft["model"] = kind + "-model"
	if kind == "audio" {
		draft["voice"] = "nova"
	}
	decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", draft))
}
func createGeneration(t *testing.T, s *Server, kind, source string) chapterGenerationView {
	t.Helper()
	w := mediaConfigRequest(t, s, "POST", "/api/chapters/1/media/generate", map[string]any{"directory": s.dir, "kind": kind, "source": source, "instructions": "保持正文细节"})
	var g chapterGenerationView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &g) != nil {
		t.Fatalf("create generation: %d %s", w.Code, w.Body.String())
	}
	return g
}
func awaitGeneration(t *testing.T, s *Server, id string) chapterGenerationView {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		w := mediaConfigRequest(t, s, "GET", "/api/chapters/1/media/generate?directory="+url.QueryEscape(s.dir), nil)
		var view struct {
			Jobs []chapterGenerationView `json:"jobs"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		for _, g := range view.Jobs {
			if g.ID == id && g.Status != "running" {
				return g
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("generation did not finish")
	return chapterGenerationView{}
}
func TestChapterGenerationAllKindsUseSelectedModelsAndSavedBody(t *testing.T) {
	s := newTestServer(t, false)
	st := seedChapterEditor(t, s)
	body := strings.Repeat("完整正文不可截断。", 1000)
	if err := st.Drafts.SaveFinalChapter(1, body); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var spoken []string
	var models []string
	var videoPolls int
	png := chapterMediaPNG(t)
	mp3 := generationMP3()
	video := []byte("\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isommp42")
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer media-secret-never-in-response" {
			t.Error("wrong provider credential")
		}
		switch r.URL.Path {
		case "/v1/images/generations":
			var value struct {
				Model, Prompt string
				N             int
			}
			json.NewDecoder(r.Body).Decode(&value)
			if value.Model != "image-model" || !strings.Contains(value.Prompt, body) || value.N != 1 {
				t.Errorf("image input mismatch: %+v", value)
			}
			mu.Lock()
			models = append(models, value.Model)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png)}}})
		case "/v1/audio/speech":
			var value struct {
				Model, Input, Voice string
				Format              string `json:"response_format"`
			}
			json.NewDecoder(r.Body).Decode(&value)
			if value.Model != "audio-model" || value.Voice != "nova" || value.Format != "mp3" || len([]rune(value.Input)) > 4096 {
				t.Errorf("speech input mismatch: %+v", value)
			}
			mu.Lock()
			spoken = append(spoken, value.Input)
			models = append(models, value.Model)
			mu.Unlock()
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write(mp3)
		case "/v1/videos":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("model") != "video-model" || !strings.Contains(r.FormValue("prompt"), body) || r.FormValue("seconds") != "4" {
				t.Error("video input mismatch")
			}
			mu.Lock()
			models = append(models, "video-model")
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": "video_123", "status": "queued"})
		case "/v1/videos/video_123":
			mu.Lock()
			videoPolls++
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": "video_123", "status": "completed", "progress": 100})
		case "/v1/videos/video_123/content":
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(video)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()
	for _, kind := range []string{"image", "audio", "video"} {
		setGenerationConnection(t, s, kind, mock.URL)
	}
	for _, kind := range []string{"image", "audio", "video"} {
		g := createGeneration(t, s, kind, "final")
		g = awaitGeneration(t, s, g.ID)
		if g.Status != "completed" || g.SourceSHA256 != generationHash(body) || g.Model != kind+"-model" || g.URL == "" {
			t.Fatalf("%s result: %+v", kind, g)
		}
		w := mediaConfigRequest(t, s, "GET", g.URL, nil)
		if w.Code != 200 || w.Header().Get("Content-Type") != g.MIME {
			t.Fatalf("content: %d %s", w.Code, w.Body.String())
		}
		if kind == "image" {
			items, err := chaptermedia.List(s.dir, 1)
			if err != nil || len(items) != 1 || items[0].ID != g.ImageID || !bytes.Equal(w.Body.Bytes(), png) {
				t.Fatal("generated image not attached to chapter")
			}
		} else {
			r := httptest.NewRequest("GET", "http://127.0.0.1:8080"+g.URL, nil)
			r.Header.Set("Range", "bytes=0-3")
			rw := httptest.NewRecorder()
			s.Handler().ServeHTTP(rw, r)
			if rw.Code != 206 || rw.Body.Len() != 4 || rw.Header().Get("Content-Range") == "" {
				t.Fatalf("range playback failed: %d %v", rw.Code, rw.Header())
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(spoken, "") != body || len(spoken) < 2 || videoPolls != 1 || len(models) < 4 {
		t.Fatalf("lost speech or video polling: %d/%d", len(spoken), videoPolls)
	}
	current, _ := st.Drafts.LoadChapterText(1)
	if current != body {
		t.Fatal("generation mutated prose")
	}
	// Completed artifacts and provenance survive process state loss.
	s.mu.Lock()
	s.mediaGenerations = nil
	s.mu.Unlock()
	jobs, err := chaptermedia.ListGenerations(s.dir, 1)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("durable generation records: %v %+v", err, jobs)
	}
	for _, g := range jobs {
		if _, data, err := chaptermedia.ReadGeneration(s.dir, 1, g.ID); err != nil || len(data) == 0 {
			t.Fatalf("durable artifact: %s %v", g.Kind, err)
		}
	}
}
func TestChapterGenerationRefusesMissingBodyWrongProjectAndCustomProtocol(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	setGenerationConnection(t, s, "image", "http://127.0.0.1:1")
	for _, test := range []struct {
		chapter, directory, source string
		status                     int
	}{{"1", s.dir + "-wrong", "final", 409}, {"2", s.dir, "final", 400}, {"1", s.dir, "draft", 400}, {"1", s.dir, "plan", 400}} {
		w := mediaConfigRequest(t, s, "POST", "/api/chapters/"+test.chapter+"/media/generate", map[string]string{"directory": test.directory, "kind": "image", "source": test.source})
		if w.Code != test.status {
			t.Fatalf("expected %d got %d: %s", test.status, w.Code, w.Body.String())
		}
	}
	custom := mediaConnectionDraft("video")
	custom["protocol"] = "custom"
	decodeMediaConfigResponse(t, mediaConfigRequest(t, s, "POST", "/api/media-models", custom))
	w := mediaConfigRequest(t, s, "POST", "/api/chapters/1/media/generate", map[string]string{"directory": s.dir, "kind": "video"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "自定义协议") {
		t.Fatalf("custom falsely executable: %d %s", w.Code, w.Body.String())
	}
}
func TestChapterGenerationRejectsChangedBodyAndCancellationDoesNotBlockState(t *testing.T) {
	for _, mode := range []string{"changed", "cancelled", "project-switch"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestServer(t, false)
			st := seedChapterEditor(t, s)
			entered := make(chan struct{})
			release := make(chan struct{})
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(chapterMediaPNG(t))}}})
				case <-r.Context().Done():
				}
			}))
			defer mock.Close()
			setGenerationConnection(t, s, "image", mock.URL)
			g := createGeneration(t, s, "image", "final")
			<-entered
			if w := mediaConfigRequest(t, s, "GET", "/api/state", nil); w.Code != 200 {
				t.Fatalf("generation blocked state: %d", w.Code)
			}
			if w := mediaConfigRequest(t, s, "POST", "/api/chapters/1/media/generate", map[string]string{"directory": s.dir, "kind": "image"}); w.Code != 409 {
				t.Fatal("duplicate charge admitted")
			}
			original := s.dir
			switch mode {
			case "changed":
				if err := st.Drafts.SaveFinalChapter(1, "更新后的正文"); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				w := mediaConfigRequest(t, s, "DELETE", "/api/chapters/1/media/generate/"+g.ID+"?directory="+url.QueryEscape(s.dir), nil)
				if w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			case "project-switch":
				s.mu.Lock()
				s.dir = t.TempDir()
				s.mu.Unlock()
			}
			close(release)
			if mode == "project-switch" {
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					s.mu.Lock()
					done := s.mediaGenerations[g.ID].Status != "running"
					s.mu.Unlock()
					if done {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				s.mu.Lock()
				s.dir = original
				s.mu.Unlock()
			}
			result := awaitGeneration(t, s, g.ID)
			if mode == "cancelled" {
				if result.Status != "cancelled" {
					t.Fatalf("cancel: %+v", result)
				}
			} else if mode == "project-switch" {
				if result.Status != "completed" {
					t.Fatalf("switch lost original project result: %+v", result)
				}
			} else if result.Status != "failed" {
				t.Fatalf("stale output accepted: %+v", result)
			}
			expected := 0
			if mode == "project-switch" {
				expected = 1
			}
			if items, err := chaptermedia.List(original, 1); err != nil || len(items) != expected {
				t.Fatalf("stale result attached: %v %+v", err, items)
			}
		})
	}
}
func TestChapterGenerationRestartMarksInterruptedAndDoesNotLeakKeys(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	g := chaptermedia.Generation{ID: strings.Repeat("a", 32), Chapter: 1, Kind: "audio", Source: "final", SourceSHA256: strings.Repeat("b", 64), Model: "audio-model", Status: "running", CreatedAt: time.Now().Format(time.RFC3339Nano)}
	if err := chaptermedia.SaveGeneration(s.dir, g, nil); err != nil {
		t.Fatal(err)
	}
	result := awaitGeneration(t, s, g.ID)
	if result.Status != "failed" || !strings.Contains(result.Error, "中断") {
		t.Fatalf("stuck after restart: %+v", result)
	}
	w := mediaConfigRequest(t, s, "DELETE", "/api/chapters/1/media/generate/"+g.ID+"?directory="+url.QueryEscape(s.dir), nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte("echo: media-secret-never-in-response"))
	}))
	defer mock.Close()
	setGenerationConnection(t, s, "audio", mock.URL)
	job := createGeneration(t, s, "audio", "final")
	result = awaitGeneration(t, s, job.ID)
	if result.Status != "failed" || strings.Contains(result.Error, "media-secret-never-in-response") || !strings.Contains(result.Error, "401") {
		t.Fatalf("error leaked or hidden: %+v", result)
	}
	files, _ := filepath.Glob(filepath.Join(s.dir, "media", "chapters", "000001", "generated", "*.json"))
	for _, file := range files {
		b, _ := os.ReadFile(file)
		if bytes.Contains(b, []byte("media-secret-never-in-response")) {
			t.Fatal("credential persisted in project")
		}
	}
}

func TestSpeechChunksPreserveChineseEmojiAndMP3SegmentsHaveNoLengthTags(t *testing.T) {
	body := strings.Repeat("正文☁️🌙。\n English passage. ", 1700)
	chunks := speechChunks(body, 4000)
	if strings.Join(chunks, "") != body || len(chunks) < 2 {
		t.Fatal("speech lost text")
	}
	for _, chunk := range chunks {
		if len(chunk) > 1500 || len([]rune(chunk)) > 4000 {
			t.Fatal("speech chunk exceeded token/character budget")
		}
	}
	frame := generationMP3()
	info := generationMP3()
	copy(info[36:], "Xing")
	id3 := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'}
	segment := append(append(append([]byte{}, id3...), info...), frame...)
	frames, err := mp3Frames(segment)
	if err != nil || !bytes.Equal(frames, frame) {
		t.Fatalf("MP3 segment metadata retained: %v %d", err, len(frames))
	}
	for _, invalid := range [][]byte{[]byte(`{"error":"no audio"}`), frame[:len(frame)-1], append(append([]byte{}, frame...), 0xff), []byte("ID3")} {
		if _, err := mp3Frames(invalid); err == nil {
			t.Fatal("malformed audio accepted")
		}
	}
	// Real fixture exercises the actual encoder's headers and frame sizes.
	fixture, err := os.ReadFile("../../../desktop/scripts/fixtures/media.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mp3Frames(fixture); err != nil {
		t.Fatalf("real MP3 rejected: %v", err)
	}
}

func TestGeneratedImageURLNeverGetsProviderCredentialAndDraftIsExplicit(t *testing.T) {
	s := newTestServer(t, false)
	st := seedChapterEditor(t, s)
	draft := "只朗读草稿内容，不能误用终稿"
	if err := st.Drafts.SaveDraft(1, draft); err != nil {
		t.Fatal(err)
	}
	png := chapterMediaPNG(t)
	var address string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/images/generations":
			var input struct{ Prompt string }
			json.NewDecoder(r.Body).Decode(&input)
			if !strings.Contains(input.Prompt, draft) || strings.Contains(input.Prompt, "原始正文") {
				t.Error("wrong saved source sent")
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"url": address + "/asset.png"}}})
		case "/asset.png":
			if r.Header.Get("Authorization") != "" {
				t.Error("provider key leaked to image URL")
			}
			w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()
	address = mock.URL
	setGenerationConnection(t, s, "image", mock.URL)
	job := createGeneration(t, s, "image", "draft")
	result := awaitGeneration(t, s, job.ID)
	if result.Status != "completed" || result.Source != "draft" || result.SourceSHA256 != generationHash(draft) {
		t.Fatalf("draft generation: %+v", result)
	}
}
