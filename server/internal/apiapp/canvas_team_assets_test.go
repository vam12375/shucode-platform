package apiapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/teamassets"
)

func seedCanvasArchiveProject(t *testing.T, f *apiFixture, team string, actor teamAssetTestActor) (*media.Project, media.Asset, []byte) {
	t.Helper()
	ctx := context.Background()
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX1kAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	id, size, err := f.deps.Media.Blobs.Put(team, bytes.NewReader(raw), media.MaxUpload)
	if err != nil {
		t.Fatal(err)
	}
	source := media.Asset{ID: id, Name: "generated.png", Kind: "image", Mime: "image/png", Size: size, CreatedAt: time.Now().UTC()}
	if err := f.deps.Media.Repo.AddAsset(ctx, team, source); err != nil {
		t.Fatal(err)
	}
	p := &media.Project{ID: uuid.NewString(), Version: 1, TeamID: team, UserID: actor.userID, DeviceID: actor.deviceID, Status: "completed", Canvas: &media.CanvasRequest{Kind: "image", ModelID: "image-fixture", Prompt: "a lake", AssetIDs: []string{}, Resolution: "2K", AspectRatio: "16:9", ExpectedQuoteMicro: 100000, Count: 1, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture"}, Shots: []media.Shot{{Status: "succeeded", OutputAssetID: id, ChargedMicro: 100000, QuoteMicro: 100000}}, OutputAssetID: id, QuoteMicro: 100000, ChargedMicro: 100000, CreatedAt: source.CreatedAt, UpdatedAt: source.CreatedAt}
	if err := f.deps.Media.Repo.Index(ctx, media.ProjectKey(team, p.ID)); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Media.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	return p, source, raw
}

func TestCanvasTeamAssetAutomaticBackfillOwnershipAndIndependentBytes(t *testing.T) {
	f, root := newTeamAssetTestFixture(t)
	author := newTeamAssetTestActor(t, f, "原生成者")
	viewer := newTeamAssetTestActor(t, f, "同团队查看者")
	team := newTeamAssetTestTeam(t, f, author, viewer)
	p, source, raw := seedCanvasArchiveProject(t, f, team, author)
	ctx := context.Background()
	f.deps.Media.Tick(ctx)
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, viewer, http.MethodGet, "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 1 || page.Data.Team.Storage.UsedBytes != int64(len(raw)) {
		t.Fatalf("completed output not archived: %+v", page.Data)
	}
	asset := page.Data.Assets[0]
	if asset.UploadedBy.ID != author.userID || asset.UploadedBy.Name != author.name || asset.Kind != "image" || asset.Mime != "image/png" {
		t.Fatalf("archive identity: %+v", asset)
	}
	stored, err := f.store.GetTeamAsset(ctx, team, asset.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if sourcePath, targetPath := f.deps.Media.Blobs.Path(team, source.ID), filepath.Join(root, team, stored.BlobID); sourcePath == targetPath {
		t.Fatal("archive shares media path")
	} else {
		a, err := os.Stat(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(targetPath)
		if err != nil || os.SameFile(a, b) {
			t.Fatalf("archive is not independent bytes: %v", err)
		}
	}
	content := teamAssetTestRequest(f, viewer, http.MethodGet, "/api/v1/team-assets/"+asset.ID+"/content", "", nil)
	if content.Code != 200 || !bytes.Equal(content.Body.Bytes(), raw) {
		t.Fatalf("teammate cannot read archive: %d", content.Code)
	}
	view := teamAssetTestRequest(f, viewer, http.MethodGet, "/api/v1/media/projects/"+p.ID, "", nil)
	var envelope struct {
		Data media.ProjectView `json:"data"`
	}
	if view.Code != 200 || json.Unmarshal(view.Body.Bytes(), &envelope) != nil || len(envelope.Data.TeamAssetArchives) != 1 || envelope.Data.TeamAssetArchives[0].Status != "saved" || envelope.Data.TeamAssetArchives[0].TeamAssetID != asset.ID || envelope.Data.ChargedMicro != p.ChargedMicro || envelope.Data.Status != p.Status {
		t.Fatalf("archive public projection: %d %s", view.Code, view.Body.String())
	}
	for _, secret := range []string{"blobId", "leaseOwner", "userId", root} {
		if strings.Contains(view.Body.String(), secret) {
			t.Fatalf("archive projection leaked %s", secret)
		}
	}
	overview := teamAssetTestRequest(f, viewer, http.MethodGet, "/api/v1/media/overview", "", nil)
	var overviewEnvelope struct {
		Data struct {
			Generations []media.ProjectView `json:"generations"`
		} `json:"data"`
	}
	if overview.Code != 200 || json.Unmarshal(overview.Body.Bytes(), &overviewEnvelope) != nil || len(overviewEnvelope.Data.Generations) != 1 || len(overviewEnvelope.Data.Generations[0].TeamAssetArchives) != 1 || overviewEnvelope.Data.Generations[0].TeamAssetArchives[0] != envelope.Data.TeamAssetArchives[0] {
		t.Fatalf("overview archive parity: %d %s", overview.Code, overview.Body.String())
	}
	version := envelope.Data.Version
	f.deps.Media.WorkerID = uuid.NewString()
	for range 3 {
		f.deps.Media.Tick(ctx)
	}
	current, _ := f.deps.Media.Repo.Get(ctx, team, p.ID)
	if current.Version != version {
		t.Fatal("saved recheck changed identical project facts")
	}
	request := decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, "/api/v1/team-assets/"+asset.ID+"/deletion-requests", "application/json", strings.NewReader(`{}`)))
	decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, "/api/v1/team-assets/deletion-requests/"+request.ID+"/review", "application/json", strings.NewReader(`{"decision":"approve"}`)))
	f.deps.Media.Tick(ctx)
	current, _ = f.deps.Media.Repo.Get(ctx, team, p.ID)
	if current.TeamAssetArchives[0].Status != "deleted" || current.ChargedMicro != p.ChargedMicro || current.Status != p.Status {
		t.Fatalf("deleted archive changed generation: %+v", current)
	}
	f.deps.Media.Tick(ctx)
	if got, _ := f.store.GetTeamAsset(ctx, team, asset.ID); got != nil {
		t.Fatal("approved archive resurrected")
	}
	if original, err := os.ReadFile(f.deps.Media.Blobs.Path(team, source.ID)); err != nil || !bytes.Equal(original, raw) {
		t.Fatalf("team asset deletion damaged canvas original: %v", err)
	}
}

func TestCanvasTeamAssetQuotaAndPermissionRecovery(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	admin := newTeamAssetTestActor(t, f, "管理员")
	author := newTeamAssetTestActor(t, f, "生成者")
	team := newTeamAssetTestTeam(t, f, admin, author)
	p, _, raw := seedCanvasArchiveProject(t, f, team, author)
	ctx := context.Background()
	var filler store.TeamAssetRecord
	used := int64(0)
	for i := 0; used < teamassets.TeamStorageLimitBytes; i++ {
		size := min(teamassets.MaxAssetBytes, teamassets.TeamStorageLimitBytes-used)
		filler = store.TeamAssetRecord{ID: uuid.NewString(), Name: "E2E_QUOTA.bin", Kind: "other", Mime: "application/octet-stream", Size: size, CreatedAt: time.Now().UTC(), UploadedByID: author.userID, UploadedByName: author.name, SHA256: fmt.Sprintf("%064x", i+1), BlobID: uuid.NewString()}
		if _, _, err := f.store.CommitTeamAsset(ctx, team, filler, teamassets.TeamStorageLimitBytes); err != nil {
			t.Fatal(err)
		}
		used += size
	}
	f.deps.Media.Tick(ctx)
	current, _ := f.deps.Media.Repo.Get(ctx, team, p.ID)
	if len(current.TeamAssetArchives) != 1 || current.TeamAssetArchives[0].Status != "failed" || current.TeamAssetArchives[0].Error != "canvas_archive_storage_limit" || current.Status != "completed" || current.ChargedMicro != p.ChargedMicro {
		t.Fatalf("quota archive failure: %+v", current)
	}
	r, err := f.store.RequestTeamAssetDeletion(ctx, team, author.userID, filler.ID, uuid.NewString(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReviewTeamAssetDeletion(ctx, team, admin.userID, r.ID, "approve", time.Now()); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if _, err := f.store.UpdateTeamMember(ctx, team, author.userID, store.TeamMemberPatch{HasDisabled: true, DisabledAt: &at}); err != nil {
		t.Fatal(err)
	}
	f.deps.Media.Tick(ctx)
	current, _ = f.deps.Media.Repo.Get(ctx, team, p.ID)
	if current.TeamAssetArchives[0].Error != "canvas_archive_forbidden" {
		t.Fatalf("disabled author still published: %+v", current.TeamAssetArchives)
	}
	if _, err := f.store.UpdateTeamMember(ctx, team, author.userID, store.TeamMemberPatch{HasDisabled: true}); err != nil {
		t.Fatal(err)
	}
	f.deps.Media.Tick(ctx)
	current, _ = f.deps.Media.Repo.Get(ctx, team, p.ID)
	if current.TeamAssetArchives[0].Status != "saved" || current.ChargedMicro != p.ChargedMicro || current.QuoteMicro != p.QuoteMicro || current.Status != p.Status {
		t.Fatalf("archive did not recover independently: %+v", current)
	}
	page, _ := f.store.ListTeamAssetPage(ctx, team, nil, "", 100)
	if page.UsedBytes != used-filler.Size+int64(len(raw)) {
		t.Fatalf("archive recovery double counted quota: %d", page.UsedBytes)
	}
}

type blockCanvasArchiveReceiptStore struct {
	store.PlatformStore
	block bool
}

func (s *blockCanvasArchiveReceiptStore) CompareAndSwapConfigValue(ctx context.Context, key string, old, next []byte) (bool, error) {
	if s.block && strings.HasPrefix(key, "media:project:") && bytes.Contains(next, []byte(`"status":"saved"`)) {
		return false, nil
	}
	return s.PlatformStore.CompareAndSwapConfigValue(ctx, key, old, next)
}

func TestCanvasTeamAssetCommittedBeforeReceiptRecoveryAndDeletionRace(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			f, _ := newTeamAssetTestFixture(t)
			author := newTeamAssetTestActor(t, f, "生成者")
			team := newTeamAssetTestTeam(t, f, author)
			p, _, raw := seedCanvasArchiveProject(t, f, team, author)
			ctx := context.Background()
			blocked := &blockCanvasArchiveReceiptStore{PlatformStore: f.store, block: true}
			f.deps.Media.Repo.Store = blocked
			f.deps.Media.Tick(ctx)
			current, _ := f.deps.Media.Repo.Get(ctx, team, p.ID)
			page, _ := f.store.ListTeamAssetPage(ctx, team, nil, "", 100)
			if len(current.TeamAssetArchives) != 1 || current.TeamAssetArchives[0].Status != "pending" || len(page.Records) != 1 {
				t.Fatalf("crash boundary fixture: %+v %+v", current, page)
			}
			assetID := page.Records[0].ID
			if deleted {
				r, err := f.store.RequestTeamAssetDeletion(ctx, team, author.userID, assetID, uuid.NewString(), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.ReviewTeamAssetDeletion(ctx, team, author.userID, r.ID, "approve", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			blocked.block = false
			f.deps.Media.WorkerID = uuid.NewString()
			f.deps.Media.Tick(ctx)
			current, _ = f.deps.Media.Repo.Get(ctx, team, p.ID)
			page, _ = f.store.ListTeamAssetPage(ctx, team, nil, "", 100)
			want, count, used := "saved", 1, int64(len(raw))
			if deleted {
				want, count, used = "deleted", 0, 0
			}
			if current.TeamAssetArchives[0].Status != want || current.TeamAssetArchives[0].TeamAssetID != assetID || len(page.Records) != count || page.UsedBytes != used || current.ChargedMicro != p.ChargedMicro {
				t.Fatalf("receipt recovery: %+v %+v", current, page)
			}
		})
	}
}

func TestCanvasTeamAssetSizeFailureKeepsGeneration(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	author := newTeamAssetTestActor(t, f, "生成者")
	team := newTeamAssetTestTeam(t, f, author)
	p, source, _ := seedCanvasArchiveProject(t, f, team, author)
	ctx := context.Background()
	assets, err := f.deps.Media.Repo.Assets(ctx, team)
	if err != nil {
		t.Fatal(err)
	}
	for i := range assets {
		if assets[i].ID == source.ID {
			assets[i].Size = teamassets.MaxAssetBytes + 1
		}
	}
	encoded, _ := json.Marshal(assets)
	if err := f.store.SetConfigValue(ctx, "media:assets:"+team, encoded); err != nil {
		t.Fatal(err)
	}
	f.deps.Media.Tick(ctx)
	current, _ := f.deps.Media.Repo.Get(ctx, team, p.ID)
	if len(current.TeamAssetArchives) != 1 || current.TeamAssetArchives[0].Error != "canvas_archive_size_limit" || current.Status != p.Status || current.ChargedMicro != p.ChargedMicro {
		t.Fatalf("oversized output invalidated generation: %+v", current)
	}
}

func TestCanvasTeamAssetStableIDCollisionWithDifferentBytesDoesNotPublish(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	author := newTeamAssetTestActor(t, f, "原生成者")
	team := newTeamAssetTestTeam(t, f, author)
	p, source, raw := seedCanvasArchiveProject(t, f, team, author)
	input := media.CanvasArchiveInput{TeamID: team, UserID: author.userID, ProjectID: p.ID, Source: source}
	id := canvasTeamAssetID(input)
	// 同尺寸的人工上传也不能冒充来源原图，首次确认必须核对原字节摘要。
	other := append([]byte(nil), raw...)
	other[len(other)-1] ^= 1
	uploader := teamassets.Service{Repository: teamassets.Repository{Store: f.store}, Blobs: media.Blobs{Root: filepath.Join(t.TempDir(), "team-assets")}, Now: time.Now}
	if _, err := uploader.Upload(context.Background(), team, author.userID, author.name, id, source.Name, bytes.NewReader(other)); err != nil {
		t.Fatal(err)
	}
	f.deps.Media.Tick(context.Background())
	current, _ := f.deps.Media.Repo.Get(context.Background(), team, p.ID)
	page, _ := f.store.ListTeamAssetPage(context.Background(), team, nil, "", 10)
	if len(current.TeamAssetArchives) != 1 || current.TeamAssetArchives[0].Status != "failed" || current.TeamAssetArchives[0].Error != "canvas_archive_conflict" || len(page.Records) != 1 || page.UsedBytes != int64(len(raw)) || current.Status != p.Status || current.ChargedMicro != p.ChargedMicro {
		t.Fatalf("stable ID collision was accepted: %+v %+v", current, page)
	}
}

func TestCanvasGenerationCompletesWithAutomaticTeamAssetsAndSingleCharge(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	author := newTeamAssetTestActor(t, f, "真实生成者")
	viewer := newTeamAssetTestActor(t, f, "同事")
	team := newTeamAssetTestTeam(t, f, author, viewer)
	ctx := context.Background()
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX1kAAAAASUVORK5CYII=")
	var submissions atomic.Int64
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			submissions.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"url": upstream.URL + "/result.png"}}})
			return
		}
		_, _ = w.Write(raw)
	}))
	defer upstream.Close()
	f.deps.Media.Blobs.Client = upstream.Client()
	f.deps.Media.Provider = func(context.Context, media.Model) (media.Provider, error) {
		return media.Provider{BaseURL: upstream.URL, Client: upstream.Client()}, nil
	}
	model := media.Model{ID: "image-fixture", Name: "image", Kind: "image", Adapter: "ark", ProviderID: uuid.NewString(), UpstreamModel: "image-fixture", PriceMicro: 100000, PriceVersion: "v1"}
	models, _ := json.Marshal([]media.Model{model})
	if err := f.store.SetConfigValue(ctx, media.ConfigKey, models); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	input := media.CanvasRequest{Kind: "image", ModelID: model.ID, Prompt: "a calm lake", AssetIDs: []string{}, Resolution: "2K", AspectRatio: "16:9", Count: 2, ExpectedQuoteMicro: 200000, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture"}
	body, _ := json.Marshal(map[string]any{"id": id, "request": input})
	response := teamAssetTestRequest(f, author, http.MethodPost, "/api/v1/media/generations", "application/json", bytes.NewReader(body))
	if response.Code != 200 {
		t.Fatalf("generation rejected: %d %s", response.Code, response.Body.String())
	}
	for range 16 {
		f.deps.Media.Tick(ctx)
	}
	p, err := f.deps.Media.Repo.Get(ctx, team, id)
	if err != nil || p.Status != "completed" || len(p.TeamAssetArchives) != 2 || p.ChargedMicro != 200000 || submissions.Load() != 2 {
		t.Fatalf("generation/archive lifecycle: %+v submissions=%d err=%v", p, submissions.Load(), err)
	}
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, viewer, http.MethodGet, "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 2 || page.Data.Team.Storage.UsedBytes != 2*int64(len(raw)) {
		t.Fatalf("multiple outputs not automatically published: %+v", page.Data)
	}
	for _, asset := range page.Data.Assets {
		if asset.UploadedBy.ID != author.userID {
			t.Fatalf("viewer stole generated asset ownership: %+v", asset)
		}
	}
	for range 4 {
		f.deps.Media.Tick(ctx)
	}
	ledger, _ := f.store.GetTeam(ctx, team)
	if ledger.UsedCredits != "0.200000" || submissions.Load() != 2 {
		t.Fatalf("archive retriggered generation/charge: %+v calls=%d", ledger, submissions.Load())
	}
}
