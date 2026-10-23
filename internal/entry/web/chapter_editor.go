package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

const maxWorkbenchBodyBytes = 8 << 20

var errChapterEditChanged = errors.New("章节或规划已变化，请重新读取后合并修改")
var errChapterEditBusy = errors.New("章节有未完成的提交或修订，请先恢复或同步后再保存")

type chapterEditView struct {
	Directory           string         `json:"directory"`
	Chapter             int            `json:"chapter"`
	Revision            string         `json:"revision"`
	Title               string         `json:"title"`
	VolumeTitle         string         `json:"volumeTitle"`
	CoreEvent           string         `json:"coreEvent"`
	Hook                string         `json:"hook"`
	Scenes              []string       `json:"scenes"`
	Content             string         `json:"content"`
	HasContent          bool           `json:"hasContent"`
	HasPlan             bool           `json:"hasPlan"`
	SyncRequired        bool           `json:"syncRequired"`
	PlanningFingerprint string         `json:"-"`
	Workflow            map[string]any `json:"workflow"`
}

func readChapterEdit(st *store.Store, chapter int) (chapterEditView, error) {
	view := chapterEditView{Directory: st.Dir(), Chapter: chapter, Scenes: []string{}}
	outline, err := st.Outline.LoadOutlineView()
	if err != nil {
		return view, err
	}
	for _, entry := range outline {
		if entry.Chapter == chapter {
			view.Title, view.VolumeTitle = entry.Title, entry.VolumeTitle
			view.CoreEvent, view.Hook = entry.CoreEvent, entry.Hook
			view.Scenes = append([]string{}, entry.Scenes...)
			view.HasPlan = true
			break
		}
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return view, err
	}
	completed := progress != nil && slices.Contains(progress.CompletedChapters, chapter)
	if completed {
		view.Content, err = st.Drafts.LoadChapterText(chapter)
		if err != nil {
			return view, err
		}
		view.HasContent = strings.TrimSpace(view.Content) != ""
		if title, err := st.Summaries.LoadSummaryTitle(chapter); err != nil {
			return view, err
		} else if title != "" {
			view.Title = title
		}
		if record, err := st.ChapterRecords.Load(chapter); err != nil {
			return view, err
		} else if record != nil {
			view.SyncRequired = record.ContentSHA256 != domain.ChapterContentSHA256(view.Content)
		}
	}
	if !view.HasPlan && !completed {
		return view, store.ErrOutlineChapterNotFound
	}
	view.PlanningFingerprint, err = st.PlanningFingerprint()
	if err != nil && !os.IsNotExist(err) {
		return view, err
	}
	// A whole-plan fingerprint detects a rename of any chapter's parent volume,
	// while the content digest prevents an old editor from replacing newer prose.
	encoded, err := json.Marshal(struct {
		Planning, Title, Content string
		Chapter                  int
	}{view.PlanningFingerprint, view.Title, domain.ChapterContentSHA256(view.Content), chapter})
	if err != nil {
		return view, err
	}
	digest := sha256.Sum256(encoded)
	view.Revision = hex.EncodeToString(digest[:])
	view.Workflow, err = workflowView(st)
	return view, err
}

func (s *Server) chapterEditErrorLocked(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, errChapterEditChanged) || errors.Is(err, errChapterEditBusy) || errors.Is(err, host.ErrProjectSnapshotBusy) || errors.Is(err, store.ErrWorkbenchPlanChanged) {
		status = http.StatusConflict
	} else if errors.Is(err, store.ErrOutlineChapterNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, store.ErrWorkbenchPlanInvalid) {
		status = http.StatusBadRequest
	}
	fail(w, status, s.redactLocked(err.Error()))
}

func (s *Server) chapterEditor(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter <= 0 {
		fail(w, http.StatusBadRequest, "无效章节")
		return
	}
	var req struct {
		Directory, ExpectedRevision string
		SavePlan, SaveContent       bool
		Title, VolumeTitle          string
		CoreEvent, Hook, Content    string
		Scenes                      []string
	}
	if r.Method == http.MethodPost {
		if !readJSONLimit(w, r, &req, maxWorkbenchBodyBytes+(1<<20)) {
			return
		}
		if req.ExpectedRevision == "" || req.SavePlan == req.SaveContent {
			fail(w, http.StatusBadRequest, "请先读取章节，并单独保存规划或正文")
			return
		}
		if req.SaveContent && (strings.TrimSpace(req.Content) == "" || !utf8.ValidString(req.Content) || len(req.Content) > maxWorkbenchBodyBytes) {
			fail(w, http.StatusBadRequest, "正文须为非空 UTF-8 文本，且不超过 8 MiB")
			return
		}
	} else {
		req.Directory = r.URL.Query().Get("directory")
	}
	s.mu.Lock()
	if req.Directory == "" || req.Directory != s.dir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "当前项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.projectRunningLocked(s.dir) || s.backgroundProjectBusyLocked(s.dir) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先暂停并等待本项目操作结束，再编辑章节")
		return
	}
	directory := s.dir
	var before chapterEditView
	err = s.withVersionSnapshotLocked(func() error {
		var readErr error
		before, readErr = readChapterEdit(store.NewStore(directory), chapter)
		return readErr
	})
	if err != nil {
		s.chapterEditErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	if r.Method == http.MethodGet {
		reply(w, http.StatusOK, before)
		s.mu.Unlock()
		return
	}
	if req.ExpectedRevision != before.Revision {
		s.chapterEditErrorLocked(w, errChapterEditChanged)
		s.mu.Unlock()
		return
	}
	if req.SaveContent && !before.HasContent {
		fail(w, http.StatusConflict, "请先生成并完成本章正文")
		s.mu.Unlock()
		return
	}
	project, err := readProject(directory)
	if err != nil || project.Archived {
		fail(w, http.StatusConflict, "项目资料不可用或项目已归档")
		s.mu.Unlock()
		return
	}
	// Closing an idle Host before changing source files prevents cached titles,
	// sessions and usage autosaves from replacing the user's saved result.
	rt := s.rt
	s.rt = nil
	s.busy, s.job = true, "chapter-edit"
	done := make(chan struct{})
	s.jobDone = done
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if rt != nil {
		rt.Close()
	}
	var after chapterEditView
	err = withWorkspaceLease(directory, func() error {
		st := store.NewStore(directory)
		current, err := readChapterEdit(st, chapter)
		if err != nil {
			return err
		}
		if current.Revision != req.ExpectedRevision {
			return errChapterEditChanged
		}
		if pending, err := st.Signals.LoadPendingCommit(); err != nil {
			return err
		} else if pending != nil {
			return errChapterEditBusy
		}
		if pending, err := st.Revisions.LoadPending(); err != nil {
			return err
		} else if pending != nil {
			return errChapterEditBusy
		}
		if req.SavePlan {
			edit := store.WorkbenchChapterPlanEdit{}
			if req.Title != current.Title {
				edit.Title = &req.Title
			}
			if req.VolumeTitle != current.VolumeTitle {
				edit.VolumeTitle = &req.VolumeTitle
			}
			if req.CoreEvent != current.CoreEvent {
				edit.CoreEvent = &req.CoreEvent
			}
			if req.Hook != current.Hook {
				edit.Hook = &req.Hook
			}
			if !slices.Equal(req.Scenes, current.Scenes) {
				edit.Scenes = &req.Scenes
			}
			_, err = st.UpdateWorkbenchChapterPlan(chapter, edit, current.PlanningFingerprint)
		} else {
			_, err = st.ChapterVersions.SaveManual(chapter, current.Title, req.Content, domain.ChapterContentSHA256(current.Content))
		}
		if err != nil {
			return err
		}
		after, err = readChapterEdit(st, chapter)
		return err
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy, s.job, s.jobDone = false, "", nil
	close(done)
	if !s.closed {
		s.initLocked()
		s.broadcastLocked("refresh", nil)
	}
	if err != nil {
		s.chapterEditErrorLocked(w, err)
		return
	}
	// The response is the exact saved source, not a model's proposed change.
	reply(w, http.StatusOK, after)
}
