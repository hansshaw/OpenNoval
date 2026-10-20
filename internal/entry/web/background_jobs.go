package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/projectjobs"
	"github.com/voocel/ainovel-cli/internal/projectversion"
	"github.com/voocel/ainovel-cli/internal/store"
)

const backgroundConcurrency = 2
const backgroundProviderLimit = 1

type backgroundInput struct {
	Directory           string                          `json:"directory"`
	ProjectName         string                          `json:"project_name,omitempty"`
	Model               string                          `json:"model"`
	Count               int                             `json:"count"`
	BatchEnd            int                             `json:"batch_end"`
	PlanningFingerprint string                          `json:"planning_fingerprint"`
	Roles               map[string]bootstrap.RoleConfig `json:"roles,omitempty"`
	Style               string                          `json:"style"`
}

// ensureBackgroundLocked is lazy because tests and desktop workspaces can set
// booksDir after constructing Server. Its journal lives outside project copies.
func (s *Server) ensureBackgroundLocked() error {
	if s.closed {
		return projectjobs.ErrClosed
	}
	if s.background != nil {
		return nil
	}
	queue, err := projectjobs.New(projectjobs.Config{
		MaxConcurrent: backgroundConcurrency, DefaultProviderLimit: backgroundProviderLimit,
		PersistencePath: filepath.Join(filepath.Dir(s.booksDir), "workbench-jobs.json"),
	}, projectjobs.RunnerFunc(s.runBackgroundJob))
	if err != nil {
		return err
	}
	s.background = queue
	return nil
}

func (s *Server) backgroundAnyBusyLocked() bool {
	if s.background == nil {
		if err := s.ensureBackgroundLocked(); err != nil {
			return true
		}
	}
	busy, err := s.background.Busy(context.Background(), "")
	if err != nil {
		return true // fail closed if queue state is unavailable
	}
	return busy
}

func (s *Server) backgroundProjectBusyLocked(target string) bool {
	if s.background == nil {
		if err := s.ensureBackgroundLocked(); err != nil {
			return true
		}
	}
	anyBusy, err := s.background.Busy(context.Background(), "")
	if err != nil {
		return true
	}
	if !anyBusy {
		return false
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return true
	}
	busy, err := s.background.Busy(context.Background(), canonical)
	return err != nil || busy
}

func backgroundLimitView(limits projectjobs.Limits) map[string]any {
	return map[string]any{
		"maxConcurrent": limits.MaxConcurrent, "providerLimit": limits.ProviderLimit,
		"providerLimits": limits.ProviderLimits, "editable": true, "maxSupported": projectjobs.MaxConcurrency,
	}
}

func (s *Server) backgroundLimits(w http.ResponseWriter, r *http.Request) {
	var requested projectjobs.Limits
	if r.Method == http.MethodPost {
		if !readJSON(w, r, &requested) {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureBackgroundLocked(); err != nil {
		fail(w, http.StatusServiceUnavailable, s.redactLocked(err.Error()))
		return
	}
	var limits projectjobs.Limits
	var err error
	if r.Method == http.MethodPost {
		for provider := range requested.ProviderLimits {
			if _, ok := s.baseCfg.Providers[provider]; !ok {
				fail(w, http.StatusBadRequest, "并发覆盖中的模型连接不存在，请刷新后重新选择")
				return
			}
		}
		limits, err = s.background.SetLimits(r.Context(), requested)
	} else {
		limits, err = s.background.Limits(r.Context())
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, projectjobs.ErrInvalidLimits) {
			status = http.StatusBadRequest
		}
		fail(w, status, s.redactLocked(err.Error()))
		return
	}
	reply(w, http.StatusOK, backgroundLimitView(limits))
}

func (s *Server) listBackgroundJobs(w http.ResponseWriter, r *http.Request) {
	var after uint64
	if raw := r.URL.Query().Get("after"); raw != "" {
		var err error
		after, err = strconv.ParseUint(raw, 10, 64)
		if err != nil {
			fail(w, http.StatusBadRequest, "无效任务事件游标")
			return
		}
	}
	s.mu.Lock()
	if err := s.ensureBackgroundLocked(); err != nil {
		fail(w, http.StatusInternalServerError, s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	queue := s.background
	s.mu.Unlock()
	jobs, err := queue.List(r.Context(), "")
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	page, err := queue.Events(r.Context(), after)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	limits, err := queue.Limits(r.Context())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if jobs == nil {
		jobs = []projectjobs.Job{}
	}
	if page.Events == nil {
		page.Events = []projectjobs.Event{}
	}
	reply(w, http.StatusOK, map[string]any{"jobs": jobs, "events": page, "limits": backgroundLimitView(limits)})
}

func (s *Server) submitBackgroundJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string  `json:"directory"`
		Kind      string  `json:"kind"`
		Provider  string  `json:"provider"`
		Model     string  `json:"model"`
		Count     int     `json:"count"`
		BudgetUSD float64 `json:"budgetUSD"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Kind != "batch" && req.Kind != "resume" {
		fail(w, http.StatusBadRequest, "后台任务类型须为 batch 或 resume")
		return
	}
	req.Provider, req.Model = strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model)
	if (req.Provider == "") != (req.Model == "") {
		fail(w, http.StatusBadRequest, "后台模型连接与模型名称必须同时选择，或同时留空以沿用当前模型")
		return
	}
	if math.IsNaN(req.BudgetUSD) || math.IsInf(req.BudgetUSD, 0) || req.BudgetUSD < 0 || (req.Kind == "batch" && (req.Count < 1 || req.Count > 100)) {
		fail(w, http.StatusBadRequest, "章节数量须为 1–100，预算必须为非负有限数字")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.busy || s.stopping || s.styleBusy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		fail(w, http.StatusConflict, "请先暂停前台创作和其他操作，再提交后台任务")
		return
	}
	target, err := s.validWorkspaceLocked(req.Directory)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		fail(w, http.StatusBadRequest, "项目目录不可用")
		return
	}
	current, _ := filepath.EvalSymlinks(s.dir)
	if target == current {
		fail(w, http.StatusConflict, "当前打开的项目由前台工作台管理，请先切换到其他项目后再把它加入后台队列")
		return
	}
	if err := recoverBackgroundProject(r.Context(), target); err != nil {
		fail(w, http.StatusConflict, "项目存在未完成的版本恢复，恢复失败："+s.redactLocked(err.Error()))
		return
	}
	project, err := readProject(target)
	if err != nil || project.Archived {
		fail(w, http.StatusConflict, "项目资料不可读或已归档，不能创建后台任务")
		return
	}
	cfg := bootstrap.CloneConfig(s.baseCfg)
	if req.Provider != "" {
		cfg.Provider, cfg.ModelName = req.Provider, req.Model
	}
	applyProjectRoleConfigs(&cfg, project.RoleModels)
	if project.StylePreset != "" {
		cfg.Style = project.StylePreset
	}
	if err := validateBackgroundModels(cfg, cfg.Provider, cfg.ModelName, cfg.Roles); err != nil {
		fail(w, http.StatusConflict, redactConfig(cfg, err.Error()))
		return
	}
	input := backgroundInput{Directory: target, ProjectName: project.Name, Model: cfg.ModelName, Count: req.Count, Roles: cfg.Roles, Style: cfg.Style}
	if err := prepareBackgroundWorkflow(&input, req.Kind); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.ensureBackgroundLocked(); err != nil {
		fail(w, http.StatusInternalServerError, s.redactLocked(err.Error()))
		return
	}
	// One outstanding batch per project prevents a second batch from executing
	// before the user has reviewed the first batch's chapters.
	if s.backgroundProjectBusyLocked(target) {
		fail(w, http.StatusConflict, "该项目已有排队或运行中的后台任务")
		return
	}
	payload, err := json.Marshal(input)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.background.Submit(r.Context(), projectjobs.Request{ProjectID: target, ProviderID: cfg.Provider, Kind: req.Kind, Payload: payload, BudgetUSD: req.BudgetUSD})
	if err != nil {
		fail(w, http.StatusConflict, s.redactLocked(err.Error()))
		return
	}
	reply(w, http.StatusCreated, job)
}

func (s *Server) cancelBackgroundJob(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	if err := s.ensureBackgroundLocked(); err != nil {
		fail(w, http.StatusInternalServerError, s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	queue := s.background
	s.mu.Unlock()
	err := queue.Cancel(r.Context(), r.PathValue("job"))
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, projectjobs.ErrNotFound) {
			status = http.StatusNotFound
		}
		fail(w, status, err.Error())
		return
	}
	job, err := queue.Get(r.Context(), r.PathValue("job"))
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	reply(w, http.StatusOK, job)
}

// Validate both at admission and execution. No unlisted model is silently
// substituted, and every role/fallback must fit the job's provider quota.
func validateBackgroundModels(cfg bootstrap.Config, provider, model string, roles map[string]bootstrap.RoleConfig) error {
	check := func(ref bootstrap.ModelRef) error {
		if ref.Provider != provider {
			return fmt.Errorf("后台任务暂不支持跨模型连接的角色或备用模型：%s；请把全部角色和备用模型配置到 %s", ref.Provider, provider)
		}
		pc, ok := cfg.Providers[ref.Provider]
		if !ok {
			return fmt.Errorf("任务绑定的模型连接 %s 已不存在", ref.Provider)
		}
		if _, ok := pc.ModelConfig(ref.Model); !ok {
			return fmt.Errorf("任务绑定的模型 %s/%s 不在已配置模型列表中，请添加模型后重新提交", ref.Provider, ref.Model)
		}
		return nil
	}
	if err := check(bootstrap.ModelRef{Provider: provider, Model: model}); err != nil {
		return err
	}
	for _, role := range roles {
		if err := check(bootstrap.ModelRef{Provider: role.Provider, Model: role.Model}); err != nil {
			return err
		}
		for _, fallback := range role.Fallbacks {
			if err := check(fallback); err != nil {
				return err
			}
		}
	}
	cfg.Provider, cfg.ModelName, cfg.Roles = provider, model, roles
	return cfg.ValidateBase()
}

func prepareBackgroundWorkflow(input *backgroundInput, kind string) error {
	st := store.NewStore(input.Directory)
	view, err := workflowView(st)
	if err != nil {
		return fmt.Errorf("读取项目工作流失败: %w", err)
	}
	if approved, _ := view["approved"].(bool); !approved {
		return fmt.Errorf("请先在项目工作台完成规划并确认当前大纲后，再创建后台任务")
	}
	p, err := st.Progress.Load()
	if err != nil || p == nil || p.Phase != domain.PhaseWriting {
		return fmt.Errorf("项目尚未进入写作阶段或已经完结")
	}
	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil || meta.Workbench == nil {
		return fmt.Errorf("项目工作台控制记录不可用")
	}
	w := meta.Workbench
	if p.LatestCompleted() > w.AcceptedChapter && (kind == "batch" || p.LatestCompleted() >= w.BatchEnd) {
		return fmt.Errorf("请先在项目工作台验收当前批次，再创建下一个后台任务")
	}
	if kind == "resume" && p.NextChapter() > w.BatchEnd && len(p.PendingRewrites) == 0 {
		return fmt.Errorf("没有已经授权但尚未完成的批次，请使用创建新批次")
	}
	input.PlanningFingerprint, _ = view["fingerprint"].(string)
	input.BatchEnd = w.BatchEnd
	if kind == "batch" {
		input.BatchEnd = p.LatestCompleted() + input.Count
	}
	return nil
}

func validateBackgroundWorkflowBinding(input backgroundInput, kind string) error {
	current := input
	if err := prepareBackgroundWorkflow(&current, kind); err != nil {
		return err
	}
	if current.PlanningFingerprint != input.PlanningFingerprint || current.BatchEnd != input.BatchEnd {
		return fmt.Errorf("排队期间规划或批次已变化，请检查项目并重新提交任务")
	}
	return nil
}

func recoverBackgroundProject(ctx context.Context, target string) error {
	pending, err := projectversion.HasPendingRecovery(target)
	if err != nil || !pending {
		return err
	}
	return withWorkspaceLease(target, func() error {
		_, err := projectversion.Recover(ctx, target)
		return err
	})
}

func applyBackgroundDirectives(cfg *bootstrap.Config, project map[string]string) {
	if cfg.RoleDirectives == nil {
		cfg.RoleDirectives = make(map[string]string)
	}
	for role, directive := range project {
		cfg.RoleDirectives[role] = directive
	}
}

func (s *Server) runBackgroundJob(ctx context.Context, job projectjobs.Job, report projectjobs.Reporter) error {
	if job.Kind != "batch" && job.Kind != "resume" {
		return fmt.Errorf("不支持的后台任务类型: %s", job.Kind)
	}
	var input backgroundInput
	if err := json.Unmarshal(job.Payload, &input); err != nil {
		return fmt.Errorf("后台任务输入损坏: %w", err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return projectjobs.ErrClosed
	}
	target, err := s.validWorkspaceLocked(input.Directory)
	if err == nil {
		target, err = filepath.EvalSymlinks(target)
	}
	current, _ := filepath.EvalSymlinks(s.dir)
	if err != nil || target != job.ProjectID || input.Directory != job.ProjectID || target == current {
		s.mu.Unlock()
		return fmt.Errorf("后台项目目录变化、不可用或已经被前台打开，请重新提交")
	}
	cfg := bootstrap.CloneConfig(s.baseCfg)
	s.mu.Unlock()
	if err := recoverBackgroundProject(ctx, target); err != nil {
		return fmt.Errorf("恢复未完成的版本事务失败: %w", err)
	}
	project, err := readProject(target)
	if err != nil || project.Archived {
		return fmt.Errorf("后台项目已归档或资料不可用")
	}
	if err := validateBackgroundModels(cfg, job.ProviderID, input.Model, input.Roles); err != nil {
		return errors.New(redactConfig(cfg, err.Error()))
	}
	if err := validateBackgroundWorkflowBinding(input, job.Kind); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg.Provider, cfg.ModelName, cfg.Roles = job.ProviderID, input.Model, input.Roles
	cfg.OutputDir, cfg.Style = target, input.Style
	applyBackgroundDirectives(&cfg, project.RoleDirectives)
	bundle := assets.Load(cfg.Style, assets.DefaultLoadOptions(target))
	if _, ok := bundle.Styles[cfg.Style]; !ok {
		return fmt.Errorf("任务绑定的项目风格已不可用")
	}
	// WithFileLog changes the process-wide default logger; using it here would
	// mix concurrent projects and restore closed loggers out of order. Every
	// structured project event instead belongs to this job's journal stream.
	rt, err := host.New(cfg, bundle)
	if err != nil {
		return errors.New(redactConfig(cfg, err.Error()))
	}
	defer rt.Close()
	// Preflight reads do not own the project lease. Another process may have
	// changed its plan or accepted a chapter before host.New obtained the lock.
	// Recheck the exact authorized batch while this Host now holds that lease.
	if err := validateBackgroundWorkflowBinding(input, job.Kind); err != nil {
		return err
	}
	lockedProject, err := readProject(target)
	if err != nil || lockedProject.Archived {
		return fmt.Errorf("后台项目已归档或资料不可用")
	}
	if err := rt.EnableWorkbench(); err != nil {
		rt.Close()
		return err
	}
	err = executeBackgroundHost(ctx, job.Kind, input.Count, rt, report, func(value string) string { return redactConfig(cfg, value) })
	if err != nil {
		return errors.New(redactConfig(cfg, err.Error()))
	}
	p, err := store.NewStore(target).Progress.Load()
	if err != nil {
		return err
	}
	if p == nil || (p.Phase != domain.PhaseComplete && (p.LatestCompleted() < input.BatchEnd || len(p.PendingRewrites) > 0 || p.InProgressChapter > 0)) {
		return fmt.Errorf("创作已停止但授权批次未完成，请查看任务事件并处理后恢复")
	}
	return nil
}

type backgroundHost interface {
	Resume() (string, error)
	BeginWorkbenchBatch(int) error
	TotalCostUSD() float64
	Events() <-chan host.Event
	Stream() <-chan string
	Done() <-chan struct{}
	Close()
}

// Keep diagnostics bounded so one unusually large tool response cannot multiply
// every durable journal write. Redact before truncating to preserve secrecy.
func backgroundEventText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	const suffix = "…（已截断）"
	end := limit - len(suffix)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + suffix, true
}

// Cancellation must close the independent Host and wait for startup and all
// model requests before releasing the scheduler's concurrency/project slots.
func executeBackgroundHost(ctx context.Context, kind string, count int, rt backgroundHost, report projectjobs.Reporter, redact func(string) string) error {
	if err := ctx.Err(); err != nil {
		rt.Close()
		return err
	}
	lastCost := rt.TotalCostUSD()
	recordCost := func() error {
		total := rt.TotalCostUSD()
		if total <= lastCost {
			return nil
		}
		delta := total - lastCost
		lastCost = total
		return report.RecordCost(delta)
	}
	started := make(chan error, 1)
	go func() {
		var startErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				startErr = fmt.Errorf("后台项目启动异常: %v", recovered)
			}
			started <- startErr
		}()
		if kind == "batch" {
			startErr = rt.BeginWorkbenchBatch(count)
			return
		}
		label, err := rt.Resume()
		if err == nil && strings.TrimSpace(label) == "" {
			err = fmt.Errorf("项目没有可恢复的创作进度")
		}
		startErr = err
	}()
	closed := make(chan struct{})
	closing := false
	closeHost := func() {
		if !closing {
			closing = true
			go func() { rt.Close(); close(closed) }()
		}
	}
	startedDone, hostDone, closeDone := false, false, false
	var result error
	events, stream, done, cancelSignal := rt.Events(), rt.Stream(), rt.Done(), ctx.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for !(startedDone && closeDone && events == nil && stream == nil) {
		select {
		case err := <-started:
			startedDone, started = true, nil
			if err != nil {
				result = err
				closeHost()
			} else if hostDone {
				closeHost()
			}
		case <-cancelSignal:
			cancelSignal = nil
			result = ctx.Err()
			closeHost()
		case <-done:
			hostDone, done = true, nil
			if startedDone {
				closeHost()
			}
		case <-closed:
			closeDone, closed = true, nil
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			summary, summaryTruncated := backgroundEventText(redact(ev.Summary), 4096)
			detail, detailTruncated := backgroundEventText(redact(ev.Detail), 16<<10)
			payload, _ := json.Marshal(map[string]any{"event_id": ev.ID, "category": ev.Category, "agent": ev.Agent, "level": ev.Level, "detail": detail, "finished_at": ev.FinishedAt, "truncated": summaryTruncated || detailTruncated})
			if err := report.Emit("host.event", summary, payload); err != nil {
				result = err
				closeHost()
			}
		case _, ok := <-stream:
			if !ok {
				stream = nil
			}
		case <-ticker.C:
			if err := recordCost(); err != nil {
				result = err
				closeHost()
			}
		}
	}
	if err := recordCost(); err != nil && result == nil {
		result = err
	}
	return result
}
