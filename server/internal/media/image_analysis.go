package media

import (
	"context"
	"reflect"
	"slices"
	"strings"
)

func validImageAnalysisBinding(p *Project, a Analysis) bool {
	if p.Analysis != nil && (a.TaskID != p.Analysis.TaskID || a.WorkspacePath != p.Analysis.WorkspacePath || a.WorkspaceIdentity != p.Analysis.WorkspaceIdentity || a.InputID == p.Analysis.InputID) {
		return false
	}
	if p.ImageAnalysis == nil {
		return a.Kind == "images"
	}
	return a.Kind == "planning" && p.Analysis != nil
}

// Text 保存模型原文；内嵌字段仅兼容此前的结构化观察记录。
type ImageAnalysis struct {
	Text string `json:"text,omitempty"`
	*AssetUnderstanding
}

func cloneImageAnalysis(value *ImageAnalysis) *ImageAnalysis {
	if value == nil {
		return nil
	}
	out := *value
	if value.AssetUnderstanding != nil {
		legacy := *value.AssetUnderstanding
		legacy.Keywords = slices.Clone(legacy.Keywords)
		legacy.Assets = slices.Clone(legacy.Assets)
		legacy.TemplateCandidates = slices.Clone(legacy.TemplateCandidates)
		out.AssetUnderstanding = &legacy
	}
	return &out
}

// 旧实现把主体疑问、逐图职责和模板校验当成识图成功门槛，导致完整回答被拒收。
// 此处只接收当前输入的非空原文；执行参数在后续规划/生成准入时校验。
func (s *Service) SetImageAnalysis(ctx context.Context, team, id string, version int64, input string, observation ImageAnalysis) (*Project, error) {
	p, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if p == nil || !p.Brief.ImageAnalysisFirst || p.Analysis == nil || p.Analysis.Kind != "images" || p.Analysis.InputID != input || p.CancelRequested {
		return nil, ErrConflict
	}
	if p.Status == "image_review" && reflect.DeepEqual(p.ImageAnalysis, &observation) {
		return p, nil
	}
	if strings.TrimSpace(observation.Text) == "" && (observation.AssetUnderstanding == nil || strings.TrimSpace(observation.Summary) == "") {
		return nil, ErrInvalid
	}
	// 只恢复未开始规划的旧失败输入；不覆盖已有方案、镜头或成片。
	eligible := func(v *Project) bool {
		return (v.Status == "analyzing" || v.Status == "analysis_failed") && v.Plan == nil && len(v.Shots) == 0 && v.OutputAssetID == ""
	}
	if !eligible(p) || p.Version != version {
		return nil, ErrConflict
	}
	return s.Mutate(ctx, team, id, version, func(v *Project) error {
		if v.CancelRequested || !eligible(v) || v.Analysis == nil || v.Analysis.InputID != input || v.Analysis.Kind != "images" {
			return ErrConflict
		}
		v.ImageAnalysis = cloneImageAnalysis(&observation)
		v.Status, v.Error = "image_review", ""
		return nil
	})
}
