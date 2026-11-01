package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 真实后期/品牌用例依赖 ffmpeg 与 CJK 字体；CI check 会安装并执行，本地缺少时按既有约定跳过。
func requireFinishingComposition(t *testing.T) {
	t.Helper()
	if !FinishingCompositionAvailable() {
		t.Skip("ffmpeg finishing composition unavailable")
	}
}

func requireBrandComposition(t *testing.T) {
	t.Helper()
	if !BrandCompositionAvailable() {
		t.Skip("ffmpeg/CJK fonts brand composition unavailable")
	}
}

func TestCompositionTextLimitsMatchZodUnicodeCodePoints(t *testing.T) {
	for _, tc := range []struct {
		name                string
		title, sub, caption int
		bad                 bool
	}{
		{"title-limit", 80, 0, 0, false}, {"title-over", 81, 0, 0, true}, {"subtitle-limit", 0, 160, 0, false}, {"subtitle-over", 0, 161, 0, true}, {"caption-limit", 0, 0, 300, false}, {"caption-over", 0, 0, 301, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brand := testVideoBrand("")
			brand.EndTitle = " " + strings.Repeat("😀", tc.title) + " "
			brand.EndSubtitle = strings.Repeat("😀", tc.sub)
			caption := strings.Repeat("😀", tc.caption)
			edit := VideoCompositionEdit{FPS: 30, Subtitles: "none", Brand: brand, Clips: []VideoCompositionClip{{SourceShotIndex: 0, OutFrame: 150, Speed: 1, Subtitle: &caption}}}
			if err := validateCompositionEdit(edit); (err != nil) != tc.bad {
				t.Fatalf("Unicode code-point contract err=%v want bad=%v", err, tc.bad)
			}
		})
	}
}

func TestCompositionJSONRejectsMissingNullAndUnknownNestedFields(t *testing.T) {
	valid := map[string]any{"fps": 30, "voiceover": false, "music": false, "subtitles": "none", "clips": []any{map[string]any{"sourceShotIndex": 0, "inFrame": 0, "outFrame": 150, "speed": 1}}}
	brand := map[string]any{"preset": "clean", "fontFamily": "sans", "primaryColor": "#123456", "backgroundColor": "#000000", "textColor": "#FFFFFF"}
	for name, mutate := range map[string]func(map[string]any){
		"edit-alias": func(v map[string]any) { v["Voiceover"] = true },
		"clip-alias": func(v map[string]any) { v["clips"].([]any)[0].(map[string]any)["SourceShotIndex"] = 1 },
		"brand-alias": func(v map[string]any) {
			var b map[string]any
			raw, _ := json.Marshal(brand)
			_ = json.Unmarshal(raw, &b)
			b["endTitle"] = "safe"
			b["EndTitle"] = "override"
			v["brand"] = b
		},
		"brand-empty-logo": func(v map[string]any) {
			var b map[string]any
			raw, _ := json.Marshal(brand)
			_ = json.Unmarshal(raw, &b)
			b["logoAssetId"] = " "
			v["brand"] = b
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var v map[string]any
			_ = json.Unmarshal(raw, &v)
			mutate(v)
			raw, _ = json.Marshal(v)
			var edit VideoCompositionEdit
			if json.Unmarshal(raw, &edit) == nil {
				t.Fatalf("accepted ambiguous shape: %s", raw)
			}
		})
	}
	for _, field := range []string{"sourceShotIndex", "inFrame", "outFrame", "speed", "subtitle"} {
		for _, missing := range []bool{false, true} {
			if missing && field == "subtitle" {
				continue
			}
			t.Run(field+fmt.Sprint(missing), func(t *testing.T) {
				raw, _ := json.Marshal(valid)
				var input map[string]any
				_ = json.Unmarshal(raw, &input)
				clip := input["clips"].([]any)[0].(map[string]any)
				if missing {
					delete(clip, field)
				} else {
					clip[field] = nil
				}
				raw, _ = json.Marshal(input)
				var edit VideoCompositionEdit
				if json.Unmarshal(raw, &edit) == nil {
					t.Fatalf("accepted missing/null nested field: %s", raw)
				}
			})
		}
	}
	for _, field := range []string{"preset", "fontFamily", "primaryColor", "backgroundColor", "textColor", "logoAssetId", "endTitle", "endSubtitle", "unknown"} {
		t.Run("brand-"+field, func(t *testing.T) {
			raw, _ := json.Marshal(brand)
			var input map[string]any
			_ = json.Unmarshal(raw, &input)
			input[field] = nil
			copyInput := map[string]any{}
			for k, v := range valid {
				copyInput[k] = v
			}
			copyInput["brand"] = input
			raw, _ = json.Marshal(copyInput)
			var edit VideoCompositionEdit
			if json.Unmarshal(raw, &edit) == nil {
				t.Fatalf("accepted null/unknown brand field: %s", raw)
			}
		})
	}
}

func TestRecomposeConcurrentIdenticalRequestsShareAcceptedVersion(t *testing.T) {
	requireFinishingComposition(t)
	f, p, edit := editableProjectFixture(t)
	request := uuid.NewString()
	type result struct {
		p   *Project
		err error
	}
	results := make(chan result, 6)
	start := make(chan struct{})
	for range 6 {
		go func() {
			<-start
			v, err := f.s.Recompose(context.Background(), f.team, f.user, p.ID, p.Version, request, edit)
			results <- result{v, err}
		}()
	}
	close(start)
	for range 6 {
		v := <-results
		if v.err != nil || v.p.Version != p.Version+1 {
			t.Fatalf("identical concurrent request was not replayed: %+v %v", v.p, v.err)
		}
	}
	stored := current(t, f, p)
	if len(stored.CompositionReceipts) != 1 || f.submits.Load() != 0 {
		t.Fatal("concurrent retry duplicated admission or paid generation")
	}
}

func editableProjectFixture(t *testing.T) (*fixture, *Project, VideoCompositionEdit) {
	t.Helper()
	f := setup(t)
	p := f.project(t)
	raw, err := os.ReadFile("testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := f.s.Blobs.Put(f.team, bytes.NewReader(raw), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(context.Background(), f.team, Asset{ID: id, Kind: "video", Mime: "video/mp4", Size: int64(len(raw))}); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Status, v.OutputAssetID, v.ChargedMicro = "completed", id, 1_000_000
		v.Brief.Finishing = &FinishingOptions{Subtitles: "none"}
		for i := range v.Shots {
			v.Shots[i].Status, v.Shots[i].OutputAssetID, v.Shots[i].ChargedMicro = "succeeded", id, 500_000
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	edit := VideoCompositionEdit{FPS: 30, Subtitles: "none", Clips: []VideoCompositionClip{{SourceShotIndex: 0, InFrame: 0, OutFrame: 150, Speed: 1}, {SourceShotIndex: 1, InFrame: 0, OutFrame: 150, Speed: 1}}}
	return f, p, edit
}

func TestRecomposeAdmissionIsIdempotentAndPreservesPaidGeneration(t *testing.T) {
	requireFinishingComposition(t)
	f, p, edit := editableProjectFixture(t)
	ctx := context.Background()
	original, _ := f.s.Repo.Get(ctx, f.team, p.ID)
	request := uuid.NewString()
	next, err := f.s.Recompose(ctx, f.team, f.user, p.ID, p.Version, request, edit)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "composing" || next.OutputAssetID != p.OutputAssetID || next.Composition == nil || next.Composition.SourceVersion != p.Version || next.Composition.RequestID != request {
		t.Fatalf("admission lost original delivery: %+v", next)
	}
	if next.ChargedMicro != p.ChargedMicro || next.QuoteMicro != p.QuoteMicro || !reflect.DeepEqual(next.Shots, original.Shots) || !reflect.DeepEqual(next.Plan, original.Plan) || !reflect.DeepEqual(next.Brief, original.Brief) || f.submits.Load() != 0 {
		t.Fatal("recompose changed generation or billing")
	}
	replay, err := f.s.Recompose(ctx, f.team, f.user, p.ID, p.Version, request, edit)
	if err != nil || replay.Version != next.Version {
		t.Fatalf("stale-version same request was not reused: %+v %v", replay, err)
	}
	edit.Clips[0].Speed = 2
	if _, err = f.s.Recompose(ctx, f.team, f.user, p.ID, next.Version, request, edit); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting request accepted: %v", err)
	}
	if _, err = f.s.Recompose(ctx, f.team, "another-account", p.ID, next.Version, uuid.NewString(), edit); err == nil {
		t.Fatal("another account edited original project")
	}
}

func TestRecomposeRejectsInvalidSourceFramesAndUnsafeParameters(t *testing.T) {
	requireFinishingComposition(t)
	cases := map[string]func(*VideoCompositionEdit){
		"wrong-fps":      func(e *VideoCompositionEdit) { e.FPS = 24 },
		"duplicate":      func(e *VideoCompositionEdit) { e.Clips[1].SourceShotIndex = 0 },
		"negative":       func(e *VideoCompositionEdit) { e.Clips[0].InFrame = -1 },
		"source-overrun": func(e *VideoCompositionEdit) { e.Clips[0].OutFrame = 151 },
		"empty-range":    func(e *VideoCompositionEdit) { e.Clips[0].InFrame = 150 },
		"invalid-speed":  func(e *VideoCompositionEdit) { e.Clips[0].Speed = 1.1 },
		"too-short":      func(e *VideoCompositionEdit) { e.Clips = e.Clips[:1]; e.Clips[0].OutFrame = 60 },
		"invalid-color": func(e *VideoCompositionEdit) {
			e.Brand = &VideoBrand{Preset: "clean", FontFamily: "sans", PrimaryColor: "red;movie=x", BackgroundColor: "#000000", TextColor: "#FFFFFF"}
		},
		"unfrozen-logo": func(e *VideoCompositionEdit) {
			e.Brand = &VideoBrand{Preset: "clean", FontFamily: "sans", PrimaryColor: "#112233", BackgroundColor: "#000000", TextColor: "#FFFFFF", LogoAssetID: "not-frozen"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f, p, e := editableProjectFixture(t)
			change(&e)
			if _, err := f.s.Recompose(context.Background(), f.team, f.user, p.ID, p.Version, uuid.NewString(), e); err == nil {
				t.Fatal("invalid edit accepted")
			}
			actual := current(t, f, p)
			if actual.Version != p.Version || actual.Composition != nil || actual.ChargedMicro != p.ChargedMicro || f.submits.Load() != 0 {
				t.Fatal("failed validation changed persisted project")
			}
		})
	}
}

func TestRecomposeNarrationRequiresOriginalTimelineAndExistingReadyAudio(t *testing.T) {
	requireFinishingComposition(t)
	f, p, e := editableProjectFixture(t)
	e.Voiceover = true
	e.Clips[0], e.Clips[1] = e.Clips[1], e.Clips[0]
	if _, err := f.s.Recompose(context.Background(), f.team, f.user, p.ID, p.Version, uuid.NewString(), e); err == nil || err.Error() != "composition_voiceover_timeline_changed" {
		t.Fatalf("reordered narration accepted: %v", err)
	}
	e.Clips[0], e.Clips[1] = e.Clips[1], e.Clips[0]
	if _, err := f.s.Recompose(context.Background(), f.team, f.user, p.ID, p.Version, uuid.NewString(), e); err == nil || err.Error() != "composition_narration_unavailable" {
		t.Fatalf("missing narration accepted: %v", err)
	}
	p, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Narration = &NarrationState{Status: "unknown", Text: "unchanged"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Voiceover = false
	if _, err = f.s.Recompose(context.Background(), f.team, f.user, p.ID, p.Version, uuid.NewString(), e); err == nil {
		t.Fatal("unknown narration submission was edited")
	}
	if f.submits.Load() != 0 {
		t.Fatal("recompose submitted paid media")
	}
}

func TestRecomposeRejectsLiveLeaseAndRetainsPriorDeliveryOnCancel(t *testing.T) {
	requireFinishingComposition(t)
	f, p, e := editableProjectFixture(t)
	p, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.LeaseOwner = "another-worker"
		v.LeaseUntil = time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Recompose(context.Background(), f.team, f.user, p.ID, p.Version, uuid.NewString(), e); !errors.Is(err, ErrConflict) {
		t.Fatalf("live lease was bypassed: %v", err)
	}
}

func TestCompositionEditsRespectVisibleCopyBudgetAndForbiddenClaims(t *testing.T) {
	for _, tc := range []struct {
		name, text, claims string
		short              bool
		out                int
		speed              float64
		bad                bool
	}{
		{"cn-budget", "一二三四五六七八九十", "", true, 30, 2, true},
		{"en-budget", "TwentyFourCharactersABCDEF", "", true, 30, 2, true},
		{"line-width", "一二三四五六七八九十一二三四五六七八九", "", true, 150, 1, true},
		{"three-lines", "一\n二\n三", "", true, 150, 1, true},
		{"cn-claim", "绝对有效", "绝对", true, 150, 1, true},
		{"en-claim", "BEST MAGIC", "magic", true, 150, 1, true},
		{"disabled-not-visible", "绝对有效\n一二三四五六七八九十一二三四五六七八九", "绝对", false, 30, 2, false},
		{"valid", "清晰展示", "", true, 150, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.text
			p := &Project{Brief: Brief{AvoidClaims: tc.claims}, Shots: []Shot{{Plan: ShotPlan{Duration: 5, Subtitle: text}}}}
			subtitles := "none"
			if tc.short {
				subtitles = "short"
			}
			edit := VideoCompositionEdit{FPS: 30, Subtitles: subtitles, Clips: []VideoCompositionClip{{SourceShotIndex: 0, OutFrame: tc.out, Speed: tc.speed}}}
			if err := validateCompositionCopy(p, edit); (err != nil) != tc.bad {
				t.Fatalf("copy result=%v want bad=%v", err, tc.bad)
			}
		})
	}
	p := &Project{Brief: Brief{AvoidClaims: "禁用,MAGIC"}, Shots: []Shot{{Plan: ShotPlan{Duration: 5}}}}
	e := VideoCompositionEdit{Subtitles: "none", Brand: &VideoBrand{EndTitle: "Magic finish"}}
	if err := validateCompositionCopy(p, e); err == nil {
		t.Fatal("disabled captions bypassed brand claims")
	}
}

func TestCompositionCopyUsesActualMarginsAndRejectsTailOcclusion(t *testing.T) {
	text := "一二三四五六七八九十一二三\n四五六七八九十一二三四五六"
	p := &Project{Brief: Brief{Finishing: &FinishingOptions{Subtitles: "short"}}, Shots: []Shot{{Plan: ShotPlan{Duration: 5, Subtitle: text}}, {Plan: ShotPlan{Duration: 5, Subtitle: "清晰"}}}}
	e := VideoCompositionEdit{FPS: 30, Subtitles: "short", Clips: []VideoCompositionClip{{SourceShotIndex: 0, OutFrame: 150, Speed: 1}}}
	if err := validateCompositionCopy(p, e); err == nil || err.Error() != "composition_caption_invalid" {
		t.Fatalf("26 characters fit full 5s but must not fit 4.7s ASS window: %v", err)
	}
	e.Clips[0].Subtitle = func() *string {
		v := "一二三四五六七八九十一二\n三四五六七八九十一二三四五"
		return &v
	}()
	if err := validateCompositionCopy(p, e); err != nil {
		t.Fatal("25 characters should fit actual 4.7s budget", err)
	}
	empty := ""
	e.Clips = []VideoCompositionClip{{SourceShotIndex: 0, OutFrame: 135, Speed: 1, Subtitle: &empty}, {SourceShotIndex: 1, OutFrame: 15, Speed: 1}}
	e.Brand = testVideoBrand("")
	e.Brand.EndTitle = "Finish"
	if err := validateCompositionCopy(p, e); err == nil || err.Error() != "composition_caption_invalid" {
		t.Fatalf("tail card fully hid last 0.5s nonempty subtitle: %v", err)
	}
	e.Clips[1].Subtitle = &empty
	if err := validateCompositionCopy(p, e); err != nil {
		t.Fatal("empty tail caption should remain valid", err)
	}
}

func TestRecomposeCancelCleansOnlyNewUndeliveredBlob(t *testing.T) {
	requireFinishingComposition(t)
	f, p, e := editableProjectFixture(t)
	ctx := context.Background()
	p, err := f.s.Recompose(ctx, f.team, f.user, p.ID, p.Version, uuid.NewString(), e)
	if err != nil {
		t.Fatal(err)
	}
	newID, _, err := f.s.Blobs.Put(f.team, bytes.NewBufferString("undelivered"), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.CompositionAssetID = newID; return nil })
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	f.s.Tick(ctx)
	actual := current(t, f, p)
	if actual.OutputAssetID != p.OutputAssetID || actual.CompositionAssetID != "" || actual.Status != "cancelled" || actual.ChargedMicro != p.ChargedMicro {
		t.Fatalf("cancel damaged previous delivery: %+v", actual)
	}
	if _, err = os.Stat(f.s.Blobs.Path(f.team, newID)); !os.IsNotExist(err) {
		t.Fatal("undelivered blob was not removed")
	}
	if _, err = os.Stat(f.s.Blobs.Path(f.team, p.OutputAssetID)); err != nil {
		t.Fatal("original delivery deleted")
	}
	if f.submits.Load() != 0 {
		t.Fatal("cancel triggered paid request")
	}
}

func TestRecomposeRecoveryRejectsCorruptFileAndRetryDoesNotRegenerate(t *testing.T) {
	requireFinishingComposition(t)
	f, p, e := editableProjectFixture(t)
	ctx := context.Background()
	p, err := f.s.Recompose(ctx, f.team, f.user, p.ID, p.Version, uuid.NewString(), e)
	if err != nil {
		t.Fatal(err)
	}
	newID, _, err := f.s.Blobs.Put(f.team, bytes.NewBufferString("not an mp4"), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.CompositionAssetID = newID; return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	actual := current(t, f, p)
	if actual.Status != "failed" || actual.OutputAssetID != p.OutputAssetID || actual.ChargedMicro != p.ChargedMicro || f.submits.Load() != 0 {
		t.Fatalf("corrupt recovery was delivered: %+v", actual)
	}
	retried, err := f.s.Action(ctx, f.team, p.ID, actual.Version, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "composing" || retried.CompositionAssetID != "" || retried.Composition.RequestID != p.Composition.RequestID || !reflect.DeepEqual(retried.Shots, p.Shots) || f.submits.Load() != 0 {
		t.Fatal("retry reentered generation")
	}
}

func TestRecomposeReplacementRetiresFailedPendingOutputAndQualityFrames(t *testing.T) {
	requireFinishingComposition(t)
	f, p, e := editableProjectFixture(t)
	ctx := context.Background()
	p, err := f.s.Recompose(ctx, f.team, f.user, p.ID, p.Version, uuid.NewString(), e)
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := f.s.Blobs.Put(f.team, bytes.NewBufferString("failed-attempt"), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	priorFrame, _, err := f.s.Blobs.Put(f.team, bytes.NewBufferString("prior-report-frame"), MaxUpload)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: priorFrame, Kind: "image", Internal: true}); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "failed"
		v.CompositionAssetID = pending
		v.QualityReport = &VideoQualityReport{Keyframes: []VideoQualityKeyframe{{AssetID: priorFrame}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	frames := compositionQualityFrames(p, segments)
	for _, frame := range frames {
		if _, _, err = f.s.Blobs.PutWithID(f.team, frame.AssetID, bytes.NewBufferString("pending-frame"), MaxUpload); err != nil {
			t.Fatal(err)
		}
		if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: frame.AssetID, Kind: "image", Internal: true}); err != nil {
			t.Fatal(err)
		}
	}
	next, err := f.s.Recompose(ctx, f.team, f.user, p.ID, p.Version, uuid.NewString(), e)
	if err != nil {
		t.Fatal(err)
	}
	if next.OutputAssetID != p.OutputAssetID || next.CompositionAssetID != "" || f.submits.Load() != 0 {
		t.Fatal("replacement changed prior delivery or entered paid generation")
	}
	retiredIDs := []string{pending}
	for _, frame := range frames {
		retiredIDs = append(retiredIDs, frame.AssetID)
	}
	for _, id := range retiredIDs {
		if _, err = os.Stat(f.s.Blobs.Path(f.team, id)); !os.IsNotExist(err) {
			t.Fatal("failed attempt blob leaked", id, err)
		}
	}
	for _, id := range []string{p.OutputAssetID, priorFrame} {
		if _, err = os.Stat(f.s.Blobs.Path(f.team, id)); err != nil {
			t.Fatal("prior delivery evidence deleted", id, err)
		}
	}
}

func TestStaleCompositionFinalizeCleansOnlyItsRetiredAttempt(t *testing.T) {
	requireFinishingComposition(t)
	f, p, e := editableProjectFixture(t)
	ctx := context.Background()
	p, err := f.s.Recompose(ctx, f.team, f.user, p.ID, p.Version, uuid.NewString(), e)
	if err != nil {
		t.Fatal(err)
	}
	oldID, _, err := f.s.Blobs.Put(f.team, bytes.NewBufferString("old-worker-late-output"), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.CompositionAssetID = oldID; return nil })
	if err != nil {
		t.Fatal(err)
	}
	old := *p
	newID, _, err := f.s.Blobs.Put(f.team, bytes.NewBufferString("current-pending"), MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Composition = &VideoCompositionState{RequestID: uuid.NewString(), SourceVersion: p.Version, Edit: e}
		v.CompositionAssetID = newID
		v.LeaseOwner = f.s.WorkerID
		v.LeaseUntil = time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.finishComposition(ctx, &old, oldID); !errors.Is(err, ErrConflict) {
		t.Fatal("stale output finalized", err)
	}
	if _, err = os.Stat(f.s.Blobs.Path(f.team, oldID)); !os.IsNotExist(err) {
		t.Fatal("stale output leaked", err)
	}
	for _, id := range []string{newID, p.OutputAssetID} {
		if _, err = os.Stat(f.s.Blobs.Path(f.team, id)); err != nil {
			t.Fatal("active or prior output removed", id, err)
		}
	}
}
