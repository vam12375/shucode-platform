package media

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFractionalSubtitleClock(t *testing.T) {
	for input, want := range map[float64]string{0: "0:00:00.00", 1.235: "0:00:01.24", 59.999: "0:01:00.00", 3661.27: "1:01:01.27"} {
		if got := subtitleTime(input); got != want {
			t.Errorf("subtitleTime(%v)=%s want %s", input, got, want)
		}
	}
}

func TestReplicaCompositionUsesSourceClockAndBoundaryFrames(t *testing.T) {
	p := &Project{Brief: Brief{ReferenceAssetID: "source"}}
	for i := range 10 {
		p.Shots = append(p.Shots, Shot{Plan: ShotPlan{Duration: 5, SourceStart: float64(i) * 1.117, SourceEnd: float64(i+1) * 1.117, Transition: "cut"}})
	}
	p.Shots[1].Plan.Transition = "fade"
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	for _, segment := range segments {
		frames += segment.frames
	}
	if frames != 335 {
		t.Fatalf("frames=%d want round(11.17*30)=335", frames)
	}
	if math.Abs(segments[1].start-1.117) > .00001 || math.Abs(segments[1].end-2.234) > .00001 {
		t.Fatalf("source clock lost: %+v", segments[1])
	}
	if segments[0].fadeOut <= 0 || segments[1].fadeIn <= 0 || segments[1].fadeOut != 0 || segments[2].fadeIn != 0 {
		t.Fatalf("fade must apply only at the selected boundary: %+v", segments[:3])
	}
	initialFade, err := compositionTimeline(&Project{Brief: Brief{ReferenceAssetID: "source"}, Shots: []Shot{{Plan: ShotPlan{Duration: 5, SourceStart: 0, SourceEnd: 1.117, Transition: "fade"}}}})
	if err != nil || initialFade[0].fadeIn <= 0 {
		t.Fatalf("first shot fade must start from black: %+v %v", initialFade, err)
	}
	legacy, err := compositionTimeline(&Project{Shots: []Shot{{Plan: ShotPlan{Duration: 5, SourceStart: 1, SourceEnd: 2.1}}}})
	if err != nil || legacy[0].frames != 150 || legacy[0].end != 5 {
		t.Fatalf("legacy duration changed: %+v %v", legacy, err)
	}
}

func TestReplicaCompositionRejectsUndeliverableRange(t *testing.T) {
	for _, end := range []float64{0, .001, math.NaN(), math.Inf(1), 6} {
		p := &Project{Brief: Brief{ReferenceAssetID: "source"}, Shots: []Shot{{Plan: ShotPlan{Duration: 5, SourceEnd: end}}}}
		if _, err := compositionTimeline(p); err == nil {
			t.Errorf("accepted invalid source end %v", end)
		}
	}
}

func TestSubtitleMetrics(t *testing.T) {
	cases := []struct {
		w, h                    int
		font, marginLR, marginV int
	}{
		{854, 480, 32, 26, 53},
		{1280, 720, 48, 40, 80},
		{1920, 1080, 72, 60, 120},
		{3840, 2160, 144, 120, 240},
	}
	for _, c := range cases {
		font, marginLR, marginV := subtitleMetrics(c.w, c.h)
		if font != c.font || marginLR != c.marginLR || marginV != c.marginV {
			t.Errorf("subtitleMetrics(%d, %d) = %d/%d/%d, want %d/%d/%d", c.w, c.h, font, marginLR, marginV, c.font, c.marginLR, c.marginV)
		}
	}
}

func TestFourKComposeBudget(t *testing.T) {
	if composeTimeout("4k") <= composeTimeout("1080p") {
		t.Fatal("4k compose timeout must be longer than 1080p")
	}
	if workerLeaseDuration("4k") <= composeTimeout("4k") {
		t.Fatal("4k worker lease must cover the compose timeout")
	}
	if composeTimeout("1080p") != defaultComposeTimeout {
		t.Fatal("non-4k compose timeout must stay unchanged")
	}
}

func TestCompositionRecoveryReusesPersistedFinalAsset(t *testing.T) {
	for _, assetAlreadyRegistered := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash-before-asset-registration", true: "crash-before-project-finalize"}[assetAlreadyRegistered], func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			p := f.project(t)
			id := uuid.NewString()
			path := f.s.Blobs.Path(f.team, id)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("durable-final"), 0600); err != nil {
				t.Fatal(err)
			}
			if assetAlreadyRegistered {
				asset := Asset{ID: id, Name: "fixture product.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len("durable-final"))}
				if err := f.s.Repo.AddAsset(ctx, f.team, asset); err != nil {
					t.Fatal(err)
				}
			}
			p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Status = "composing"
				v.CompositionAssetID = id
				v.LeaseOwner = "expired-worker"
				v.LeaseUntil = time.Now().Add(-time.Minute)
				for i := range v.Shots {
					v.Shots[i].Status = "succeeded"
					v.Shots[i].OutputAssetID = uuid.NewString()
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.s.Repo.Assets(ctx, f.team)
			if err != nil {
				t.Fatal(err)
			}
			f.s.Tick(ctx)
			p = current(t, f, p)
			after, err := f.s.Repo.Assets(ctx, f.team)
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != "completed" || p.OutputAssetID != id || p.CompositionAssetID != "" {
				t.Fatalf("composition recovery did not finalize stable output: %+v", p)
			}
			wantAssetCount := len(before)
			if !assetAlreadyRegistered {
				wantAssetCount++
			}
			if len(after) != wantAssetCount {
				t.Fatalf("composition recovery asset count=%d want=%d", len(after), wantAssetCount)
			}
			f.s.Tick(ctx)
			afterRetry, err := f.s.Repo.Assets(ctx, f.team)
			if err != nil || len(afterRetry) != wantAssetCount {
				t.Fatalf("repeated Tick duplicated composition asset: count=%d want=%d err=%v", len(afterRetry), wantAssetCount, err)
			}
		})
	}
}

func TestCancelledCompositionCleansPersistedFinalAsset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	id := uuid.NewString()
	path := f.s.Blobs.Path(f.team, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cancelled-final"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: id, Name: "cancelled.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len("cancelled-final"))}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "cancelled"
		v.CancelRequested = true
		v.CompositionAssetID = id
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.CompositionAssetID != "" || p.OutputAssetID != "" || p.Status != "cancelled" {
		t.Fatalf("cancelled composition cleanup changed terminal semantics: %+v", p)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancelled composition blob remains: %v", err)
	}
	assets, err := f.s.Repo.Assets(ctx, f.team)
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		if asset.ID == id {
			t.Fatalf("cancelled composition asset remains: %+v", asset)
		}
	}
}

func TestCancellationRacingCompositionFinalizeRemovesUndeliveredAsset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	id := uuid.NewString()
	path := f.s.Blobs.Path(f.team, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cancelled-final"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: id, Name: "cancelled.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len("cancelled-final"))}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "composing"
		v.CompositionAssetID = id
		v.LeaseOwner = f.s.WorkerID
		v.LeaseUntil = time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cancel wins the project CAS after the blob and asset are durable but before final delivery.
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.CancelRequested = true
		v.Status = "cancelling"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.finishComposition(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	p = current(t, f, p)
	if p.Status != "cancelled" || p.OutputAssetID != "" || p.CompositionAssetID != id {
		t.Fatalf("cancelled render was delivered or lost its cleanup marker: %+v", p)
	}
	p, err = f.s.workerUpdate(ctx, p, func(v *Project) {
		v.LeaseOwner = ""
		v.LeaseUntil = time.Time{}
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Status != "cancelled" || p.OutputAssetID != "" || p.CompositionAssetID != "" {
		t.Fatalf("cancel cleanup changed terminal project state: %+v", p)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancelled composition blob remains: %v", err)
	}
	assets, err := f.s.Repo.Assets(ctx, f.team)
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		if asset.ID == id {
			t.Fatalf("cancelled composition asset remains: %+v", asset)
		}
	}
}

func TestOutputSize(t *testing.T) {
	cases := []struct {
		resolution, ratio string
		w, h              int
	}{
		{"480p", "16:9", 864, 496},
		{"480p", "9:16", 496, 864},
		{"480p", "1:1", 640, 640},
		{"480p", "4:3", 752, 560},
		{"480p", "3:4", 560, 752},
		{"480p", "21:9", 992, 432},
		{"720p", "16:9", 1280, 720},
		{"720p", "9:16", 720, 1280},
		{"720p", "1:1", 960, 960},
		{"720p", "4:3", 1112, 834},
		{"720p", "3:4", 834, 1112},
		{"720p", "21:9", 1470, 630},
		{"1080p", "16:9", 1920, 1080},
		{"1080p", "9:16", 1080, 1920},
		{"1080p", "1:1", 1440, 1440},
		{"1080p", "4:3", 1664, 1248},
		{"1080p", "3:4", 1248, 1664},
		{"1080p", "21:9", 2206, 946},
		{"4k", "16:9", 3840, 2160},
		{"4k", "9:16", 2160, 3840},
		{"4k", "1:1", 2880, 2880},
		{"4k", "4:3", 3326, 2494},
		{"4k", "3:4", 2494, 3326},
		{"4k", "21:9", 4398, 1886},
		{"720p", "auto", 0, 0},
		{"720p", "unknown", 0, 0},
	}
	for _, c := range cases {
		w, h := outputSize(c.resolution, c.ratio)
		if w != c.w || h != c.h {
			t.Errorf("outputSize(%q, %q) = %dx%d, want %dx%d", c.resolution, c.ratio, w, h, c.w, c.h)
		}
	}
}
