package contracts

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCloudAssetClientFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Pages []json.RawMessage `json:"cloudAssetPages"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Pages) != 2 {
		t.Fatal("缺少客户端 schema 冻结样本")
	}
	for _, raw := range fixtures.Pages {
		var page CloudAssetPage
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if page.Team.Storage.LimitBytes != CloudAssetTeamStorageLimitBytes {
			t.Fatal("团队容量与客户端契约不一致")
		}
		for _, asset := range page.Assets {
			if asset.Size > CloudAssetMaxBytes {
				t.Fatal("单文件大小与客户端契约不一致")
			}
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Go wire 与客户端 schema 不同构: got %s want %s", encoded, raw)
		}
	}
}

func TestCloudAssetDeletionClientFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Overviews []json.RawMessage `json:"cloudAssetDeletionOverviews"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Overviews) != 3 {
		t.Fatal("缺少删除审批跨端冻结样本")
	}
	for _, raw := range fixtures.Overviews {
		var overview CloudAssetDeletionOverview
		if err := json.Unmarshal(raw, &overview); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(overview)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("删除审批 wire 不同构: got %s want %s", encoded, raw)
		}
	}
}
