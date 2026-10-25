package media

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type CanvasRequest struct {
	Kind               string                 `json:"kind"`
	ModelID            string                 `json:"modelId"`
	Prompt             string                 `json:"prompt"`
	AssetIDs           []string               `json:"assetIds"`
	VideoReferences    []CanvasVideoReference `json:"videoReferences,omitempty"`
	Resolution         string                 `json:"resolution"`
	AspectRatio        string                 `json:"aspectRatio"`
	Duration           int                    `json:"duration"`
	Count              int                    `json:"count"`
	ExpectedQuoteMicro int64                  `json:"expectedQuoteMicro"`
	BoardID            string                 `json:"boardId"`
	NodeID             string                 `json:"nodeId"`
	WorkspacePath      string                 `json:"workspacePath"`
	WorkspaceIdentity  string                 `json:"workspaceIdentity,omitempty"`
	RemoteSessionID    string                 `json:"remoteSessionId,omitempty"`
}

type CanvasVideoReference struct {
	AssetID              string   `json:"assetId"`
	StartSeconds         *float64 `json:"startSeconds,omitempty"`
	EndSeconds           *float64 `json:"endSeconds,omitempty"`
	ReferenceClipAssetID string   `json:"referenceClipAssetId,omitempty"`
}

func (s *Service) CreateCanvas(ctx context.Context, team, user, device, id string, r CanvasRequest) (*Project, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrInvalid
	}
	if r.AssetIDs == nil {
		r.AssetIDs = []string{}
	}
	requestForProject := r
	requestForProject.VideoReferences = slices.Clone(r.VideoReferences)
	for i := range requestForProject.VideoReferences {
		requestForProject.VideoReferences[i].ReferenceClipAssetID = ""
	}
	existing, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.UserID != user || !reflect.DeepEqual(existing.Canvas, &requestForProject) {
			return nil, ErrConflict
		}
		return existing, nil
	}
	models, err := s.Repo.Models(ctx)
	if err != nil {
		return nil, err
	}
	m, err := FindModel(models, r.ModelID, r.Kind)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Prompt) == "" || utf8.RuneCountInString(r.Prompt) > 4000 || r.Count < 1 || r.Count > 4 || r.BoardID == "" || len(r.BoardID) > 200 || r.NodeID == "" || len(r.NodeID) > 200 || r.WorkspacePath == "" || len(r.WorkspacePath) > 4096 || len(r.WorkspaceIdentity) > 4096 || len(r.RemoteSessionID) > 200 {
		return nil, ErrInvalid
	}
	if m.Adapter == "toapis" {
		if r.Kind == "image" {
			if validateToAPIsImageInput(m, r, len(r.AssetIDs)) != nil {
				return nil, ErrInvalid
			}
		} else if validateToAPIsInput(m, r.Resolution, r.AspectRatio, r.Duration, len(r.AssetIDs), len(r.VideoReferences) > 0) != nil {
			return nil, ErrInvalid
		}
	}
	if r.Kind == "image" {
		if len(r.VideoReferences) > 0 || r.Duration != 0 || !slices.Contains(ImageResolutions(m), r.Resolution) || !slices.Contains(ModelAspectRatios(m), r.AspectRatio) || len(r.AssetIDs) > MaxReferenceImages(m) {
			return nil, ErrInvalid
		}
	} else if r.Kind != "video" || !slices.Contains(ModelAspectRatios(m), r.AspectRatio) || !m.DurationSupported(r.Duration) || !slices.Contains(m.Resolutions, r.Resolution) {
		return nil, ErrInvalid
	} else if len(r.VideoReferences) == 0 {
		if len(r.AssetIDs) > 2 {
			return nil, ErrInvalid
		}
	} else {
		if m.VideoReference == nil || len(r.VideoReferences) != 1 || len(r.AssetIDs) == 0 || len(r.AssetIDs) > m.VideoReference.MaxImages {
			return nil, fmt.Errorf("video_reference_unsupported")
		}
		ref := r.VideoReferences[0]
		if ref.AssetID == "" || ref.ReferenceClipAssetID == "" || (ref.StartSeconds == nil) != (ref.EndSeconds == nil) {
			return nil, ErrInvalid
		}
	}
	var videoReference *videoReferenceSource
	if len(r.VideoReferences) > 0 {
		ref := r.VideoReferences[0]
		if ref.ReferenceClipAssetID == "" {
			return nil, fmt.Errorf("video_reference_clip_required")
		}
		resolved, err := s.resolveVideoReference(ctx, team, ref.AssetID, ref.StartSeconds, ref.EndSeconds, m.VideoReference.MaxDurationSeconds)
		if err != nil {
			return nil, err
		}
		if _, err = s.validateUploadedReferenceClip(ctx, team, resolved, ref.ReferenceClipAssetID); err != nil {
			return nil, err
		}
		videoReference = &resolved
	}
	policy, err := s.Repo.Store.GetTeamPolicy(ctx, team)
	if err != nil {
		return nil, err
	}
	if !store.TeamModelAllowed(policy, m.ID) {
		return nil, fmt.Errorf("model_not_allowed")
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range r.AssetIDs {
		if seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		if !slices.ContainsFunc(assets, func(a Asset) bool { return a.ID == id && a.Kind == "image" && !a.Internal }) {
			return nil, ErrInvalid
		}
	}
	unit := m.PriceMicro
	if r.Kind == "video" {
		if videoReference != nil {
			unit = videoReferenceQuoteMicro(m.VideoReference, r.Resolution, r.AspectRatio, r.Duration)
		} else {
			unit *= int64(r.Duration)
		}
	}
	total := unit * int64(r.Count)
	if unit <= 0 || total > 1_000_000_000 || total != r.ExpectedQuoteMicro {
		return nil, fmt.Errorf("generation_quote_changed")
	}
	shots := make([]Shot, r.Count)
	for i := range shots {
		plan := ShotPlan{Prompt: r.Prompt, ImagePrompt: r.Prompt, Duration: r.Duration}
		if videoReference != nil {
			plan.SourceStart = videoReference.StartSeconds
			plan.SourceEnd = videoReference.EndSeconds
			plan.ReferenceVideoAssetID = r.VideoReferences[0].ReferenceClipAssetID
		}
		shots[i] = Shot{Status: "pending", Attempt: 1, Plan: plan, ReferenceVideoAssetID: plan.ReferenceVideoAssetID, QuoteMicro: unit}
	}
	now := time.Now().UTC()
	p := &Project{ID: id, TeamID: team, UserID: user, DeviceID: device, Version: 1, Status: "queued", Canvas: &requestForProject, Models: []Model{m}, Shots: shots, QuoteMicro: total, CreatedAt: now, UpdatedAt: now}
	if err = s.Repo.Index(ctx, ProjectKey(team, id)); err != nil {
		return nil, err
	}
	if err = s.Repo.Save(ctx, p, nil); err != nil {
		// 同 UUID 的并发受理只有 CAS 胜者拥有任务，败者读取它，不能重新创建另一份付费任务。
		if err == ErrConflict {
			return s.CreateCanvas(ctx, team, user, device, id, r)
		}
		return nil, err
	}
	return p, nil
}
