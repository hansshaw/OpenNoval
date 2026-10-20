package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/projectversion"
)

func TestProjectVersionsPreviewRestoreAndBackup(t *testing.T) {
	s := newTestServer(t, true)
	chapter := filepath.Join(s.dir, "chapters", "01.md")
	if err := os.MkdirAll(filepath.Dir(chapter), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chapter, []byte("初稿正文"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveProject(s.dir, projectInfo{ID: "stable-id", Name: "当前项目"}); err != nil {
		t.Fatal(err)
	}
	created := request(s, "POST", "/api/versions", mustJSON(t, map[string]any{"directory": s.dir, "name": "初稿"}))
	if created.Code != http.StatusCreated {
		t.Fatal(created.Code, created.Body.String())
	}
	var version projectversion.Manifest
	if err := json.Unmarshal(created.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chapter, []byte("改写正文"), 0600); err != nil {
		t.Fatal(err)
	}
	compared := request(s, "GET", "/api/versions/"+version.ID+"/compare?directory="+s.dir, "")
	if compared.Code != http.StatusOK {
		t.Fatal(compared.Code, compared.Body.String())
	}
	var comparison projectversion.Comparison
	if err := json.Unmarshal(compared.Body.Bytes(), &comparison); err != nil {
		t.Fatal(err)
	}
	if len(comparison.Changes) == 0 || comparison.Fingerprint == "" {
		t.Fatal("missing restore preview")
	}
	restored := request(s, "POST", "/api/versions/restore", mustJSON(t, map[string]any{
		"directory": s.dir, "id": version.ID, "fingerprint": comparison.Fingerprint,
	}))
	if restored.Code != http.StatusOK {
		t.Fatal(restored.Code, restored.Body.String())
	}
	data, err := os.ReadFile(chapter)
	if err != nil || string(data) != "初稿正文" {
		t.Fatalf("restored content %q %v", data, err)
	}
	project, err := readProject(s.dir)
	if err != nil || project.ID != "stable-id" || project.Name != "当前项目" {
		t.Fatal("project identity changed", project, err)
	}
	list, err := projectversion.List(s.dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("restore backup missing: %+v %v", list, err)
	}
	if s.rt == nil || s.busy {
		t.Fatal("restored project Host was not reopened")
	}
}

func TestProjectVersionsRejectStaleOrRunningRequests(t *testing.T) {
	s := newTestServer(t, true)
	for _, endpoint := range []string{"/api/versions", "/api/versions/restore"} {
		body := map[string]any{"directory": "stale", "name": "snapshot"}
		if endpoint == "/api/versions/restore" {
			body = map[string]any{"directory": "stale", "id": "invalid", "fingerprint": "old"}
		}
		w := request(s, "POST", endpoint, mustJSON(t, body))
		if w.Code != 409 {
			t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	w := request(s, "POST", "/api/versions", mustJSON(t, map[string]any{"directory": s.dir, "name": "snapshot"}))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}
