package desktopui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

const maxManualChapters = 10000

type manualChapterInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

func validateManualChapter(input manualChapterInput) error {
	if !utf8.ValidString(input.Title) || utf8.RuneCountInString(strings.TrimSpace(input.Title)) > 200 || strings.ContainsAny(input.Title, "\x00\r\n") {
		return fmt.Errorf("%w: 章节名称须为不超过 200 字的单行文本", store.ErrWorkbenchPlanInvalid)
	}
	if !utf8.ValidString(input.Content) || len(input.Content) > maxWorkbenchBodyBytes {
		return fmt.Errorf("%w: 正文须为 UTF-8 文本，且不超过 8 MiB", store.ErrWorkbenchPlanInvalid)
	}
	return nil
}

func manualChapterTitle(title string, chapter int) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	return fmt.Sprintf("第 %d 章", chapter)
}

// createManualProjectLocked is shared with document import. The caller owns
// s.mu. It creates a separate workspace without changing the active project.
// Finalizers write source-specific files while the same lease is held. Publishing
// web-project.json last keeps partial imports out of every instance's book list.
func (s *Server) createManualProjectLocked(name string, chapters []manualChapterInput, finalize ...func(string) error) (directory string, err error) {
	name = strings.TrimSpace(name)
	if !validProjectName(name) || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("%w: 请输入 1–80 字的项目名称", store.ErrWorkbenchPlanInvalid)
	}
	if len(chapters) > maxManualChapters {
		return "", fmt.Errorf("%w: 章节数不能超过 %d", store.ErrWorkbenchPlanInvalid, maxManualChapters)
	}
	for _, chapter := range chapters {
		if err := validateManualChapter(chapter); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(s.booksDir, 0700); err != nil {
		return "", err
	}
	directory, err = os.MkdirTemp(s.booksDir, time.Now().Format("20060102-150405")+"-manual-*")
	if err != nil {
		return "", err
	}
	created := directory
	defer func() {
		if err != nil {
			_ = os.RemoveAll(created)
		}
	}()
	err = withWorkspaceLease(directory, func() error {
		st := store.NewStore(directory)
		if err := st.Init(); err != nil {
			return err
		}
		entries := make([]domain.OutlineEntry, 0, len(chapters))
		progress := newManualProgress()
		for i, chapter := range chapters {
			number := i + 1
			title := manualChapterTitle(chapter.Title, number)
			entries = append(entries, domain.OutlineEntry{Chapter: number, Title: title, VolumeID: 1, VolumeTitle: "正文", Scenes: []string{}})
			if err := st.Drafts.SaveFinalChapter(number, chapter.Content); err != nil {
				return err
			}
			if strings.TrimSpace(chapter.Content) != "" {
				progress.CompletedChapters = append(progress.CompletedChapters, number)
				progress.ChapterWordCounts[number] = domain.WordCount(chapter.Content)
				if _, err := st.ChapterVersions.EnsureCurrent(number, title, chapter.Content); err != nil {
					return err
				}
			}
		}
		finalizeManualProgress(progress, len(chapters))
		if err := st.Outline.SaveOutline(entries); err != nil {
			return err
		}
		if err := st.Progress.Save(progress); err != nil {
			return err
		}
		if err := st.Book.Save(domain.BookMetadata{Title: name, Synopsis: "手动创建或导入的作品，可在章节工作台自由编辑。"}); err != nil {
			return err
		}
		for _, finish := range finalize {
			if err := finish(directory); err != nil {
				return err
			}
		}
		now := time.Now().Format(time.RFC3339)
		return saveProject(directory, projectInfo{Mode: "manual", Name: name, CreatedAt: now, UpdatedAt: now})
	})
	if err != nil {
		return "", err
	}
	return directory, nil
}

func newManualProgress() *domain.Progress {
	return &domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 1, CompletedChapters: []int{}, ChapterWordCounts: map[int]int{}}
}

func updateManualProgress(progress *domain.Progress, chapter, total int, content string) {
	if progress.ChapterWordCounts == nil {
		progress.ChapterWordCounts = map[int]int{}
	}
	progress.CompletedChapters = slices.DeleteFunc(progress.CompletedChapters, func(n int) bool { return n == chapter })
	delete(progress.ChapterWordCounts, chapter)
	if strings.TrimSpace(content) != "" {
		progress.CompletedChapters = append(progress.CompletedChapters, chapter)
		progress.ChapterWordCounts[chapter] = domain.WordCount(content)
	}
	finalizeManualProgress(progress, total)
}

func finalizeManualProgress(progress *domain.Progress, total int) {
	slices.Sort(progress.CompletedChapters)
	progress.TotalWordCount = 0
	for _, count := range progress.ChapterWordCounts {
		progress.TotalWordCount += count
	}
	progress.TotalChapters, progress.Phase = total, domain.PhaseWriting
	progress.CurrentChapter = 1
	for _, chapter := range progress.CompletedChapters {
		if chapter > progress.CurrentChapter {
			break
		}
		if chapter == progress.CurrentChapter {
			progress.CurrentChapter++
		}
	}
}

func manualOutlineFingerprint(entries []domain.OutlineEntry) (string, error) {
	data, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// manualFilesTransaction restores all sources if one write fails. Callers hold
// the project lease so neither another window nor the AI engine can race it.
func manualFilesTransaction(directory string, paths []string, write func() error) error {
	before := make([]projectFileSnapshot, 0, len(paths))
	for _, relative := range paths {
		path := filepath.Join(directory, filepath.FromSlash(relative))
		for current := path; current != directory; current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (current == path && !info.Mode().IsRegular()) || (current != path && !info.IsDir()) {
				return fmt.Errorf("作品文件不安全：%s", relative)
			}
		}
		snapshot, err := snapshotProjectFile(path)
		if err != nil {
			return err
		}
		before = append(before, snapshot)
	}
	if err := write(); err != nil {
		for i := len(before) - 1; i >= 0; i-- {
			err = errors.Join(err, before[i].restore())
		}
		return err
	}
	return nil
}

// writeManualChapter updates plain manuscripts without inventing an AI facts
// baseline or making an AI planning approval. A nil content preserves the body.
func writeManualChapter(st *store.Store, chapter int, entry domain.OutlineEntry, content *string, create bool, activateVersion ...string) error {
	project, err := readProject(st.Dir())
	if err != nil {
		return err
	}
	if project.Mode != "manual" {
		return fmt.Errorf("%w: 只能在手动项目中直接添加和编辑章节", store.ErrWorkbenchPlanInvalid)
	}
	if project.Archived {
		return fmt.Errorf("%w: 项目已归档", errChapterEditBusy)
	}
	if err := validateManualChapter(manualChapterInput{Title: entry.Title}); err != nil {
		return err
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
	entries, err := st.Outline.LoadOutlineView()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(entries, func(e domain.OutlineEntry) bool { return e.Chapter == chapter })
	if create && (index >= 0 || len(entries) >= maxManualChapters) {
		return errChapterEditChanged
	}
	if !create && index < 0 {
		return store.ErrOutlineChapterNotFound
	}
	entry.Chapter, entry.Title = chapter, manualChapterTitle(entry.Title, chapter)
	if entry.VolumeTitle == "" {
		entry.VolumeTitle = "正文"
	}
	if entry.VolumeID <= 0 {
		entry.VolumeID = 1
	}
	if create {
		entries = append(entries, entry)
	} else {
		entries[index] = entry
	}
	previous, err := st.Drafts.LoadChapterText(chapter)
	if err != nil {
		return err
	}
	if content != nil {
		if err := validateManualChapter(manualChapterInput{Content: *content}); err != nil {
			return err
		}
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return err
	}
	if progress == nil {
		progress = newManualProgress()
	}
	body := previous
	if content != nil {
		body = *content
	}
	updateManualProgress(progress, chapter, len(entries), body)
	paths := []string{"outline.json", "outline.md", "meta/progress.json", "meta/web-project.json", fmt.Sprintf("chapters/%02d.md", chapter), fmt.Sprintf("meta/chapter_versions/%06d.json", chapter)}
	return manualFilesTransaction(st.Dir(), paths, func() error {
		if strings.TrimSpace(previous) != "" {
			if _, err := st.ChapterVersions.EnsureCurrent(chapter, entry.Title, previous); err != nil {
				return err
			}
		}
		if len(activateVersion) > 0 {
			if err := st.ChapterVersions.Activate(chapter, activateVersion[0]); err != nil {
				return err
			}
		} else if content != nil && body != previous && strings.TrimSpace(body) != "" {
			version, err := st.ChapterVersions.Append(chapter, entry.Title, body, "", "manual")
			if err != nil {
				return err
			}
			if err := st.ChapterVersions.Activate(chapter, version.ID); err != nil {
				return err
			}
		}
		if err := st.Outline.SaveOutline(entries); err != nil {
			return err
		}
		if content != nil {
			if err := st.Drafts.SaveFinalChapter(chapter, body); err != nil {
				return err
			}
		}
		if err := st.Progress.Save(progress); err != nil {
			return err
		}
		project.UpdatedAt = time.Now().Format(time.RFC3339)
		return saveProject(st.Dir(), project)
	})
}

func (s *Server) createManualProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stopping {
		fail(w, http.StatusConflict, "请等待当前操作结束")
		return
	}
	directory, err := s.createManualProjectLocked(req.Name, nil)
	if err != nil {
		s.chapterEditErrorLocked(w, err)
		return
	}
	s.broadcastLocked("workspace", nil)
	reply(w, http.StatusCreated, map[string]any{"directory": directory, "manual": true})
}

func (s *Server) createManualChapter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
		manualChapterInput
	}
	if !readJSONLimit(w, r, &req, maxWorkbenchBodyBytes+(1<<20)) {
		return
	}
	if err := validateManualChapter(req.manualChapterInput); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.versionDirectoryLocked(w, req.Directory) {
		return
	}
	if s.projectRunningLocked(req.Directory) {
		fail(w, http.StatusConflict, "请先暂停并等待当前操作结束")
		return
	}
	var after chapterEditView
	err := s.withVersionSnapshotLocked(func() error {
		st := store.NewStore(req.Directory)
		entries, err := st.Outline.LoadOutlineView()
		if err != nil {
			return err
		}
		chapter := 1
		for _, entry := range entries {
			chapter = max(chapter, entry.Chapter+1)
		}
		entry := domain.OutlineEntry{Title: req.Title, Scenes: []string{}}
		if err := writeManualChapter(st, chapter, entry, &req.Content, true); err != nil {
			return err
		}
		after, err = readChapterEdit(st, chapter)
		return err
	})
	if err != nil {
		s.chapterEditErrorLocked(w, err)
		return
	}
	s.broadcastLocked("refresh", nil)
	reply(w, http.StatusCreated, after)
}
