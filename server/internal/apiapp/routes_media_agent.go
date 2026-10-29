package apiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

type agentGenerationRequestBody struct {
	RequestID          string   `json:"requestId"`
	Intent             string   `json:"intent"`
	Kind               string   `json:"kind"`
	Prompt             string   `json:"prompt"`
	AssetIDs           []string `json:"assetIds"`
	ModelID            string   `json:"modelId"`
	Resolution         string   `json:"resolution"`
	AspectRatio        string   `json:"aspectRatio"`
	Duration           int      `json:"duration"`
	Count              int      `json:"count"`
	SourceArtifactURIs []string `json:"sourceArtifactUris"`
}

type agentGenerationGenerateBody struct {
	Request            agentGenerationRequestBody `json:"request"`
	ConfirmationToken  string                     `json:"confirmationToken"`
	ExpectedQuoteMicro int64                      `json:"expectedQuoteMicro"`
}

type agentQuoteClaims struct {
	TeamID, UserID, DeviceID, RequestID string
	RequestHash                         string
	QuoteMicro                          int64
	ExpiresAt                           time.Time
}

type agentGenerationAsset struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	Mime            string  `json:"mime"`
	URL             string  `json:"url"`
	Name            string  `json:"name"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	TeamAssetID     string  `json:"teamAssetId,omitempty"`
	ArchiveStatus   string  `json:"archiveStatus"`
	ArchiveError    string  `json:"archiveError,omitempty"`
}

type agentGenerationResponse struct {
	RequestID       string                 `json:"requestId"`
	Status          string                 `json:"status"`
	Stage           string                 `json:"stage"`
	CancelRequested bool                   `json:"cancelRequested"`
	QuoteMicro      int64                  `json:"quoteMicro"`
	ChargedMicro    int64                  `json:"chargedMicro"`
	CloudArchive    string                 `json:"cloudArchive"`
	Assets          []agentGenerationAsset `json:"assets"`
	Error           string                 `json:"error,omitempty"`
}

func registerMediaAgentGenerationRoutes(router *gin.Engine, deps *Deps) {
	router.POST("/api/v1/media/agent/generations/quote", func(c *gin.Context) {
		a, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		if deps.SecretBox == nil {
			c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "media_confirmation_unavailable"))
			return
		}
		var body agentGenerationRequestBody
		if !mediaBody(c, &body) {
			return
		}
		if len(body.SourceArtifactURIs) > 0 {
			c.JSON(http.StatusUnprocessableEntity, fail(http.StatusUnprocessableEntity, "media_source_artifact_unsupported"))
			return
		}
		request := body.mediaRequest()
		model, quote, err := deps.Media.ValidateAgentGeneration(c.Request.Context(), membership.Team.ID, request)
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		now := deps.Now()
		expiresAt := now.Add(5 * time.Minute).UTC()
		requestHash, err := agentGenerationRequestHash(request)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		claim := agentQuoteClaims{TeamID: membership.Team.ID, UserID: a.User.ID, DeviceID: a.Device.ID, RequestID: request.RequestID, RequestHash: requestHash, QuoteMicro: quote, ExpiresAt: expiresAt}
		payload, err := json.Marshal(claim)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		token, err := deps.SecretBox.Encrypt(string(payload))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		name := model.DisplayName
		if name == "" {
			name = model.Name
		}
		c.JSON(http.StatusOK, okData(gin.H{
			"requestId": request.RequestID, "quoteMicro": quote, "confirmationToken": token,
			"currency": "credit", "expiresAt": expiresAt.Format(time.RFC3339Nano),
			"modelId": model.ID, "modelName": name, "kind": model.Kind,
			"resolution": request.Resolution, "aspectRatio": request.AspectRatio,
			"duration": request.Duration, "count": request.Count,
		}))
	})

	router.POST("/api/v1/media/agent/generations", func(c *gin.Context) {
		a, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		if deps.SecretBox == nil {
			c.JSON(http.StatusServiceUnavailable, fail(http.StatusServiceUnavailable, "media_confirmation_unavailable"))
			return
		}
		var body agentGenerationGenerateBody
		if !mediaBody(c, &body) {
			return
		}
		if len(body.Request.SourceArtifactURIs) > 0 {
			c.JSON(http.StatusUnprocessableEntity, fail(http.StatusUnprocessableEntity, "media_source_artifact_unsupported"))
			return
		}
		request := body.Request.mediaRequest()
		requestHash, err := agentGenerationRequestHash(request)
		if err != nil {
			internalError(c, deps, err)
			return
		}
		plaintext, err := deps.SecretBox.Decrypt(body.ConfirmationToken)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, fail(http.StatusUnprocessableEntity, "media_confirmation_invalid"))
			return
		}
		var claim agentQuoteClaims
		if json.Unmarshal([]byte(plaintext), &claim) != nil || claim.TeamID != membership.Team.ID || claim.UserID != a.User.ID || claim.DeviceID != a.Device.ID || claim.RequestID != request.RequestID || claim.RequestHash != requestHash || claim.QuoteMicro != body.ExpectedQuoteMicro || !deps.Now().Before(claim.ExpiresAt) {
			c.JSON(http.StatusConflict, fail(http.StatusConflict, "media_confirmation_expired_or_changed"))
			return
		}
		_, quote, err := deps.Media.ValidateAgentGeneration(c.Request.Context(), membership.Team.ID, request)
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		if quote != claim.QuoteMicro {
			c.JSON(http.StatusConflict, fail(http.StatusConflict, "generation_quote_changed"))
			return
		}
		project, err := deps.Media.CreateAgentGeneration(c.Request.Context(), membership.Team.ID, a.User.ID, a.Device.ID, request)
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		c.JSON(http.StatusOK, okData(agentGenerationProjection(c.Request.Context(), deps.Media, *project, request.RequestID)))
	})

	router.GET("/api/v1/media/agent/generations/:requestId", func(c *gin.Context) {
		a, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		requestID := c.Param("requestId")
		if requestID == "" || len(requestID) > 200 {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "media_request_invalid"))
			return
		}
		project, err := deps.Media.GetAgentGeneration(c.Request.Context(), membership.Team.ID, a.User.ID, requestID)
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		if project == nil {
			c.JSON(http.StatusNotFound, fail(http.StatusNotFound, "media_generation_not_found"))
			return
		}
		c.JSON(http.StatusOK, okData(agentGenerationProjection(c.Request.Context(), deps.Media, *project, requestID)))
	})

	router.POST("/api/v1/media/agent/generations/:requestId/cancel", func(c *gin.Context) {
		a, membership, ok := mediaIdentity(c, deps)
		if !ok {
			return
		}
		requestID := c.Param("requestId")
		if requestID == "" || len(requestID) > 200 {
			c.JSON(http.StatusBadRequest, fail(http.StatusBadRequest, "media_request_invalid"))
			return
		}
		project, err := deps.Media.CancelAgentGeneration(c.Request.Context(), membership.Team.ID, a.User.ID, requestID)
		if err != nil {
			mediaError(c, deps, err)
			return
		}
		if project == nil {
			c.JSON(http.StatusNotFound, fail(http.StatusNotFound, "media_generation_not_found"))
			return
		}
		c.JSON(http.StatusOK, okData(agentGenerationProjection(c.Request.Context(), deps.Media, *project, requestID)))
	})
}

func (body agentGenerationRequestBody) mediaRequest() media.AgentGenerationRequest {
	return media.AgentGenerationRequest{
		RequestID: body.RequestID, Intent: body.Intent, Kind: body.Kind, Prompt: body.Prompt,
		AssetIDs: body.AssetIDs, ModelID: body.ModelID, Resolution: body.Resolution,
		AspectRatio: body.AspectRatio, Duration: body.Duration, Count: body.Count,
	}
}

func agentGenerationRequestHash(request media.AgentGenerationRequest) (string, error) {
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.AssetIDs == nil {
		request.AssetIDs = []string{}
	}
	request.ExpectedQuoteMicro = 0
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func agentGenerationProjection(ctx context.Context, service *media.Service, project media.Project, requestID string) agentGenerationResponse {
	archives := make(map[string]media.CanvasTeamAssetArchive, len(project.TeamAssetArchives))
	for _, archive := range project.TeamAssetArchives {
		archives[archive.SourceAssetID] = archive
	}
	assetsByID := map[string]media.Asset{}
	assets, _ := service.Repo.Assets(ctx, project.TeamID)
	for _, asset := range assets {
		assetsByID[asset.ID] = asset
	}
	sources := []string{}
	if project.Canvas != nil || project.Agent != nil {
		for _, shot := range project.Shots {
			if shot.Status == "succeeded" && shot.OutputAssetID != "" {
				sources = append(sources, shot.OutputAssetID)
			}
		}
	} else if project.Status == "completed" && project.OutputAssetID != "" {
		sources = append(sources, project.OutputAssetID)
	}
	resultAssets := make([]agentGenerationAsset, 0, len(sources))
	for _, id := range sources {
		asset, ok := assetsByID[id]
		if !ok || asset.Internal {
			continue
		}
		archive := archives[id]
		archiveStatus := archive.Status
		if archiveStatus == "" {
			archiveStatus = "pending"
		}
		resultAssets = append(resultAssets, agentGenerationAsset{
			ID: asset.ID, Kind: asset.Kind, Mime: asset.Mime, URL: service.Blobs.URL(project.TeamID, asset.ID),
			Name: asset.Name, Width: asset.Width, Height: asset.Height, DurationSeconds: asset.DurationSeconds,
			TeamAssetID: archive.TeamAssetID, ArchiveStatus: archiveStatus, ArchiveError: archive.Error,
		})
	}
	response := agentGenerationResponse{
		RequestID: requestID, Status: media.AgentGenerationStatus(project.Status), Stage: media.AgentGenerationStage(project), CancelRequested: project.CancelRequested, QuoteMicro: project.QuoteMicro,
		ChargedMicro: project.ChargedMicro, CloudArchive: media.AgentGenerationCloudArchive(project), Assets: resultAssets,
	}
	if response.Status == "completed" && response.CloudArchive == "pending" {
		response.Stage = "saving"
	}
	if response.Status == "failed" {
		response.Error = publicAgentGenerationError(project.Error)
	}
	return response
}

func publicAgentGenerationError(code string) string {
	if code == "" || len(code) > 100 || strings.ContainsAny(code, " /\\\r\n") {
		return "media_generation_failed"
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "media_generation_failed"
		}
	}
	return code
}
