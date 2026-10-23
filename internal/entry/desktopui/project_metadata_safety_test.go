package desktopui

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/voocel/ainovel-cli/internal/host"
)

func TestProjectMetadataWritesRespectForeignLease(t *testing.T) {
	for _, operation := range []string{"update", "archive-current", "archive-other"} {
		t.Run(operation, func(t *testing.T) {
			s := newTestServer(t, false)
			target := s.dir
			if operation == "archive-other" {
				target = filepath.Join(s.booksDir, "other-project")
			}
			if err := saveProject(target, projectInfo{Name: "不可交叉改写", TargetChapters: 12}); err != nil {
				t.Fatal(err)
			}
			projectPath := filepath.Join(target, "meta", "web-project.json")
			before, err := os.ReadFile(projectPath)
			if err != nil {
				t.Fatal(err)
			}
			lease := flock.New(filepath.Join(target, ".ainovel.lock"))
			locked, err := lease.TryLock()
			if err != nil || !locked {
				t.Fatalf("acquire foreign lease: %v %v", locked, err)
			}
			t.Cleanup(func() { _ = lease.Close() })
			method, endpoint := http.MethodPost, "/api/workspaces/archive"
			body := map[string]any{"directory": target, "archived": true}
			if operation == "update" {
				method, endpoint = http.MethodPatch, "/api/workspaces"
				body = map[string]any{"directory": target, "name": "错误的改名", "targetChapters": 20}
			}
			response := request(s, method, endpoint, mustJSON(t, body))
			if response.Code != http.StatusConflict {
				t.Fatalf("mutated a leased project: %d %s", response.Code, response.Body.String())
			}
			after, err := os.ReadFile(projectPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed mutation changed project: %q %v", after, err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			response = request(s, method, endpoint, mustJSON(t, body))
			if response.Code != http.StatusOK {
				t.Fatalf("released project was not editable: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProjectMetadataWritesRespectDeletionMarker(t *testing.T) {
	s := newTestServer(t, false)
	if err := saveProject(s.dir, projectInfo{Name: "删除过程中的项目"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, host.ProjectDeletingFile), []byte("deleting\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/workspaces", "/api/workspaces/archive"} {
		method := http.MethodPost
		body := map[string]any{"directory": s.dir, "archived": true}
		if endpoint == "/api/workspaces" {
			method = http.MethodPatch
			body = map[string]any{"directory": s.dir, "name": "不应保存"}
		}
		response := request(s, method, endpoint, mustJSON(t, body))
		if response.Code != http.StatusConflict {
			t.Fatalf("mutation bypassed deletion marker: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestUnarchiveReleasesMetadataLeaseBeforeRebuildingHost(t *testing.T) {
	s := newTestServer(t, true)
	if err := saveProject(s.dir, projectInfo{Name: "归档后重新打开"}); err != nil {
		t.Fatal(err)
	}
	for _, archived := range []bool{true, false} {
		response := request(s, http.MethodPost, "/api/workspaces/archive", mustJSON(t, map[string]any{"directory": s.dir, "archived": archived}))
		if response.Code != http.StatusOK {
			t.Fatalf("archive=%v: %d %s", archived, response.Code, response.Body.String())
		}
	}
	if s.rt == nil || s.hostError != "" {
		t.Fatalf("unarchive did not reopen Host: %s", s.hostError)
	}
}

func TestProjectSetupCanSaveBeforeFirstHostExists(t *testing.T) {
	s := newTestServer(t, false)
	response := request(s, http.MethodPatch, "/api/workspaces", mustJSON(t, map[string]any{"directory": s.dir, "name": "首次设置"}))
	if response.Code != http.StatusOK {
		t.Fatalf("setup could not initialize project: %d %s", response.Code, response.Body.String())
	}
}

func TestCoCreateResetRespectsForeignLease(t *testing.T) {
	s := newTestServer(t, false)
	session := webCoCreateSession{DraftPrompt: "不可删除的创作原则", Ready: true}
	if err := saveWebCoCreate(s.dir, session); err != nil {
		t.Fatal(err)
	}
	lease := flock.New(filepath.Join(s.dir, ".ainovel.lock"))
	locked, err := lease.TryLock()
	if err != nil || !locked {
		t.Fatalf("foreign lease: %v %v", locked, err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	body := mustJSON(t, map[string]string{"directory": s.dir})
	response := request(s, http.MethodPost, "/api/cocreate/reset", body)
	if response.Code != http.StatusConflict {
		t.Fatalf("reset ignored foreign lease: %d %s", response.Code, response.Body.String())
	}
	saved, err := readWebCoCreate(s.dir)
	if err != nil || saved.DraftPrompt != session.DraftPrompt || !saved.Ready {
		t.Fatalf("foreign session was modified: %+v %v", saved, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	response = request(s, http.MethodPost, "/api/cocreate/reset", body)
	if response.Code != http.StatusOK {
		t.Fatalf("reset did not recover after release: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(coCreatePath(s.dir)); !os.IsNotExist(err) {
		t.Fatalf("reset did not remove session: %v", err)
	}
}
