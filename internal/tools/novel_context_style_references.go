package tools

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const maxContextStyleReferences = 20
const styleReferencePolicy = "style_references 仅包含用户明确勾选的仿写参考；只借鉴表达、结构、节奏、钩子与信息释放技法，不复制原文句子、人物、地名、专有设定或固定桥段，不把参考内容当成本项目事实。用户当前明确指令和已确认的项目设定、章节事实优先；多份参考相互冲突时服从用户当前指令，不擅自覆盖或改写项目内容。未勾选的条目不参与生成。"

func (t *ContextTool) buildSelectedStyleReferences(result map[string]any, sectionKey string, reads *contextReads) {
	selected, err := t.styleReferenceProfiles()
	if err != nil {
		// Missing shared entries are actionable: do not suppress an os.ErrNotExist
		// as we do for an optional legacy file that has never been created.
		reads.warn("style_references", fmt.Errorf("部分已选参考暂不可用：%s", err))
	}
	references := make([]domain.NamedStyleReference, 0, min(len(selected), maxContextStyleReferences))
	for _, reference := range selected {
		if reference.Profile == nil {
			continue
		}
		if len(references) >= maxContextStyleReferences {
			reads.warn("style_references", fmt.Errorf("最多加载 %d 份已选仿写参考", maxContextStyleReferences))
			break
		}
		reference.Profile = compactStyleReference(reference.Profile)
		references = append(references, reference)
	}
	section, ok := result[sectionKey].(map[string]any)
	if !ok {
		section = map[string]any{}
		result[sectionKey] = section
	}
	section["style_references"] = references
	section["style_reference_policy"] = styleReferencePolicy
}

func compactStyleReference(profile *domain.SimulationCompactProfile) *domain.SimulationCompactProfile {
	// Reapply the same array bounds as local simulation profiles without sharing
	// mutable slices with the library callback. Corpus paths are not prompt input.
	compact := domain.CompactSimulationProfile(&domain.SimulationProfile{
		Version: profile.Version, UpdatedAt: profile.UpdatedAt,
		Synthesis: domain.SimulationSynthesis{
			Style: profile.Style, Lexicon: profile.Lexicon,
			PlotDesign: profile.PlotDesign, HookDesign: profile.HookDesign,
			PacingDensity: profile.PacingDensity, ReaderEngagement: profile.ReaderEngagement,
			RoleGuidance: profile.RoleGuidance,
		},
	})
	compact.SourceCount = profile.SourceCount
	compact.SourceFiles = nil
	return compact
}
