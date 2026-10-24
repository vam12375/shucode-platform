package media

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

// 原片报告只记录观察事实；生成素材、提示和供应商秒数在后续方案中单独绑定。
type SourceShot struct {
	SourceURL             string  `json:"sourceUrl"`
	SourceStart           float64 `json:"sourceStart"`
	SourceEnd             float64 `json:"sourceEnd"`
	Framing               string  `json:"framing"`
	Camera                string  `json:"camera"`
	Action                string  `json:"action"`
	Purpose               string  `json:"purpose"`
	Subject               string  `json:"subject,omitempty"`
	Scene                 string  `json:"scene,omitempty"`
	Sound                 string  `json:"sound,omitempty"`
	Subtitle              string  `json:"subtitle,omitempty"`
	Transition            string  `json:"transition,omitempty"`
	TransitionDescription string  `json:"transitionDescription,omitempty"`
}

type SourceAnalysis struct {
	SourceChecksum string       `json:"sourceChecksum"`
	Summary        string       `json:"summary"`
	References     []Reference  `json:"references"`
	Shots          []SourceShot `json:"shots"`
}

func (shot *SourceShot) UnmarshalJSON(raw []byte) error {
	type sourceShotJSON SourceShot
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"sourceUrl", "sourceStart", "sourceEnd", "framing", "camera", "action", "purpose"} {
		if value, ok := fields[key]; !ok || bytes.Equal(value, []byte("null")) {
			return ErrInvalid
		}
	}
	var parsed sourceShotJSON
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	*shot = SourceShot(parsed)
	return nil
}

func validSourceChecksum(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && value == strings.ToLower(value)
}

func (s *Service) CreateReferenceAnalysis(ctx context.Context, team, user, device, id, assetID string) (*Project, error) {
	if _, err := uuid.Parse(id); err != nil || assetID == "" {
		return nil, ErrInvalid
	}
	existing, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.UserID != user || existing.DeviceID != device || existing.Brief.ReferenceAssetID != assetID || existing.SourceChecksum == "" {
			return nil, ErrConflict
		}
		return existing, nil
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return nil, err
	}
	var source *Asset
	for i := range assets {
		if assets[i].ID == assetID {
			source = &assets[i]
			break
		}
	}
	if source == nil || source.Internal || source.Kind != "video" || !finitePositive(source.DurationSeconds) || source.DurationSeconds > MaxReferenceSeconds || !validSourceChecksum(source.Checksum) {
		return nil, fmt.Errorf("reference_asset_not_found")
	}
	name := strings.TrimSpace(source.Name)
	if name == "" {
		name = "参考视频"
	}
	if utf8.RuneCountInString(name) > 200 {
		name = string([]rune(name)[:200])
	}
	references := []string{}
	if source.SourceURL != "" {
		if !ValidURL(source.SourceURL) {
			return nil, ErrInvalid
		}
		references = append(references, source.SourceURL)
	}
	brief := Brief{SourceOnly: true, Mode: "replicate", Product: name, AssetIDs: []string{}, ReferenceURLs: references, Platforms: []string{"douyin", "xiaohongshu"}, ReviewPlan: true, ReferenceAssetID: source.ID, ReferenceDurationSeconds: source.DurationSeconds, AspectRatio: "9:16", Duration: max(5, int(math.Ceil(source.DurationSeconds))), MaxCreditsMicro: 50_000_000}
	brief.AspectRatio = nearestFixedVideoRatio(source.Width, source.Height)
	now := time.Now().UTC()
	p := &Project{ID: id, Version: 1, Status: "draft", TeamID: team, UserID: user, DeviceID: device, Brief: brief, SourceChecksum: source.Checksum, Shots: []Shot{}, Models: []Model{}, CreatedAt: now, UpdatedAt: now}
	if err = s.Repo.Index(ctx, ProjectKey(team, id)); err != nil {
		return nil, err
	}
	if err = s.Repo.Save(ctx, p, nil); err != nil {
		// 同请求并发只能有一个制作单；CAS 失败后读取获胜者，不创建第二个分析身份。
		latest, readErr := s.Repo.Get(ctx, team, id)
		if readErr == nil && latest != nil && latest.UserID == user && latest.DeviceID == device && latest.Brief.ReferenceAssetID == assetID && latest.SourceChecksum == source.Checksum {
			return latest, nil
		}
		return nil, err
	}
	return p, nil
}

func validateSourceAnalysis(p *Project, report SourceAnalysis) error {
	if !validSourceChecksum(report.SourceChecksum) || report.SourceChecksum != p.SourceChecksum || strings.TrimSpace(report.Summary) == "" || utf8.RuneCountInString(report.Summary) > 12000 || len(report.References) < 1 || len(report.References) > 20 || len(report.Shots) < 1 || len(report.Shots) > 30 {
		return ErrInvalid
	}
	refs := map[string]bool{}
	for _, ref := range report.References {
		if (len(p.Brief.ReferenceURLs) == 0 && ref.URL != "") || (len(p.Brief.ReferenceURLs) > 0 && !slices.Contains(p.Brief.ReferenceURLs, ref.URL)) || strings.TrimSpace(ref.Title) == "" || strings.TrimSpace(ref.Evidence) == "" || strings.TrimSpace(ref.SelectionReason) == "" || ref.CapturedAt.IsZero() || utf8.RuneCountInString(ref.Title) > 300 || utf8.RuneCountInString(ref.Evidence) > 3000 || utf8.RuneCountInString(ref.SelectionReason) > 1000 || (ref.Likes != nil && (*ref.Likes < 0 || math.IsNaN(*ref.Likes) || math.IsInf(*ref.Likes, 0))) {
			return ErrInvalid
		}
		refs[ref.URL] = true
	}
	end := float64(0)
	for _, shot := range report.Shots {
		if !refs[shot.SourceURL] || math.IsNaN(shot.SourceStart) || math.IsInf(shot.SourceStart, 0) || !finitePositive(shot.SourceEnd) || math.Abs(shot.SourceStart-end) > .001 || shot.SourceEnd > p.Brief.ReferenceDurationSeconds+.001 || shot.SourceEnd-shot.SourceStart < 1.0/30 || strings.TrimSpace(shot.Framing) == "" || strings.TrimSpace(shot.Camera) == "" || strings.TrimSpace(shot.Action) == "" || strings.TrimSpace(shot.Purpose) == "" || utf8.RuneCountInString(shot.Framing) > 500 || utf8.RuneCountInString(shot.Camera) > 500 || utf8.RuneCountInString(shot.Action) > 1000 || utf8.RuneCountInString(shot.Purpose) > 500 || utf8.RuneCountInString(shot.Subject) > 1000 || utf8.RuneCountInString(shot.Scene) > 1000 || utf8.RuneCountInString(shot.Sound) > 1000 || utf8.RuneCountInString(shot.Subtitle) > 300 || utf8.RuneCountInString(shot.TransitionDescription) > 1000 || (shot.Transition != "" && shot.Transition != "cut" && shot.Transition != "fade") {
			return ErrInvalid
		}
		end = shot.SourceEnd
	}
	if math.Abs(end-p.Brief.ReferenceDurationSeconds) > .001 {
		return ErrInvalid
	}
	return nil
}

func (s *Service) SetSourceAnalysis(ctx context.Context, team, id string, version int64, input string, report SourceAnalysis) (*Project, error) {
	current, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, ErrInvalid
	}
	if current.SourceAnalysis != nil && current.Analysis != nil && current.Analysis.Kind == "source" && current.Analysis.InputID == input && equalSourceAnalysis(*current.SourceAnalysis, report) {
		if current.Status == "analysis_settling" {
			return s.finishAnalysisFee(ctx, current)
		}
		if current.Status == "source_ready" {
			return current, nil
		}
	}
	p, err := s.Mutate(ctx, team, id, version, func(v *Project) error {
		if !v.Brief.SourceOnly || v.Status != "analyzing" || v.Analysis == nil || v.Analysis.Kind != "source" || v.Analysis.InputID != input || v.AnalysisFee == nil {
			return ErrConflict
		}
		if err := validateSourceAnalysis(v, report); err != nil {
			return err
		}
		v.SourceAnalysis = &report
		v.Status = "analysis_settling"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.finishAnalysisFee(ctx, p)
}

func (s *Service) ConfigureReference(ctx context.Context, team, id string, version int64, brief Brief) (*Project, error) {
	current, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.SourceAnalysis == nil || current.SourceChecksum != current.SourceAnalysis.SourceChecksum {
		return nil, ErrConflict
	}
	if brief.SourceOnly || brief.ReferenceAssetID != current.Brief.ReferenceAssetID || math.Abs(brief.ReferenceDurationSeconds-current.Brief.ReferenceDurationSeconds) > .001 {
		return nil, ErrInvalid
	}
	if brief.GenerationMode == "video_reference" && current.SourceAnalysis == nil {
		return nil, fmt.Errorf("video_reference_source_analysis_required")
	}
	brief.ReviewPlan = true
	brief.ReferenceDurationSeconds = current.Brief.ReferenceDurationSeconds
	// 配置重放只读取已冻结结果，不能清空已经派发的 adaptation 输入或重新收费。
	if !current.Brief.SourceOnly && reflect.DeepEqual(current.Brief, brief) {
		return current, nil
	}
	if current.Status != "source_ready" || !current.Brief.SourceOnly {
		return nil, ErrConflict
	}
	models, err := s.Repo.Models(ctx)
	if err != nil {
		return nil, err
	}
	policy, err := s.Repo.Store.GetTeamPolicy(ctx, team)
	if err != nil {
		return nil, err
	}
	if !store.TeamModelAllowed(policy, brief.VideoModel) || (brief.ImageModel != "" && !store.TeamModelAllowed(policy, brief.ImageModel)) {
		return nil, fmt.Errorf("model_not_allowed")
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return nil, err
	}
	if err = ValidateBrief(brief, models, assets); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(brief.ReferenceURLs, current.Brief.ReferenceURLs) {
		return nil, ErrInvalid
	}
	configured, err := s.Mutate(ctx, team, id, version, func(v *Project) error {
		if v.Status != "source_ready" || !v.Brief.SourceOnly || v.SourceAnalysis == nil {
			return ErrConflict
		}
		v.Brief = brief
		v.Models = models
		// 原片与改编属于同一聊天；配置期保留定位，下一阶段仍须以新 input/kind 经 CAS 原子替换。
		v.Status = "draft"
		v.Error = ""
		return nil
	})
	if errors.Is(err, ErrConflict) {
		latest, readErr := s.Repo.Get(ctx, team, id)
		if readErr == nil && latest != nil && !latest.Brief.SourceOnly && reflect.DeepEqual(latest.Brief, brief) {
			return latest, nil
		}
	}
	return configured, err
}

func validateSourceBoundaries(p *Project, plan Plan) error {
	if p.SourceAnalysis == nil {
		return nil
	}
	if p.SourceAnalysis.SourceChecksum != p.SourceChecksum || len(plan.Shots) != len(p.SourceAnalysis.Shots) {
		return ErrInvalid
	}
	for i, shot := range plan.Shots {
		source := p.SourceAnalysis.Shots[i]
		if shot.SourceURL != source.SourceURL || math.Abs(shot.SourceStart-source.SourceStart) > .001 || math.Abs(shot.SourceEnd-source.SourceEnd) > .001 {
			return ErrInvalid
		}
	}
	return nil
}

func cloneSourceAnalysis(report *SourceAnalysis) *SourceAnalysis {
	if report == nil {
		return nil
	}
	raw, _ := json.Marshal(report)
	var result SourceAnalysis
	_ = json.Unmarshal(raw, &result)
	return &result
}

func equalSourceAnalysis(left, right SourceAnalysis) bool {
	a, err := json.Marshal(left)
	if err != nil {
		return false
	}
	b, err := json.Marshal(right)
	return err == nil && string(a) == string(b)
}

var deepSeekFlashIdentity = regexp.MustCompile(`^deepseek(?:[-_./:]v?[0-9]+(?:\.[0-9]+)*)*[-_./:]flash(?:[-_./:][0-9]+(?:\.[0-9]+)*)*$`)

// Flash 身份按最终上游校验；分隔后的版本/日期可变，但 Pro 和子串冒充不能放行。
func IsDeepSeekAnalysisModel(value string) bool {
	return deepSeekFlashIdentity.MatchString(strings.ToLower(strings.TrimSpace(value)))
}
