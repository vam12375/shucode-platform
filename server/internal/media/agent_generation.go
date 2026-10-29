package media

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func AgentGenerationProjectID(team, user, requestID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("mendao:agent-generation:v1:"+team+":"+user+":"+requestID)).String()
}

func (s *Service) ValidateAgentGeneration(ctx context.Context, team string, request AgentGenerationRequest) (Model, int64, error) {
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.RequestID == "" || len(request.RequestID) > 200 ||
		(request.Intent != "generate" && request.Intent != "edit") ||
		(request.Kind != "image" && request.Kind != "video") ||
		request.Prompt == "" || utf8.RuneCountInString(request.Prompt) > 4000 ||
		request.Count < 1 || request.Count > 4 || len(request.AssetIDs) > 8 {
		return Model{}, 0, ErrInvalid
	}
	if request.AssetIDs == nil {
		request.AssetIDs = []string{}
	}
	if request.Intent == "edit" && len(request.AssetIDs) == 0 {
		return Model{}, 0, fmt.Errorf("media_edit_asset_required")
	}
	models, err := s.Repo.Models(ctx)
	if err != nil {
		return Model{}, 0, err
	}
	model, err := FindModel(models, request.ModelID, request.Kind)
	if err != nil {
		return Model{}, 0, err
	}
	if request.Resolution == "" || request.AspectRatio == "" || !slices.Contains(ModelAspectRatios(model), request.AspectRatio) {
		return Model{}, 0, ErrInvalid
	}
	if request.Kind == "image" {
		if request.Duration != 0 || !slices.Contains(ImageResolutions(model), request.Resolution) || len(request.AssetIDs) > MaxReferenceImages(model) {
			return Model{}, 0, ErrInvalid
		}
	} else if !slices.Contains(model.Resolutions, request.Resolution) || !model.DurationSupported(request.Duration) || len(request.AssetIDs) > 2 {
		return Model{}, 0, ErrInvalid
	}
	policy, err := s.Repo.Store.GetTeamPolicy(ctx, team)
	if err != nil {
		return Model{}, 0, err
	}
	if !store.TeamModelAllowed(policy, model.ID) {
		return Model{}, 0, fmt.Errorf("model_not_allowed")
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return Model{}, 0, err
	}
	seen := map[string]bool{}
	for _, id := range request.AssetIDs {
		if seen[id] {
			return Model{}, 0, ErrInvalid
		}
		seen[id] = true
		if !slices.ContainsFunc(assets, func(asset Asset) bool { return asset.ID == id && asset.Kind == "image" && !asset.Internal }) {
			return Model{}, 0, ErrInvalid
		}
	}
	unit := model.PriceMicro
	if request.Kind == "video" {
		unit = mediaVideoQuoteMicro(model, request.Resolution, request.AspectRatio, request.Duration)
	}
	total := unit * int64(request.Count)
	if unit <= 0 || total <= 0 || total > 1_000_000_000 {
		return Model{}, 0, ErrInvalid
	}
	return model, total, nil
}

func (s *Service) CreateAgentGeneration(ctx context.Context, team, user, device string, request AgentGenerationRequest) (*Project, error) {
	model, quote, err := s.ValidateAgentGeneration(ctx, team, request)
	if err != nil {
		return nil, err
	}
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.AssetIDs == nil {
		request.AssetIDs = []string{}
	}
	id := AgentGenerationProjectID(team, user, request.RequestID)
	request.ExpectedQuoteMicro = quote
	if existing, err := s.Repo.Get(ctx, team, id); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.UserID != user || existing.Agent == nil || !reflect.DeepEqual(existing.Agent, &request) {
			return nil, ErrConflict
		}
		return existing, nil
	}
	shots := make([]Shot, request.Count)
	for i := range shots {
		shots[i] = Shot{
			Status: "pending", Attempt: 1,
			Plan:       ShotPlan{Prompt: request.Prompt, ImagePrompt: request.Prompt, Duration: request.Duration},
			QuoteMicro: quote / int64(request.Count),
		}
	}
	now := time.Now().UTC()
	p := &Project{
		ID: id, TeamID: team, UserID: user, DeviceID: device, Version: 1,
		Status: "queued", Agent: &request, Models: []Model{model}, Shots: shots,
		QuoteMicro: quote, CreatedAt: now, UpdatedAt: now,
	}
	if err = s.Repo.Index(ctx, ProjectKey(team, id)); err != nil {
		return nil, err
	}
	if err = s.Repo.Save(ctx, p, nil); err != nil {
		if err == ErrConflict {
			return s.CreateAgentGeneration(ctx, team, user, device, request)
		}
		return nil, err
	}
	return p, nil
}

func (s *Service) GetAgentGeneration(ctx context.Context, team, user, requestID string) (*Project, error) {
	id := AgentGenerationProjectID(team, user, requestID)
	p, err := s.Repo.Get(ctx, team, id)
	if err != nil || p == nil {
		return p, err
	}
	if p.UserID != user || p.Agent == nil || p.Agent.RequestID != requestID {
		return nil, ErrConflict
	}
	return p, nil
}

// CancelAgentGeneration 用制作单版本记录取消请求；worker 决定最终状态并按已受理的上游结果结算。
func (s *Service) CancelAgentGeneration(ctx context.Context, team, user, requestID string) (*Project, error) {
	if requestID == "" || len(requestID) > 200 {
		return nil, ErrInvalid
	}
	project, err := s.GetAgentGeneration(ctx, team, user, requestID)
	if err != nil || project == nil {
		return project, err
	}
	for range 4 {
		if project.CancelRequested || project.Status == "completed" || project.Status == "failed" || project.Status == "cancelled" {
			return project, nil
		}
		project, err = s.Action(ctx, team, project.ID, project.Version, "cancel")
		if !errors.Is(err, ErrConflict) {
			return project, err
		}
		project, err = s.GetAgentGeneration(ctx, team, user, requestID)
		if err != nil || project == nil {
			return project, err
		}
	}
	return nil, ErrConflict
}

func AgentGenerationCloudArchive(p Project) string {
	sources := generatedArchiveSources(p)
	if len(sources) == 0 {
		return "unknown"
	}
	statuses := make(map[string]string, len(p.TeamAssetArchives))
	for _, archive := range p.TeamAssetArchives {
		statuses[archive.SourceAssetID] = archive.Status
	}
	allSaved := true
	for _, source := range sources {
		switch statuses[source] {
		case "failed", "deleted":
			return "failed"
		case "saved":
		default:
			allSaved = false
		}
	}
	if allSaved {
		return "saved"
	}
	return "pending"
}

func AgentGenerationStatus(status string) string {
	switch status {
	case "queued":
		return "queued"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	default:
		return "running"
	}
}

func AgentGenerationStage(p Project) string {
	switch AgentGenerationStatus(p.Status) {
	case "queued":
		return "queued"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	default:
		return "generating"
	}
}
