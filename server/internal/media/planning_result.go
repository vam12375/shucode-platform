package media

import (
	"context"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

type PlanningIssue struct {
	Path string `json:"path"`
	Code string `json:"code"`
}
type PlanningResult struct {
	Text   string          `json:"text"`
	Issues []PlanningIssue `json:"issues"`
}

var planningIssueCode = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)

func clonePlanningResult(value *PlanningResult) *PlanningResult {
	if value == nil {
		return nil
	}
	out := *value
	out.Issues = slices.Clone(value.Issues)
	return &out
}

func planningResultEligible(p *Project, input string) bool {
	return p.Brief.Mode == "auto" && p.Brief.ReferenceAssetID == "" && !p.Brief.SourceOnly && p.Analysis != nil && p.Analysis.Kind != "images" && p.Analysis.Kind != "source" && p.Analysis.InputID == input && input != "" && !p.CancelRequested && p.Plan == nil && len(p.Shots) == 0 && p.OutputAssetID == "" && slices.Contains([]string{"analyzing", "analysis_failed", "analysis_ready"}, p.Status)
}

// 分析正常返回不等于执行参数已经编译；先保存完整结果，不再把格式差异写成模型失败。
func (s *Service) SetPlanningResult(ctx context.Context, team, id string, version int64, input string, result PlanningResult) (*Project, error) {
	if strings.TrimSpace(result.Text) == "" || utf8.RuneCountInString(result.Text) > 200000 || result.Issues == nil || len(result.Issues) > 30 {
		return nil, ErrInvalid
	}
	for _, issue := range result.Issues {
		if utf8.RuneCountInString(issue.Path) > 200 || len(issue.Code) > 100 || !planningIssueCode.MatchString(issue.Code) {
			return nil, ErrInvalid
		}
	}
	p, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if p == nil || !planningResultEligible(p, input) {
		return nil, ErrConflict
	}
	if p.Status == "analysis_ready" && reflect.DeepEqual(p.PlanningResult, &result) {
		return p, nil
	}
	return s.Mutate(ctx, team, id, version, func(v *Project) error {
		if !planningResultEligible(v, input) {
			return ErrConflict
		}
		if v.PlanningResult != nil && v.PlanningResult.Text != result.Text {
			v.PlanReview = nil
		}
		v.PlanningResult = clonePlanningResult(&result)
		v.Status, v.Error = "analysis_ready", ""
		return nil
	})
}
