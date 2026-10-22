package apiapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestTeamAssetDeletionPagingCountScopeAndByteBudget(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	ctx := context.Background()
	actor := newTeamAssetTestActor(t, f, strings.Repeat("🧑", 1000))
	team := newTeamAssetTestTeam(t, f, actor)
	now := time.Now().UTC()
	other := newTeamAssetTestActor(t, f, "其他成员")
	if err := f.store.AddMember(ctx, team, other.userID, store.MemberRoleMember); err != nil {
		t.Fatal(err)
	}
	var oldest store.TeamAssetDeletionRecord
	for i := range 201 {
		asset := store.TeamAssetRecord{ID: uuid.NewString(), Name: strings.Repeat("📚", 251) + ".png", Kind: "image", Mime: "image/png", Size: 1, CreatedAt: now, UploadedByID: actor.userID, UploadedByName: actor.name, SHA256: strings.Repeat("a", 64), BlobID: uuid.NewString()}
		if _, _, err := f.store.CommitTeamAsset(ctx, team, asset, 1000); err != nil {
			t.Fatal(err)
		}
		r, err := f.store.RequestTeamAssetDeletion(ctx, team, actor.userID, asset.ID, uuid.NewString(), now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if i < 100 {
			if _, err := f.store.ReviewTeamAssetDeletion(ctx, team, actor.userID, r.ID, "reject", now.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatal(err)
			}
		} else {
			oldest = r
		}
	}
	read := func(actor teamAssetTestActor, cursor string) contracts.CloudAssetDeletionOverview {
		r := teamAssetTestRequest(f, actor, http.MethodGet, "/api/v1/team-assets/deletion-requests"+cursor, "", nil)
		if r.Code != 200 {
			t.Fatalf("overview %d %s", r.Code, r.Body.String())
		}
		if r.Body.Len() > 512*1024 {
			t.Fatalf("bounded overview exceeded 512KiB: %d", r.Body.Len())
		}
		t.Logf("overview bytes: %d", r.Body.Len())
		var envelope struct {
			Data contracts.CloudAssetDeletionOverview `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	first := read(actor, "")
	if len([]rune(first.Requests[0].RequestedBy.Name)) != 255 {
		t.Fatal("actor display name is unbounded")
	}
	if first.TeamID != team || first.PendingCount != 101 || len(first.Requests) <= 100 || len(first.Requests) > 200 || first.NextCursor == nil {
		t.Fatalf("first page %+v", first)
	}
	seen := map[string]bool{}
	for _, r := range first.Requests {
		seen[r.ID] = true
	}
	next := read(actor, "?cursor="+url.QueryEscape(*first.NextCursor))
	if next.PendingCount != 101 || len(next.Requests) != 1 || next.NextCursor != nil || next.Requests[0].Status != "pending" || seen[next.Requests[0].ID] {
		t.Fatalf("next page %+v", next)
	}
	if _, err := f.store.ReviewTeamAssetDeletion(ctx, team, actor.userID, oldest.ID, "reject", now.Add(300*time.Second)); err != nil {
		t.Fatal(err)
	}
	changed := read(actor, "")
	if changed.PendingCount != 100 || len(changed.Requests) <= 100 || len(changed.Requests) > 200 || changed.Requests[100].ID != oldest.ID || changed.NextCursor != nil {
		t.Fatalf("fresh count %+v", changed)
	}
	// 游标不携带可写team，重用其他成员游标及伪造位置均不能读取团队历史。
	for _, cursor := range []string{*first.NextCursor, "invalid"} {
		r := teamAssetTestRequest(f, other, http.MethodGet, "/api/v1/team-assets/deletion-requests?cursor="+url.QueryEscape(cursor), "", nil)
		if r.Code != 400 {
			t.Fatalf("invalid/foreign actor cursor %d", r.Code)
		}
	}
	foreign := newTeamAssetTestActor(t, f, "他团队")
	newTeamAssetTestTeam(t, f, foreign)
	if r := teamAssetTestRequest(f, foreign, http.MethodGet, "/api/v1/team-assets/deletion-requests?cursor="+url.QueryEscape(*first.NextCursor), "", nil); r.Code != 400 {
		t.Fatalf("foreign team cursor %d", r.Code)
	}
}
