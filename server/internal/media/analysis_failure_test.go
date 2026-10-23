package media

import (
	"context"
	"errors"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestAnalysisFailureRetainsSafeReasonAndReleasesUnchargedIdentity(t *testing.T) {
	for _, code := range []string{"analysis_failed", "video_analysis_scope_invalid", "video_analysis_scope_budget_exhausted", "video_analysis_scope_unavailable", "source_timeline_invalid", "source_url_mismatch", "source_report_invalid", "video_reference_duration_unsupported", "model_not_configured", "duration_or_budget_exceeded", "video_reference_plan_invalid", "video_reference_prompt_limit", "video_reference_unsupported", "video_reference_image_limit", "first_frame_required", "video_source_changed"} {
		t.Run(code, func(t *testing.T) {
			f, p, _ := analysisFeeFixture(t)
			ctx := context.Background()
			p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.FailAnalysis(ctx, f.team, p.ID, p.Version, "input", code)
			if err != nil {
				t.Fatal(err)
			}
			identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, p.AnalysisFee.ID)
			team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
			if p.Status != "analysis_failed" || p.Error != code || identity.Status != store.ReservationReleased || team.UsedCredits != "0.000000" {
				t.Fatalf("failure lost safe code or charged: status=%s error=%s identity=%s used=%s", p.Status, p.Error, identity.Status, team.UsedCredits)
			}
		})
	}
}

func TestAnalysisFailureRejectsStaleInputAndUnsafeReasonWithoutMutation(t *testing.T) {
	f, p, _ := analysisFeeFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "current", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input, code string
		version     int64
		expected    error
	}{
		{"old", "video_analysis_scope_invalid", p.Version, ErrConflict},
		{"current", "video_analysis_scope_invalid", p.Version - 1, ErrConflict},
		{"current", "untrusted provider payload", p.Version, ErrInvalid},
		{"", "analysis_failed", p.Version, ErrInvalid},
	} {
		if _, err := f.s.FailAnalysis(ctx, f.team, p.ID, test.version, test.input, test.code); !errors.Is(err, test.expected) {
			t.Fatalf("unexpected rejection: %v", err)
		}
	}
	current, _ := f.s.Repo.Get(ctx, f.team, p.ID)
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, p.AnalysisFee.ID)
	if current.Version != p.Version || current.Status != "analyzing" || current.Error != "" || identity.Status != store.ReservationReserved {
		t.Fatal("rejected failure mutated current input or fee")
	}
}
