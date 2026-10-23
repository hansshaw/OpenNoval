// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hansshaw/AInoval-web-ui/internal/errs"
	"github.com/hansshaw/AInoval-web-ui/internal/llmcontract"
	"github.com/hansshaw/AInoval-web-ui/internal/modelrequest"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/litellm"
)

// FailoverEvent 表示一次显式 provider 切换。
// Reason 为短标签（rate_limit / timeout / stream_idle / network），用于结构化日志。
type FailoverEvent struct {
	Role         string
	Reason       string
	FromProvider string
	FromModel    string
	ToProvider   string
	ToModel      string
	Err          error
}

// FailoverReporter 在发生显式切换时被调用。
type FailoverReporter func(FailoverEvent)

type modelTarget struct {
	provider   string
	name       string
	model      agentcore.ChatModel
	jsonSchema *bool
}

// SwappableModel 是可热切换的 ChatModel 包装器。
// 已开始的请求继续使用旧实例；后续请求自动切到新实例。
type SwappableModel struct {
	*agentcore.SwappableModel
	mu       sync.RWMutex
	provider string
	name     string
	// jsonSchema 是当前选中模型的 config json_schema 三态声明，与 provider/name
	// 同锁原子切换；llmcontract.Resolve 经结构匹配接口每次现读。
	jsonSchema *bool
}

func NewSwappableModel(provider, name string, model agentcore.ChatModel, jsonSchema *bool) *SwappableModel {
	return &SwappableModel{
		SwappableModel: agentcore.NewSwappableModel(model),
		provider:       provider,
		name:           name,
		jsonSchema:     jsonSchema,
	}
}

func (m *SwappableModel) ProviderName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider
}

func (m *SwappableModel) Info() llm.ModelInfo {
	return m.StructuredOutputFacts().Info
}

// StructuredOutputFacts 在同一把锁下读取模型实例、身份和配置覆盖，保证一次
// 结构化协议选择只观察到一个完整版本。
func (m *SwappableModel) StructuredOutputFacts() llmcontract.ModelFacts {
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.SwappableModel.Current()
	facts := llmcontract.ModelFacts{
		Info:               llm.ModelInfo{Name: m.name, Provider: m.provider},
		JSONSchemaOverride: cloneBoolPtr(m.jsonSchema),
	}
	if cp, ok := current.(llm.CapabilityProvider); ok {
		facts.Capabilities = cp.Capabilities()
	}
	if info, ok := current.(interface{ Info() llm.ModelInfo }); ok {
		modelInfo := info.Info()
		if modelInfo.Name == "" {
			modelInfo.Name = m.name
		}
		if modelInfo.Provider == "" {
			modelInfo.Provider = m.provider
		}
		facts.Info = modelInfo
	}
	return facts
}

func (m *SwappableModel) Capabilities() llm.Capabilities {
	return m.StructuredOutputFacts().Capabilities
}

func (m *SwappableModel) Swap(provider, name string, model agentcore.ChatModel, jsonSchema *bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SwappableModel.Swap(model)
	m.provider = provider
	m.name = name
	m.jsonSchema = jsonSchema
}

// JSONSchemaOverride 返回当前选中模型的 config json_schema 三态声明。
func (m *SwappableModel) JSONSchemaOverride() *bool {
	return m.StructuredOutputFacts().JSONSchemaOverride
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (m *SwappableModel) Current() (provider, name string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provider, m.name
}

// ModelSet 持有按角色分配的模型实例，未配置的角色回退到默认模型。
type ModelSet struct {
	mu                sync.RWMutex
	Default           *SwappableModel
	models            map[string]*SwappableModel
	inherited         map[string]*SwappableModel // stable role wrappers that follow Default
	fallbacks         map[string][]modelTarget
	config            Config
	requestJournal    *modelrequest.Journal
	requestLifecycle  *modelRequestLifecycle
	requestLifecycles map[*modelRequestLifecycle]struct{}
	requestsClosed    bool
}

// ForRole 返回指定角色的模型，未配置时返回默认模型。
func (ms *ModelSet) ForRole(role string) agentcore.ChatModel {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.roleModelLocked(role)
}

// Workers keep this pointer for their lifetime. Even an inherited role needs
// its own wrapper so adding or removing an override updates existing workers.
func (ms *ModelSet) roleModelLocked(role string) *SwappableModel {
	if m, ok := ms.models[role]; ok {
		return m
	}
	if !knownRoles[role] {
		return ms.Default
	}
	if m, ok := ms.inherited[role]; ok {
		return m
	}
	if ms.inherited == nil {
		ms.inherited = make(map[string]*SwappableModel)
	}
	provider, name := ms.Default.Current()
	m := NewSwappableModel(provider, name, ms.Default.SwappableModel.Current(), ms.Default.JSONSchemaOverride())
	ms.inherited[role] = m
	return m
}

// ForRoleWithFailover 返回带有单次请求级 fallback 的角色模型。
// 备用列表在每次请求失败时读取，运行中添加或移除 fallbacks 对已绑定的 Worker 同样生效。
func (ms *ModelSet) ForRoleWithFailover(role string, report FailoverReporter) agentcore.ChatModel {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	primary := ms.roleModelLocked(role)
	if !knownRoles[role] {
		return primary
	}
	return &failoverModel{
		role: role, primary: primary, set: ms, report: report,
	}
}

// Summary 返回模型分配摘要（供日志使用）。
func (ms *ModelSet) Summary() string {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	var parts []string
	for role, m := range ms.models {
		provider, name := m.Current()
		parts = append(parts, fmt.Sprintf("%s=%s/%s", role, provider, name))
	}
	if len(parts) == 0 {
		provider, name := ms.Default.Current()
		return fmt.Sprintf("default=%s/%s", provider, name)
	}
	provider, name := ms.Default.Current()
	return fmt.Sprintf("default=%s/%s %s", provider, name, strings.Join(parts, " "))
}

// CurrentSelection 返回角色当前生效的 provider/model。
// role 为空或 "default" 时返回默认模型。
func (ms *ModelSet) CurrentSelection(role string) (provider, model string, explicit bool) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if role == "" || role == "default" {
		provider, model = ms.Default.Current()
		return provider, model, true
	}
	if sw, ok := ms.models[role]; ok {
		provider, model = sw.Current()
		return provider, model, true
	}
	provider, model = ms.Default.Current()
	return provider, model, false
}

// Swap 切换默认模型或指定角色模型。
// role 为空或 "default" 时切换默认模型；其他角色切换为显式覆盖。
func (ms *ModelSet) Swap(role, provider, model string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.requestsClosed {
		return context.Canceled
	}
	pc, ok := ms.config.Providers[provider]
	if !ok {
		return fmt.Errorf("provider %q is not configured: %w", provider, errs.ErrConfig)
	}
	limit, _ := ms.config.ResolveMaxOutputTokens(provider, model)
	next, err := createRuntimeModel(provider, model, pc, limit, make(map[string]agentcore.ChatModel), ms.requestJournal, ms.config.AcquireModelRequest, ms.requestLifecycle)
	if err != nil {
		return fmt.Errorf("切换模型失败: %w", err)
	}

	jsonSchema := ms.config.ModelJSONSchema(provider, model)
	if role == "" || role == "default" {
		ms.Default.Swap(provider, model, next, jsonSchema)
		for _, inherited := range ms.inherited {
			inherited.Swap(provider, model, next, jsonSchema)
		}
		ms.config.Provider = provider
		ms.config.ModelName = model
		return nil
	}

	if !knownRoles[role] {
		return fmt.Errorf("unknown role %q: %w", role, errs.ErrConfig)
	}

	if existing, ok := ms.models[role]; ok {
		existing.Swap(provider, model, next, jsonSchema)
	} else if existing, ok := ms.inherited[role]; ok {
		existing.Swap(provider, model, next, jsonSchema)
		ms.models[role] = existing
		delete(ms.inherited, role)
	} else {
		ms.models[role] = NewSwappableModel(provider, model, next, jsonSchema)
	}
	if ms.config.Roles == nil {
		ms.config.Roles = make(map[string]RoleConfig)
	}
	rc := ms.config.Roles[role]
	rc.Provider = provider
	rc.Model = model
	ms.config.Roles[role] = rc
	return nil
}

// ResolveContextWindow 使用 ModelSet 的最新配置解析窗口，供运行时热切换后的
// ContextManagerFactory 使用，避免捕获启动时的 Config 副本。
func (ms *ModelSet) ResolveContextWindow(provider, model string) (int, ContextWindowSource) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.config.ResolveContextWindow(provider, model)
}

// ResolveMaxOutputTokens uses the live model configuration so a hot-swapped
// model reports the same output cap that the request boundary will enforce.
func (ms *ModelSet) ResolveMaxOutputTokens(provider, model string) (int, OutputTokenSource) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.config.ResolveMaxOutputTokens(provider, model)
}

// ApplyPrepared 提交一个已成功构建的候选 ModelSet。已有 SwappableModel 的地址
// 保持不变，因此已装配的 Worker/Arbiter 会在下一次请求自动使用新客户端。
// The candidate's request lifecycles transfer to this set. A consumed candidate
// must not be independently closed while this Host still owns its clients.
func (ms *ModelSet) ApplyPrepared(candidate *ModelSet) {
	if candidate == nil || candidate == ms {
		return
	}
	// Snapshot the candidate before taking the destination lock: configuration
	// preparation and shutdown must not create cross-ModelSet lock inversions.
	candidate.mu.RLock()
	defaultProvider, defaultName := candidate.Default.Current()
	defaultModel, defaultSchema := candidate.Default.SwappableModel.Current(), candidate.Default.JSONSchemaOverride()
	preparedModels := make(map[string]modelTarget, len(candidate.models))
	for role, next := range candidate.models {
		provider, name := next.Current()
		preparedModels[role] = modelTarget{provider: provider, name: name, model: next.SwappableModel.Current(), jsonSchema: next.JSONSchemaOverride()}
	}
	preparedFallbacks := make(map[string][]modelTarget, len(candidate.fallbacks))
	for role, targets := range candidate.fallbacks {
		preparedFallbacks[role] = append([]modelTarget(nil), targets...)
	}
	preparedConfig := CloneConfig(candidate.config)
	preparedJournal, preparedLifecycle := candidate.requestJournal, candidate.requestLifecycle
	preparedLifecycles := make([]*modelRequestLifecycle, 0, len(candidate.requestLifecycles))
	for lifecycle := range candidate.requestLifecycles {
		preparedLifecycles = append(preparedLifecycles, lifecycle)
	}
	candidate.mu.RUnlock()
	ms.mu.Lock()
	if ms.requestsClosed {
		ms.mu.Unlock()
		candidate.CloseRequests()
		return
	}
	defer ms.mu.Unlock()

	ms.Default.Swap(defaultProvider, defaultName, defaultModel, defaultSchema)

	nextModels := make(map[string]*SwappableModel, len(preparedModels))
	for role, next := range preparedModels {
		existing := ms.models[role]
		if existing == nil {
			existing = ms.inherited[role]
		}
		if existing != nil {
			existing.Swap(next.provider, next.name, next.model, next.jsonSchema)
			nextModels[role] = existing
		} else {
			nextModels[role] = NewSwappableModel(next.provider, next.name, next.model, next.jsonSchema)
		}
	}
	nextInherited := make(map[string]*SwappableModel)
	inherit := func(role string, model *SwappableModel) {
		if _, explicit := nextModels[role]; explicit {
			return
		}
		model.Swap(defaultProvider, defaultName, defaultModel, defaultSchema)
		nextInherited[role] = model
	}
	for role, model := range ms.models {
		inherit(role, model)
	}
	for role, model := range ms.inherited {
		inherit(role, model)
	}
	ms.models = nextModels
	ms.inherited = nextInherited
	ms.fallbacks = preparedFallbacks
	ms.config = preparedConfig
	ms.requestJournal = preparedJournal
	if ms.requestLifecycles == nil {
		ms.requestLifecycles = make(map[*modelRequestLifecycle]struct{})
	}
	for _, lifecycle := range preparedLifecycles {
		ms.requestLifecycles[lifecycle] = struct{}{}
	}
	ms.requestLifecycle = preparedLifecycle
}

func (ms *ModelSet) fallbackTargets(role string) []modelTarget {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return append([]modelTarget(nil), ms.fallbacks[role]...)
}

// ModelName 从 ChatModel 中提取当前模型名，失败返回空字符串。
// 支持 SwappableModel 的热切换：调用时总是返回最新值。
func ModelName(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Name
	}
	return ""
}

// ModelProvider 从 ChatModel 中提取当前 provider 名称，失败返回空字符串。
func ModelProvider(m agentcore.ChatModel) string {
	if info, ok := m.(interface{ Info() llm.ModelInfo }); ok {
		return info.Info().Provider
	}
	if provider, ok := m.(interface{ ProviderName() string }); ok {
		return provider.ProviderName()
	}
	return ""
}

// NewModelSet 根据配置创建多模型集合。
// 相同 provider+model 组合复用同一个实例。
func NewModelSet(cfg Config) (*ModelSet, error) {
	cache := make(map[string]agentcore.ChatModel)
	lifecycle := &modelRequestLifecycle{}
	var journal *modelrequest.Journal
	if cfg.OutputDir != "" {
		var secrets []string
		for _, provider := range cfg.Providers {
			secrets = append(secrets, provider.APIKey)
			if headers, ok := provider.Extra["headers"].(map[string]any); ok {
				for _, value := range headers {
					if value, ok := value.(string); ok {
						secrets = append(secrets, value)
					}
				}
			}
		}
		var err error
		journal, err = modelrequest.Open(cfg.OutputDir, secrets...)
		if err != nil {
			slog.Warn("模型请求记录暂不可用", "module", "model_request", "err", err)
		}
	}

	// 创建默认模型
	defaultPC := cfg.DefaultProviderConfig()
	defaultLimit, defaultLimitSource := cfg.ResolveMaxOutputTokens(cfg.Provider, cfg.ModelName)
	LogOutputLimitChoice("default", cfg.ModelName, defaultLimit, defaultLimitSource)
	defaultModel, err := createRuntimeModel(cfg.Provider, cfg.ModelName, defaultPC, defaultLimit, cache, journal, cfg.AcquireModelRequest, lifecycle)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}

	ms := &ModelSet{
		Default:           NewSwappableModel(cfg.Provider, cfg.ModelName, defaultModel, cfg.ModelJSONSchema(cfg.Provider, cfg.ModelName)),
		models:            make(map[string]*SwappableModel),
		inherited:         make(map[string]*SwappableModel),
		fallbacks:         make(map[string][]modelTarget),
		config:            cfg,
		requestJournal:    journal,
		requestLifecycle:  lifecycle,
		requestLifecycles: map[*modelRequestLifecycle]struct{}{lifecycle: {}},
	}

	// 创建角色覆盖模型
	for role, rc := range cfg.Roles {
		pc, ok := cfg.Providers[rc.Provider]
		if !ok {
			return nil, fmt.Errorf("role %s references unknown provider %q: %w", role, rc.Provider, errs.ErrConfig)
		}
		limit, _ := cfg.ResolveMaxOutputTokens(rc.Provider, rc.Model)
		m, err := createRuntimeModel(rc.Provider, rc.Model, pc, limit, cache, journal, cfg.AcquireModelRequest, lifecycle)
		if err != nil {
			return nil, fmt.Errorf("role %s model: %w", role, err)
		}
		ms.models[role] = NewSwappableModel(rc.Provider, rc.Model, m, cfg.ModelJSONSchema(rc.Provider, rc.Model))
		slog.Info("角色模型分配", "module", "config", "role", role, "provider", rc.Provider, "model", rc.Model)
		if len(rc.Fallbacks) == 0 {
			continue
		}

		targets := make([]modelTarget, 0, len(rc.Fallbacks))
		for _, fallback := range rc.Fallbacks {
			fpc, ok := cfg.Providers[fallback.Provider]
			if !ok {
				return nil, fmt.Errorf("role %s fallback references unknown provider %q: %w", role, fallback.Provider, errs.ErrConfig)
			}
			limit, _ := cfg.ResolveMaxOutputTokens(fallback.Provider, fallback.Model)
			fm, err := createRuntimeModel(fallback.Provider, fallback.Model, fpc, limit, cache, journal, cfg.AcquireModelRequest, lifecycle)
			if err != nil {
				return nil, fmt.Errorf("role %s fallback %s/%s: %w", role, fallback.Provider, fallback.Model, err)
			}
			targets = append(targets, modelTarget{
				provider:   fallback.Provider,
				name:       fallback.Model,
				model:      fm,
				jsonSchema: cfg.ModelJSONSchema(fallback.Provider, fallback.Model),
			})
		}
		ms.fallbacks[role] = targets
	}

	return ms, nil
}

// createModelFromConfig 创建或复用 ChatModel 实例。
//
// maxOutput 是本次模型实例的默认单次输出上限。调用方若通过
// agentcore.WithMaxTokens 显式指定了更具体的任务预算，包装器不会覆盖它。
func createModelFromConfig(providerKey, model string, pc ProviderConfig, maxOutput int, cache map[string]agentcore.ChatModel, journals ...*modelrequest.Journal) (agentcore.ChatModel, error) {
	var journal *modelrequest.Journal
	if len(journals) > 0 {
		journal = journals[0]
	}
	return createRuntimeModel(providerKey, model, pc, maxOutput, cache, journal, nil)
}

func createRuntimeModel(providerKey, model string, pc ProviderConfig, maxOutput int, cache map[string]agentcore.ChatModel, journal *modelrequest.Journal, acquire func(context.Context, string) (func(), error), lifecycles ...*modelRequestLifecycle) (agentcore.ChatModel, error) {
	cacheKey := fmt.Sprintf("%s|%s|%d", providerKey, model, maxOutput)
	if m, ok := cache[cacheKey]; ok {
		return m, nil
	}

	providerType, err := pc.ProviderType(providerKey)
	if err != nil {
		return nil, fmt.Errorf("解析 provider 类型失败: %w", err)
	}
	providerExtra := cloneMap(pc.Extra)
	if pc.API != "" {
		if providerExtra == nil {
			providerExtra = make(map[string]any, 1)
		}
		providerExtra["api"] = pc.API
	}

	streamIdle, err := pc.StreamIdleTimeoutValue()
	if err != nil {
		return nil, fmt.Errorf("provider %s stream_idle_timeout: %w: %w", providerKey, errs.ErrConfig, err)
	}
	apiKey := pc.APIKey
	if apiKey == "" && pc.Type == "openai" && pc.BaseURL != "" {
		endpoint, parseErr := url.Parse(pc.BaseURL)
		if parseErr == nil && (endpoint.Scheme == "http" || endpoint.Scheme == "https") && endpoint.Hostname() != "" && !strings.EqualFold(strings.TrimSuffix(endpoint.Hostname(), "."), "api.openai.com") {
			// The OpenAI adapter requires a nonempty credential even for local
			// compatible servers without authentication. Keep this public marker
			// runtime-only; saved credentials and official endpoints are unchanged.
			apiKey = "ainovel-local-no-key"
		}
	}

	m, err := llm.NewModel(providerType, model,
		llm.WithAPIKey(apiKey),
		llm.WithBaseURL(pc.BaseURL),
		llm.WithStreamIdleTimeout(streamIdle),
		llm.WithProviderExtra(providerExtra),
		llm.WithExtra(pc.ExtraBody),
		// 部分上游的 tool_use id 带 '#'、':' 等字符，发出前的校验只接受 [A-Za-z0-9_-]；
		// 归一化后 tool_result 按同一映射成对改写。
		llm.WithClientOptions(litellm.WithMessageRepair(litellm.RepairNormalizeToolUseIDs)),
	)
	if err != nil {
		return nil, fmt.Errorf("provider %s (%s): %w: %w", providerKey, providerType, errs.ErrProvider, err)
	}
	var observed agentcore.ChatModel = m
	if journal != nil {
		observed = newRequestLoggedModel(m, journal, providerKey, model, pc.BaseURL, maxOutput, streamIdle, lifecycles...)
	}
	limited := newOutputLimitedModel(observed, providerKey, model, maxOutput)
	if wrapper, ok := limited.(*outputLimitedModel); ok {
		wrapper.acquireRequest = acquire
	}
	cache[cacheKey] = limited
	return limited, nil
}

// outputLimitedModel 为未显式设置 max_tokens 的请求补充模型级安全上限。
//
// 这是请求边界上的最后一层保护：业务任务仍可通过 WithMaxTokens 指定更小的
// 预算（如结构化裁定、文本蒸馏）；未指定时不再静默继承 agentcore 的 65536。
type outputLimitedModel struct {
	agentcore.ChatModel
	provider             string
	model                string
	limit                int
	streamRetryNotBefore atomic.Int64
	acquireRequest       func(context.Context, string) (func(), error)
}

func newOutputLimitedModel(model agentcore.ChatModel, provider, name string, limit int) agentcore.ChatModel {
	if model == nil || limit <= 0 {
		return model
	}
	return &outputLimitedModel{ChatModel: model, provider: provider, model: name, limit: limit}
}

func (m *outputLimitedModel) callOptions(opts []agentcore.CallOption) []agentcore.CallOption {
	if m.limit <= 0 || agentcore.ResolveCallConfig(opts).MaxTokens > 0 {
		return opts
	}
	out := make([]agentcore.CallOption, 0, len(opts)+1)
	out = append(out, opts...)
	return append(out, agentcore.WithMaxTokens(m.limit))
}

func (m *outputLimitedModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	release, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.ChatModel.Generate(ctx, messages, tools, m.callOptions(opts)...)
}

func (m *outputLimitedModel) acquire(ctx context.Context) (func(), error) {
	if m.acquireRequest != nil {
		return m.acquireRequest(ctx, m.provider)
	}
	return func() {}, ctx.Err()
}

func (m *outputLimitedModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.streamWithPolicy(ctx, messages, tools, m.callOptions(opts)...)
}

func (m *outputLimitedModel) ProviderName() string {
	if provider, ok := m.ChatModel.(interface{ ProviderName() string }); ok {
		if name := provider.ProviderName(); name != "" {
			return name
		}
	}
	return m.provider
}

func (m *outputLimitedModel) ModelName() string {
	if namer, ok := m.ChatModel.(interface{ ModelName() string }); ok {
		if name := namer.ModelName(); name != "" {
			return name
		}
	}
	return m.model
}

func (m *outputLimitedModel) Info() llm.ModelInfo {
	if infoProvider, ok := m.ChatModel.(interface{ Info() llm.ModelInfo }); ok {
		info := infoProvider.Info()
		if info.Name == "" {
			info.Name = m.model
		}
		if info.Provider == "" {
			info.Provider = m.provider
		}
		return info
	}
	return llm.ModelInfo{Name: m.model, Provider: m.provider}
}

func (m *outputLimitedModel) Capabilities() llm.Capabilities {
	if provider, ok := m.ChatModel.(llm.CapabilityProvider); ok {
		return provider.Capabilities()
	}
	return llm.Capabilities{Provider: m.provider, Model: m.model}
}

func (m *outputLimitedModel) JSONSchemaOverride() *bool {
	if provider, ok := m.ChatModel.(interface{ JSONSchemaOverride() *bool }); ok {
		return provider.JSONSchemaOverride()
	}
	return nil
}

func (m *outputLimitedModel) StructuredOutputFacts() llmcontract.ModelFacts {
	if provider, ok := m.ChatModel.(interface{ StructuredOutputFacts() llmcontract.ModelFacts }); ok {
		return provider.StructuredOutputFacts()
	}
	return llmcontract.ModelFacts{
		Info:               m.Info(),
		JSONSchemaOverride: m.JSONSchemaOverride(),
		Capabilities:       m.Capabilities(),
	}
}

type failoverModel struct {
	role    string
	primary *SwappableModel
	set     *ModelSet
	report  FailoverReporter
}

func (m *failoverModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	ctx = withDefaultModelRequestAgent(ctx, m.role)
	current := m.currentTarget()
	resp, err := current.model.Generate(ctx, messages, tools, opts...)
	if err == nil {
		return resp, nil
	}

	next, reason, ok := m.pickFallback(current, err, requestsJSONSchema(opts))
	if !ok {
		return nil, err
	}
	m.reportFailover(current, next, reason, err)
	return next.model.Generate(ctx, messages, tools, opts...)
}

func (m *failoverModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	ctx = withDefaultModelRequestAgent(ctx, m.role)
	out := make(chan agentcore.StreamEvent, 100)

	go func() {
		defer close(out)

		current := m.currentTarget()
		fallbackUsed := false

	retry:
		source, resp, err := m.startAttempt(ctx, current, messages, tools, opts...)
		if err != nil {
			if !fallbackUsed {
				if next, reason, ok := m.pickFallback(current, err, requestsJSONSchema(opts)); ok {
					if waitErr := waitForStreamDelay(ctx, streamRetryDelay(err)); waitErr != nil {
						return
					}
					fallbackUsed = true
					m.reportFailover(current, next, reason, err)
					current = next
					goto retry
				}
			}
			sendModelEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err})
			return
		}
		if resp != nil {
			sendModelEvent(ctx, out, agentcore.StreamEvent{
				Type:       agentcore.StreamEventDone,
				Message:    resp.Message,
				StopReason: resp.Message.StopReason,
			})
			return
		}

		forwarded := false
		for ev := range source {
			switch ev.Type {
			case agentcore.StreamEventError:
				if ev.Err != nil && !forwarded && !fallbackUsed {
					if next, reason, ok := m.pickFallback(current, ev.Err, requestsJSONSchema(opts)); ok {
						if waitErr := waitForStreamDelay(ctx, streamRetryDelay(ev.Err)); waitErr != nil {
							return
						}
						fallbackUsed = true
						m.reportFailover(current, next, reason, ev.Err)
						current = next
						goto retry
					}
				}
				sendModelEvent(ctx, out, ev)
				return
			case agentcore.StreamEventDone:
				sendModelEvent(ctx, out, ev)
				return
			default:
				forwarded = true
				if !sendModelEvent(ctx, out, ev) {
					return
				}
			}
		}
	}()

	return out, nil
}

func (m *failoverModel) SupportsTools() bool {
	return m.primary != nil && m.primary.SupportsTools()
}

func (m *failoverModel) ProviderName() string {
	if m.primary == nil {
		return ""
	}
	return m.primary.ProviderName()
}

func (m *failoverModel) Info() llm.ModelInfo {
	if m.primary == nil {
		return llm.ModelInfo{}
	}
	return m.primary.Info()
}

func (m *failoverModel) Capabilities() llm.Capabilities {
	return m.StructuredOutputFacts().Capabilities
}

func (m *failoverModel) JSONSchemaOverride() *bool {
	return m.StructuredOutputFacts().JSONSchemaOverride
}

func (m *failoverModel) StructuredOutputFacts() llmcontract.ModelFacts {
	if m.primary == nil {
		return llmcontract.ModelFacts{}
	}
	return m.primary.StructuredOutputFacts()
}

func (m *failoverModel) currentTarget() modelTarget {
	if m.primary == nil {
		return modelTarget{}
	}
	m.primary.mu.RLock()
	defer m.primary.mu.RUnlock()
	return modelTarget{
		provider:   m.primary.provider,
		name:       m.primary.name,
		model:      m.primary.SwappableModel.Current(),
		jsonSchema: cloneBoolPtr(m.primary.jsonSchema),
	}
}

func (m *failoverModel) pickFallback(current modelTarget, err error, requireJSONSchema bool) (modelTarget, string, bool) {
	if err == nil || current.model == nil {
		return modelTarget{}, "", false
	}
	if errors.Is(err, context.Canceled) {
		return modelTarget{}, "", false
	}

	if !agentcore.IsFailoverEligible(err) {
		return modelTarget{}, agentcore.FailoverReason(err), false
	}
	reason := agentcore.FailoverReason(err)
	var targets []modelTarget
	if m.set != nil {
		targets = m.set.fallbackTargets(m.role)
	}
	for _, target := range targets {
		if target.provider == current.provider && target.name == current.name {
			continue
		}
		if target.model == nil {
			continue
		}
		if requireJSONSchema && !supportsJSONSchema(target) {
			continue
		}
		return target, reason, true
	}
	return modelTarget{}, reason, false
}

func requestsJSONSchema(opts []agentcore.CallOption) bool {
	format := agentcore.ResolveCallConfig(opts).ResponseFormat
	return format != nil && format.Type == agentcore.ResponseFormatJSONSchema
}

func supportsJSONSchema(target modelTarget) bool {
	if target.jsonSchema != nil {
		return *target.jsonSchema
	}
	cp, ok := target.model.(llm.CapabilityProvider)
	return ok && cp.Capabilities().Structured.JSONSchema == llm.SupportYes
}

func (m *failoverModel) reportFailover(from, to modelTarget, reason string, err error) {
	if m.report != nil {
		m.report(FailoverEvent{
			Role:         m.role,
			Reason:       reason,
			FromProvider: from.provider,
			FromModel:    from.name,
			ToProvider:   to.provider,
			ToModel:      to.name,
			Err:          err,
		})
	}
}

func (m *failoverModel) startAttempt(ctx context.Context, target modelTarget, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, *agentcore.LLMResponse, error) {
	if target.model == nil {
		return nil, nil, fmt.Errorf("no model configured")
	}

	streamCh, err := target.model.GenerateStream(ctx, messages, tools, opts...)
	if err == nil {
		return streamCh, nil, nil
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	return nil, nil, err
}
