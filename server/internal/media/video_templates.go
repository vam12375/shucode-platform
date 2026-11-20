package media

import (
	"context"
	"fmt"
	"slices"
	"unicode/utf8"
)

type TemplateText struct {
	ZH string `json:"zh"`
	EN string `json:"en"`
}
type TemplateBeat struct {
	Purpose string  `json:"purpose"`
	Framing string  `json:"framing"`
	Action  string  `json:"action"`
	Camera  string  `json:"camera"`
	Weight  float64 `json:"weight"`
	// v2 配方的可选字段：全部 omitempty，旧配方序列化字节与快照 digest 不变，
	// 旧客户端按未知字段忽略。Seconds 是标称整片中的秒数；Tier 决定压缩时的取舍顺序；
	// Group 把相邻的一组镜头绑成一个动作环（如“提起→细看→放回”），精简时整组一同保留或一同删去。
	Seconds int    `json:"seconds,omitempty"`
	Tier    string `json:"tier,omitempty"`
	Act     string `json:"act,omitempty"`
	Group   string `json:"group,omitempty"`
	Sound   string `json:"sound,omitempty"`
}

// 压缩时 core 永不删除；先删 extra 再删 support，这两类插入镜不能改变主线状态。
// 同组镜头作为一个整体取舍：组内首镜的起态与末镜的完成态都等于组前后的静止状态，整组删去不会断链。
const (
	TemplateTierCore    = "core"
	TemplateTierSupport = "support"
	TemplateTierExtra   = "extra"
)

// TemplateStyle 是整片统一的拍摄风格；独立生成的每一段都要复述它，不能只写"沿用全片"。
type TemplateStyle struct {
	Look      string `json:"look"`
	Lens      string `json:"lens"`
	Motion    string `json:"motion"`
	Sound     string `json:"sound"`
	Narration string `json:"narration,omitempty"`
}
type TemplateDurationRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}
type TemplatePreview struct {
	CoverURL string `json:"coverUrl,omitempty"`
	VideoURL string `json:"videoUrl,omitempty"`
}
type VideoTemplate struct {
	Experiment        *TemplateExperiment   `json:"experiment,omitempty"`
	Preview           *TemplatePreview      `json:"preview,omitempty"`
	SkillIDs          []string              `json:"skillIds,omitempty"`
	Provenance        *TemplateProvenance   `json:"provenance,omitempty"`
	ID                string                `json:"id"`
	Version           string                `json:"version"`
	Family            string                `json:"family"`
	Name              TemplateText          `json:"name"`
	Description       TemplateText          `json:"description"`
	Industries        []string              `json:"industries"`
	Objectives        []string              `json:"objectives"`
	Keywords          []string              `json:"keywords"`
	RequiredFacts     []string              `json:"requiredFacts"`
	AssetRequirements []string              `json:"assetRequirements"`
	AvoidWhen         []string              `json:"avoidWhen"`
	MinAssets         int                   `json:"minAssets"`
	AspectRatios      []string              `json:"aspectRatios"`
	DurationRange     TemplateDurationRange `json:"durationRange"`
	NominalSeconds    int                   `json:"nominalSeconds,omitempty"`
	Style             *TemplateStyle        `json:"style,omitempty"`
	Beats             []TemplateBeat        `json:"beats"`
	QualityChecks     []string              `json:"qualityChecks"`
	VerifiedModelIDs  []string              `json:"verifiedModelIds"`
}
type TemplateExperiment struct {
	SuiteID      string `json:"suiteId"`
	FormulaID    string `json:"formulaId"`
	Instructions string `json:"instructions,omitempty"`
}

func VideoTemplateDefinitions() ([]VideoTemplate, error) {
	out, err := loadVideoTemplateCatalog(videoTemplateFiles)
	if err != nil {
		return nil, fmt.Errorf("video_templates_unavailable")
	}
	seen := map[string]bool{}
	for i := range out {
		t := &out[i]
		if !boundedNonempty(t.ID, 100) || !boundedNonempty(t.Version, 100) || seen[t.ID] || len(t.Beats) == 0 || t.MinAssets < 1 || t.DurationRange.Min < 5 || t.DurationRange.Max > MaxBriefSeconds {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
		if len(t.SkillIDs) > 6 {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
		skillSeen := map[string]bool{}
		for _, id := range t.SkillIDs {
			if !boundedNonempty(id, 100) || skillSeen[id] {
				return nil, fmt.Errorf("video_templates_unavailable")
			}
			skillSeen[id] = true
		}
		if !validTemplateTiming(*t) {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
		seen[t.ID] = true
		t.VerifiedModelIDs = []string{}
	}
	return out, nil
}

// 没有任何 v2 字段的旧配方原样通过；一旦出现任一 v2 字段，整份配方必须自洽，
// 否则客户端按标称秒数压缩时会得到缺字段或总和不符的节拍表。
func validTemplateTiming(t VideoTemplate) bool {
	timed := t.NominalSeconds != 0 || t.Style != nil
	for _, beat := range t.Beats {
		if beat.Seconds != 0 || beat.Tier != "" || beat.Act != "" || beat.Group != "" || beat.Sound != "" {
			timed = true
		}
	}
	if !timed {
		return true
	}
	style := t.Style
	if t.NominalSeconds < t.DurationRange.Min || t.NominalSeconds > t.DurationRange.Max || len(t.Beats) > 30 || style == nil ||
		!boundedNonempty(style.Look, 600) || !boundedNonempty(style.Lens, 600) || !boundedNonempty(style.Motion, 600) || !boundedNonempty(style.Sound, 600) || utf8.RuneCountInString(style.Narration) > 600 {
		return false
	}
	total := 0
	seenGroups, groupSizes := map[string]bool{}, map[string]int{}
	previousGroup, previousTier := "", ""
	for _, beat := range t.Beats {
		switch beat.Tier {
		case TemplateTierCore, TemplateTierSupport, TemplateTierExtra:
		default:
			return false
		}
		if beat.Seconds < 2 || beat.Seconds > 30 || beat.Weight != float64(beat.Seconds) || !boundedNonempty(beat.Act, 30) || !boundedNonempty(beat.Sound, 400) {
			return false
		}
		if beat.Group != "" {
			// 整组一同取舍：组必须相邻、同层级且不含核心镜，否则精简会拆开一个完整的动作环。
			if !boundedNonempty(beat.Group, 30) || beat.Tier == TemplateTierCore ||
				beat.Group != previousGroup && seenGroups[beat.Group] || beat.Group == previousGroup && beat.Tier != previousTier {
				return false
			}
			seenGroups[beat.Group] = true
			groupSizes[beat.Group]++
		}
		previousGroup, previousTier = beat.Group, beat.Tier
		total += beat.Seconds
	}
	for _, size := range groupSizes {
		if size < 2 {
			return false
		}
	}
	return total == t.NominalSeconds
}

// VideoTemplateCatalog 只为团队可用的视频模型计算兼容性，公式正文仅在选定后随快照下发。
func (s *Service) VideoTemplateCatalog(ctx context.Context, models []Model) (regular, formulas []VideoTemplate, err error) {
	catalog, err := s.resolveTemplateCatalog(ctx, models)
	if err != nil {
		return nil, nil, err
	}
	regular, formulas = []VideoTemplate{}, []VideoTemplate{}
	for _, entry := range catalog {
		if !entry.enabled {
			continue
		}
		t := entry.snapshot.Template
		// 旧客户端的解码上限为 100，超出会导致整份目录解析失败。上限只作用于兼容字段。
		const legacyModelLimit = 100
		t.VerifiedModelIDs = t.VerifiedModelIDs[:min(len(t.VerifiedModelIDs), legacyModelLimit)]
		if t.Experiment == nil {
			regular = append(regular, t)
			continue
		}
		meta := *t.Experiment
		meta.Instructions = ""
		t.Experiment = &meta
		formulas = append(formulas, t)
	}
	return regular, formulas, nil
}

func findVideoTemplate(templates []VideoTemplate, id, version string) (VideoTemplate, error) {
	for _, t := range templates {
		if t.ID == id && t.Version == version {
			return t, nil
		}
	}
	return VideoTemplate{}, adaptiveResearchError()
}

func validateTemplateSelection(b Brief, templates []VideoTemplate) (VideoTemplate, error) {
	t, err := findVideoTemplate(templates, b.TemplateID, b.TemplateVersion)
	if err != nil {
		return VideoTemplate{}, err
	}
	assetCount := len(videoProductAssetIDs(b))
	if b.AssetUnderstanding != nil {
		assetCount = 0
		for _, asset := range b.AssetUnderstanding.Assets {
			// 片尾绑定优先于识图角色；不参与生成的图片不能补足模板主体数，与客户端一致。
			if b.Finishing != nil && b.Finishing.Brand != nil && asset.AssetID == b.Finishing.Brand.LogoAssetID {
				continue
			}
			// 品牌/IP 可用角色造型图锁身份；Logo 和场景图不能补足商品证据。
			if asset.Role == "product" || (t.Family == "brand" && asset.Role == "character") {
				assetCount++
			}
		}
	}
	if assetCount < t.MinAssets || !slices.Contains(t.AspectRatios, b.AspectRatio) || b.Duration < t.DurationRange.Min || b.Duration > t.DurationRange.Max {
		return VideoTemplate{}, adaptiveResearchError()
	}
	// 原因：旧套件的三张约定被当成模型容量；图片上限交给制作单及真实模型能力校验。
	if t.Experiment != nil && (b.Duration != 30 || len(b.AssetIDs) < 1 || b.ReferenceMode != "multimodal" || b.ImageModel != "" || (b.GenerationPolicy != "" && b.GenerationPolicy != "whole-first") || (b.ImageAnalysisFirst && b.GenerationPolicy != "whole-first") || b.PromptPolicy == "direct" || b.Mode != "auto" || b.SourceOnly || b.ReferenceAssetID != "" || b.ResearchPolicy != "disabled" || len(b.ReferenceURLs) != 0 || b.CreativeExpansion == "") {
		return VideoTemplate{}, adaptiveResearchError()
	}
	// 新导演模板的六个编辑时段不是供应商请求；只走已有整片分段，旧快照仍保留原规则。
	if t.Experiment != nil && t.Experiment.SuiteID == productAdDirectorSuite && b.GenerationPolicy != "whole-first" {
		return VideoTemplate{}, adaptiveResearchError()
	}
	return t, nil
}

func validateFormulaTestPlan(b Brief, p Plan, t *TemplateSnapshot, models ...Model) error {
	if t == nil || t.Template.Experiment == nil {
		return nil
	}
	// 原因：创作短镜头曾被误映射成付费请求；新单按模型能力执行，旧快照短段规则保持。
	whole := b.GenerationPolicy == "whole-first"
	if t.Template.Experiment.SuiteID == productAdDirectorSuite && !whole {
		return fmt.Errorf("video_formula_test_plan_invalid")
	}
	if !whole && (len(p.Shots) < 5 || len(p.Shots) > 7) || whole && len(p.Shots) == 0 {
		return fmt.Errorf("video_formula_test_plan_invalid")
	}
	if whole && len(models) > 0 {
		model, err := FindModel(models, b.VideoModel, "video")
		if err != nil || len(p.Shots) != len(planImageGenerationDurations(b, model)) {
			return fmt.Errorf("video_formula_test_plan_invalid")
		}
	}
	// 只检查归属会漏掉未使用的输入图；同时记录实际引用以落实完整素材约束。
	used := map[string]bool{}
	for i, shot := range p.Shots {
		if (!whole && (shot.Duration < 4 || shot.Duration > 6)) || shot.AssetID != "" || shot.ImagePrompt != "" || shot.Creative == nil || shot.Creative.Source != "asset" || len(shot.Creative.AssetIDs) == 0 {
			return fmt.Errorf("video_formula_test_plan_invalid")
		}
		delivery := shot.Creative.OutFrame - shot.Creative.InFrame
		min, max := 4, 6
		if i == 0 {
			max = 5
		}
		if i == len(p.Shots)-1 {
			min, max = 3, 4
		}
		if (!whole && (delivery < min*30 || delivery > max*30)) || delivery > shot.Duration*30 {
			return fmt.Errorf("video_formula_test_plan_invalid")
		}
		for _, id := range shot.Creative.AssetIDs {
			if !slices.Contains(b.AssetIDs, id) {
				return fmt.Errorf("video_formula_test_plan_invalid")
			}
			used[id] = true
		}
	}
	for _, id := range b.AssetIDs {
		if !used[id] {
			return fmt.Errorf("video_formula_test_plan_invalid")
		}
	}
	return nil
}
func (s *Service) validateAdaptivePlan(_ context.Context, b Brief, p Plan, models []Model, snapshots ...*TemplateSnapshot) error {
	if err := validateStoryPlan(b, p); err != nil {
		return err
	}
	var snapshot *TemplateSnapshot
	if len(snapshots) > 0 {
		snapshot = snapshots[0]
	}
	if err := validateFormulaTestPlan(b, p, snapshot, models...); err != nil {
		return err
	}
	if err := validateSelectedImageUsage(b, p); err != nil {
		return err
	}
	// 模板已在制作单创建/选择时冻结；目录、验证与研究说明不重复否决已完成镜头。
	return validateResearchPlan(b, p)
}

// 测试使用全能参考；新整片制作按真实能力分段，旧单仍验证短段能力。
func validateFormulaTestModel(b Brief, t VideoTemplate, models []Model) error {
	if t.Experiment == nil {
		return nil
	}
	if t.Experiment.SuiteID == productAdDirectorSuite && b.GenerationPolicy != "whole-first" {
		return adaptiveResearchError()
	}
	m, err := FindModel(models, b.VideoModel, "video")
	if err != nil {
		return err
	}
	if err = validateBriefReferenceImages(b, m); err != nil {
		return err
	}
	if b.GenerationPolicy == "whole-first" {
		if len(planImageGenerationDurations(b, m)) == 0 {
			return adaptiveResearchError()
		}
		return nil
	}
	for _, seconds := range m.AllowedDurations() {
		if seconds == 5 || seconds == 6 {
			return nil
		}
	}
	return adaptiveResearchError()
}
