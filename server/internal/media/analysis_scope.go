package media

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

const AnalysisScopeHeader = "X-Mendao-Video-Analysis"
const AnalysisScopeMaxRequests int64 = 128
const AnalysisScopeMaxTokens int64 = 1_000_000

var ErrAnalysisScopeInvalid = errors.New("video_analysis_scope_invalid")
var ErrAnalysisScopeBudget = errors.New("video_analysis_scope_budget_exhausted")
var ErrAnalysisScopeUnavailable = errors.New("video_analysis_scope_unavailable")

type analysisScope struct {
	Purpose   string `json:"purpose"`
	TeamID    string `json:"teamId"`
	UserID    string `json:"userId"`
	DeviceID  string `json:"deviceId"`
	ProjectID string `json:"projectId"`
	InputID   string `json:"inputId"`
	FeeID     string `json:"feeId"`
	ModelID   string `json:"modelId"`
	IssuedAt  int64  `json:"issuedAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

type analysisScopeBudget struct {
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

func analysisScopeCurrent(ctx context.Context, repo Repository, scope analysisScope) error {
	p, err := repo.Get(ctx, scope.TeamID, scope.ProjectID)
	if err != nil {
		return ErrAnalysisScopeUnavailable
	}
	if p == nil || p.TeamID != scope.TeamID || p.UserID != scope.UserID || p.DeviceID != scope.DeviceID || p.Status != "analyzing" || p.Brief.ReferenceAssetID == "" || p.Analysis == nil || p.Analysis.InputID != scope.InputID || p.AnalysisFee == nil || p.AnalysisFee.ID != scope.FeeID {
		return ErrAnalysisScopeInvalid
	}
	identity, err := repo.Store.GetQuotaReservation(ctx, scope.FeeID)
	if err != nil {
		return ErrAnalysisScopeUnavailable
	}
	if identity == nil || !identity.Postpaid || identity.Status != store.ReservationReserved || identity.TeamID != scope.TeamID || identity.UserID != scope.UserID || identity.RequestID != p.AnalysisFee.RequestID {
		return ErrAnalysisScopeInvalid
	}
	return nil
}

func IssueAnalysisScope(ctx context.Context, repo Repository, box *crypto.SecretBox, p *Project, model string, now time.Time) (string, error) {
	if box == nil {
		return "", ErrAnalysisScopeUnavailable
	}
	if p == nil || p.Analysis == nil || p.AnalysisFee == nil || model == "" || len(model) > 256 || p.UserID == "" || p.DeviceID == "" {
		return "", ErrAnalysisScopeInvalid
	}
	scope := analysisScope{Purpose: "media:reference-analysis:v1", TeamID: p.TeamID, UserID: p.UserID, DeviceID: p.DeviceID, ProjectID: p.ID, InputID: p.Analysis.InputID, FeeID: p.AnalysisFee.ID, ModelID: model, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	if err := analysisScopeCurrent(ctx, repo, scope); err != nil {
		return "", err
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", ErrAnalysisScopeUnavailable
	}
	ticket, err := box.Encrypt(string(raw))
	if err != nil {
		return "", ErrAnalysisScopeUnavailable
	}
	return ticket, nil
}

func ValidateAnalysisScope(ctx context.Context, repo Repository, box *crypto.SecretBox, ticket, team, user, device, model string, estimatedTokens int64, now time.Time) error {
	if box == nil || ticket == "" || len(ticket) > 8192 || estimatedTokens < 1 {
		return ErrAnalysisScopeInvalid
	}
	plain, err := box.Decrypt(ticket)
	if err != nil {
		return ErrAnalysisScopeInvalid
	}
	var scope analysisScope
	if json.Unmarshal([]byte(plain), &scope) != nil || scope.Purpose != "media:reference-analysis:v1" || scope.TeamID != team || scope.UserID != user || scope.DeviceID != device || scope.ModelID != model || scope.ProjectID == "" || scope.InputID == "" || scope.FeeID == "" || scope.ExpiresAt <= now.Unix() || scope.IssuedAt > now.Unix() || scope.ExpiresAt-scope.IssuedAt != int64(time.Hour/time.Second) {
		return ErrAnalysisScopeInvalid
	}
	if err = analysisScopeCurrent(ctx, repo, scope); err != nil {
		return err
	}
	// 计数归属已准入费用身份；跨进程、重签scope与并发请求都不能重置分析预算。
	err = updateJSON(ctx, repo.Store, "media:analysis-scope-budget:"+team+":"+scope.FeeID, analysisScopeBudget{}, func(budget *analysisScopeBudget) error {
		if budget.Requests < 0 || budget.Tokens < 0 || budget.Requests >= AnalysisScopeMaxRequests || estimatedTokens > AnalysisScopeMaxTokens || budget.Tokens > AnalysisScopeMaxTokens-estimatedTokens {
			return ErrAnalysisScopeBudget
		}
		budget.Requests++
		budget.Tokens += estimatedTokens
		return nil
	})
	if err != nil && !errors.Is(err, ErrAnalysisScopeBudget) {
		return ErrAnalysisScopeUnavailable
	}
	return err
}
