package media

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"reflect"
	"testing"
)

func TestCanvasReplayUsesAcceptedSnapshotBeforeCurrentCatalogueAndAssets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := seedanceCanvasRequest(t, f)
	r.AssetIDs = []string{canvasReferenceAsset(t, f, "image", 0).ID}
	video := canvasReferenceAsset(t, f, "video", 3)
	r.VideoReferences = []CanvasVideoReference{{AssetID: video.ID, ReferenceClipAssetID: "host-prepared-field"}}
	r.ExpectedQuoteMicro = 1000000
	id := uuid.NewString()
	accepted, err := f.s.CreateCanvas(ctx, f.team, f.user, "first-device", id, r)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Canvas.VideoReferences[0].ReferenceClipAssetID != "" {
		t.Fatal("internal clip field leaked into public frozen request")
	}
	for _, key := range []string{ConfigKey, assetKey(f.team)} {
		if err := f.s.Repo.Store.SetConfigValue(ctx, key, []byte("[]")); err != nil {
			t.Fatal(err)
		}
	}
	r.VideoReferences[0].ReferenceClipAssetID = ""
	replay, err := f.s.CreateCanvas(ctx, f.team, f.user, "another-device", id, r)
	if err != nil || !reflect.DeepEqual(accepted, replay) {
		t.Fatalf("accepted snapshot not recovered: %v %+v", err, replay)
	}
	for _, change := range []func(*CanvasRequest){
		func(v *CanvasRequest) { v.Prompt = "changed" }, func(v *CanvasRequest) { v.ModelID = "changed" }, func(v *CanvasRequest) { v.ExpectedQuoteMicro++ }, func(v *CanvasRequest) { v.WorkspacePath = "/other" }, func(v *CanvasRequest) { v.WorkspaceIdentity = "remote:other" }, func(v *CanvasRequest) { v.RemoteSessionID = "other" },
	} {
		changed := r
		change(&changed)
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", id, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed immutable request accepted: %v", err)
		}
	}
	if _, err := f.s.CreateCanvas(ctx, f.team, uuid.NewString(), "", id, r); !errors.Is(err, ErrConflict) {
		t.Fatalf("other creator recovered via same team lookup: %v", err)
	}
	all, err := f.s.Repo.All(ctx)
	if err != nil || len(all) != 1 || f.submits.Load() != 0 {
		t.Fatal("replay created or submitted a new task", err)
	}
	team, err := f.s.Repo.Store.GetTeam(ctx, f.team)
	if err != nil || team.UsedCredits != "0.000000" {
		raw, _ := json.Marshal(team)
		t.Fatalf("replay altered the ledger: %s %v", raw, err)
	}
}
