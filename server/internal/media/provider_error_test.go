package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestProviderErrorOnlyAcceptsBoundedCodesAndKnownCategories(t *testing.T) {
	for _, tc := range []struct{ code, category string }{
		{"InputImageSensitiveContentDetected", "content_policy"},
		{"OutputVideoSensitiveContentDetected.PolicyViolation", "content_policy"},
		{"OutputVideoSensitiveContentDetected.PrivacyInformation", "content_policy"},
		{"InputImageSensitiveContentDetected.PrivacyInformation", "content_policy"},
		{"InvalidParameter", "invalid_input"},
		{"AuthenticationError", "authentication"},
		{"ModelNotOpen", "authentication"},
		{"QuotaExceeded", "quota"},
		{"RateLimitExceeded", "rate_limit"},
		{"ServiceUnavailable", "service_unavailable"},
		{"FutureProviderError.V2", ""},
		{"NotReallySensitiveContentDetected", ""},
		{"ModelNotOpen.Future", ""},
		{"modelnotopen", ""},
		{"InvalidEndpointOrModel.NotFound", ""},
		{"InvalidEndpointOrModel.ModelIDAccessDisabled", ""},
	} {
		t.Run(tc.code, func(t *testing.T) {
			got := sanitizeProviderError(tc.code)
			if got == nil || got.Code != tc.code || got.Category != tc.category {
				t.Fatalf("safe error = %+v", got)
			}
		})
	}
	for _, code := range []string{"", "https://example.test/private?token=secret", "error with user data", "错误", "line\nprivate", strings.Repeat("x", 129)} {
		if got := sanitizeProviderError(code); got != nil {
			t.Fatalf("unsafe code was accepted: %+v", got)
		}
	}
}

func TestProviderHTTPErrorRetainsStableClassificationAndSafeCode(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{`{"error":{"code":"InvalidParameter","message":"secret https://example.test/private?token=secret"}}`, "InvalidParameter"},
		{`{"error":{"code":"https://example.test/private","message":"secret"}}`, ""},
		{`not JSON secret`, ""},
	} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Request-ID", "private-request")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(tc.body))
		}))
		p := Provider{BaseURL: upstream.URL, Client: upstream.Client()}
		_, err := p.Submit(context.Background(), Model{}, Brief{}, ShotPlan{}, "https://example.test/reference.png")
		upstream.Close()
		if err == nil || err.Error() != "provider_http_422" {
			t.Fatalf("HTTP classification changed: %v", err)
		}
		got := providerErrorFrom(err)
		if tc.code == "" && got != nil || tc.code != "" && (got == nil || got.Code != tc.code) {
			t.Fatalf("diagnostic = %+v", got)
		}
	}
}

func TestProviderQueryMissingOrMalformedErrorPreservesTerminalStatus(t *testing.T) {
	for _, detail := range []string{"", `,"error":null`, `,"error":"unstructured secret"`, `,"error":{"code":123,"message":"secret"}`, `,"error":{"code":"https://example.test/private?token=secret"}`} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"failed"` + detail + `}`))
		}))
		p := Provider{BaseURL: upstream.URL, Client: upstream.Client()}
		result, err := p.Query(context.Background(), "original-task")
		upstream.Close()
		if err != nil || result.Status != "failed" || sanitizeProviderError(result.Error.Code) != nil {
			t.Fatalf("missing error lost terminal status: %+v err=%v", result, err)
		}
	}
}

func TestProviderTerminalFailurePersistsSafeDiagnosticForReplicaAndCanvas(t *testing.T) {
	for _, canvas := range []bool{false, true} {
		for _, status := range []string{"failed", "cancelled", "expired"} {
			t.Run(status+map[bool]string{true: "/canvas", false: "/replica"}[canvas], func(t *testing.T) {
				f := setup(t)
				ctx := context.Background()
				requests := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						requests++
						_ = json.NewEncoder(w).Encode(map[string]string{"id": "original-task"})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "error": map[string]string{"code": "InputImageSensitiveContentDetected", "message": "secret https://example.test/private?token=secret"}})
				}))
				defer upstream.Close()
				f.s.Provider = func(context.Context, Model) (Provider, error) {
					return Provider{BaseURL: upstream.URL, Client: upstream.Client()}, nil
				}
				var p *Project
				if canvas {
					r := CanvasRequest{Kind: "video", ModelID: "v", Prompt: "controlled test", Resolution: "720p", AspectRatio: "16:9", Duration: 5, Count: 1, ExpectedQuoteMicro: 500000, BoardID: "b", NodeID: "n", WorkspacePath: "/fixture"}
					var err error
					p, err = f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					p = f.project(t)
				}
				for range 4 {
					f.s.Tick(ctx)
				}
				p = current(t, f, p)
				shot := p.Shots[0]
				if p.Status != "failed" || shot.Error != "provider_"+status || shot.ProviderError == nil || shot.ProviderError.Code != "InputImageSensitiveContentDetected" || shot.ProviderError.Category != "content_policy" || shot.VideoTaskID != "original-task" || requests != 1 {
					t.Fatalf("terminal failure = %+v requests=%d", shot, requests)
				}
				public, _ := json.Marshal(p.View())
				if !strings.Contains(string(public), `"providerError":{"code":"InputImageSensitiveContentDetected","category":"content_policy"}`) || !strings.Contains(string(public), `"videoTaskId":"original-task"`) || strings.Contains(string(public), "secret") {
					t.Fatalf("unsafe or missing public diagnostic: %s", public)
				}
				team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
				if p.ChargedMicro != 0 || shot.ReservationID != "" || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
					t.Fatalf("failed task charged or retained admission: %+v %+v", shot, team)
				}
				p, err := f.s.Action(ctx, f.team, p.ID, p.Version, "retry")
				if err != nil || p.Shots[0].ProviderError != nil || p.Shots[0].VideoTaskID != "" || p.Shots[0].Attempt != 2 || requests != 1 {
					t.Fatalf("retry did not clear current diagnostic: %+v err=%v", p, err)
				}
			})
		}
	}
}

func TestProjectViewOmitsUnsafeTaskIDsAndRechecksProviderDiagnostic(t *testing.T) {
	for _, id := range []string{"https://example.test/private?token=secret", "line\nsecret", strings.Repeat("x", 257)} {
		p := Project{Shots: []Shot{{VideoTaskID: id, ReservationID: "private-admission", ProviderError: &ProviderError{Code: "FutureError", Category: "content_policy"}}}}
		view := p.View()
		public, _ := json.Marshal(view)
		if strings.Contains(string(public), "videoTaskId") || strings.Contains(string(public), "private-admission") || view.Shots[0].ProviderError.Category != "" {
			t.Fatalf("unsafe task projection: %s", public)
		}
		view.Shots[0].ProviderError.Code = "changed"
		if p.Shots[0].ProviderError.Code != "FutureError" {
			t.Fatal("public diagnostic aliases persisted state")
		}
	}
}

func TestProviderSubmissionDiagnosticDoesNotChangeUnknownAdmission(t *testing.T) {
	for _, tc := range []struct {
		status         int
		code, category string
	}{
		{http.StatusUnprocessableEntity, "InvalidParameter", "invalid_input"},
		{http.StatusNotFound, "ModelNotOpen", "authentication"},
		{http.StatusBadGateway, "InvalidParameter", ""},
		{http.StatusBadGateway, "ModelNotOpen", ""},
	} {
		t.Run(http.StatusText(tc.status)+"/"+tc.code, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": tc.code, "message": "secret"}})
			}))
			defer upstream.Close()
			f.s.Provider = func(context.Context, Model) (Provider, error) {
				return Provider{BaseURL: upstream.URL, Client: upstream.Client()}, nil
			}
			p := f.project(t)
			for range 4 {
				f.s.Tick(ctx)
			}
			p = current(t, f, p)
			shot := p.Shots[0]
			if requests != 1 || p.ChargedMicro != 0 {
				t.Fatalf("failed submit repeated or charged: %+v requests=%d", shot, requests)
			}
			if tc.status != http.StatusBadGateway {
				if p.Status != "failed" || shot.Error != "provider_rejected" || shot.ProviderError == nil || shot.ProviderError.Code != tc.code || shot.ProviderError.Category != tc.category || shot.ReservationID != "" || shot.VideoTaskID != "" {
					t.Fatalf("rejected = %+v", shot)
				}
			} else if p.Status != "reconciliation_required" || shot.ReservationID == "" || shot.ProviderError != nil {
				t.Fatalf("unknown submission was closed: %+v", shot)
			}
			team, err := f.s.Repo.Store.GetTeam(ctx, f.team)
			if err != nil || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
				t.Fatalf("submission diagnostic changed balance: %+v err=%v", team, err)
			}
		})
	}
}

func TestProjectViewClassifiesLegacyModelNotOpenWithoutMutatingStoredFact(t *testing.T) {
	p := Project{Shots: []Shot{{ProviderError: &ProviderError{Code: "ModelNotOpen"}}}}
	view := p.View()
	if view.Shots[0].ProviderError == nil || view.Shots[0].ProviderError.Code != "ModelNotOpen" || view.Shots[0].ProviderError.Category != "authentication" || p.Shots[0].ProviderError.Category != "" {
		t.Fatalf("legacy model activation diagnostic lost classification or changed persisted fact: %+v", view.Shots[0].ProviderError)
	}
}

func TestProviderQueryErrorKeepsReconciliationOrRetryingState(t *testing.T) {
	for _, status := range []int{401, 403, 404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					requests++
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "original-task"})
					return
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "AuthenticationError", "message": "secret"}})
			}))
			defer upstream.Close()
			f.s.Provider = func(context.Context, Model) (Provider, error) {
				return Provider{BaseURL: upstream.URL, Client: upstream.Client()}, nil
			}
			p := f.project(t)
			for range 4 {
				f.s.Tick(ctx)
			}
			p = current(t, f, p)
			shot := p.Shots[0]
			if requests != 1 || shot.VideoTaskID != "original-task" || shot.ReservationID == "" || shot.ProviderError != nil || p.ChargedMicro != 0 {
				t.Fatalf("query error changed admission: %+v", shot)
			}
			if status == 503 && (shot.Status != "video_running" || shot.Error != "provider_query_retrying") || status != 503 && p.Status != "reconciliation_required" {
				t.Fatalf("query classification changed: %+v", shot)
			}
		})
	}
}
