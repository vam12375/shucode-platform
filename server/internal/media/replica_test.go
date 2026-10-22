package media

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func replicaFixture() (Brief, []Model, []Asset, Plan) {
	v := fixtureVideoModel()
	im := Model{ID: "im", Kind: "image", PriceMicro: 10_000}
	b := Brief{Mode: "replicate", Product: "人物舞蹈", AssetIDs: []string{}, ReferenceURLs: []string{}, ReferenceAssetID: "source", ReferenceDurationSeconds: 1.3, VideoModel: v.ID, ImageModel: "im", Resolution: "720p", AspectRatio: "9:16", Duration: 2, MaxCreditsMicro: 100_000_000, ReviewPlan: true}
	assets := []Asset{{ID: "source", Kind: "video", DurationSeconds: 1.3}}
	ref := Reference{URL: "", Title: "原片", CapturedAt: time.Now(), Evidence: "Read原片", SelectionReason: "用户导入"}
	shot := ShotPlan{SourceURL: "", SourceStart: 0, SourceEnd: 1.3, Duration: 4, Framing: "全景", Camera: "推进", Action: "舞蹈", Purpose: "开场", Prompt: "舞蹈", ImagePrompt: "人物首帧", Transition: "cut"}
	return b, []Model{v, im}, assets, Plan{References: []Reference{ref}, Shots: []ShotPlan{shot}}
}

func TestReplicaSupportsFractionalSourceClockAndOptionalReplacement(t *testing.T) {
	b, models, assets, plan := replicaFixture()
	if err := ValidateBrief(b, models, assets); err != nil {
		t.Fatal(err)
	}
	shots, quote, err := BuildShots(b, plan, models)
	if err != nil || len(shots) != 1 || quote != models[0].PriceMicro*4+10_000 {
		t.Fatalf("provider seconds must determine quote: %d %v", quote, err)
	}
	b.ImageModel = ""
	if err := ValidateBrief(b, models, assets); err == nil {
		t.Fatal("no image and no image model accepted")
	}
}

func TestReplicaRejectsMissingSourceCoverageAndInsufficientProviderDuration(t *testing.T) {
	b, models, _, plan := replicaFixture()
	for _, mutate := range []func(*ShotPlan){func(s *ShotPlan) { s.SourceStart = .1 }, func(s *ShotPlan) { s.SourceEnd = 1.2 }, func(s *ShotPlan) { s.Duration = 1 }, func(s *ShotPlan) { s.ImagePrompt = "" }, func(s *ShotPlan) { s.Transition = "unsupported" }} {
		p := plan
		p.Shots = append([]ShotPlan(nil), plan.Shots...)
		mutate(&p.Shots[0])
		if _, _, err := BuildShots(b, p, models); err == nil {
			t.Fatalf("invalid replica plan accepted: %+v", p.Shots[0])
		}
	}
}

func TestReplicaFreezesMeasuredSourceAndAllowsReviewRevisionOnly(t *testing.T) {
	f := setup(t)
	b, models, assets, plan := replicaFixture()
	ctx := context.Background()
	for _, a := range assets {
		if err := f.s.Repo.AddAsset(ctx, f.team, a); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(models)
	f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw)
	b.ReferenceDurationSeconds = 80
	p, err := f.s.Create(ctx, f.team, f.user, uuid.NewString(), uuid.NewString(), b)
	if err != nil || p.Brief.ReferenceDurationSeconds != 1.3 {
		t.Fatalf("source duration not frozen: %+v %v", p, err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input", plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Shots[0].Prompt = "替换动作"
	p, err = f.s.Revise(ctx, f.team, p.ID, p.Version, plan)
	if err != nil || p.Plan.Shots[0].Prompt != "替换动作" {
		t.Fatal(err)
	}
	if _, err = f.s.Revise(ctx, f.team, p.ID, p.Version-1, plan); err == nil {
		t.Fatal("stale revision accepted")
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "generate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Revise(ctx, f.team, p.ID, p.Version, plan); err == nil {
		t.Fatal("queued project revised")
	}
}
