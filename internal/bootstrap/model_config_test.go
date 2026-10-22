package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/errs"
)

func TestModelConfigAcceptsLegacyAndObjectEntries(t *testing.T) {
	var cfg Config
	input := `{
  "provider":"custom","model":"legacy-model",
  "providers":{"custom":{"type":"openai","models":[
    "legacy-model",
    {"name":"large-model","context_window":400000}
  ]}}
}`
	if err := json.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	models := cfg.Providers["custom"].Models
	if len(models) != 2 || models[0].Name != "legacy-model" || models[0].ContextWindow != 0 || models[0].MaxOutputTokens != 0 {
		t.Fatalf("legacy model decode = %#v", models)
	}
	if models[1].Name != "large-model" || models[1].ContextWindow != 400000 {
		t.Fatalf("object model decode = %#v", models[1])
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"models":["legacy-model"`) {
		t.Fatalf("models should be normalized to objects: %s", data)
	}
	if !strings.Contains(string(data), `"name":"legacy-model"`) {
		t.Fatalf("normalized model missing: %s", data)
	}
}

// json_schema 三态：未配置=nil（按 adapter 能力）、true/false=显式声明；
// legacy 字符串条目读入为 nil；写回再读取不得改变三态。
func TestModelConfigJSONSchemaTriState(t *testing.T) {
	var cfg Config
	input := `{"providers":{"custom":{"models":[
    {"name":"a","json_schema":true},
    {"name":"b","json_schema":false},
    {"name":"c"},
    "legacy"
  ]}}}`
	if err := json.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertTriState := func(models []ModelConfig, stage string) {
		t.Helper()
		if models[0].JSONSchema == nil || !*models[0].JSONSchema {
			t.Fatalf("%s: a 应为 true, got %v", stage, models[0].JSONSchema)
		}
		if models[1].JSONSchema == nil || *models[1].JSONSchema {
			t.Fatalf("%s: b 应为 false, got %v", stage, models[1].JSONSchema)
		}
		if models[2].JSONSchema != nil || models[3].JSONSchema != nil {
			t.Fatalf("%s: c/legacy 应为 nil, got %v %v", stage, models[2].JSONSchema, models[3].JSONSchema)
		}
	}
	assertTriState(cfg.Providers["custom"].Models, "decode")

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again Config
	if err := json.Unmarshal(data, &again); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	assertTriState(again.Providers["custom"].Models, "round-trip")

	if v := cfg.ModelJSONSchema("custom", "a"); v == nil || !*v {
		t.Fatalf("ModelJSONSchema(custom,a) = %v", v)
	}
	if v := cfg.ModelJSONSchema("custom", "missing"); v != nil {
		t.Fatalf("未列入模型应为 nil, got %v", v)
	}
	if v := cfg.ModelJSONSchema("nope", "a"); v != nil {
		t.Fatalf("未知 provider 应为 nil, got %v", v)
	}
}

// SwappableModel 的 json_schema 覆盖值必须随热切换原子更新：
// 切到声明不同的模型后，下一次 JSONSchemaOverride 现读即得新事实。
func TestSwappableModelJSONSchemaOverrideFollowsSwap(t *testing.T) {
	tr, fa := true, false
	cfg := Config{
		Provider: "proxy", ModelName: "a",
		Providers: map[string]ProviderConfig{"proxy": {
			Type: "openai", APIKey: "k", BaseURL: "https://example.com/v1",
			Models: []ModelConfig{{Name: "a", JSONSchema: &tr}, {Name: "b", JSONSchema: &fa}, {Name: "c"}},
		}},
	}
	ms, err := NewModelSet(cfg)
	if err != nil {
		t.Fatalf("new model set: %v", err)
	}
	if v := ms.Default.JSONSchemaOverride(); v == nil || !*v {
		t.Fatalf("初始应为 true, got %v", v)
	}
	facts := ms.Default.StructuredOutputFacts()
	if facts.Info.Name != "a" || facts.Info.Provider != "openai" || facts.JSONSchemaOverride == nil || !*facts.JSONSchemaOverride {
		t.Fatalf("初始结构化事实快照不一致: %+v", facts)
	}
	if err := ms.Swap("default", "proxy", "b"); err != nil {
		t.Fatalf("swap b: %v", err)
	}
	if v := ms.Default.JSONSchemaOverride(); v == nil || *v {
		t.Fatalf("切到 b 后应为 false, got %v", v)
	}
	facts = ms.Default.StructuredOutputFacts()
	if facts.Info.Name != "b" || facts.JSONSchemaOverride == nil || *facts.JSONSchemaOverride {
		t.Fatalf("切换后结构化事实快照不一致: %+v", facts)
	}
	if err := ms.Swap("default", "proxy", "c"); err != nil {
		t.Fatalf("swap c: %v", err)
	}
	if v := ms.Default.JSONSchemaOverride(); v != nil {
		t.Fatalf("切到未声明的 c 后应为 nil, got %v", v)
	}
}

func TestResolveContextWindowIsProviderAware(t *testing.T) {
	cfg := Config{
		ContextWindow: 300000,
		Providers: map[string]ProviderConfig{
			"one": {Models: []ModelConfig{{Name: "same", ContextWindow: 128000}}},
			"two": {Models: []ModelConfig{{Name: "same", ContextWindow: 900000}}},
		},
	}
	if got, source := cfg.ResolveContextWindow("one", "same"); got != 128000 || source != CtxWindowModelConfig {
		t.Fatalf("one/same = %d %s", got, source)
	}
	if got, source := cfg.ResolveContextWindow("two", "same"); got != 900000 || source != CtxWindowModelConfig {
		t.Fatalf("two/same = %d %s", got, source)
	}
	if got, source := cfg.ResolveContextWindow("one", "unknown"); got != 300000 || source != CtxWindowConfig {
		t.Fatalf("legacy fallback = %d %s", got, source)
	}
}

func TestResolveMaxOutputTokensPriorityAndContextCap(t *testing.T) {
	cfg := Config{
		ContextWindow: 32000,
		Providers: map[string]ProviderConfig{
			"proxy": {Models: []ModelConfig{
				{Name: "explicit", ContextWindow: 100000, MaxOutputTokens: 24000},
				{Name: "too-large", ContextWindow: 30000, MaxOutputTokens: 60000},
				{Name: "gpt-4o-mini"},
			}},
		},
	}
	cases := []struct {
		name      string
		provider  string
		model     string
		wantLimit int
		wantSrc   OutputTokenSource
	}{
		{"model config wins", "proxy", "explicit", 24000, OutputTokenModelConfig},
		{"registry fallback", "proxy", "gpt-4o-mini", 16384, OutputTokenRegistry},
		{"unknown default", "proxy", "unknown-model", DefaultMaxOutputTokens, OutputTokenDefault},
		{"context cap", "proxy", "too-large", 30000, OutputTokenContextCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, source := cfg.ResolveMaxOutputTokens(tc.provider, tc.model)
			if got != tc.wantLimit || source != tc.wantSrc {
				t.Fatalf("ResolveMaxOutputTokens(%q,%q) = %d %s, want %d %s",
					tc.provider, tc.model, got, source, tc.wantLimit, tc.wantSrc)
			}
		})
	}
}

func TestValidateBaseRejectsNegativeModelLimits(t *testing.T) {
	for _, model := range []ModelConfig{
		{Name: "bad-context", ContextWindow: -1},
		{Name: "bad-output", MaxOutputTokens: -1},
	} {
		cfg := Config{
			Provider: "proxy", ModelName: model.Name,
			Providers: map[string]ProviderConfig{
				"proxy": {Type: "openai", APIKey: "test-key", Models: []ModelConfig{model}},
			},
		}
		if err := cfg.ValidateBase(); err == nil || !errors.Is(err, errs.ErrConfig) {
			t.Fatalf("model %+v should be rejected with ErrConfig, got %v", model, err)
		}
	}
}

func TestOutputLimitedModelHonorsExplicitTaskBudget(t *testing.T) {
	inner := &recordingModel{}
	limited := newOutputLimitedModel(inner, "proxy", "writer-model", 12000)

	if _, err := limited.Generate(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := inner.generateMaxTokens[0]; got != 12000 {
		t.Fatalf("default Generate max tokens = %d, want 12000", got)
	}
	if _, err := limited.Generate(context.Background(), nil, nil, agentcore.WithMaxTokens(3200)); err != nil {
		t.Fatal(err)
	}
	if got := inner.generateMaxTokens[1]; got != 3200 {
		t.Fatalf("explicit Generate max tokens = %d, want 3200", got)
	}

	stream, err := limited.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	if got := inner.streamMaxTokens[0]; got != 12000 {
		t.Fatalf("default GenerateStream max tokens = %d, want 12000", got)
	}
	stream, err = limited.GenerateStream(context.Background(), nil, nil, agentcore.WithMaxTokens(2800))
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	if got := inner.streamMaxTokens[1]; got != 2800 {
		t.Fatalf("explicit GenerateStream max tokens = %d, want 2800", got)
	}
}

type recordingModel struct {
	generateMaxTokens []int
	streamMaxTokens   []int
}

func (m *recordingModel) Generate(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.generateMaxTokens = append(m.generateMaxTokens, agentcore.ResolveCallConfig(opts).MaxTokens)
	return &agentcore.LLMResponse{}, nil
}

func (m *recordingModel) GenerateStream(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.streamMaxTokens = append(m.streamMaxTokens, agentcore.ResolveCallConfig(opts).MaxTokens)
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: agentcore.Message{Role: agentcore.RoleAssistant, StopReason: agentcore.StopReasonStop}, StopReason: agentcore.StopReasonStop}
	close(ch)
	return ch, nil
}

func (*recordingModel) SupportsTools() bool { return false }

func TestSaveProviderConfigPreservesSelectionAndUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ainovel", "config.json")
	original := Config{
		Provider: "old", ModelName: "old-model", Style: "fantasy",
		Providers: map[string]ProviderConfig{"old": {Type: "openai", Models: []ModelConfig{{Name: "old-model"}}}},
		Budget:    BudgetConfig{BookUSD: 20, WarnRatio: 0.8},
	}
	if err := SaveConfig(path, original); err != nil {
		t.Fatalf("seed: %v", err)
	}
	pc := ProviderConfig{Type: "openai", Models: []ModelConfig{{Name: "new-model", ContextWindow: 500000}}}
	if err := SaveProviderConfig(path, "new", pc); err != nil {
		t.Fatalf("save provider config: %v", err)
	}
	got, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// 只补 providers 段：无关字段与顶层 provider/model 选择必须原样保留。
	if got.Style != "fantasy" || got.Budget.BookUSD != 20 || got.Provider != "old" || got.ModelName != "old-model" {
		t.Fatalf("selection or unrelated fields mutated: %#v", got)
	}
	if _, ok := got.Providers["old"]; !ok {
		t.Fatal("existing provider was removed")
	}
	if got.Providers["new"].Models[0].ContextWindow != 500000 {
		t.Fatalf("new provider not patched in: %#v", got.Providers["new"])
	}
	// 权限断言只在有 POSIX 权限位语义的平台上有意义：Windows 把一切上报为
	// 0666/0444，此断言在该平台恒假（参见 version.TestReplaceExecutable 同款处理）。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
		}
	}
}
