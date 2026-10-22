package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	s                      *Service
	team, user             string
	submits                atomic.Int64
	server                 *httptest.Server
	unknown                bool
	video                  []byte
	failQueries            atomic.Bool
	imageDownloads         atomic.Int64
	videoDownloads         atomic.Int64
	queries                atomic.Int64
	failFirstVideoDownload bool
	requestMu              sync.Mutex
	requestBodies          []map[string]any
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, e := store.NewMemoryStore(ctx, store.MemoryOptions{})
	if e != nil {
		t.Fatal(e)
	}
	user, e := st.CreateUser(ctx, store.UserInput{Email: "media@example.test", PasswordHash: "x", DisplayName: "test", Role: store.RoleUser})
	if e != nil {
		t.Fatal(e)
	}
	team, e := st.CreateTeam(ctx, store.CreateTeamInput{Name: "media", QuotaCredits: "100.000000"})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.AddMember(ctx, team.ID, user.ID, store.TeamMemberRole("owner")); e != nil {
		t.Fatal(e)
	}
	f := &fixture{team: team.ID, user: user.ID}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			f.requestMu.Lock()
			f.requestBodies = append(f.requestBodies, body)
			f.requestMu.Unlock()
			f.submits.Add(1)
			if f.unknown {
				w.WriteHeader(502)
				return
			}
			if r.URL.Path == "/images/generations" {
				json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": f.server.URL + "/result.png"}}})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"id": "task-fixture"})
			return
		}
		if r.URL.Path == "/result.png" {
			if f.imageDownloads.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX1kAAAAASUVORK5CYII=")
			w.Write(raw)
			return
		}
		if r.URL.Path == "/result.mp4" {
			if f.videoDownloads.Add(1) == 1 && f.failFirstVideoDownload {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(f.video)
			return
		}
		status := "succeeded"
		f.queries.Add(1)
		if f.failQueries.Load() {
			status = "failed"
		}
		json.NewEncoder(w).Encode(map[string]any{"status": status, "content": map[string]string{"video_url": f.server.URL + "/result.mp4"}})
	}))
	t.Cleanup(f.server.Close)
	f.s = &Service{Repo: Repository{Store: st}, WorkerID: uuid.NewString(), Blobs: Blobs{Root: t.TempDir(), Origin: "https://media.example.test", SigningKey: "test", Client: f.server.Client()}}
	f.s.Provider = func(context.Context, Model) (Provider, error) {
		return Provider{BaseURL: f.server.URL, Key: "fixture", Client: f.server.Client()}, nil
	}
	models := []Model{{ID: "v", Name: "fixture", Kind: "video", Adapter: "ark", ProviderID: uuid.NewString(), UpstreamModel: "fixture", Resolutions: []string{"720p"}, Durations: []int{5}, PriceMicro: 100_000, PriceVersion: "v1"}}
	raw, _ := json.Marshal(models)
	if e = st.SetConfigValue(ctx, ConfigKey, raw); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: "a", Name: "product", Kind: "image"}); e != nil {
		t.Fatal(e)
	}
	return f
}

func TestScriptOnlyFirstFrameIsGeneratedBeforeVideoAndSettledOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	p := f.project(t)
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Models = append(v.Models, Model{ID: "i", Kind: "image", PriceMicro: 200_000, ProviderID: "fixture", UpstreamModel: "image-model", Adapter: "ark", PriceVersion: "v1"})
		v.Brief.ImageModel = "i"
		v.Shots[0].Plan.AssetID = ""
		v.Shots[0].Plan.ImagePrompt = "从脚本生成舞蹈人物首帧"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 7 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Shots[0].Status != "succeeded" || p.ChargedMicro != 700_000 || f.submits.Load() != 2 || f.imageDownloads.Load() != 2 {
		t.Fatalf("unexpected script-only lifecycle: %+v submits=%d", p, f.submits.Load())
	}
	f.requestMu.Lock()
	defer f.requestMu.Unlock()
	if _, exists := f.requestBodies[0]["image"]; exists {
		t.Fatalf("script-only image sent reference: %#v", f.requestBodies[0])
	}
	content := f.requestBodies[1]["content"].([]any)
	frame := content[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if frame != f.s.Blobs.URL(f.team, p.Shots[0].ImageAssetID) {
		t.Fatalf("video did not use generated first frame: %v", frame)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.UsedCredits != "0.700000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("double or missing settlement: %+v", team)
	}
}

func TestRestoredShotWithoutFirstFrameFailsBeforeAdmission(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	_, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Shots[0].Plan.AssetID = ""; return nil })
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "failed" || p.Shots[0].Error != "first_frame_required" || p.Shots[0].ReservationID != "" || f.submits.Load() != 0 {
		t.Fatalf("invalid restored plan admitted: %+v submits=%d", p, f.submits.Load())
	}
}
func (f *fixture) project(t *testing.T) *Project {
	t.Helper()
	ctx := context.Background()
	p, e := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), Brief{Mode: "auto", Product: "fixture product", AssetIDs: []string{"a"}, Platforms: []string{"douyin"}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 10, MaxCreditsMicro: 10_000_000})
	if e != nil {
		t.Fatal(e)
	}
	a := Analysis{TaskID: "analysis", InputID: "input", WorkspacePath: "/fixture"}
	p, e = f.s.Bind(ctx, f.team, p.ID, p.Version, a)
	if e != nil {
		t.Fatal(e)
	}
	shot := ShotPlan{SourceURL: "https://example.com/ref", SourceEnd: 5, Duration: 5, Framing: "close", Camera: "push", Action: "show", Purpose: "sell", Prompt: "product", AssetID: "a", Subtitle: "商品"}
	plan := Plan{References: []Reference{{URL: shot.SourceURL, Title: "fixture", CapturedAt: time.Now(), Evidence: "observed", SelectionReason: "relevant"}}, Shots: []ShotPlan{shot, shot}}
	p, e = f.s.SetPlan(ctx, f.team, p.ID, p.Version, a.InputID, plan)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func current(t *testing.T, f *fixture, p *Project) *Project {
	t.Helper()
	next, e := f.s.Repo.Get(context.Background(), f.team, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	return next
}
func TestCreateIsolationStalePlanAndIdempotency(t *testing.T) {
	f := setup(t)
	p := f.project(t)
	again, e := f.s.Create(context.Background(), f.team, f.user, "", p.ID, p.Brief)
	if e != nil || again.ID != p.ID {
		t.Fatalf("idempotency: %v", e)
	}
	other, e := f.s.Repo.Get(context.Background(), uuid.NewString(), p.ID)
	if e != nil || other != nil {
		t.Fatal("cross-team read")
	}
	if _, e = f.s.SetPlan(context.Background(), f.team, p.ID, 1, "input", *p.Plan); e != ErrConflict {
		t.Fatalf("stale plan: %v", e)
	}
}
func TestUnknownSubmissionNeverResubmitsOrChangesBalance(t *testing.T) {
	f := setup(t)
	f.unknown = true
	p := f.project(t)
	for range 5 {
		f.s.Tick(context.Background())
	}
	p = current(t, f, p)
	if p.Status != "reconciliation_required" || f.submits.Load() != 1 {
		t.Fatalf("%s submits=%d", p.Status, f.submits.Load())
	}
	if _, e := f.s.Repo.Store.ExpireStaleReservations(context.Background(), time.Now().Add(72*time.Hour)); e != nil {
		t.Fatal(e)
	}
	team, _ := f.s.Repo.Store.GetTeam(context.Background(), f.team)
	if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000000" || p.Shots[0].PendingChargeMicro != 500000 || p.Shots[0].ReservationID == "" {
		t.Fatalf("unknown task changed balance or lost recovery state: %+v %+v", team, p.Shots[0])
	}
}
func TestInsufficientCreditsDoesNotSubmit(t *testing.T) {
	f := setup(t)
	p := f.project(t)
	zero := "0.000000"
	if _, e := f.s.Repo.Store.UpdateTeam(context.Background(), f.team, store.UpdateTeamPatch{QuotaCredits: &zero}); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		f.s.Tick(context.Background())
	}
	p = current(t, f, p)
	if p.Status != "failed" || f.submits.Load() != 0 {
		t.Fatalf("status=%s submits=%d", p.Status, f.submits.Load())
	}
}
func TestMultiClipGenerationCompositionAndExactlyOnceCharge(t *testing.T) {
	if !CompositionAvailable() {
		t.Skip("ffmpeg unavailable")
	}
	f := setup(t)
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "fixture.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=160x90:r=30", "-t", "5", "-c:v", "libx264", "-pix_fmt", "yuv420p", videoPath)
	if e := cmd.Run(); e != nil {
		t.Fatal(e)
	}
	f.video, _ = os.ReadFile(videoPath)
	p := f.project(t)
	for range 12 {
		f.s.Tick(context.Background())
		p = current(t, f, p)
		if p.Status == "completed" {
			break
		}
	}
	if p.Status != "completed" {
		t.Fatalf("status=%s error=%s shots=%+v", p.Status, p.Error, p.Shots)
	}
	if f.submits.Load() != 2 || p.ChargedMicro != 1_000_000 {
		t.Fatalf("submits=%d charged=%d", f.submits.Load(), p.ChargedMicro)
	}
	for range 3 {
		f.s.Tick(context.Background())
	}
	team, _ := f.s.Repo.Store.GetTeam(context.Background(), f.team)
	if team.UsedCredits != "1.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("financial state %+v", team)
	}
	out, e := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", f.s.Blobs.Path(f.team, p.OutputAssetID)).Output()
	if e != nil {
		t.Fatal(e)
	}
	var duration float64
	fmt.Sscanf(string(out), "%f", &duration)
	if duration < 9.9 || duration > 10.2 {
		t.Fatalf("duration=%v", duration)
	}
}

func TestReplicaFractionalCompositionPreservesClockAndAppliesFade(t *testing.T) {
	if !CompositionAvailable() {
		t.Skip("ffmpeg unavailable")
	}
	for _, transition := range []string{"cut", "fade"} {
		t.Run(transition, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			videoPath := filepath.Join(t.TempDir(), "fixture.mp4")
			cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=160x90:r=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "5", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", videoPath)
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			f.video, _ = os.ReadFile(videoPath)
			p := f.project(t)
			p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Brief.ReferenceAssetID = "source"
				v.Brief.ReferenceDurationSeconds = 2.251
				v.Brief.Resolution = "480p"
				v.Shots[0].Plan.SourceStart, v.Shots[0].Plan.SourceEnd = 0, 1.117
				v.Shots[1].Plan.SourceStart, v.Shots[1].Plan.SourceEnd = 1.117, 2.251
				v.Shots[1].Plan.Transition = transition
				for i := range v.Shots {
					v.Shots[i].Plan.Subtitle = ""
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for range 12 {
				f.s.Tick(ctx)
				p = current(t, f, p)
				if p.Status == "completed" {
					break
				}
			}
			if p.Status != "completed" || p.ChargedMicro != 1_000_000 || f.submits.Load() != 2 {
				t.Fatalf("source trim changed supplier billing or failed: %+v submits=%d", p, f.submits.Load())
			}
			output := f.s.Blobs.Path(f.team, p.OutputAssetID)
			raw, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", output).Output()
			if err != nil {
				t.Fatal(err)
			}
			var duration float64
			if _, err = fmt.Sscanf(string(raw), "%f", &duration); err != nil || math.Abs(duration-2.251) > .04 {
				t.Fatalf("duration=%f source=2.251 err=%v", duration, err)
			}
			blue := func(at string) byte {
				t.Helper()
				frame, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-ss", at, "-i", output, "-vf", "scale=1:1", "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1").Output()
				if err != nil || len(frame) != 3 {
					t.Fatalf("sample=%v err=%v", frame, err)
				}
				return frame[2]
			}
			if got := blue("0.5"); got < 180 {
				t.Fatalf("normal frame unexpectedly faded: blue=%d", got)
			}
			if got := blue("1.12"); (transition == "fade" && got > 100) || (transition == "cut" && got < 180) {
				t.Fatalf("boundary %s: blue=%d", transition, got)
			}
			if got := blue("1.4"); got < 180 {
				t.Fatalf("fade did not restore image: blue=%d", got)
			}
		})
	}
}

func TestCancelBeforeSubmitClosesLegacyReservationIntent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	id := uuid.NewString()
	if result, e := f.s.Repo.Store.ReserveQuota(ctx, store.ReserveQuotaInput{ID: id, TeamID: f.team, UserID: f.user, RequestID: "media:" + id, Micro: 500_000, HeldByTask: true, TTL: time.Hour}); e != nil || result != store.ReserveReserved {
		t.Fatal(e, result)
	}
	p, e := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Shots[0].ReservationID = id
		v.Shots[0].ReservedMicro = 500_000
		v.Shots[0].Stage = "video"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel"); e != nil {
		t.Fatal(e)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "cancelled" || team.ReservedCredits != "0.000000" || f.submits.Load() != 0 {
		t.Fatalf("%s %+v", p.Status, team)
	}
}
func TestRootReconciliationClosesOnlyConfirmedUnacceptedSubmit(t *testing.T) {
	f := setup(t)
	f.unknown = true
	ctx := context.Background()
	p := f.project(t)
	f.s.Tick(ctx)
	p = current(t, f, p)
	if _, e := f.s.Reconcile(ctx, f.team, p.ID, Reconciliation{Version: p.Version - 1, Shot: 0, Outcome: "not_accepted", Reason: "已在供应商核实未受理"}); e != ErrConflict {
		t.Fatal(e)
	}
	p, e := f.s.Reconcile(ctx, f.team, p.ID, Reconciliation{Version: p.Version, Shot: 0, Outcome: "not_accepted", Reason: "已在供应商核实未受理"})
	if e != nil {
		t.Fatal(e)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "failed" || team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000000" {
		t.Fatalf("%s %+v", p.Status, team)
	}
}
func TestCancelAfterAcceptedResultSettlesAndStopsNextShot(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	p := f.project(t)
	f.s.Tick(ctx)
	p = current(t, f, p)
	if _, e := f.s.Action(ctx, f.team, p.ID, p.Version, "cancel"); e != nil {
		t.Fatal(e)
	}
	for range 4 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "cancelled" || f.submits.Load() != 1 || team.UsedCredits != "0.500000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("%s submits=%d %+v", p.Status, f.submits.Load(), team)
	}
}
func TestTeamModelPolicyRejectsDirectSubmission(t *testing.T) {
	f := setup(t)
	p := f.project(t)
	ctx := context.Background()
	_, e := f.s.Repo.Store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.team, AllowedModelIDs: []string{"other"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), p.Brief); e == nil {
		t.Fatal("policy bypass")
	}
}

func TestFrozenDesktopWireContract(t *testing.T) {
	raw, e := os.ReadFile("testdata/wire.json")
	if e != nil {
		t.Fatal(e)
	}
	var wire struct {
		Brief  Brief   `json:"brief"`
		Plan   Plan    `json:"plan"`
		Models []Model `json:"models"`
		Quote  int64   `json:"quoteMicro"`
	}
	if e = json.Unmarshal(raw, &wire); e != nil {
		t.Fatal(e)
	}
	if e = ValidateBrief(wire.Brief, wire.Models, []Asset{{ID: "asset-fixture", Kind: "image"}, {ID: "source-fixture", Kind: "video", DurationSeconds: 10}}); e != nil {
		t.Fatal(e)
	}
	_, quote, e := BuildShots(wire.Brief, wire.Plan, wire.Models)
	if e != nil || quote != wire.Quote {
		t.Fatal(e, quote)
	}
}

func TestPartialRetryReusesSuccessfulShotAndOnlyChargesNewSuccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	p := f.project(t)
	for range 4 {
		f.s.Tick(ctx)
	}
	f.failQueries.Store(true)
	for range 2 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "failed" || p.Shots[0].Status != "succeeded" || p.ChargedMicro != 500_000 {
		t.Fatalf("before retry %+v", p)
	}
	f.failQueries.Store(false)
	if _, e := f.s.Action(ctx, f.team, p.ID, p.Version, "retry"); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if f.submits.Load() != 3 || p.ChargedMicro != 1_000_000 || p.Shots[0].Attempt != 1 || p.Shots[1].Attempt != 2 {
		t.Fatalf("retry submits=%d %+v", f.submits.Load(), p)
	}
}
func TestImageDownloadRetryDoesNotRegenerateOrLoseIdentity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	image := Model{ID: "i", Name: "image", Kind: "image", PriceMicro: 200_000, ProviderID: "fixture", UpstreamModel: "fixture", Adapter: "ark", PriceVersion: "v1"}
	p, e := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(p *Project) error {
		p.Models = append(p.Models, image)
		p.Brief.ImageModel = "i"
		p.Shots[0].Plan.ImagePrompt = "image"
		p.Shots[0].QuoteMicro += 200_000
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	for range 4 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if f.submits.Load() != 1 || f.imageDownloads.Load() != 2 || p.Shots[0].Status != "image_ready" || p.ChargedMicro != 200_000 || p.Shots[0].ImageAssetID == "" {
		t.Fatalf("image submits=%d downloads=%d %+v", f.submits.Load(), f.imageDownloads.Load(), p)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.200000" {
		t.Fatalf("%+v", team)
	}
}

func TestVideoDownloadRetryUsesPersistedURLWithoutProviderQuery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	f.failFirstVideoDownload = true
	p := f.project(t)
	for range 2 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Shots[0].Status != "video_ready" || p.Shots[0].ResultURL == "" || f.queries.Load() != 1 || f.videoDownloads.Load() != 1 {
		t.Fatalf("result URL not persisted before retry: queries=%d downloads=%d %+v", f.queries.Load(), f.videoDownloads.Load(), p.Shots[0])
	}
	// 成功结果下载不能依赖上游查询接口继续可用，也不能重复提交或释放已受理预留。
	f.s.Provider = func(context.Context, Model) (Provider, error) {
		return Provider{}, fmt.Errorf("provider disabled after success")
	}
	for range 2 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Shots[0].Status != "succeeded" || p.ChargedMicro != 500_000 || f.queries.Load() != 1 || f.submits.Load() != 1 || f.videoDownloads.Load() != 2 {
		t.Fatalf("download recovery: queries=%d submits=%d downloads=%d %+v", f.queries.Load(), f.submits.Load(), f.videoDownloads.Load(), p.Shots[0])
	}
}
