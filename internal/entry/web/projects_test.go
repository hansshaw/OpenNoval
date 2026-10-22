package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectTargetChapterContractUsesRunMeta(t *testing.T) {
	s := newTestServer(t, true)
	if err := saveProject(s.dir, projectInfo{Name: "目标章数同步", TargetChapters: 20}); err != nil {
		t.Fatal(err)
	}
	if err := store.NewStore(s.dir).RunMeta.SetTargetChapters(24); err != nil {
		t.Fatal(err)
	}

	project, err := readProject(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if project.TargetChapters != 24 {
		t.Fatalf("RunMeta target must override the web sidecar: got %d, want 24", project.TargetChapters)
	}

	w := request(s, "PATCH", "/api/workspaces", mustJSON(t, map[string]any{
		"directory":      s.dir,
		"targetChapters": 30,
	}))
	if w.Code != 200 {
		t.Fatalf("update target: %d %s", w.Code, w.Body.String())
	}
	meta, err := store.NewStore(s.dir).RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.TargetChapters != 30 {
		t.Fatalf("RunMeta target not updated: %+v", meta)
	}
	if meta.Workbench == nil || !meta.Workbench.PlanningRevisionPending {
		t.Fatalf("target revision must invalidate planning approval: %+v", meta)
	}
	wantRequest := "目标章节总数由 24 章改为 30 章。请根据用户需求和第一阶段原则重新规划全部卷、全部弧，为每一弧提供真实 chapters，并保证所有章节总数严格等于 30；保留已完成正文及非空在写草稿。"
	if meta.Workbench.PlanningRevisionRequest != wantRequest {
		t.Fatalf("unexpected planning request: %q", meta.Workbench.PlanningRevisionRequest)
	}
	if meta.Workbench.ApprovedFingerprint != "" ||
		meta.Workbench.ApprovedFoundationFingerprint != "" ||
		meta.Workbench.BatchEnd != 0 {
		t.Fatalf("target revision left stale approval state: %+v", meta.Workbench)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "meta", "web-project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar projectInfo
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatal(err)
	}
	if sidecar.TargetChapters != 30 {
		t.Fatalf("web sidecar target not synchronized: %+v", sidecar)
	}
}

func TestProjectTargetChapterContractRollsBackSidecarOnRunMetaFailure(t *testing.T) {
	dir := t.TempDir()
	before := projectInfo{Name: "目标章数原子回滚", TargetChapters: 20}
	if err := saveProject(dir, before); err != nil {
		t.Fatal(err)
	}
	rawBefore, err := os.ReadFile(filepath.Join(dir, "meta", "web-project.json"))
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected run meta failure")
	called := false
	after := before
	after.TargetChapters = 30
	err = saveProjectWithTargetContractWithOps(dir, after, before.TargetChapters, targetContractOps{
		saveProject: saveProject,
		applyRevision: func(string, int, string) error {
			called = true
			return injected
		},
	})
	if !called || !errors.Is(err, injected) {
		t.Fatalf("expected injected run meta failure, called=%v err=%v", called, err)
	}
	rawAfter, readErr := os.ReadFile(filepath.Join(dir, "meta", "web-project.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(rawAfter, rawBefore) {
		t.Fatalf("run meta failure must restore web sidecar bytes:\nbefore=%s\nafter=%s", rawBefore, rawAfter)
	}
}

func TestProjectTargetChapterContractRollsBackSidecarOnProjectFailure(t *testing.T) {
	dir := t.TempDir()
	before := projectInfo{Name: "项目写入原子回滚", TargetChapters: 12}
	if err := saveProject(dir, before); err != nil {
		t.Fatal(err)
	}
	rawBefore, err := os.ReadFile(filepath.Join(dir, "meta", "web-project.json"))
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected web project failure")
	after := before
	after.TargetChapters = 18
	err = saveProjectWithTargetContractWithOps(dir, after, before.TargetChapters, targetContractOps{
		saveProject: func(dir string, p projectInfo) error {
			if saveErr := saveProject(dir, p); saveErr != nil {
				return saveErr
			}
			return injected
		},
		applyRevision: func(string, int, string) error {
			t.Fatal("run meta must not be written after the sidecar operation fails")
			return nil
		},
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected project failure, got %v", err)
	}
	rawAfter, readErr := os.ReadFile(filepath.Join(dir, "meta", "web-project.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(rawAfter, rawBefore) {
		t.Fatalf("project failure must restore web sidecar bytes:\nbefore=%s\nafter=%s", rawBefore, rawAfter)
	}
}
