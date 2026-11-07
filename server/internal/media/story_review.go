package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func candidatePlanHash(plan Plan) (string, error) {
	if plan.Story != nil {
		copyPlan := plan
		copyStory := *plan.Story
		copyStory.Review = nil
		copyPlan.Story = &copyStory
		plan = copyPlan
	}
	raw, err := json.Marshal(withoutReferenceClipBindings(plan))
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", ErrInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) PlanReview(ctx context.Context, team, id string, input PlanReviewRequest) (*Project, bool, error) {
	if input.Version <= 0 || input.RequestID == "" || input.InputID == "" || len(input.RequestID) > 100 || len(input.CandidateHash) != 64 || input.CandidateHash != strings.ToLower(input.CandidateHash) {
		return nil, false, ErrInvalid
	}
	current, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, false, err
	}
	if current == nil {
		return nil, false, ErrInvalid
	}
	if !storyPlanning(current.Brief) {
		return nil, false, fmt.Errorf("video_story_invalid")
	}
	if current.Analysis == nil || current.Analysis.InputID != input.InputID {
		return nil, false, ErrConflict
	}
	switch input.Operation {
	case "start":
		if input.Review != nil {
			return nil, false, ErrInvalid
		}
		if existing := current.PlanReview; existing != nil {
			if existing.InputID == input.InputID && existing.CandidateHash == input.CandidateHash {
				if existing.Status == "unavailable" && existing.RequestID == input.RequestID {
					return nil, false, fmt.Errorf("video_story_review_required")
				}
				if existing.RequestID == input.RequestID || existing.Status == "pending" || existing.Status == "passed" || existing.Status == "warning" {
					return current, false, nil
				}
				if existing.Status != "unavailable" {
					return nil, false, fmt.Errorf("video_story_review_required")
				}
			} else if existing.Status == "pending" {
				return nil, false, fmt.Errorf("video_story_review_required")
			}
		}
		started := false
		p, err := s.Mutate(ctx, team, id, input.Version, func(p *Project) error {
			if p.Status != "analyzing" || p.Analysis == nil || p.Analysis.InputID != input.InputID || p.CancelRequested {
				return ErrConflict
			}
			if p.PlanReview != nil {
				existing := p.PlanReview
				if existing.InputID != input.InputID || existing.CandidateHash != input.CandidateHash {
					return fmt.Errorf("video_story_review_required")
				}
				if existing.Status == "pending" || existing.Status == "passed" || existing.Status == "warning" {
					return nil
				}
				// unavailable 是未知模型结果：同一 requestId 禁止重发，显式新 requestId 才可重新复核同一候选。
				if existing.RequestID == input.RequestID {
					return fmt.Errorf("video_story_review_required")
				}
			}
			p.PlanReview = &PlanReview{RequestID: input.RequestID, InputID: input.InputID, CandidateHash: input.CandidateHash, Status: "pending", Issues: []string{}}
			started = true
			return nil
		})
		return p, started, err
	case "complete":
		if input.Review == nil || !slices.Contains([]string{"passed", "warning", "unavailable"}, input.Review.Status) || input.Review.Issues == nil || len(input.Review.Issues) > 20 {
			return nil, false, ErrInvalid
		}
		for _, issue := range input.Review.Issues {
			if !storyText(issue, 500, true) {
				return nil, false, ErrInvalid
			}
		}
		p, err := s.Mutate(ctx, team, id, input.Version, func(p *Project) error {
			r := p.PlanReview
			if p.Status != "analyzing" || p.Analysis == nil || p.Analysis.InputID != input.InputID || r == nil || r.Status != "pending" || r.RequestID != input.RequestID || r.InputID != input.InputID || r.CandidateHash != input.CandidateHash {
				return fmt.Errorf("video_story_review_conflict")
			}
			r.Status = input.Review.Status
			r.Issues = slices.Clone(input.Review.Issues)
			return nil
		})
		return p, false, err
	default:
		return nil, false, ErrInvalid
	}
}

func (s *Service) MachineReview(ctx context.Context, team, id string, input MachineReviewRequest) (*Project, bool, error) {
	if input.Version <= 0 || input.RequestID == "" || input.OutputAssetID == "" || len(input.RequestID) > 100 || len(input.OutputAssetID) > 100 {
		return nil, false, ErrInvalid
	}
	if input.Operation == "start" {
		if input.Review != nil {
			return nil, false, ErrInvalid
		}
		current, err := s.Repo.Get(ctx, team, id)
		if err != nil {
			return nil, false, err
		}
		if current == nil {
			return nil, false, ErrInvalid
		}
		if !storyPlanning(current.Brief) || current.Canvas != nil || current.Agent != nil || current.Status != "completed" || current.OutputAssetID != input.OutputAssetID {
			return nil, false, fmt.Errorf("machine_review_conflict")
		}
		if existing := current.MachineReview; existing != nil && existing.OutputAssetID == input.OutputAssetID {
			if existing.RequestID == input.RequestID || existing.Status == "pending" || existing.Status == "passed" || existing.Status == "warning" || existing.Status == "failed" {
				return current, false, nil
			}
			if existing.Status != "unavailable" {
				return nil, false, fmt.Errorf("machine_review_conflict")
			}
		}
		started := false
		p, err := s.Mutate(ctx, team, id, input.Version, func(p *Project) error {
			if !storyPlanning(p.Brief) || p.Canvas != nil || p.Agent != nil || p.Status != "completed" || p.OutputAssetID == "" || p.OutputAssetID != input.OutputAssetID {
				return fmt.Errorf("machine_review_conflict")
			}
			if p.MachineReview != nil && p.MachineReview.OutputAssetID == input.OutputAssetID {
				r := p.MachineReview
				if r.Status == "pending" || r.Status == "passed" || r.Status == "warning" || r.Status == "failed" {
					return nil
				}
			}
			p.MachineReview = &VideoMachineReview{RequestID: input.RequestID, OutputAssetID: input.OutputAssetID, SourceVersion: p.Version, Status: "pending", Summary: "", Issues: []MachineReviewIssue{}, AudioReview: "not_evaluated"}
			started = true
			return nil
		})
		return p, started, err
	}
	if input.Operation == "complete" {
		p, err := s.CompleteMachineReview(ctx, team, id, input)
		return p, false, err
	}
	return nil, false, ErrInvalid
}

func (s *Service) CompleteMachineReview(ctx context.Context, team, id string, input MachineReviewRequest) (*Project, error) {
	if input.Version <= 0 || input.Operation != "complete" || input.Review == nil || input.RequestID == "" || input.OutputAssetID == "" {
		return nil, ErrInvalid
	}
	r := input.Review
	if !slices.Contains([]string{"passed", "warning", "failed", "unavailable"}, r.Status) || r.AudioReview != "not_evaluated" || r.Issues == nil || len(r.Issues) > 50 || utf8.RuneCountInString(r.Summary) > 2000 {
		return nil, ErrInvalid
	}
	for _, issue := range r.Issues {
		if !storyText(issue.Code, 100, true) || !storyText(issue.Detail, 1000, true) || !slices.Contains([]string{"recompose", "regenerate", "plan", "none"}, issue.Repair) {
			return nil, ErrInvalid
		}
		if issue.ShotIndex != nil && (*issue.ShotIndex < 0 || *issue.ShotIndex >= 30) || issue.Frame != nil && *issue.Frame < 0 {
			return nil, ErrInvalid
		}
	}
	return s.Mutate(ctx, team, id, input.Version, func(p *Project) error {
		stored := p.MachineReview
		if !storyPlanning(p.Brief) || p.Status != "completed" || p.OutputAssetID != input.OutputAssetID || stored == nil || stored.Status != "pending" || stored.RequestID != input.RequestID || stored.OutputAssetID != input.OutputAssetID || stored.SourceVersion+1 != p.Version {
			return fmt.Errorf("machine_review_conflict")
		}
		totalFrames := 0
		if p.QualityReport != nil && p.QualityReport.TotalFrames > 0 {
			totalFrames = p.QualityReport.TotalFrames
		} else {
			totalFrames = p.Brief.Duration * 30
		}
		for _, issue := range r.Issues {
			if issue.ShotIndex != nil && *issue.ShotIndex >= len(p.Shots) || issue.Frame != nil && *issue.Frame >= totalFrames {
				return ErrInvalid
			}
		}
		stored.Status, stored.Summary, stored.Issues, stored.AudioReview = r.Status, strings.TrimSpace(r.Summary), slices.Clone(r.Issues), r.AudioReview
		return nil
	})
}

func (s *Service) RepairShots(ctx context.Context, team, id string, version int64, requestID string, indices []int) (*Project, error) {
	if requestID == "" || len(requestID) > 100 || len(indices) == 0 || len(indices) > 30 {
		return nil, fmt.Errorf("repair_invalid")
	}
	indices = slices.Clone(indices)
	slices.Sort(indices)
	for i, index := range indices {
		if index < 0 || index >= 30 || i > 0 && index == indices[i-1] {
			return nil, fmt.Errorf("repair_invalid")
		}
	}
	signatureBytes, _ := json.Marshal(indices)
	// 使用稳定内容摘要，避免重复请求因为数组顺序不同而改变身份。
	hash := sha256.Sum256(signatureBytes)
	sig := hex.EncodeToString(hash[:])
	current, err := s.Repo.Get(ctx, team, id)
	if err != nil || current == nil {
		return nil, err
	}
	if receipt, ok := current.RepairReceipts[requestID]; ok {
		if receipt.Signature != sig {
			return nil, ErrConflict
		}
		return current, nil
	}
	return s.Mutate(ctx, team, id, version, func(p *Project) error {
		if p.Status != "completed" || p.CancelRequested || p.OutputAssetID == "" || p.LeaseOwner != "" && p.LeaseUntil.After(nowUTC()) || p.MachineReview == nil || p.MachineReview.OutputAssetID != p.OutputAssetID || p.MachineReview.Status != "warning" && p.MachineReview.Status != "failed" {
			return fmt.Errorf("repair_invalid")
		}
		for _, index := range indices {
			if index >= len(p.Shots) || p.Shots[index].Status != "succeeded" {
				return fmt.Errorf("repair_invalid")
			}
			allowed := false
			for _, issue := range p.MachineReview.Issues {
				if issue.ShotIndex != nil && *issue.ShotIndex == index && issue.Repair == "regenerate" {
					allowed = true
				}
			}
			if !allowed {
				return fmt.Errorf("repair_invalid")
			}
			if p.Shots[index].Attempt >= 2 {
				return fmt.Errorf("repair_attempt_exceeded")
			}
		}
		// 制作预算已移除；仍由请求身份、显式选镜和 attempt 边界控制修复。
		if p.RepairReceipts == nil {
			p.RepairReceipts = map[string]repairReceipt{}
		}
		p.RepairReceipts[requestID] = repairReceipt{Signature: sig, Version: p.Version + 1}
		for _, index := range indices {
			shot := &p.Shots[index]
			shot.Attempt++
			shot.Status, shot.Error = "pending", ""
			shot.ProviderError = nil
			shot.VideoTaskID, shot.ImageTaskID = "", ""
			shot.ResultURL, shot.LastFrameResultURL = "", ""
			shot.OutputAssetID, shot.LastFrameAssetID = "", ""
			shot.ReservationID, shot.PendingChargeMicro, shot.ReservedMicro = "", 0, 0
			shot.Stage, shot.ImageAssetID = "", ""
			shot.ProviderID, shot.ProviderUsageTokens = "", 0
			shot.TokenChargeMicro, shot.TokenRateMicroPerMillion, shot.TokenCostMicroPerMillion = 0, 0, 0
			shot.ImageNextQueryAt = time.Time{}
		}
		p.MachineReview = nil
		p.OutputAssetID = ""
		p.Composition = nil
		p.CompositionAssetID = ""
		p.QualityReport = nil
		p.Status, p.Error = "queued", ""
		return nil
	})
}

func nowUTC() time.Time { return time.Now().UTC() }
