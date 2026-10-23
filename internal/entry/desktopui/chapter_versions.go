package desktopui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

func (s *Server) chapterVersions(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter <= 0 {
		fail(w, 400, "无效章节")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Query().Get("directory") != s.dir {
		fail(w, 409, "项目已切换，请刷新后重试")
		return
	}
	st := store.NewStore(s.dir)
	archiveOriginal := !s.closed && !s.stopping && !s.projectRunningLocked(s.dir) && !s.backgroundProjectBusyLocked(s.dir)
	var response map[string]any
	load := func() error {
		progress, err := st.Progress.Load()
		if err != nil {
			return fmt.Errorf("进度读取失败：%w", err)
		}
		if progress == nil || !slices.Contains(progress.CompletedChapters, chapter) {
			return fmt.Errorf("请选择已完成的章节")
		}
		content, err := st.Drafts.LoadChapterText(chapter)
		if err != nil || strings.TrimSpace(content) == "" {
			return fmt.Errorf("章节正文不可用")
		}
		title := fmt.Sprintf("第 %d 章", chapter)
		if entry, err := st.Outline.GetChapterOutline(chapter); err == nil && entry != nil && entry.Title != "" {
			title = entry.Title
		}
		if archiveOriginal {
			if _, err := st.ChapterVersions.EnsureCurrent(chapter, title, content); err != nil {
				return fmt.Errorf("原稿版本保存失败：%w", err)
			}
		}
		versions, err := st.ChapterVersions.List(chapter, content)
		if err != nil {
			return fmt.Errorf("版本读取失败：%w", err)
		}
		baseline, err := st.ChapterRecords.Load(chapter)
		if err != nil {
			return fmt.Errorf("章节接纳记录读取失败：%w", err)
		}
		hash := domain.ChapterContentSHA256(content)
		response = map[string]any{"directory": s.dir, "chapter": chapter, "currentContentHash": hash, "versions": versions, "syncRequired": baseline == nil || baseline.ContentSHA256 != hash}
		return nil
	}
	if archiveOriginal {
		err = s.withVersionSnapshotLocked(load)
	} else {
		err = load()
	}
	if err != nil {
		fail(w, http.StatusConflict, s.redactLocked(err.Error()))
		return
	}
	reply(w, 200, response)
}

func (s *Server) chapterRewrites(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory   string `json:"directory"`
		Chapters    []int  `json:"chapters"`
		Instruction string `json:"instruction"`
		AutoApply   bool   `json:"autoApply"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Chapters) == 0 || len(req.Chapters) > 100 || strings.TrimSpace(req.Instruction) == "" || len(req.Instruction) > 64<<10 {
		fail(w, 400, "请选择 1–100 章并填写明确的重写指令")
		return
	}
	seen := map[int]bool{}
	for _, chapter := range req.Chapters {
		if chapter <= 0 || seen[chapter] {
			fail(w, 400, "章节号无效或重复")
			return
		}
		seen[chapter] = true
	}
	s.startChapterVersionJob(w, r, req.Directory, "chapter-rewrites", true, func(ctx context.Context, rt *host.Host, _ string) (map[string]any, error) {
		versions, err := rt.RewriteWorkbenchChapters(ctx, req.Chapters, req.Instruction, req.AutoApply)
		generated, applied := []int{}, []int{}
		for _, version := range versions {
			generated = append(generated, version.Chapter)
			if version.Active {
				applied = append(applied, version.Chapter)
			}
		}
		return map[string]any{"versions": versions, "syncRequired": len(applied) > 0, "generatedChapters": generated, "appliedChapters": applied}, err
	})
}

func (s *Server) applyChapterVersion(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter <= 0 {
		fail(w, 400, "无效章节")
		return
	}
	versionID := r.PathValue("version")
	var req struct {
		Directory           string `json:"directory"`
		ExpectedContentHash string `json:"expectedContentHash"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.ExpectedContentHash == "" {
		fail(w, 400, "缺少正文版本，请刷新后重试")
		return
	}
	s.startChapterVersionJob(w, r, req.Directory, "apply-chapter-version", false, func(ctx context.Context, rt *host.Host, directory string) (map[string]any, error) {
		var version domain.ChapterVersion
		var err error
		if rt != nil {
			version, err = rt.ApplyWorkbenchChapterVersion(ctx, chapter, versionID, req.ExpectedContentHash)
		} else {
			err = withWorkspaceLease(directory, func() error {
				st := store.NewStore(directory)
				if pending, e := st.Signals.LoadPendingCommit(); e != nil {
					return e
				} else if pending != nil {
					return fmt.Errorf("章节提交尚未完成，请先恢复任务")
				}
				if pending, e := st.Revisions.LoadPending(); e != nil {
					return e
				} else if pending != nil {
					return fmt.Errorf("修订同步尚未完成，请先同步修订")
				}
				version, err = st.ChapterVersions.Apply(chapter, versionID, req.ExpectedContentHash)
				return err
			})
		}
		return map[string]any{"version": version, "syncRequired": err == nil}, err
	})
}

func (s *Server) startChapterVersionJob(w http.ResponseWriter, _ *http.Request, directory, action string, modelRequired bool, run func(context.Context, *host.Host, string) (map[string]any, error)) {
	s.mu.Lock()
	if directory == "" || directory != s.dir {
		s.mu.Unlock()
		fail(w, 409, "当前项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping || s.projectRunningLocked(directory) || s.backgroundProjectBusyLocked(directory) {
		s.mu.Unlock()
		fail(w, 409, "请先暂停并等待当前项目任务结束")
		return
	}
	project, err := readProject(directory)
	if err != nil || project.Archived {
		s.mu.Unlock()
		fail(w, 409, "项目不可用或已归档")
		return
	}
	if modelRequired && s.rt == nil {
		s.initLocked()
	}
	rt := s.rt
	if modelRequired && rt == nil {
		s.mu.Unlock()
		fail(w, 409, "请先配置模型并重新连接工作台")
		return
	}
	if rt != nil {
		if err := s.reserveProjectRunLocked(rt); err != nil {
			message := s.projectRunErrorLocked(err)
			s.mu.Unlock()
			fail(w, 409, message)
			return
		}
	}
	s.busy, s.job = true, action
	ctx, cancel := context.WithCancel(s.ctx)
	jobDone := make(chan struct{})
	s.cancelJob, s.jobDone = cancel, jobDone
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer close(jobDone)
		defer cancel()
		result, err := run(ctx, rt, directory)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.dir == directory && s.jobDone == jobDone {
			s.busy = false
			s.job = ""
			s.cancelJob = nil
			s.jobDone = nil
		}
		if rt != nil {
			s.releaseIdleProjectRunLocked(rt)
		}
		if result == nil {
			result = map[string]any{}
		}
		result["action"], result["directory"], result["ok"] = action, directory, err == nil
		if err != nil {
			result["error"] = s.redactLocked(err.Error())
		}
		s.broadcastLocked("result", result)
		s.broadcastLocked("refresh", nil)
	}()
	reply(w, http.StatusAccepted, map[string]bool{"ok": true})
}
