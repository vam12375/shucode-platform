package media

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAutomaticAssetSelectionFreezesTeamMatchesAndHonorsManualChoice(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.project(t).Brief
	b.Product = "  Brand SKU  "
	b.AssetIDs = []string{}
	for _, a := range []Asset{
		{ID: "older", Product: "brand sku", Kind: "image", CreatedAt: time.Unix(1, 0)},
		{ID: "newer", Product: "Brand SKU", Kind: "image", CreatedAt: time.Unix(2, 0)},
		{ID: "fallback", Name: "Brand SKU-front.png", Kind: "image", CreatedAt: time.Unix(3, 0)},
		{ID: "movie", Product: "Brand SKU", Kind: "video", CreatedAt: time.Unix(4, 0)},
	} {
		if e := f.s.Repo.AddAsset(ctx, f.team, a); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.s.Repo.AddAsset(ctx, "other-team", Asset{ID: "private", Product: "Brand SKU", Kind: "image"}); e != nil {
		t.Fatal(e)
	}
	id := uuid.NewString()
	p, e := f.s.Create(ctx, f.team, f.user, "", id, b)
	if e != nil {
		t.Fatal(e)
	}
	if !slices.Equal(p.Brief.AssetIDs, []string{"newer", "older"}) {
		t.Fatalf("resolved assets: %v", p.Brief.AssetIDs)
	}
	if e = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: "later", Product: "Brand SKU", Kind: "image", CreatedAt: time.Unix(5, 0)}); e != nil {
		t.Fatal(e)
	}
	again, e := f.s.Create(ctx, f.team, f.user, "", id, b)
	if e != nil || !slices.Equal(again.Brief.AssetIDs, p.Brief.AssetIDs) || again.Version != p.Version {
		t.Fatalf("request replay changed assets: %+v %v", again, e)
	}
	changed := b
	changed.Product = "another SKU"
	if _, e = f.s.Create(ctx, f.team, f.user, "", id, changed); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed request: %v", e)
	}
	b.AssetIDs = []string{"a"}
	manual, e := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b)
	if e != nil || !slices.Equal(manual.Brief.AssetIDs, []string{"a"}) {
		t.Fatalf("manual selection changed: %+v %v", manual, e)
	}
	if f.submits.Load() != 0 {
		t.Fatal("selection must not call a paid provider")
	}
}

func TestAutomaticAssetSelectionFallsBackToNamesAndLimitsToTwenty(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.project(t).Brief
	b.Product = "SHOE"
	b.AssetIDs = nil
	for i := range 23 {
		if e := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: fmt.Sprintf("shoe-%02d", i), Name: fmt.Sprintf("shoe-%02d.png", i), Kind: "image", CreatedAt: time.Unix(int64(i), 0)}); e != nil {
			t.Fatal(e)
		}
	}
	p, e := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), b)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Brief.AssetIDs) != 20 || p.Brief.AssetIDs[0] != "shoe-22" || p.Brief.AssetIDs[19] != "shoe-03" {
		t.Fatalf("fallback limit/order: %v", p.Brief.AssetIDs)
	}
}

func TestAutomaticAssetSelectionRejectsMissingProductAssets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.project(t).Brief
	b.Product = "missing SKU"
	b.AssetIDs = nil
	id := uuid.NewString()
	if _, e := f.s.Create(ctx, f.team, f.user, "", id, b); e == nil || e.Error() != "product_assets_not_found" {
		t.Fatalf("missing assets: %v", e)
	}
	p, e := f.s.Repo.Get(ctx, f.team, id)
	if e != nil || p != nil || f.submits.Load() != 0 {
		t.Fatal("unmatched request must not create a production or submit a provider job")
	}
}
