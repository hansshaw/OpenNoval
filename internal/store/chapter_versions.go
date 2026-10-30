package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hansshaw/OpenNovel/internal/domain"
)

var ErrChapterVersionNotFound = errors.New("chapter version not found")

type ChapterVersionStore struct {
	io *IO
	mu *sync.Mutex
}

var chapterVersionLocks sync.Map

func NewChapterVersionStore(io *IO) *ChapterVersionStore {
	key, err := filepath.Abs(io.dir)
	if err != nil {
		key = filepath.Clean(io.dir)
	}
	if resolved, err := filepath.EvalSymlinks(key); err == nil {
		key = resolved
	}
	lock, _ := chapterVersionLocks.LoadOrStore(key, &sync.Mutex{})
	return &ChapterVersionStore{io: io, mu: lock.(*sync.Mutex)}
}

type chapterVersionsFile struct {
	Chapter  int                     `json:"chapter"`
	ActiveID string                  `json:"activeId,omitempty"`
	Versions []domain.ChapterVersion `json:"versions"`
}

var chapterVersionID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func chapterVersionsPath(chapter int) string {
	return fmt.Sprintf("meta/chapter_versions/%06d.json", chapter)
}

func (s *ChapterVersionStore) loadUnlocked(chapter int) (chapterVersionsFile, error) {
	file := chapterVersionsFile{Chapter: chapter, Versions: []domain.ChapterVersion{}}
	if chapter <= 0 {
		return file, fmt.Errorf("无效章节")
	}
	if err := s.io.ReadJSONUnlocked(chapterVersionsPath(chapter), &file); err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return file, err
	}
	if file.Chapter != chapter {
		return file, fmt.Errorf("章节版本记录不匹配")
	}
	seen := map[string]bool{}
	for i, version := range file.Versions {
		if !chapterVersionID.MatchString(version.ID) || seen[version.ID] || version.Chapter != chapter || version.Number != i+1 || version.ContentHash != domain.ChapterContentSHA256(version.Content) {
			return file, fmt.Errorf("章节版本记录损坏")
		}
		seen[version.ID] = true
	}
	return file, nil
}

// EnsureCurrent archives the existing manuscript before any rewrite or manual
// edit. Repeated reads with unchanged content do not add duplicate versions.
func (s *ChapterVersionStore) EnsureCurrent(chapter int, title, content string) (domain.ChapterVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result domain.ChapterVersion
	err := s.io.WithWriteLock(func() error {
		file, err := s.loadUnlocked(chapter)
		if err != nil {
			return err
		}
		hash := domain.ChapterContentSHA256(content)
		for i := len(file.Versions) - 1; i >= 0; i-- {
			if file.Versions[i].ContentHash == hash {
				result = file.Versions[i]
				return nil
			}
		}
		result, err = makeChapterVersion(chapter, len(file.Versions)+1, title, content, "", "original")
		if err != nil {
			return err
		}
		file.Versions = append(file.Versions, result)
		file.ActiveID = result.ID
		return s.io.WriteJSONUnlocked(chapterVersionsPath(chapter), file)
	})
	return result, err
}

func makeChapterVersion(chapter, number int, title, content, instruction, kind string) (domain.ChapterVersion, error) {
	if chapter <= 0 || strings.TrimSpace(content) == "" {
		return domain.ChapterVersion{}, fmt.Errorf("章节正文不能为空")
	}
	if len(content) > 8<<20 {
		return domain.ChapterVersion{}, fmt.Errorf("章节正文不能超过 8 MiB")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return domain.ChapterVersion{}, err
	}
	return domain.ChapterVersion{ID: hex.EncodeToString(id[:]), Number: number, Chapter: chapter, Title: title,
		Content: content, ContentHash: domain.ChapterContentSHA256(content), Instruction: instruction, Kind: kind, CreatedAt: time.Now()}, nil
}

func (s *ChapterVersionStore) Append(chapter int, title, content, instruction, kind string) (domain.ChapterVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result domain.ChapterVersion
	err := s.io.WithWriteLock(func() error {
		file, err := s.loadUnlocked(chapter)
		if err != nil {
			return err
		}
		result, err = makeChapterVersion(chapter, len(file.Versions)+1, title, content, instruction, kind)
		if err != nil {
			return err
		}
		file.Versions = append(file.Versions, result)
		return s.io.WriteJSONUnlocked(chapterVersionsPath(chapter), file)
	})
	return result, err
}

func (s *ChapterVersionStore) List(chapter int, currentContent string) ([]domain.ChapterVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.ChapterVersion
	err := s.io.WithWriteLock(func() error {
		file, err := s.loadUnlocked(chapter)
		if err != nil {
			return err
		}
		hash := domain.ChapterContentSHA256(currentContent)
		active := -1
		for i, version := range file.Versions {
			if version.ContentHash == hash {
				active = i
			}
			if version.ID == file.ActiveID && version.ContentHash == hash {
				active = i
				break
			}
		}
		result = file.Versions
		for i := range result {
			result[i].Active = i == active
		}
		return nil
	})
	return result, err
}

func (s *ChapterVersionStore) Get(chapter int, id string) (domain.ChapterVersion, error) {
	if !chapterVersionID.MatchString(id) {
		return domain.ChapterVersion{}, ErrChapterVersionNotFound
	}
	versions, err := s.List(chapter, "")
	if err != nil {
		return domain.ChapterVersion{}, err
	}
	for _, version := range versions {
		if version.ID == id {
			return version, nil
		}
	}
	return domain.ChapterVersion{}, ErrChapterVersionNotFound
}

func (s *ChapterVersionStore) Activate(chapter int, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.io.WithWriteLock(func() error {
		file, err := s.loadUnlocked(chapter)
		if err != nil {
			return err
		}
		for _, version := range file.Versions {
			if version.ID == id {
				file.ActiveID = id
				return s.io.WriteJSONUnlocked(chapterVersionsPath(chapter), file)
			}
		}
		return ErrChapterVersionNotFound
	})
}

// SaveManual is deliberately offline: the user's save is durable immediately.
// ChapterRecords remains the last accepted facts baseline, so revision.Scan
// prevents generation until the existing revision-sync flow rebuilds it.
func (s *ChapterVersionStore) SaveManual(chapter int, title, content, expectedSHA string) (domain.ChapterVersion, error) {
	return s.saveContent(chapter, "", title, content, expectedSHA)
}

func (s *ChapterVersionStore) Apply(chapter int, id, expectedSHA string) (domain.ChapterVersion, error) {
	if !chapterVersionID.MatchString(id) {
		return domain.ChapterVersion{}, ErrChapterVersionNotFound
	}
	return s.saveContent(chapter, id, "", "", expectedSHA)
}

func (s *ChapterVersionStore) saveContent(chapter int, id, title, content, expectedSHA string) (domain.ChapterVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result domain.ChapterVersion
	err := s.io.WithWriteLock(func() error {
		if expectedSHA == "" {
			return fmt.Errorf("缺少正文版本，请刷新后再保存")
		}
		if pending, err := NewSignalStore(newIO(s.io.dir)).LoadPendingCommit(); err != nil {
			return err
		} else if pending != nil {
			return fmt.Errorf("章节提交尚未完成，请先恢复任务")
		}
		if pending, err := NewRevisionStore(newIO(s.io.dir)).LoadPending(); err != nil {
			return err
		} else if pending != nil {
			return fmt.Errorf("修订同步尚未完成，请先同步修订")
		}
		progress, err := NewProgressStore(newIO(s.io.dir)).Load()
		if err != nil {
			return err
		}
		completed := false
		if progress != nil {
			for _, number := range progress.CompletedChapters {
				if number == chapter {
					completed = true
					break
				}
			}
		}
		if !completed {
			return fmt.Errorf("请选择已完成的章节")
		}
		previousProgress, err := s.io.ReadFileUnlocked("meta/progress.json")
		if err != nil {
			return err
		}
		baseline, err := NewChapterRecordStore(newIO(s.io.dir)).Load(chapter)
		if err != nil {
			return err
		}
		if baseline == nil {
			return fmt.Errorf("本章缺少接纳记录，请先重新连接工作台完成旧版迁移后再保存正文")
		}
		current, err := s.io.ReadFileUnlocked(fmt.Sprintf("chapters/%02d.md", chapter))
		if err != nil {
			return err
		}
		if domain.ChapterContentSHA256(string(current)) != expectedSHA {
			return fmt.Errorf("正文已变化，请刷新后再保存或覆盖")
		}
		file, err := s.loadUnlocked(chapter)
		if err != nil {
			return err
		}
		foundCurrent := false
		for _, version := range file.Versions {
			if version.ContentHash == expectedSHA {
				foundCurrent = true
				break
			}
		}
		if !foundCurrent {
			originalTitle := title
			if summaryTitle, err := NewSummaryStore(newIO(s.io.dir), NewOutlineStore(newIO(s.io.dir))).LoadSummaryTitle(chapter); err == nil && summaryTitle != "" {
				originalTitle = summaryTitle
			}
			original, err := makeChapterVersion(chapter, len(file.Versions)+1, originalTitle, string(current), "", "original")
			if err != nil {
				return err
			}
			file.Versions = append(file.Versions, original)
			file.ActiveID = original.ID
		}
		if id != "" {
			for _, version := range file.Versions {
				if version.ID == id {
					result = version
					break
				}
			}
			if result.ID == "" {
				return ErrChapterVersionNotFound
			}
		} else {
			result, err = makeChapterVersion(chapter, len(file.Versions)+1, title, content, "用户手动编辑并保存", "manual")
			if err != nil {
				return err
			}
			file.Versions = append(file.Versions, result)
		}
		// Save both old and new manuscripts before touching the active file.
		if err := s.io.WriteJSONUnlocked(chapterVersionsPath(chapter), file); err != nil {
			return err
		}
		rollback := func(cause error) error {
			result.Active = false
			// Recovery writes bypass the injected/failing mutation path: a failed
			// save must never report failure while silently replacing the original.
			return errors.Join(cause,
				s.io.writeFileUnlockedRaw(fmt.Sprintf("chapters/%02d.md", chapter), current, 0o644),
				s.io.writeFileUnlockedRaw("meta/progress.json", previousProgress, 0o644))
		}
		if err := s.io.WriteMarkdownUnlocked(fmt.Sprintf("chapters/%02d.md", chapter), result.Content); err != nil {
			return err
		}
		if progress.ChapterWordCounts == nil {
			progress.ChapterWordCounts = map[int]int{}
		}
		progress.TotalWordCount += domain.WordCount(result.Content) - progress.ChapterWordCounts[chapter]
		progress.ChapterWordCounts[chapter] = domain.WordCount(result.Content)
		if err := s.io.WriteJSONUnlocked("meta/progress.json", progress); err != nil {
			return rollback(err)
		}
		file.ActiveID = result.ID
		result.Active = true
		if err := s.io.WriteJSONUnlocked(chapterVersionsPath(chapter), file); err != nil {
			return rollback(err)
		}
		return nil
	})
	return result, err
}
