package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// Store 是状态管理的组合根，持有所有子存储。
type Store struct {
	dir string

	Progress        *ProgressStore
	Book            *BookStore
	Outline         *OutlineStore
	Drafts          *DraftStore
	Summaries       *SummaryStore
	RunMeta         *RunMetaStore
	UserRules       *UserRulesStore
	Signals         *SignalStore
	Runtime         *RuntimeStore
	Characters      *CharacterStore
	World           *WorldStore
	Checkpoints     *CheckpointStore
	Sessions        *SessionStore
	Usage           *UsageStore
	Simulation      *SimulationStore
	Decisions       *DecisionStore
	ChapterRecords  *ChapterRecordStore
	ChapterVersions *ChapterVersionStore
	Revisions       *RevisionStore

	crossMu sync.Mutex // 串行化跨域协调；不代表多个文件具备事务原子性
}

const (
	LegacyProjectFormatVersion        = 1
	ChapterRecordProjectFormatVersion = 2
	CurrentProjectFormatVersion       = 3
	projectFormatPath                 = "meta/format.json"
)

type projectFormat struct {
	Version int `json:"version"`
}

// NewStore 创建状态管理器，dir 为小说输出根目录。
func NewStore(dir string) *Store {
	io := newIO(dir)
	outline := NewOutlineStore(io)
	return &Store{
		dir:             dir,
		Progress:        NewProgressStore(newIO(dir)),
		Book:            NewBookStore(newIO(dir)),
		Outline:         outline,
		Drafts:          NewDraftStore(newIO(dir)),
		Summaries:       NewSummaryStore(newIO(dir), outline),
		RunMeta:         NewRunMetaStore(newIO(dir)),
		UserRules:       NewUserRulesStore(newIO(dir)),
		Signals:         NewSignalStore(newIO(dir)),
		Runtime:         NewRuntimeStore(newIO(dir)),
		Characters:      NewCharacterStore(newIO(dir), outline),
		World:           NewWorldStore(newIO(dir)),
		Checkpoints:     NewCheckpointStore(io),
		Sessions:        NewSessionStore(newIO(dir)),
		Usage:           NewUsageStore(newIO(dir)),
		Simulation:      NewSimulationStore(newIO(dir)),
		Decisions:       NewDecisionStore(newIO(dir)),
		ChapterRecords:  NewChapterRecordStore(newIO(dir)),
		ChapterVersions: NewChapterVersionStore(newIO(dir)),
		Revisions:       NewRevisionStore(newIO(dir)),
	}
}

// Dir 返回输出根目录。
func (s *Store) Dir() string { return s.dir }

// LoadProjectFormatVersion 返回作品目录的数据格式版本。旧作品没有版本文件，
// 视为 v1，由启动迁移统一升级，业务代码无需保留旧格式分支。
func (s *Store) LoadProjectFormatVersion() (int, error) {
	var format projectFormat
	if err := s.Progress.io.ReadJSON(projectFormatPath, &format); err != nil {
		if os.IsNotExist(err) {
			return LegacyProjectFormatVersion, nil
		}
		return 0, err
	}
	if format.Version <= 0 {
		return 0, fmt.Errorf("项目格式版本无效: %d", format.Version)
	}
	return format.Version, nil
}

// SaveProjectFormatVersion 在一次迁移全部完成后原子更新项目格式版本。
func (s *Store) SaveProjectFormatVersion(version int) error {
	if version <= 0 {
		return fmt.Errorf("项目格式版本必须大于 0: %d", version)
	}
	return s.Progress.io.WriteJSON(projectFormatPath, projectFormat{Version: version})
}

// CheckConsistency 对事实层做一次浅层校验，用于启动/恢复时生成 warning。
// 纯只读：不修正数据，仅返回可读的问题描述。调用方决定如何展示（log / UI）。
// 为避免扫全目录带来的 IO 开销，只校验 Progress 的关键点：
//   - 最后一个完成章节必须在 chapters/ 下存在终稿
//   - Layered 模式下，当前 Volume/Arc 必须能在 layered_outline 中找到
func (s *Store) CheckConsistency() []string {
	var warnings []string
	progress, err := s.Progress.Load()
	if err != nil {
		return append(warnings, fmt.Sprintf("progress 读取失败: %v", err))
	}
	if progress == nil {
		return warnings
	}
	if n := len(progress.CompletedChapters); n > 0 {
		lastCh := progress.CompletedChapters[n-1]
		if text, err := s.Drafts.LoadChapterText(lastCh); err != nil {
			warnings = append(warnings, fmt.Sprintf("第 %d 章终稿读取失败: %v", lastCh, err))
		} else if text == "" {
			warnings = append(warnings, fmt.Sprintf("progress 标记第 %d 章已完成，但 chapters/%02d.md 不存在或为空", lastCh, lastCh))
		}
	}
	if progress.Layered && progress.CurrentVolume > 0 && progress.CurrentArc > 0 {
		volumes, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("分层大纲读取失败: %v", err))
		} else if len(volumes) > 0 {
			found := false
			for _, v := range volumes {
				if v.Index != progress.CurrentVolume {
					continue
				}
				for _, a := range v.Arcs {
					if a.Index == progress.CurrentArc {
						found = true
						break
					}
				}
				break
			}
			if !found {
				warnings = append(warnings, fmt.Sprintf("progress 当前 V%d A%d 在分层大纲中找不到对应条目", progress.CurrentVolume, progress.CurrentArc))
			}
		}
	}
	return warnings
}

// FoundationMissing 返回初始规划中尚缺的作品信息与基础设定，顺序稳定。
// 长篇模式（已有 layered_outline）额外要求 compass。读取失败必须原样返回，不能把
// 损坏或无权限读取的工件误判成“尚未创建”，否则调用方可能覆盖真实数据。
func (s *Store) FoundationMissing() ([]string, error) {
	var missing []string
	book, err := s.Book.Load()
	if err != nil {
		return nil, fmt.Errorf("load book metadata: %w", err)
	}
	if book == nil {
		missing = append(missing, "book")
	}
	premise, err := s.Outline.LoadPremise()
	if err != nil {
		return nil, fmt.Errorf("load premise: %w", err)
	}
	if premise == "" {
		missing = append(missing, "premise")
	}
	outline, err := s.Outline.LoadOutlineView()
	if err != nil {
		return nil, fmt.Errorf("load outline: %w", err)
	}
	if len(outline) == 0 {
		missing = append(missing, "outline")
	}
	characters, err := s.Characters.Load()
	if err != nil {
		return nil, fmt.Errorf("load characters: %w", err)
	}
	if len(characters) == 0 {
		missing = append(missing, "characters")
	}
	rules, err := s.World.LoadWorldRules()
	if err != nil {
		return nil, fmt.Errorf("load world rules: %w", err)
	}
	if len(rules) == 0 {
		missing = append(missing, "world_rules")
	}
	layered, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, fmt.Errorf("load layered outline: %w", err)
	}
	if len(layered) > 0 {
		compass, err := s.Outline.LoadCompass()
		if err != nil {
			return nil, fmt.Errorf("load compass: %w", err)
		}
		if compass == nil {
			missing = append(missing, "compass")
		}
	}
	// 新书只有经过模型对已落盘工件的显式语义审查，才允许从规划进入写作。
	// PhaseWriting/Complete 代表旧书或已审查的新书，保持历史项目兼容；审查本身
	// 是一个动作而非文件缺失，因此只在其它工件齐全时追加。
	if len(missing) == 0 {
		progress, err := s.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress: %w", err)
		}
		meta, err := s.RunMeta.Load()
		if err != nil {
			return nil, fmt.Errorf("load planning revision state: %w", err)
		}
		revisionPending := meta != nil && meta.Workbench != nil && meta.Workbench.PlanningRevisionPending
		if revisionPending || progress == nil || (progress.Phase != domain.PhaseWriting && progress.Phase != domain.PhaseComplete) {
			missing = append(missing, "foundation_audit")
		}
	}
	return missing, nil
}

// FoundationFingerprint 返回当前基础设定工件的内容指纹。Architect 必须把
// novel_context 读到的这个值原样交回审查工具，确保结论针对的是实际落盘版本，
// 而不是会话中尚未保存或已经过期的内容。
func (s *Store) FoundationFingerprint() (string, error) {
	files := []string{"meta/book.json", "premise.md", "outline.json", "characters.json", "world_rules.json"}
	layered, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return "", fmt.Errorf("load layered outline: %w", err)
	}
	if len(layered) > 0 {
		files = append(files, "layered_outline.json", "meta/compass.json")
	}

	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Init 创建所需的子目录结构。
func (s *Store) Init() error {
	if err := s.Checkpoints.InitError(); err != nil {
		return fmt.Errorf("load checkpoints: %w", err)
	}
	return s.Progress.io.EnsureDirs([]string{
		"chapters", "summaries", "drafts", "reviews", "meta", "meta/chapter_records", "meta/runtime", "meta/runtime/tasks", "meta/sessions", "meta/sessions/agents",
	})
}

// ── 跨域协调方法 ──

// ArcPosition 是由分层大纲故事顺序确定的卷弧位置。
type ArcPosition struct {
	Volume int
	Arc    int
}

// ReplaceFlatOutline 全量替换扁平大纲，并同步清理旧的分层来源。
// 已有正文可以保留，但新目录必须覆盖所有已完成章节；非空在写草稿所在章节
// 也必须仍在新目录中。其他 Progress 事实保持不变。
func (s *Store) ReplaceFlatOutline(entries []domain.OutlineEntry) (int, error) {
	if len(entries) == 0 {
		return 0, fmt.Errorf("outline 不能为空: %w", errs.ErrToolPrecondition)
	}
	normalized := append([]domain.OutlineEntry(nil), entries...)
	for i := range normalized {
		normalized[i].Chapter = i + 1
		if err := validateReplacementOutlineEntry(normalized[i], i+1); err != nil {
			return 0, err
		}
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

	targetContract, err := s.loadChapterTargetContractUnlocked()
	if err != nil {
		return 0, err
	}
	if err := targetContract.validateExact(len(normalized)); err != nil {
		return 0, err
	}
	p, preserveDraft, err := s.validateOutlineReplacementUnlocked(len(normalized))
	if err != nil {
		return 0, err
	}
	p, err = replacementProgress(p, len(normalized), preserveDraft)
	if err != nil {
		return 0, err
	}
	p.Layered = false
	p.CurrentVolume = 0
	p.CurrentArc = 0
	if err := s.withOutlineMutationRollback(func() error {
		if err := s.Outline.saveOutlineUnlocked(normalized); err != nil {
			return fmt.Errorf("save outline: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := s.Outline.clearLayeredOutlineUnlocked(); err != nil {
			return fmt.Errorf("clear layered outline: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := s.Progress.saveUnlocked(p); err != nil {
			return fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return len(normalized), nil
}

// ReplaceLayeredOutline 全量替换分层大纲。新目录的每个弧都必须带真实章节，
// system 会重建卷弧索引和扁平派生视图，并按已完成正文安全定位当前卷弧。
// 全量规划代表当前已规划故事的完整终点，因此只把最后一卷规范为收官卷。
func (s *Store) ReplaceLayeredOutline(volumes []domain.VolumeOutline) (int, error) {
	if len(volumes) == 0 {
		return 0, fmt.Errorf("layered_outline 不能为空: %w", errs.ErrToolPrecondition)
	}
	assignLayeredIndexes(volumes)
	normalizeCompletePlanFinale(volumes)
	if err := validateReplacementVolumes(volumes); err != nil {
		return 0, err
	}
	total := len(domain.FlattenOutline(volumes))
	if total == 0 {
		return 0, fmt.Errorf("layered_outline 必须包含真实章节: %w", errs.ErrToolPrecondition)
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

	targetContract, err := s.loadChapterTargetContractUnlocked()
	if err != nil {
		return 0, err
	}
	if err := targetContract.validateExact(total); err != nil {
		return 0, err
	}
	p, preserveDraft, err := s.validateOutlineReplacementUnlocked(total)
	if err != nil {
		return 0, err
	}
	p, err = replacementProgress(p, total, preserveDraft)
	if err != nil {
		return 0, err
	}
	p.Layered = true
	volume, arc, ok := locateReplacementChapter(volumes, p.CurrentChapter)
	if !ok {
		return 0, fmt.Errorf("无法在新分层大纲中定位第 %d 章: %w", p.CurrentChapter, errs.ErrToolPrecondition)
	}
	p.CurrentVolume, p.CurrentArc = volume, arc
	if err := s.withOutlineMutationRollback(func() error {
		if err := s.Outline.saveLayeredViewsUnlocked(volumes); err != nil {
			return fmt.Errorf("save layered outline: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := s.Progress.saveUnlocked(p); err != nil {
			return fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return total, nil
}

func normalizeCompletePlanFinale(volumes []domain.VolumeOutline) {
	for i := range volumes {
		volumes[i].Final = i == len(volumes)-1
	}
}

func (s *Store) validateOutlineReplacementUnlocked(total int) (*domain.Progress, bool, error) {
	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return nil, false, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if p != nil && p.Phase == domain.PhaseComplete {
		return nil, false, fmt.Errorf("全书已完结，不允许全量覆盖大纲；请先重开作品: %w", errs.ErrToolPrecondition)
	}

	var pending domain.PendingCommit
	if err := s.Signals.io.ReadJSONUnlocked("meta/pending_commit.json", &pending); err != nil {
		if !os.IsNotExist(err) {
			return nil, false, fmt.Errorf("load pending commit: %w: %w", errs.ErrStoreRead, err)
		}
	} else {
		return nil, false, fmt.Errorf("第 %d 章存在未恢复的提交事务，不能全量覆盖大纲: %w", pending.Chapter, errs.ErrToolPrecondition)
	}

	if p == nil {
		return nil, false, nil
	}
	if latest := p.LatestCompleted(); total < latest {
		return nil, false, fmt.Errorf(
			"新大纲只有 %d 章，少于已完成的最大章号 %d；全量覆盖不能删除已完成正文的章节: %w",
			total, latest, errs.ErrToolPrecondition,
		)
	}

	preserveDraft := false
	if p.InProgressChapter > 0 {
		hasDraft, err := s.hasDraftTextUnlocked(p.InProgressChapter)
		if err != nil {
			return nil, false, fmt.Errorf("load in-progress draft: %w: %w", errs.ErrStoreRead, err)
		}
		if hasDraft && p.InProgressChapter > total {
			return nil, false, fmt.Errorf(
				"第 %d 章存在未完成的非空草稿，但新大纲只有 %d 章；全量覆盖不能截掉该草稿所在章节: %w",
				p.InProgressChapter, total, errs.ErrToolPrecondition,
			)
		}
		preserveDraft = hasDraft
	}
	return p, preserveDraft, nil
}

func (s *Store) hasDraftTextUnlocked(chapter int) (bool, error) {
	if chapter <= 0 {
		return false, nil
	}
	data, err := s.Drafts.io.ReadFileUnlocked(fmt.Sprintf("drafts/%02d.draft.md", chapter))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(data))) > 0, nil
}

func replacementProgress(p *domain.Progress, total int, preserveDraft bool) (*domain.Progress, error) {
	if p == nil {
		p = &domain.Progress{}
	}
	p.CompletedScenes = nil
	current := p.LatestCompleted() + 1
	if current <= 0 {
		current = 1
	}
	if current > total {
		current = total
	}
	if preserveDraft && p.InProgressChapter > 0 && p.InProgressChapter <= total {
		current = p.InProgressChapter
	} else {
		p.InProgressChapter = 0
	}
	p.CurrentChapter = current
	p.TotalChapters = total
	switch p.Phase {
	case domain.PhaseWriting:
		// 保留写作阶段；目录替换本身不能把已有作品退回规划阶段。
	case "", domain.PhaseInit, domain.PhasePremise:
		p.Phase = domain.PhaseOutline
	case domain.PhaseOutline:
		// 已经是规划阶段。
	case domain.PhaseComplete:
		return nil, fmt.Errorf("全书已完结，不允许全量覆盖大纲；请先重开作品: %w", errs.ErrToolPrecondition)
	default:
		return nil, fmt.Errorf("无法基于未知创作阶段 %q 覆盖大纲: %w", p.Phase, errs.ErrToolPrecondition)
	}
	return p, nil
}

func locateReplacementChapter(volumes []domain.VolumeOutline, chapter int) (int, int, bool) {
	current := 1
	for _, volume := range volumes {
		for _, arc := range volume.Arcs {
			chapters := arc.DetailedChapters()
			if chapter >= current && chapter < current+len(chapters) {
				return volume.Index, arc.Index, true
			}
			current += len(chapters)
		}
	}
	return 0, 0, false
}

func validateReplacementVolumes(volumes []domain.VolumeOutline) error {
	for vi := range volumes {
		volume := &volumes[vi]
		if strings.TrimSpace(volume.Title) == "" {
			return fmt.Errorf("第 %d 卷缺少 title: %w", vi+1, errs.ErrToolPrecondition)
		}
		if len(volume.Arcs) == 0 {
			return fmt.Errorf("第 %d 卷必须至少包含一个弧: %w", vi+1, errs.ErrToolPrecondition)
		}
		for ai := range volume.Arcs {
			arc := &volume.Arcs[ai]
			if strings.TrimSpace(arc.Title) == "" {
				return fmt.Errorf("第 %d 卷第 %d 弧缺少 title: %w", vi+1, ai+1, errs.ErrToolPrecondition)
			}
			if strings.TrimSpace(arc.Goal) == "" {
				return fmt.Errorf("第 %d 卷第 %d 弧缺少 goal: %w", vi+1, ai+1, errs.ErrToolPrecondition)
			}
			if arc.IsSkeleton() {
				return fmt.Errorf("第 %d 卷第 %d 弧必须包含真实章节: %w", vi+1, ai+1, errs.ErrToolPrecondition)
			}
			for ci := range arc.Chapters {
				if err := validateReplacementOutlineEntry(arc.Chapters[ci], ci+1); err != nil {
					return fmt.Errorf("第 %d 卷第 %d 弧：%w", vi+1, ai+1, err)
				}
			}
			arc.EstimatedChapters = 0
		}
	}
	return nil
}

func validateReplacementOutlineEntry(entry domain.OutlineEntry, chapter int) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "title", value: entry.Title},
		{name: "core_event", value: entry.CoreEvent},
		{name: "hook", value: entry.Hook},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("第 %d 章缺少 %s: %w", chapter, field.name, errs.ErrToolPrecondition)
		}
	}
	return nil
}

// ExpandNextArc 展开当前已完成弧之后的下一弧（Outline + Progress 联动）。
// 目标由已落盘进度和大纲共同确定，模型只负责创作内容。
func (s *Store) ExpandNextArc(expansion domain.ArcExpansion) (ArcPosition, error) {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()
	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()

	var volumes []domain.VolumeOutline
	if err := s.Outline.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return ArcPosition{}, fmt.Errorf("load layered_outline: %w", err)
	}
	repairMissingLayeredIndexes(volumes)
	hasSkeletonArcs := domain.HasSkeletonArcs(volumes)

	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return ArcPosition{}, err
	}
	if p == nil {
		return ArcPosition{}, fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
	}
	boundary := checkArcBoundary(volumes, p.LatestCompleted())
	if boundary == nil || !boundary.IsArcEnd || boundary.nextVolumePos < 0 {
		return ArcPosition{}, fmt.Errorf("当前进度不在弧末，无下一弧可展开: %w", errs.ErrToolPrecondition)
	}
	position := ArcPosition{Volume: boundary.NextVolume, Arc: boundary.NextArc}
	if !hasSkeletonArcs && !sameArcExpansion(volumes[boundary.nextVolumePos].Arcs[boundary.nextArcPos], expansion) {
		return ArcPosition{}, fmt.Errorf(
			"当前分层大纲已是完整规划，不允许 expand_next_arc；同参数崩溃重放除外。请用 save_foundation(type=layered_outline) 全量重排、revise_outline 修订尚未发生的尾段，或用 append_volume 追加包含完整章节的新卷: %w",
			errs.ErrToolPrecondition,
		)
	}
	targetContract, err := s.loadChapterTargetContractUnlocked()
	if err != nil {
		return ArcPosition{}, err
	}
	projectedTotal := len(domain.FlattenOutline(volumes))
	projectedSkeletons := domain.SkeletonArcCount(volumes)
	targetArc := &volumes[boundary.nextVolumePos].Arcs[boundary.nextArcPos]
	if !targetArc.IsExpanded() {
		projectedTotal += len(expansion.Chapters)
		projectedSkeletons--
	}
	if err := targetContract.validateTail(projectedTotal, projectedSkeletons); err != nil {
		return ArcPosition{}, err
	}
	if err := s.withOutlineMutationRollback(func() error {
		var expandErr error
		volumes, expandErr = s.Outline.expandArcAtUnlocked(volumes, boundary.nextVolumePos, boundary.nextArcPos, expansion)
		if expandErr != nil {
			return expandErr
		}
		p.TotalChapters = len(domain.FlattenOutline(volumes))
		if err := s.Progress.saveUnlocked(p); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return ArcPosition{}, err
	}
	return position, nil
}

// AppendVolume 追加新卷到分层大纲末尾（Outline + Progress 联动）。
func (s *Store) AppendVolume(vol domain.VolumeOutline) (domain.VolumeOutline, error) {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()
	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	targetContract, err := s.loadChapterTargetContractUnlocked()
	if err != nil {
		return domain.VolumeOutline{}, err
	}
	var current []domain.VolumeOutline
	if err := s.Outline.io.ReadJSONUnlocked("layered_outline.json", &current); err != nil {
		return domain.VolumeOutline{}, fmt.Errorf("load layered_outline: %w: %w", errs.ErrStoreRead, err)
	}
	repairMissingLayeredIndexes(current)
	projectedVolumes := append([]domain.VolumeOutline(nil), current...)
	candidate := vol
	nextIndex := 1
	if len(current) > 0 {
		nextIndex = current[len(current)-1].Index + 1
	}
	numberVolume(&candidate, nextIndex)
	if len(current) == 0 || !sameVolumePlan(current[len(current)-1], candidate) {
		projectedVolumes = append(projectedVolumes, candidate)
	}
	projectedTotal := len(domain.FlattenOutline(projectedVolumes))
	projectedSkeletons := domain.SkeletonArcCount(projectedVolumes)
	if err := targetContract.validateTail(projectedTotal, projectedSkeletons); err != nil {
		return domain.VolumeOutline{}, err
	}
	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return domain.VolumeOutline{}, err
	}
	if p == nil {
		p = &domain.Progress{}
	}
	var volumes []domain.VolumeOutline
	var saved domain.VolumeOutline
	if err := s.withOutlineMutationRollback(func() error {
		var appendErr error
		volumes, saved, appendErr = s.Outline.appendVolumeUnlocked(vol)
		if appendErr != nil {
			return appendErr
		}
		p.TotalChapters = len(domain.FlattenOutline(volumes))
		if err := s.Progress.saveUnlocked(p); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return domain.VolumeOutline{}, err
	}
	return saved, nil
}

// ReviseOutline 从 fromChapter 起替换尚未发生的计划尾段。
// 扁平大纲替换全书尾段；分层大纲只替换目标章所在弧的尾段。这个定义让同一载荷
// 重放仍得到同一结果，同时避免 JSON Patch 和 insert/delete 等操作枚举。
func (s *Store) ReviseOutline(fromChapter int, replacement []domain.OutlineEntry) (int, error) {
	if fromChapter <= 0 {
		return 0, fmt.Errorf("from_chapter must be > 0: %w", errs.ErrToolArgs)
	}

	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()
	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	targetContract, err := s.loadChapterTargetContractUnlocked()
	if err != nil {
		return 0, err
	}
	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return 0, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if p == nil {
		return 0, fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
	}
	if p.Phase == domain.PhaseComplete {
		return 0, fmt.Errorf("全书已完结，不允许修改大纲: %w", errs.ErrToolPrecondition)
	}
	protected := p.InProgressChapter
	if latest := p.LatestCompleted(); latest > protected {
		protected = latest
	}
	if fromChapter <= protected {
		return 0, fmt.Errorf("第 %d 章已完成或正在写作；大纲修订必须从第 %d 章之后开始: %w",
			fromChapter, protected, errs.ErrToolPrecondition)
	}

	if p.Layered {
		var volumes []domain.VolumeOutline
		if err := s.Outline.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
			return 0, fmt.Errorf("load layered_outline: %w: %w", errs.ErrStoreRead, err)
		}
		repairMissingLayeredIndexes(volumes)
		if err := reviseLayeredTail(volumes, fromChapter, replacement); err != nil {
			return 0, fmt.Errorf("%w: %w", errs.ErrToolPrecondition, err)
		}
		total := len(domain.FlattenOutline(volumes))
		skeletons := domain.SkeletonArcCount(volumes)
		if err := targetContract.validateTail(total, skeletons); err != nil {
			return 0, err
		}
		p.TotalChapters = total
		if err := s.withOutlineMutationRollback(func() error {
			if err := s.Outline.saveLayeredViewsUnlocked(volumes); err != nil {
				return fmt.Errorf("save layered outline: %w: %w", errs.ErrStoreWrite, err)
			}
			if err := s.Progress.saveUnlocked(p); err != nil {
				return fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
			}
			return nil
		}); err != nil {
			return 0, err
		}
		return total, nil
	}

	var outline []domain.OutlineEntry
	if err := s.Outline.io.ReadJSONUnlocked("outline.json", &outline); err != nil {
		return 0, fmt.Errorf("load outline: %w: %w", errs.ErrStoreRead, err)
	}
	updated, err := reviseFlatTail(outline, fromChapter, replacement)
	if err != nil {
		return 0, err
	}
	total := len(updated)
	if err := targetContract.validateExact(total); err != nil {
		return 0, err
	}
	p.TotalChapters = total
	if err := s.withOutlineMutationRollback(func() error {
		if err := s.Outline.saveOutlineUnlocked(updated); err != nil {
			return fmt.Errorf("save outline: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := s.Progress.saveUnlocked(p); err != nil {
			return fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return total, nil
}

// ClearHandledSteer 清除 PendingSteer 并重置旧版 FlowSteering 状态。
// 两个文件无法组成文件系统事务，因此先写可重复的 Progress，最后才删除恢复意图；
// 任一步失败都至少保留 PendingSteer，下一次 Resume 可以安全重放。
func (s *Store) ClearHandledSteer() error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	meta, err := s.RunMeta.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	p, err := s.Progress.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if p != nil && p.Flow == domain.FlowSteering {
		if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
			return err
		}
		p.Flow = domain.FlowWriting
		if err := s.Progress.saveUnlocked(p); err != nil {
			return err
		}
	}
	if meta != nil && meta.PendingSteer != "" {
		meta.PendingSteer = ""
		if err := s.RunMeta.saveUnlocked(*meta); err != nil {
			return err
		}
	}
	return nil
}
