package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func narrationFixtureWAV(seconds float64) []byte {
	const rate = 16000
	count := int(seconds * rate)
	buf := new(bytes.Buffer)
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+count*2))
	buf.WriteString("WAVEfmt ")
	for _, field := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(buf, binary.LittleEndian, field)
	}
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(count*2))
	for i := 0; i < count; i++ {
		_ = binary.Write(buf, binary.LittleEndian, int16(math.Sin(float64(i)*2*math.Pi*440/rate)*6000))
	}
	return buf.Bytes()
}

func newNarrationFixture(t *testing.T, handler http.HandlerFunc) (*fixture, *Project) {
	t.Helper()
	if !CompositionAvailable() {
		t.Skip("ffmpeg unavailable")
	}
	f := setup(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	f.s.Narration = &NarrationConfig{BaseURL: server.URL + "/v1", Model: "fixture-speech", Voice: "fixture-voice", APIKey: "fixture-key", Client: server.Client(), AllowInsecure: true}
	p := f.project(t)
	p, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.Finishing = &FinishingOptions{Voiceover: true, Subtitles: "none"}
		v.Brief.Script = "先观察。 再行动。"
		v.Plan.Shots[0].Narration = "先观察。 "
		v.Plan.Shots[1].Narration = "再行动。"
		for i := range v.Shots {
			v.Plan.Shots[i].Subtitle = ""
			v.Shots[i].Plan = v.Plan.Shots[i]
		}
		v.LeaseOwner = f.s.WorkerID
		v.LeaseUntil = time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

func TestNarrationConfigurationAndFinishingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config *NarrationConfig
		want   bool
	}{
		{"missing", nil, false},
		{"incomplete", &NarrationConfig{BaseURL: "https://speech.example.test/v1", Model: "voice"}, false},
		{"https", &NarrationConfig{BaseURL: "https://speech.example.test/v1", Model: "model", Voice: "voice", APIKey: "key"}, true},
		{"http", &NarrationConfig{BaseURL: "http://speech.example.test", Model: "model", Voice: "voice", APIKey: "key"}, false},
		{"userinfo", &NarrationConfig{BaseURL: "https://user:pass@speech.example.test", Model: "model", Voice: "voice", APIKey: "key"}, false},
		{"query", &NarrationConfig{BaseURL: "https://speech.example.test?token=fixture", Model: "model", Voice: "voice", APIKey: "key"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.config.Ready(); got != tc.want {
				t.Fatalf("ready=%v", got)
			}
		})
	}
	for _, b := range []Brief{
		{Mode: "replicate", Finishing: &FinishingOptions{Subtitles: "none"}},
		{Mode: "auto", ReferenceAssetID: "source", Finishing: &FinishingOptions{Subtitles: "none"}},
		{Mode: "auto", SourceOnly: true, Finishing: &FinishingOptions{Subtitles: "none"}},
		{Mode: "auto", Finishing: &FinishingOptions{Subtitles: "unrecognized"}},
	} {
		if validateFinishingOptions(b) == nil {
			t.Fatalf("accepted %+v", b)
		}
	}
	if validateFinishingOptions(Brief{Mode: "auto", Finishing: &FinishingOptions{Subtitles: "none"}}) != nil || validateFinishingOptions(Brief{}) != nil {
		t.Fatal("valid or legacy finishing rejected")
	}
}

func TestFinishingPlanValidatesExactScriptPacingAndForbiddenClaimsWithoutCaptions(t *testing.T) {
	b := Brief{Mode: "auto", Duration: 10, Script: "先观察。 再行动。", Finishing: &FinishingOptions{Voiceover: true, Subtitles: "none"}}
	plan := Plan{Shots: []ShotPlan{{Duration: 5, Narration: "先观察。 "}, {Duration: 5, Narration: "再行动。"}}}
	if err := validateFinishingPlan(b, plan); err != nil {
		t.Fatal(err)
	}
	crossBoundary := b
	crossBoundary.Script = ""
	if err := validateFinishingPlan(crossBoundary, Plan{Shots: []ShotPlan{{Duration: 5, Narration: strings.Repeat("慢", 30)}, {Duration: 5, Narration: "留白"}}}); err != nil {
		t.Fatalf("a continuous voiceover may cross the visual cut: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Brief, *Plan)
		want   string
	}{
		{"missing", func(b *Brief, p *Plan) { b.Script = ""; p.Shots[0].Narration = ""; p.Shots[1].Narration = "" }, "narration_empty"},
		{"script-order", func(_ *Brief, p *Plan) {
			p.Shots[0].Narration, p.Shots[1].Narration = p.Shots[1].Narration, p.Shots[0].Narration
		}, "narration_script_mismatch"},
		{"script-whitespace", func(_ *Brief, p *Plan) { p.Shots[0].Narration = "先观察。" }, "narration_script_mismatch"},
		{"paced", func(b *Brief, p *Plan) { b.Script = ""; p.Shots[0].Narration = strings.Repeat("慢", 46) }, "narration_too_long"},
		{"forbidden-narration", func(b *Brief, _ *Plan) { b.AvoidClaims = "行动" }, "video_plan_copy_invalid"},
		{"forbidden-across-shots", func(b *Brief, _ *Plan) { b.AvoidClaims = "观察。 再" }, "video_plan_copy_invalid"},
		{"forbidden-image", func(b *Brief, p *Plan) { b.AvoidClaims = "magic"; p.Shots[0].ImagePrompt = "MAGIC product" }, "video_plan_copy_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brief := b
			copyPlan := Plan{Shots: append([]ShotPlan(nil), plan.Shots...)}
			tc.change(&brief, &copyPlan)
			err := validateFinishingPlan(brief, copyPlan)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%s", err, tc.want)
			}
		})
	}
}

func TestFrozenFinishingDesktopWireContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/finishing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Brief Brief `json:"brief"`
		Plan  Plan  `json:"plan"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	model := Model{ID: "fixture-video", Kind: "video", Resolutions: []string{"720p"}, Durations: []int{5}, PriceMicro: 100_000}
	if err = ValidateBrief(fixture.Brief, []Model{model}, []Asset{{ID: "fixture-product", Kind: "image"}}); err != nil {
		t.Fatal(err)
	}
	shots, quote, err := BuildShots(fixture.Brief, fixture.Plan, []Model{model})
	if err != nil || len(shots) != 2 || quote != 1_000_000 || narrationText(fixture.Brief, fixture.Plan.Shots) != fixture.Brief.Script {
		t.Fatalf("wire=%d/%d %v", len(shots), quote, err)
	}
	fixture.Brief.Finishing.Subtitles = "unknown"
	if ValidateBrief(fixture.Brief, []Model{model}, []Asset{{ID: "fixture-product", Kind: "image"}}) == nil {
		t.Fatal("invalid subtitle enum accepted")
	}
}

func TestNarrationPacingFailsBeforePaidVideoAndRetainsReadyAudio(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write(narrationFixtureWAV(12)) })
	_, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error { v.LeaseOwner = ""; v.LeaseUntil = time.Time{}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(context.Background())
	next := current(t, f, p)
	if next.Status != "failed" || next.Error != "narration_too_long" || next.Narration == nil || next.Narration.Status != "ready" || f.submits.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("pacing status=%s err=%s narration=%+v video=%d tts=%d", next.Status, next.Error, next.Narration, f.submits.Load(), calls.Load())
	}
	next, err = f.s.Action(context.Background(), f.team, p.ID, next.Version, "retry")
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(context.Background())
	if calls.Load() != 1 || f.submits.Load() != 0 {
		t.Fatalf("retry paid again tts=%d video=%d", calls.Load(), f.submits.Load())
	}
}

func TestNarrationDisabledNeverRequestsSpeech(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) })
	p.Brief.Finishing.Voiceover = false
	f.s.Narration = nil
	path, err := f.s.prepareNarration(context.Background(), p, t.TempDir())
	if err != nil || path != "" || calls.Load() != 0 {
		t.Fatalf("disabled speech path=%s err=%v calls=%d", path, err, calls.Load())
	}
}

func TestNarrationRejectsSilentOrInvalidSuccessWithoutResubmitting(t *testing.T) {
	silent := narrationFixtureWAV(1)
	for i := 44; i < len(silent); i++ {
		silent[i] = 0
	}
	for _, tc := range []struct {
		name string
		body []byte
	}{{"silent", silent}, {"invalid", []byte(`{"error":"private response"}`)}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write(tc.body) })
			_, err := f.s.prepareNarration(context.Background(), p, t.TempDir())
			if err == nil || err.Error() != "narration_audio_invalid" {
				t.Fatalf("invalid success=%v", err)
			}
			_, err = f.s.prepareNarration(context.Background(), p, t.TempDir())
			if err == nil || err.Error() != "narration_submission_unknown" || calls.Load() != 1 {
				t.Fatalf("invalid result resubmitted=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestNarrationUnknownCannotBeBypassedByChangingScriptOrVoice(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(502) })
	_, _ = f.s.prepareNarration(context.Background(), p, t.TempDir())
	stored := current(t, f, p)
	stored, err := f.s.Mutate(context.Background(), f.team, p.ID, stored.Version, func(v *Project) error {
		v.Plan.Shots[0].Narration = "再观察。 "
		v.Brief.Script = "再观察。 再行动。"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Narration.Voice = "another-voice"
	_, err = f.s.prepareNarration(context.Background(), stored, t.TempDir())
	if err == nil || err.Error() != "narration_submission_unknown" || calls.Load() != 1 {
		t.Fatalf("unknown bypassed err=%v calls=%d", err, calls.Load())
	}
}

func TestNarrationReconcileImportsConfirmedAudioWithoutAnotherSpeechRequest(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(502) })
	_, _ = f.s.prepareNarration(context.Background(), p, t.TempDir())
	stored := current(t, f, p)
	_, err := f.s.Reconcile(context.Background(), f.team, p.ID, Reconciliation{Version: stored.Version, Shot: -1, Outcome: "narration_not_accepted", Reason: "已核实供应商未受理"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("active lease reconciled: %v", err)
	}
	stored, err = f.s.Mutate(context.Background(), f.team, p.ID, stored.Version, func(v *Project) error { v.Status = "failed"; v.LeaseOwner = ""; v.LeaseUntil = time.Time{}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	result := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(narrationFixtureWAV(2)) }))
	defer result.Close()
	next, err := f.s.Reconcile(context.Background(), f.team, p.ID, Reconciliation{Version: stored.Version, Shot: -1, Outcome: "narration_result", Value: result.URL + "/confirmed.wav", Reason: "已核实原请求完整脚本音频"})
	if err != nil || next.Status != "queued" || next.Narration.Status != "ready" || next.Narration.AssetID == "" || calls.Load() != 1 {
		t.Fatalf("reconcile=%+v err=%v calls=%d", next, err, calls.Load())
	}
	if next.Narration.Text != p.Brief.Script || next.LeaseOwner != "" {
		t.Fatal("reconciliation lost script or lease")
	}
	f.s.Narration.Voice = "changed-after-reconciliation"
	next, err = f.s.Mutate(context.Background(), f.team, p.ID, next.Version, func(v *Project) error {
		v.LeaseOwner = f.s.WorkerID
		v.LeaseUntil = time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.prepareNarration(context.Background(), next, t.TempDir()); err != nil || calls.Load() != 1 {
		t.Fatalf("confirmed result was regenerated after config change: %v calls=%d", err, calls.Load())
	}
}

func TestNarrationReconcileNotAcceptedRequiresExplicitRetryAndKeepsCompletedShots(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(502) })
	_, _ = f.s.prepareNarration(context.Background(), p, t.TempDir())
	stored := current(t, f, p)
	stored, err := f.s.Mutate(context.Background(), f.team, p.ID, stored.Version, func(v *Project) error {
		v.Status = "failed"
		v.LeaseOwner = ""
		v.LeaseUntil = time.Time{}
		v.Shots[0].Status = "succeeded"
		v.Shots[0].OutputAssetID = "existing-video"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.s.Reconcile(context.Background(), f.team, p.ID, Reconciliation{Version: stored.Version, Shot: -1, Outcome: "narration_not_accepted", Reason: "已核实配音供应商未受理"})
	if err != nil || next.Status != "failed" || next.Narration.Status != "failed" || calls.Load() != 1 {
		t.Fatalf("not accepted: %+v err=%v", next, err)
	}
	if next.Shots[0].Status != "succeeded" || next.Shots[0].OutputAssetID != "existing-video" {
		t.Fatal("reconcile reset a successful paid video")
	}
	next, err = f.s.Action(context.Background(), f.team, p.ID, next.Version, "retry")
	if err != nil || next.Status != "queued" || calls.Load() != 1 {
		t.Fatalf("explicit retry=%v calls=%d", err, calls.Load())
	}
}

func TestNarrationReconcileRejectsWrongStageAndInvalidAudioAndPreservesCancellation(t *testing.T) {
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) })
	_, _ = f.s.prepareNarration(context.Background(), p, t.TempDir())
	stored := current(t, f, p)
	stored, err := f.s.Mutate(context.Background(), f.team, p.ID, stored.Version, func(v *Project) error {
		v.Status = "failed"
		v.LeaseOwner = ""
		v.LeaseUntil = time.Time{}
		v.CancelRequested = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Reconcile(context.Background(), f.team, p.ID, Reconciliation{Version: stored.Version, Shot: 0, Outcome: "narration_not_accepted", Reason: "已核实供应商未受理"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong stage: %v", err)
	}
	result := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not audio")) }))
	defer result.Close()
	_, err = f.s.Reconcile(context.Background(), f.team, p.ID, Reconciliation{Version: stored.Version, Shot: -1, Outcome: "narration_result", Value: result.URL, Reason: "已核实原请求音频"})
	if err == nil || err.Error() != "narration_audio_invalid" {
		t.Fatalf("invalid audio=%v", err)
	}
	stored = current(t, f, p)
	if stored.Narration.Status != "unknown" || stored.LeaseOwner != "" {
		t.Fatal("failed result import lost unknown fact or lease cleanup")
	}
	next, err := f.s.Reconcile(context.Background(), f.team, p.ID, Reconciliation{Version: stored.Version, Shot: -1, Outcome: "narration_not_accepted", Reason: "已核实供应商未受理"})
	if err != nil || next.Status != "cancelled" {
		t.Fatalf("cancelled recovery=%+v %v", next, err)
	}
}

func TestFinishingConfigurationRemovalStopsBeforeNewPaidSubmission(t *testing.T) {
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("preflight must not request speech")
		w.WriteHeader(500)
	})
	f.s.Narration = nil
	_, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error { v.LeaseOwner = ""; v.LeaseUntil = time.Time{}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(context.Background())
	next := current(t, f, p)
	if next.Status != "failed" || next.Error != "narration_not_configured" || f.submits.Load() != 0 {
		t.Fatalf("state=%s error=%s submits=%d", next.Status, next.Error, f.submits.Load())
	}
}

func TestPrepareNarrationUsesOneFullScriptAndReusesPersistedAsset(t *testing.T) {
	var calls atomic.Int64
	var identity atomic.Value
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("unexpected speech request %s", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["input"] != "先观察。 再行动。" || body["model"] != "fixture-speech" || body["voice"] != "fixture-voice" || body["response_format"] != "wav" {
			t.Errorf("incomplete script/settings: %#v", body)
		}
		identity.Store(r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(narrationFixtureWAV(2))
	})
	path, err := f.s.prepareNarration(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = uuid.Parse(identity.Load().(string)); err != nil {
		t.Fatal("missing stable speech identity")
	}
	stored := current(t, f, p)
	if stored.Narration == nil || stored.Narration.Status != "ready" || stored.Narration.Text != p.Brief.Script || math.Abs(stored.Narration.DurationSeconds-2) > .01 {
		t.Fatalf("state=%+v", stored.Narration)
	}
	if filepath.Dir(path) != filepath.Join(f.s.Blobs.Root, f.team) {
		t.Fatalf("audio escaped same-team storage: %s", path)
	}
	for range 3 {
		again, e := f.s.prepareNarration(context.Background(), p, t.TempDir())
		if e != nil || again != path {
			t.Fatalf("retry=%s err=%v", again, e)
		}
	}
	f.s.Narration.Voice, f.s.Narration.Model, f.s.Narration.BaseURL = "changed-voice", "changed-model", "https://another-speech.example.test/v1"
	if reused, e := f.s.prepareNarration(context.Background(), p, t.TempDir()); e != nil || reused != path {
		t.Fatalf("config change discarded frozen audio: %s %v", reused, e)
	}
	if calls.Load() != 1 || f.submits.Load() != 0 {
		t.Fatalf("tts=%d video=%d", calls.Load(), f.submits.Load())
	}
	assets, _ := f.s.Repo.Assets(context.Background(), f.team)
	found := false
	for _, asset := range assets {
		if asset.ID == stored.Narration.AssetID {
			found = asset.Internal && asset.Kind == "audio" && asset.DurationSeconds > 0
		}
	}
	if !found {
		t.Fatal("narration not persisted as an internal audio asset")
	}
}

func TestFinishingReviewAllowsCopyRepairsButGenerateAndRetryStayStrict(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write(narrationFixtureWAV(1)) })
	p, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "review"
		v.Brief.ReviewPlan = true
		v.Brief.Script = ""
		v.LeaseOwner = ""
		v.LeaseUntil = time.Time{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	draft := *p.Plan
	draft.Shots = append([]ShotPlan(nil), p.Plan.Shots...)
	for i := range draft.Shots {
		draft.Shots[i].Narration = ""
		draft.Shots[i].Subtitle = "关闭字幕时忽略这条已有文字"
	}
	p, err = f.s.Revise(context.Background(), f.team, p.ID, p.Version, draft)
	if err != nil || p.Status != "review" {
		t.Fatalf("review blocked editable copy: %v", err)
	}
	if _, err = f.s.Action(context.Background(), f.team, p.ID, p.Version, "generate"); err == nil || err.Error() != "narration_empty" {
		t.Fatalf("invalid review generated: %v", err)
	}
	draft.Shots[0].Narration, draft.Shots[1].Narration = "先观察。 ", "再行动。"
	p, err = f.s.Revise(context.Background(), f.team, p.ID, p.Version, draft)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Action(context.Background(), f.team, p.ID, p.Version, "generate")
	if err != nil || p.Status != "queued" || calls.Load() != 0 {
		t.Fatalf("corrected review=%v calls=%d", err, calls.Load())
	}
	auto := p.Brief
	auto.ReviewPlan = false
	draft.Shots[0].Narration, draft.Shots[1].Narration = "", ""
	if _, _, err = BuildShots(auto, draft, p.Models); err == nil || err.Error() != "narration_empty" {
		t.Fatalf("automatic output accepted missing narration: %v", err)
	}
	p, err = f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error { v.Status = "failed"; v.Plan = &draft; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Action(context.Background(), f.team, p.ID, p.Version, "retry"); err == nil || err.Error() != "narration_empty" {
		t.Fatalf("retry accepted invalid narration: %v", err)
	}
	for i := range draft.Shots {
		draft.Shots[i].Duration = 30
	}
	if _, _, err = BuildShots(p.Brief, draft, p.Models); err == nil {
		t.Fatal("review bypassed structural duration checks")
	}
}

func TestUnknownNarrationNeverResubmitsOnPrepareOrExplicitRetry(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(502)
		_, _ = w.Write([]byte("private provider details"))
	})
	for range 3 {
		_, err := f.s.prepareNarration(context.Background(), p, t.TempDir())
		if err == nil || err.Error() != "narration_submission_unknown" {
			t.Fatalf("unknown=%v", err)
		}
	}
	stored := current(t, f, p)
	if stored.Narration == nil || stored.Narration.Status != "unknown" || calls.Load() != 1 {
		t.Fatalf("state=%+v calls=%d", stored.Narration, calls.Load())
	}
	stored, err := f.s.Mutate(context.Background(), f.team, p.ID, stored.Version, func(v *Project) error { v.Status = "failed"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Action(context.Background(), f.team, p.ID, stored.Version, "retry")
	if err == nil || err.Error() != "narration_submission_unknown" || calls.Load() != 1 {
		t.Fatalf("retry=%v calls=%d", err, calls.Load())
	}
}

func TestNarrationRedirectDoesNotForwardCredentials(t *testing.T) {
	var destinationCalls atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	_, err := f.s.prepareNarration(context.Background(), p, t.TempDir())
	if err == nil || destinationCalls.Load() != 0 {
		t.Fatalf("redirect leaked request err=%v calls=%d", err, destinationCalls.Load())
	}
}

func TestNarrationCancellationCannotBindLateAudio(t *testing.T) {
	var f *fixture
	var p *Project
	f, p = newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		stored := current(t, f, p)
		_, err := f.s.Action(context.Background(), f.team, p.ID, stored.Version, "cancel")
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(narrationFixtureWAV(1))
	})
	_, err := f.s.prepareNarration(context.Background(), p, t.TempDir())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("late cancellation=%v", err)
	}
	stored := current(t, f, p)
	if !stored.CancelRequested || stored.Narration.Status == "ready" || stored.OutputAssetID != "" {
		t.Fatalf("cancel overwritten: %+v", stored)
	}
}

func TestNarrationCancellationWinsLateProviderFailureBeforeVideo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		state  string
		error  string
	}{
		{"rejected", http.StatusBadRequest, "failed", "narration_generation_failed"},
		{"unknown", http.StatusBadGateway, "unknown", "narration_submission_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f *fixture
			var p *Project
			var calls atomic.Int64
			f, p = newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				stored := current(t, f, p)
				if _, err := f.s.Action(context.Background(), f.team, p.ID, stored.Version, "cancel"); err != nil {
					t.Error(err)
				}
				w.WriteHeader(tc.status)
			})
			var err error
			p, err = f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
				v.LeaseOwner = ""
				v.LeaseUntil = time.Time{}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			f.s.Tick(context.Background())
			stored := current(t, f, p)
			if !stored.CancelRequested || stored.Status != "cancelled" || stored.Error != tc.error || stored.Narration == nil || stored.Narration.Status != tc.state || stored.Narration.RequestID == "" || stored.OutputAssetID != "" || f.submits.Load() != 0 || calls.Load() != 1 {
				t.Fatalf("late failure status=%s cancel=%v err=%s narration=%+v video=%d tts=%d", stored.Status, stored.CancelRequested, stored.Error, stored.Narration, f.submits.Load(), calls.Load())
			}
			f.s.Tick(context.Background())
			if stored = current(t, f, p); stored.Status != "cancelled" || f.submits.Load() != 0 || calls.Load() != 1 {
				t.Fatalf("cancelled work resumed status=%s video=%d tts=%d", stored.Status, f.submits.Load(), calls.Load())
			}
		})
	}
}

func TestChangedNarrationScriptDoesNotReuseOldAudio(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write(narrationFixtureWAV(1)) })
	first, err := f.s.prepareNarration(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stored := current(t, f, p)
	stored, err = f.s.Mutate(context.Background(), f.team, p.ID, stored.Version, func(v *Project) error {
		v.Plan.Shots[0].Narration = "再观察。 "
		v.Brief.Script = "再观察。 再行动。"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.prepareNarration(context.Background(), stored, t.TempDir())
	if err != nil || first == second || calls.Load() != 2 {
		t.Fatalf("new script reused stale audio err=%v calls=%d", err, calls.Load())
	}
}

func TestFinishingConfigurationFailsBeforeVideoAndMusicRequiresRealAudio(t *testing.T) {
	if !CompositionAvailable() {
		t.Skip("ffmpeg unavailable")
	}
	f := setup(t)
	p := f.project(t)
	b := p.Brief
	b.Finishing = &FinishingOptions{Voiceover: true, Subtitles: "none"}
	_, err := f.s.Create(context.Background(), f.team, f.user, "", uuid.NewString(), b)
	if err == nil || err.Error() != "narration_not_configured" || f.submits.Load() != 0 {
		t.Fatalf("preflight=%v submits=%d", err, f.submits.Load())
	}
	b.Finishing = &FinishingOptions{Music: true, Subtitles: "none"}
	file := filepath.Join(t.TempDir(), "music.wav")
	f.s.BackgroundMusicPath = file
	if err = os.WriteFile(file, []byte("not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if f.s.validateFinishing(context.Background(), b) == nil {
		t.Fatal("invalid music accepted")
	}
	if err = os.WriteFile(file, narrationFixtureWAV(.5), 0600); err != nil {
		t.Fatal(err)
	}
	if f.s.validateFinishing(context.Background(), b) == nil {
		t.Fatal("too-short music accepted")
	}
	if err = os.WriteFile(file, narrationFixtureWAV(2), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.s.validateFinishing(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}
