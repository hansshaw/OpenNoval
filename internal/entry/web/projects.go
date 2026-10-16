package web

import (
	"encoding/json"
	"fmt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type projectInfo struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

func readProject(dir string) (projectInfo, error) {
	var p projectInfo
	b, err := os.ReadFile(filepath.Join(dir, "meta", "web-project.json"))
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

func saveProject(dir string, p projectInfo) error {
	meta := filepath.Join(dir, "meta")
	if err := os.MkdirAll(meta, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(meta, "web-project-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(meta, "web-project.json"))
}

func validProjectName(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 80
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Directory string }
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validProjectName(req.Name) {
		fail(w, 400, "请输入 1–80 字的项目名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stopping || req.Directory != s.dir {
		fail(w, 409, "项目已切换或正在停止")
		return
	}
	p, err := readProject(s.dir)
	if err != nil {
		fail(w, 500, "项目资料读取失败")
		return
	}
	p.Name = req.Name
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if err := saveProject(s.dir, p); err != nil {
		fail(w, 500, "项目名称保存失败")
		return
	}
	reply(w, 200, p)
}

func workflowView(st *store.Store) (map[string]any, error) {
	v := map[string]any{"stage": "new", "batchSize": 1, "approved": false, "canApprove": false, "latestChapter": 0}
	m, err := st.RunMeta.Load()
	if err != nil {
		return v, err
	}
	if m == nil {
		return v, nil
	}
	v["started"] = m.StartPrompt != ""
	if m.StartPrompt != "" {
		v["stage"] = "planning"
	}
	p, err := st.Progress.Load()
	if err != nil {
		return v, err
	}
	w := m.Workbench
	if w == nil {
		w = &domain.WorkbenchControl{BatchSize: 1}
	}
	v["batchSize"], v["batchEnd"], v["acceptedChapter"], v["feedback"] = w.BatchSize, w.BatchEnd, w.AcceptedChapter, w.Feedback
	missing, err := st.FoundationMissing()
	if err != nil {
		return v, err
	}
	v["missing"] = missing
	if len(missing) != 0 {
		return v, nil
	}
	fp, err := st.PlanningFingerprint()
	if err != nil {
		return v, err
	}
	approved := w.ApprovedFingerprint != "" && fp == w.ApprovedFingerprint
	v["fingerprint"], v["approved"], v["canApprove"] = fp, approved, true
	v["stage"] = "plan-review"
	if p != nil {
		latest := p.LatestCompleted()
		v["latestChapter"] = latest
		if approved {
			v["stage"] = "ready"
			if latest > w.AcceptedChapter {
				v["stage"] = "chapter-review"
			} else if p.Phase == domain.PhaseComplete {
				v["stage"] = "complete"
			} else if p.NextChapter() <= w.BatchEnd {
				v["stage"] = "writing"
			}
		}
	}
	return v, nil
}

func (s *Server) planning(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := r.URL.Query().Get("directory"); d != "" && d != s.dir {
		fail(w, 409, "项目已切换")
		return
	}
	st := store.NewStore(s.dir)
	premise, e1 := st.Outline.LoadPremise()
	outline, e2 := st.Outline.LoadOutline()
	layered, e3 := st.Outline.LoadLayeredOutline()
	book, e4 := st.Book.Load()
	workflow, e5 := workflowView(st)
	for _, err := range []error{e1, e2, e3, e4, e5} {
		if err != nil {
			fail(w, 500, fmt.Sprintf("规划读取失败：%s", s.redactLocked(err.Error())))
			return
		}
	}
	reply(w, 200, map[string]any{"directory": s.dir, "premise": premise, "outline": outline, "layered": layered, "book": book, "workflow": workflow})
}
