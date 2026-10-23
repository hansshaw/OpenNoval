package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var (
	ErrWorkbenchPlanChanged = errors.New("chapter planning changed; reload before saving")
	ErrWorkbenchPlanInvalid = errors.New("invalid chapter planning edit")
)

// WorkbenchChapterPlanEdit changes only the fields the user submitted. A volume
// title belongs to the chapter's existing volume, so renaming it updates every
// chapter in that volume without moving or renumbering any chapter.
type WorkbenchChapterPlanEdit struct {
	Title       *string   `json:"title,omitempty"`
	VolumeTitle *string   `json:"volumeTitle,omitempty"`
	CoreEvent   *string   `json:"coreEvent,omitempty"`
	Hook        *string   `json:"hook,omitempty"`
	Scenes      *[]string `json:"scenes,omitempty"`
}

// UpdateWorkbenchChapterPlan saves a user's direct edit to the authoritative
// plan and rebuilds its derived views. The expected fingerprint prevents a
// stale editor from overwriting a newer plan. Existing prose and story facts
// remain untouched, including for completed novels. A changed plan requires a
// fresh semantic audit and explicit approval before generation resumes.
//
// Callers must also hold the project's directory lease or Host snapshot lock;
// crossMu serializes mutations inside this Store, not independent processes.
func (s *Store) UpdateWorkbenchChapterPlan(chapter int, edit WorkbenchChapterPlanEdit, expectedFingerprint string) (string, error) {
	if chapter <= 0 || expectedFingerprint == "" {
		return "", fmt.Errorf("请选择章节并提供当前规划指纹: %w", ErrWorkbenchPlanInvalid)
	}
	edit, err := normalizedWorkbenchPlanEdit(edit)
	if err != nil {
		return "", err
	}

	s.crossMu.Lock()
	defer s.crossMu.Unlock()
	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()
	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()
	s.Signals.io.mu.Lock()
	defer s.Signals.io.mu.Unlock()
	s.Drafts.io.mu.Lock()
	defer s.Drafts.io.mu.Unlock()
	s.ChapterRecords.io.mu.Lock()
	defer s.ChapterRecords.io.mu.Unlock()
	// LoadSummaryTitle takes the cache lock before reading the summary file.
	s.Summaries.titleMu.Lock()
	defer s.Summaries.titleMu.Unlock()
	s.Summaries.io.mu.Lock()
	defer s.Summaries.io.mu.Unlock()

	current, err := s.PlanningFingerprint()
	if err != nil {
		return "", fmt.Errorf("当前故事内核或规划文件尚未生成完整，请完成规划后重试: %w: %w", ErrWorkbenchPlanInvalid, err)
	}
	if current != expectedFingerprint {
		return "", ErrWorkbenchPlanChanged
	}
	var pending domain.PendingCommit
	if err := s.Signals.io.ReadJSONUnlocked("meta/pending_commit.json", &pending); err == nil {
		return "", fmt.Errorf("第 %d 章提交尚未恢复，不能修改规划", pending.Chapter)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	var pendingRevision domain.PendingRevision
	if err := s.Revisions.io.ReadJSON(pendingRevisionPath, &pendingRevision); err == nil {
		return "", fmt.Errorf("章节修订尚未恢复，不能修改规划")
	} else if !os.IsNotExist(err) {
		return "", err
	}

	var volumes []domain.VolumeOutline
	if err := s.Outline.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var save func() error
	changed := false
	affected := map[int]bool{chapter: true}
	if len(volumes) > 0 {
		repairMissingLayeredIndexes(volumes)
		found := false
		number := 1
		for vi := range volumes {
			for ai := range volumes[vi].Arcs {
				arc := &volumes[vi].Arcs[ai]
				count := len(arc.DetailedChapters())
				if chapter >= number && chapter < number+count {
					found = true
					changed = applyWorkbenchChapterEdit(&arc.Chapters[chapter-number], edit)
					if edit.VolumeTitle != nil && volumes[vi].Title != *edit.VolumeTitle {
						volumes[vi].Title = *edit.VolumeTitle
						changed = true
						for _, item := range domain.FlattenOutline(volumes) {
							if item.VolumeID == volumes[vi].Index {
								affected[item.Chapter] = true
							}
						}
					}
				}
				number += count
			}
		}
		if !found {
			return "", ErrOutlineChapterNotFound
		}
		save = func() error { return s.Outline.saveLayeredViewsUnlocked(volumes) }
	} else {
		var entries []domain.OutlineEntry
		if err := s.Outline.io.ReadJSONUnlocked("outline.json", &entries); err != nil {
			return "", err
		}
		target := -1
		for i := range entries {
			if entries[i].Chapter == chapter {
				target = i
				break
			}
		}
		if target < 0 {
			return "", ErrOutlineChapterNotFound
		}
		changed = applyWorkbenchChapterEdit(&entries[target], edit)
		if edit.VolumeTitle != nil {
			volume := max(1, entries[target].VolumeID)
			for i := range entries {
				if max(1, entries[i].VolumeID) == volume && entries[i].VolumeTitle != *edit.VolumeTitle {
					entries[i].VolumeTitle = *edit.VolumeTitle
					changed = true
					affected[entries[i].Chapter] = true
				}
			}
		}
		save = func() error { return s.Outline.saveOutlineUnlocked(entries) }
	}
	var summary *domain.ChapterSummary
	var record *domain.ChapterRecord
	if edit.Title != nil {
		var sum domain.ChapterSummary
		if err := s.Summaries.io.ReadJSONUnlocked(fmt.Sprintf("summaries/%02d.json", chapter), &sum); err == nil {
			if sum.Chapter != chapter {
				return "", fmt.Errorf("章节摘要编号与目标章不一致")
			}
			if sum.Title != *edit.Title {
				sum.Title = *edit.Title
				summary = &sum
				changed = true
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		var saved domain.ChapterRecord
		if err := s.ChapterRecords.io.ReadJSONUnlocked(ChapterRecordPath(chapter), &saved); err == nil {
			if err := validateChapterRecord(saved); err != nil {
				return "", err
			}
			if saved.Chapter != chapter {
				return "", fmt.Errorf("章节接纳记录编号与目标章不一致")
			}
			if saved.Facts.Title != *edit.Title {
				saved.Facts.Title = *edit.Title
				saved.Revision++
				saved.AcceptedAt = time.Now()
				record = &saved
				changed = true
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if !changed {
		return current, nil
	}
	meta, err := s.RunMeta.loadUnlocked()
	if err != nil {
		return "", err
	}
	if meta == nil {
		meta = &domain.RunMeta{AdvanceMode: domain.ChapterAdvanceAuto}
	}
	if meta.Workbench == nil {
		meta.Workbench = &domain.WorkbenchControl{BatchSize: 1}
	}
	meta.Workbench.PlanningRevisionPending = true
	meta.Workbench.PlanningManualEdit = true
	meta.Workbench.PlanningManualAuditFingerprint = ""
	meta.Workbench.PlanningRevisionRequest = fmt.Sprintf("用户已在章节工作台手动保存第 %d 章的规划或所属卷名。请审查当前已落盘结果与第一阶段原则、已有正文的一致性；保留用户手动内容，不得擅自恢复旧规划或改写正文。如有冲突，列出问题供用户处理。", chapter)
	meta.Workbench.ApprovedFingerprint = ""
	meta.Workbench.ApprovedFoundationFingerprint = ""
	meta.Workbench.BatchEnd = 0

	snapshots, err := s.snapshotOutlineMutationFiles()
	if err != nil {
		return "", err
	}
	rels := []string{"meta/run.json"}
	if summary != nil {
		rels = append(rels, fmt.Sprintf("summaries/%02d.json", chapter))
	}
	if record != nil {
		rels = append(rels, ChapterRecordPath(chapter))
	}
	chapters := make([]int, 0, len(affected))
	for number := range affected {
		chapters = append(chapters, number)
	}
	slices.Sort(chapters)
	for _, number := range chapters {
		rels = append(rels, fmt.Sprintf("drafts/%02d.plan.json", number))
	}
	for _, rel := range rels {
		file, err := s.snapshotWorkbenchPlanFile(rel)
		if err != nil {
			return "", err
		}
		snapshots = append(snapshots, file)
	}
	err = func() error {
		if err := save(); err != nil {
			return err
		}
		if summary != nil {
			if err := s.Summaries.io.WriteJSONUnlocked(fmt.Sprintf("summaries/%02d.json", chapter), summary); err != nil {
				return err
			}
		}
		if record != nil {
			if err := s.ChapterRecords.io.WriteJSONUnlocked(ChapterRecordPath(chapter), record); err != nil {
				return err
			}
		}
		for _, number := range chapters {
			if err := s.Drafts.io.RemoveFileUnlocked(fmt.Sprintf("drafts/%02d.plan.json", number)); err != nil {
				return err
			}
		}
		return s.RunMeta.saveUnlocked(*meta)
	}()
	var updated string
	if err == nil {
		updated, err = s.PlanningFingerprint()
	}
	if err != nil {
		return "", errors.Join(err, s.restoreOutlineMutationFiles(snapshots))
	}
	if summary != nil {
		s.Summaries.titleCache[chapter] = summary.Title
	}
	return updated, nil
}

func (s *Store) snapshotWorkbenchPlanFile(rel string) (outlineMutationSnapshot, error) {
	path := filepath.Join(s.dir, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return outlineMutationSnapshot{rel: rel}, nil
	}
	if err != nil {
		return outlineMutationSnapshot{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return outlineMutationSnapshot{}, err
	}
	return outlineMutationSnapshot{rel: rel, data: data, mode: info.Mode().Perm(), exists: true}, nil
}

func normalizedWorkbenchPlanEdit(edit WorkbenchChapterPlanEdit) (WorkbenchChapterPlanEdit, error) {
	if edit.Title == nil && edit.VolumeTitle == nil && edit.CoreEvent == nil && edit.Hook == nil && edit.Scenes == nil {
		return edit, fmt.Errorf("没有要保存的规划字段: %w", ErrWorkbenchPlanInvalid)
	}
	for _, field := range []struct {
		name  string
		value **string
		limit int
		label bool
	}{{"章节名", &edit.Title, 300, true}, {"卷名", &edit.VolumeTitle, 300, true}, {"核心事件", &edit.CoreEvent, 50000, false}, {"章末钩子", &edit.Hook, 50000, false}} {
		if *field.value == nil {
			continue
		}
		value := strings.TrimSpace(**field.value)
		if (value == "" && field.name != "章末钩子") || !utf8.ValidString(value) || utf8.RuneCountInString(value) > field.limit || (field.label && strings.IndexFunc(value, unicode.IsControl) >= 0) {
			return edit, fmt.Errorf("%s不能为空或超出长度限制: %w", field.name, ErrWorkbenchPlanInvalid)
		}
		*field.value = &value
	}
	if edit.Scenes != nil {
		if len(*edit.Scenes) > 200 {
			return edit, fmt.Errorf("场景最多 200 项: %w", ErrWorkbenchPlanInvalid)
		}
		scenes := make([]string, 0, len(*edit.Scenes))
		for _, raw := range *edit.Scenes {
			value := strings.TrimSpace(raw)
			if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 20000 {
				return edit, fmt.Errorf("场景不能为空或超出长度限制: %w", ErrWorkbenchPlanInvalid)
			}
			scenes = append(scenes, value)
		}
		edit.Scenes = &scenes
	}
	return edit, nil
}

func applyWorkbenchChapterEdit(entry *domain.OutlineEntry, edit WorkbenchChapterPlanEdit) bool {
	before := *entry
	if edit.Title != nil {
		entry.Title = *edit.Title
	}
	if edit.CoreEvent != nil {
		entry.CoreEvent = *edit.CoreEvent
	}
	if edit.Hook != nil {
		entry.Hook = *edit.Hook
	}
	if edit.Scenes != nil {
		entry.Scenes = *edit.Scenes
	}
	return !reflect.DeepEqual(before, *entry)
}
