package desktopui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/modelrequest"
)

func TestModelRequestHistoryWithoutModelAndProjectIsolation(t *testing.T) {
	s := newTestServer(t, false)
	original, other := s.dir, t.TempDir()
	j, err := modelrequest.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"request-one", "request-two"} {
		record := modelrequest.Record{ID: id, Provider: "local", Model: "demo", Status: "success", StartedAt: time.Unix(int64(index+1), 0), Request: json.RawMessage(`{"messages":[{"content":"本项目"}]}`), Response: json.RawMessage(`{"text":"完成"}`)}
		if index == 0 {
			record.Status = "error"
		}
		if err := j.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	response := request(s, "GET", "/api/model-requests?limit=1", "")
	var list struct {
		Directory string                `json:"directory"`
		Requests  []modelrequest.Record `json:"requests"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &list) != nil || list.Directory != original || len(list.Requests) != 1 || list.Requests[0].ID != "request-two" || len(list.Requests[0].Request) != 0 {
		t.Fatalf("history unavailable without model: %d %s", response.Code, response.Body.String())
	}
	response = request(s, "GET", "/api/model-requests?status=error", "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Requests) != 1 || list.Requests[0].ID != "request-one" {
		t.Fatalf("status filter: %d %s", response.Code, response.Body.String())
	}
	response = request(s, "GET", "/api/model-requests/request-one", "")
	var detail modelrequest.Record
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &detail) != nil || len(detail.Request) == 0 || len(detail.Response) == 0 {
		t.Fatalf("detail unavailable: %d %s", response.Code, response.Body.String())
	}
	if response := request(s, "GET", "/api/model-requests/missing", ""); response.Code != http.StatusNotFound {
		t.Fatalf("missing request: %d %s", response.Code, response.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=501", "limit=no", "status=invalid"} {
		if response := request(s, "GET", "/api/model-requests?"+query, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid query accepted %s: %d", query, response.Code)
		}
	}
	s.mu.Lock()
	s.dir = other
	s.mu.Unlock()
	if response := request(s, "GET", "/api/model-requests/request-one", ""); response.Code != http.StatusNotFound {
		t.Fatalf("previous project detail exposed: %d %s", response.Code, response.Body.String())
	}
	for _, endpoint := range []string{"/api/model-requests", "/api/model-requests/request-one"} {
		response := request(s, "GET", endpoint+"?directory="+url.QueryEscape(original), "")
		if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "本项目") {
			t.Fatalf("foreign directory accepted: %d %s", response.Code, response.Body.String())
		}
	}
	response = request(s, "GET", "/api/model-requests", "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &list) != nil || list.Directory != other || len(list.Requests) != 0 {
		t.Fatalf("history crossed project boundary: %d %s", response.Code, response.Body.String())
	}
}

func TestModelRequestAPIReRedactsEveryStringWithCurrentCredentials(t *testing.T) {
	s := newTestServer(t, false)
	j, err := modelrequest.Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	secret := "secret-\"quoted\\value\nline"
	raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"text": secret}}, "response": map[string]any{secret: []string{secret}}})
	if err := j.Save(modelrequest.Record{ID: "old-history", Status: "success", Model: secret, Error: secret, Request: raw, Response: raw}); err != nil {
		t.Fatal(err)
	}
	// Simulate credentials configured after a historic log was captured. The API
	// must decode JSON before replacing a secret that contains JSON escapes.
	s.mu.Lock()
	s.baseCfg.Providers = map[string]bootstrap.ProviderConfig{"new-provider": {APIKey: secret}}
	s.mu.Unlock()
	for _, endpoint := range []string{"/api/model-requests", "/api/model-requests/old-history"} {
		response := request(s, "GET", endpoint, "")
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var decoded any
		if json.Unmarshal(response.Body.Bytes(), &decoded) != nil {
			t.Fatal("redaction corrupted JSON", response.Body.String())
		}
		data, _ := json.Marshal(decoded)
		encodedSecret, _ := json.Marshal(secret)
		if strings.Contains(string(data), string(encodedSecret)) || strings.Contains(string(data), "quoted") {
			t.Fatalf("API exposed JSON-escaped credential: %s", data)
		}
	}
}
