package desktopui

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

type auditChapterRecord struct {
	Version       int                  `json:"version"`
	Chapter       int                  `json:"chapter"`
	Revision      int                  `json:"revision"`
	Origin        domain.ChapterOrigin `json:"origin"`
	ContentSHA256 string               `json:"contentSha256"`
	Facts         domain.ChapterFacts  `json:"facts"`
	StyleDelta    domain.StyleDelta    `json:"styleDelta"`
	AcceptedAt    time.Time            `json:"acceptedAt"`
}

type auditDecision struct {
	ID             string `json:"id"`
	At             string `json:"at"`
	Kind           string `json:"kind"`
	Decider        string `json:"decider"`
	CheckpointSeq  int64  `json:"checkpointSeq,omitempty"`
	Model          string `json:"model,omitempty"`
	DurationMs     int64  `json:"durationMs,omitempty"`
	InputTruncated bool   `json:"inputTruncated,omitempty"`
	InputExcerpt   string `json:"inputExcerpt,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Error          string `json:"error,omitempty"`
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	rawChapter := strings.TrimSpace(r.URL.Query().Get("chapter"))
	requestedChapter := 0
	if rawChapter != "" {
		n, err := strconv.Atoi(rawChapter)
		if err != nil || n <= 0 {
			fail(w, http.StatusBadRequest, "无效章节")
			return
		}
		requestedChapter = n
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if d := r.URL.Query().Get("directory"); d != "" && d != s.dir {
		fail(w, http.StatusConflict, "项目已切换")
		return
	}
	st := store.NewStore(s.dir)
	warnings := []string{}
	warn := func(label string, err error) {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s 读取失败：%s", label, s.redactLocked(err.Error())))
		}
	}

	project, err := readProject(s.dir)
	warn("项目资料", err)
	progress, err := st.Progress.Load()
	warn("进度", err)
	chapter := requestedChapter
	if chapter == 0 && progress != nil {
		chapter = progress.LatestCompleted()
	}

	compass, err := st.Outline.LoadCompass()
	warn("故事指南针", err)
	foundationAudit, err := st.Outline.LoadFoundationAudit()
	warn("基础设定审查", err)
	characters, err := st.Characters.Load()
	warn("角色档案", err)
	snapshots, err := st.Characters.LoadLatestSnapshots()
	warn("角色快照", err)
	worldRules, err := st.World.LoadWorldRules()
	warn("世界规则", err)
	timeline, err := st.World.LoadTimeline()
	warn("时间线", err)
	foreshadows, err := st.World.LoadForeshadowLedger()
	warn("伏笔台账", err)
	relationships, err := st.World.LoadRelationships()
	warn("人物关系", err)
	stateChanges, err := st.World.LoadStateChanges()
	warn("状态变化", err)
	review, err := st.World.LoadReview(chapter)
	warn("章节评审", err)
	globalReview, err := st.World.LoadGlobalReview(chapter)
	warn("全局评审", err)

	var record any
	if chapter > 0 {
		loaded, loadErr := st.ChapterRecords.Load(chapter)
		warn("章节接纳记录", loadErr)
		if loaded != nil {
			record = auditChapterRecord{
				Version: loaded.Version, Chapter: loaded.Chapter, Revision: loaded.Revision,
				Origin: loaded.Origin, ContentSHA256: loaded.ContentSHA256,
				Facts: loaded.Facts, StyleDelta: loaded.StyleDelta, AcceptedAt: loaded.AcceptedAt,
			}
		}
	}
	decisions, err := st.Decisions.Recent(80)
	warn("决策审计", err)
	decisionViews := make([]auditDecision, 0, len(decisions))
	for _, decision := range decisions {
		decisionViews = append(decisionViews, auditDecision{
			ID: decision.ID, At: decision.At, Kind: decision.Kind, Decider: decision.Decider,
			CheckpointSeq: decision.CheckpointSeq, Model: decision.Model,
			DurationMs: decision.DurationMs, InputTruncated: decision.InputTruncated,
			InputExcerpt: truncateAuditText(s.redactLocked(decision.Input), 500),
			Reason:       truncateAuditText(s.redactLocked(decision.Reason), 1000),
			Error:        truncateAuditText(s.redactLocked(decision.Error), 1000),
		})
	}
	if err := st.Checkpoints.InitError(); err != nil {
		warn("Checkpoint 初始化", err)
	}
	checkpoints := st.Checkpoints.All()
	simulation, err := st.Simulation.Load()
	warn("风格画像", err)
	cfg, cfgErr := s.projectConfigLocked()
	warn("项目 Agent 配置", cfgErr)

	sort.Strings(warnings)
	reply(w, http.StatusOK, map[string]any{
		"directory":          s.dir,
		"project":            project,
		"chapter":            chapter,
		"progress":           progress,
		"agents":             auditAgentConfig(cfg),
		"compass":            compass,
		"foundationAudit":    foundationAudit,
		"characters":         characters,
		"characterSnapshots": snapshots,
		"worldRules":         worldRules,
		"timeline":           timeline,
		"foreshadows":        foreshadows,
		"relationships":      relationships,
		"stateChanges":       stateChanges,
		"review":             review,
		"globalReview":       globalReview,
		"chapterRecord":      record,
		"decisions":          decisionViews,
		"checkpoints":        checkpoints,
		"simulation":         domain.CompactSimulationProfile(simulation),
		"warnings":           warnings,
	})
}

func auditAgentConfig(cfg bootstrap.Config) map[string]any {
	roles := make(map[string]map[string]any, len(cfg.Roles))
	for role, rc := range cfg.Roles {
		roles[role] = map[string]any{
			"provider": rc.Provider, "model": rc.Model,
			"fallbacks": rc.Fallbacks, "reasoningEffort": rc.ReasoningEffort,
		}
	}
	directives := make(map[string]string, len(cfg.RoleDirectives))
	for role, directive := range cfg.RoleDirectives {
		directives[role] = directive
	}
	return map[string]any{
		"provider": cfg.Provider, "model": cfg.ModelName,
		"reasoningEffort": cfg.ReasoningEffort, "roles": roles,
		"roleDirectives": directives, "style": cfg.Style,
	}
}

func truncateAuditText(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}
