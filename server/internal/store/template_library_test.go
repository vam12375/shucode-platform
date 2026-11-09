package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	library "github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

func TestMemoryTemplateLibrary(t *testing.T) {
	s, close := newMemoryForTest(t)
	defer close()
	runTemplateLibrary(t, s)
}
func TestPostgresTemplateLibrary(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 TEST_DATABASE_URL")
	}
	s, close := newPostgresForTest(t, url, os.Getenv("TEST_MIGRATIONS_DIR"))
	defer close()
	runTemplateLibrary(t, s)
}
func runTemplateLibrary(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	r := library.Repository{Store: s}
	entries, err := r.List(ctx, nil)
	if err != nil || len(entries) != 94 {
		t.Fatalf("catalog: %d %v", len(entries), err)
	}
	chosen := entries[0]
	on, off := true, false
	var success atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Save(ctx, nil, chosen.ID, library.Mutation{ExpectedVersion: chosen.ExpectedVersion, Enabled: &off}, false)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, library.ErrConflict) {
				t.Errorf("CAS: %v", err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("parallel writes: %d", success.Load())
	}
	scenes, err := r.Scenes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes[0].Options["prompts"].Items) != 3 {
		t.Fatal("disabled prompt still published")
	}
	scenes[0].Options["prompts"].Items[0].Contents["cn"] = "mutated"
	fresh, err := r.Scenes(ctx)
	if err != nil || fresh[0].Options["prompts"].Items[0].Contents["cn"] == "mutated" {
		t.Fatal("aliased scenes")
	}
	var legacy library.Entry
	for _, e := range entries {
		if e.Kind == "skill" {
			legacy = e
			break
		}
	}
	fields := legacy.Fields
	fields.Content += "\nUpdated instructions.\n"
	updated, err := r.Save(ctx, nil, legacy.ID, library.Mutation{ExpectedVersion: legacy.ExpectedVersion, Fields: &fields}, false)
	if err != nil || len(updated.Package.Files) != len(legacy.Package.Files) || updated.Package.SHA256 == legacy.Package.SHA256 {
		t.Fatalf("package edit: %+v %v", updated.Package, err)
	}
	for i, file := range legacy.Package.Files {
		if file.Path != "SKILL.md" && file.SHA256 != updated.Package.Files[i].SHA256 {
			t.Fatal("package support file changed")
		}
	}
	raw, err := r.Package(ctx, updated.PackageDigest)
	if err != nil || len(raw) == 0 {
		t.Fatal("persisted package missing")
	}
	fields = library.Fields{Name: "Test prompt", Content: "Hello", Tags: []string{}, Scene: "draft-suggestion"}
	created, err := r.Save(ctx, nil, "", library.Mutation{Kind: "prompt", Enabled: &on, Fields: &fields}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Save(ctx, nil, created.ID, library.Mutation{ExpectedVersion: created.ExpectedVersion}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Save(ctx, nil, created.ID, library.Mutation{ExpectedVersion: created.ExpectedVersion, Enabled: &on}, false); !errors.Is(err, library.ErrNotFound) {
		t.Fatal("deleted item restored")
	}
	if _, err = r.Save(ctx, nil, chosen.ID, library.Mutation{ExpectedVersion: chosen.ExpectedVersion}, true); !errors.Is(err, library.ErrConflict) {
		t.Fatal("stale delete accepted")
	}
	// 仅正文更新不能丢弃整包附件；错误引用必须在发布前拒绝。
	fields = updated.Fields
	fields.Content += "\n[Missing](references/not-here.md)\n"
	if _, err = r.Save(ctx, nil, updated.ID, library.Mutation{ExpectedVersion: updated.ExpectedVersion, Fields: &fields}, false); !errors.Is(err, library.ErrInvalid) {
		t.Fatalf("missing reference accepted: %v", err)
	}
	runTemplateLibraryDeletion(t, s)
}

func runTemplateLibraryDeletion(t *testing.T, s PlatformStore) {
	ctx := context.Background()
	r := library.Repository{Store: s}
	videos := []library.Entry{{ID: library.VideoID("delete-test"), Kind: "video", Source: "builtin-video", Enabled: true}}
	countPrompts := func() int {
		scenes, err := r.Scenes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, scene := range scenes {
			if option := scene.Options["prompts"]; option != nil {
				count += len(option.Items)
				for _, items := range option.Cascades {
					count += len(items)
				}
			}
		}
		return count
	}
	for _, kind := range []string{"template", "skill", "prompt", "automation", "video"} {
		t.Run("delete-"+kind, func(t *testing.T) {
			entries, err := r.List(ctx, videos)
			if err != nil {
				t.Fatal(err)
			}
			var selected library.Entry
			for _, entry := range entries {
				if entry.Kind == kind && entry.Enabled {
					selected = entry
					break
				}
			}
			if selected.ID == "" {
				t.Fatal("missing builtin deletion fixture")
			}
			before := countPrompts()
			if _, err = r.Save(ctx, videos, selected.ID, library.Mutation{ExpectedVersion: selected.ExpectedVersion}, true); err != nil {
				t.Fatal(err)
			}
			// 模拟重新构建仓库与种子目录，删除状态必须由 Store 保留。
			r = library.Repository{Store: s}
			remaining, err := r.List(ctx, videos)
			if err != nil || len(remaining) != len(entries)-1 {
				t.Fatalf("deleted entry returned after reload: %d %v", len(remaining), err)
			}
			for _, entry := range remaining {
				if entry.ID == selected.ID {
					t.Fatal("deleted entry in admin catalog")
				}
			}
			on := true
			if _, err = r.Save(ctx, videos, selected.ID, library.Mutation{ExpectedVersion: selected.ExpectedVersion, Enabled: &on}, false); !errors.Is(err, library.ErrNotFound) {
				t.Fatalf("stale editor revived deleted entry: %v", err)
			}
			switch kind {
			case "prompt", "automation":
				if countPrompts() != before-1 {
					t.Fatal("deleted prompt remains published")
				}
			case "skill":
				skills, err := r.Skills(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, skill := range skills {
					if skill.ID == selected.ID {
						t.Fatal("deleted skill remains published")
					}
				}
			case "video":
				disabled, err := r.DisabledVideoIDs(ctx)
				if err != nil || !disabled["delete-test"] {
					t.Fatal("deleted video remains available")
				}
			}
		})
	}
}
