package media

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestProductionDoesNotUseLegacySpendingLimit(t *testing.T) {
	for _, limit := range []int64{0, 1, 2_000_000_000} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			original := f.project(t)
			brief := original.Brief
			brief.MaxCreditsMicro = limit
			created, err := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), brief)
			if err != nil {
				t.Fatal(err)
			}
			if _, quote, err := BuildShots(created.Brief, *original.Plan, original.Models); err != nil || quote <= 0 {
				t.Fatalf("quote=%d err=%v", quote, err)
			}

			failed, err := f.s.Mutate(ctx, f.team, original.ID, original.Version, func(p *Project) error {
				p.Brief.MaxCreditsMicro = limit
				p.Status = "failed"
				p.ChargedMicro = 3_000_000_000
				p.Shots[0].Status = "failed"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			retried, err := f.s.Action(ctx, f.team, failed.ID, failed.Version, "retry")
			if err != nil || retried.Status != "queued" {
				t.Fatalf("retry: %v %+v", err, retried)
			}
		})
	}
}

func TestWholeReferenceIgnoresLegacySpendingLimit(t *testing.T) {
	for _, limit := range []int64{0, 1} {
		brief, models, plan := wholeReferenceFixture()
		brief.MaxCreditsMicro = limit
		if _, quote, err := BuildShots(brief, plan, models); err != nil || quote <= 0 {
			t.Fatalf("quote=%d err=%v", quote, err)
		}
	}
}

func TestRepairDoesNotUseLegacySpendingLimitAndKeepsRequestIdentity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	original := f.project(t)
	index := 0
	completed, err := f.s.Mutate(ctx, f.team, original.ID, original.Version, func(p *Project) error {
		p.Brief.MaxCreditsMicro = 1
		p.ChargedMicro = 3_000_000_000
		p.Status, p.OutputAssetID = "completed", "E2E_OUTPUT"
		p.Shots[0].Status = "succeeded"
		p.MachineReview = &VideoMachineReview{OutputAssetID: p.OutputAssetID, Status: "warning", Issues: []MachineReviewIssue{{ShotIndex: &index, Repair: "regenerate"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := f.s.RepairShots(ctx, f.team, completed.ID, completed.Version, "E2E_REPAIR", []int{index})
	if err != nil || repaired.Shots[0].Attempt != 2 {
		t.Fatalf("repair: %v %+v", err, repaired)
	}
	again, err := f.s.RepairShots(ctx, f.team, completed.ID, completed.Version, "E2E_REPAIR", []int{index})
	if err != nil || again.Version != repaired.Version || again.Shots[0].Attempt != 2 {
		t.Fatalf("replay: %v %+v", err, again)
	}
}

func TestProductionBriefOmitsRetiredLimit(t *testing.T) {
	raw, err := json.Marshal(Brief{})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if _, exists := value["maxCreditsMicro"]; exists {
		t.Fatal("new brief contains a spending limit")
	}
}
