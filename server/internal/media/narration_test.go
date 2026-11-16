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
		v.Narration = &NarrationState{Status: "unknown", RequestID: uuid.NewString(), Text: v.Brief.Script}
		v.LeaseOwner = f.s.WorkerID
		v.LeaseUntil = time.Now().Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, p
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
	model := Model{ID: "fixture-video", Kind: "video", Resolutions: []string{"720p"}, MinDuration: 4, MaxDuration: 15, PriceMicro: 100_000}
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

func TestNarrationReconcileImportsConfirmedAudioWithoutAnotherSpeechRequest(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(502) })
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
	if err != nil || next.Status != "queued" || next.Narration.Status != "ready" || next.Narration.AssetID == "" || calls.Load() != 0 {
		t.Fatalf("reconcile=%+v err=%v calls=%d", next, err, calls.Load())
	}
	if next.Narration.Text != p.Brief.Script || next.LeaseOwner != "" {
		t.Fatal("reconciliation lost script or lease")
	}

}

func TestNarrationReconcileNotAcceptedRequiresExplicitRetryAndKeepsCompletedShots(t *testing.T) {
	var calls atomic.Int64
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(502) })
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
	if err != nil || next.Status != "failed" || next.Narration.Status != "failed" || calls.Load() != 0 {
		t.Fatalf("not accepted: %+v err=%v", next, err)
	}
	if next.Shots[0].Status != "succeeded" || next.Shots[0].OutputAssetID != "existing-video" {
		t.Fatal("reconcile reset a successful paid video")
	}
	next, err = f.s.Action(context.Background(), f.team, p.ID, next.Version, "retry")
	if err != nil || next.Status != "queued" || calls.Load() != 0 {
		t.Fatalf("explicit retry=%v calls=%d", err, calls.Load())
	}
}

func TestNarrationReconcileRejectsWrongStageAndInvalidAudioAndPreservesCancellation(t *testing.T) {
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) })
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

func TestLegacyUnknownNarrationStillRequiresReconciliation(t *testing.T) {
	f, p := newNarrationFixture(t, func(w http.ResponseWriter, _ *http.Request) { t.Fatal("speech must never be submitted") })
	p, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "failed"
		v.LeaseOwner = ""
		v.LeaseUntil = time.Time{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Action(context.Background(), f.team, p.ID, p.Version, "retry"); err == nil || err.Error() != "narration_submission_unknown" {
		t.Fatalf("unknown accepted request bypassed: %v", err)
	}
}
func TestNativeFinishingIgnoresTTSBudgetsAndPreservesForbiddenClaims(t *testing.T) {
	b := Brief{Mode: "auto", Script: "模型原生台词", Finishing: &FinishingOptions{Voiceover: true, Music: true, Subtitles: "none"}}
	plan := Plan{Shots: []ShotPlan{{Duration: 1, Prompt: "人物说模型原生台词"}}}
	if err := validateFinishingPlan(b, plan); err != nil {
		t.Fatal(err)
	}
	b.AvoidClaims = "原生台词"
	if err := validateFinishingPlan(b, plan); err == nil || err.Error() != "video_plan_copy_invalid" {
		t.Fatalf("forbidden claims bypassed: %v", err)
	}
}
