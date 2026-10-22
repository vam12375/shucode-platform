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

var ErrAnalysisScopeInvalid = errors.New("video_analysis_scope_invalid")
var ErrAnalysisScopeUnavailable = errors.New("video_analysis_scope_unavailable")

type analysisScope struct {
	Purpose           string `json:"purpose"`
	TeamID            string `json:"teamId"`
	UserID            string `json:"userId"`
	DeviceID          string `json:"deviceId"`
	ProjectID         string `json:"projectId"`
	InputID           string `json:"inputId"`
	FeeID             string `json:"feeId"`
	ModelID           string `json:"modelId"`
	RequiredReasoning string `json:"requiredReasoning,omitempty"`
	IssuedAt          int64  `json:"issuedAt"`
	ExpiresAt         int64  `json:"expiresAt"`
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
	expectedStatus := store.ReservationReserved
	if p.Analysis.Kind == "source" {
		if !p.Brief.SourceOnly || p.SourceAnalysis != nil {
			return ErrAnalysisScopeInvalid
		}
	} else if p.Analysis.Kind == "adaptation" {
		if p.Brief.SourceOnly || p.SourceAnalysis == nil || p.SourceAnalysis.SourceChecksum != p.SourceChecksum {
			return ErrAnalysisScopeInvalid
		}
		expectedStatus = store.ReservationSettled
	} else if p.Brief.SourceOnly || p.SourceAnalysis != nil {
		return ErrAnalysisScopeInvalid
	}
	expectedReasoning := "high"
	if p.Analysis.Kind == "adaptation" {
		expectedReasoning = ""
	}
	if scope.RequiredReasoning != expectedReasoning {
		return ErrAnalysisScopeInvalid
	}
	if identity == nil || !identity.Postpaid || identity.Status != expectedStatus || identity.TeamID != scope.TeamID || identity.UserID != scope.UserID || identity.RequestID != p.AnalysisFee.RequestID {
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
	if p.Analysis.Kind != "adaptation" {
		scope.RequiredReasoning = "high"
	}
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

func ValidateAnalysisScope(ctx context.Context, repo Repository, box *crypto.SecretBox, ticket, team, user, device, model string, now time.Time, actualReasoning string) error {
	if box == nil || ticket == "" || len(ticket) > 8192 {
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
	// 原片固定 high 是签名准入规则，不能只依赖客户端信封或由思考预算推断。
	if scope.RequiredReasoning != "" && actualReasoning != scope.RequiredReasoning {
		return ErrAnalysisScopeInvalid
	}
	// scope 只证明本次分析授权。旧版累计请求/token预算会把全量视觉
	// 证据误判为限流；新请求不再读取或写入该计数，输出遵循普通对话规则。
	return nil
}
