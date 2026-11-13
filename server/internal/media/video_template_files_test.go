package media

import (
	"encoding/json"
	"io/fs"
	"os"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestTemplateFilesManifestAndIsolation(t *testing.T) {
	got, err := loadVideoTemplateCatalog(videoTemplateFiles)
	if err != nil || len(got) != 60 {
		t.Fatalf("catalog: count=%d err=%v", len(got), err)
	}
	var names []string
	raw, _ := fs.ReadFile(videoTemplateFiles, "video_templates/manifest.json")
	if err := json.Unmarshal(raw, &names); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if name != got[i].ID+".json" {
			t.Fatal("manifest order differs from public catalog")
		}
	}
	before := got[0].Beats[0].Action
	got[0].Beats[0].Action = "mutated"
	again, err := loadVideoTemplateCatalog(videoTemplateFiles)
	if err != nil || again[0].Beats[0].Action != before {
		t.Fatal("catalog calls share mutable data")
	}
}

func TestTemplateFilesRejectAmbiguousCatalog(t *testing.T) {
	file := &fstest.MapFile{Data: []byte(`{"id":"a","version":"3.0.0"}`)}
	for name, files := range map[string]fstest.MapFS{
		"missing manifest": {},
		"empty":            {"video_templates/manifest.json": {Data: []byte(`[]`)}},
		"duplicate":        {"video_templates/manifest.json": {Data: []byte(`["a.json","a.json"]`)}, "video_templates/a.json": file},
		"missing":          {"video_templates/manifest.json": {Data: []byte(`["b.json"]`)}},
		"traversal":        {"video_templates/manifest.json": {Data: []byte(`["../a.json"]`)}},
		"id mismatch":      {"video_templates/manifest.json": {Data: []byte(`["b.json"]`)}, "video_templates/b.json": file},
		"orphan":           {"video_templates/manifest.json": {Data: []byte(`["a.json"]`)}, "video_templates/a.json": file, "video_templates/b.json": file},
		"unknown field":    {"video_templates/manifest.json": {Data: []byte(`["a.json"]`)}, "video_templates/a.json": {Data: []byte(`{"id":"a","unexpected":true}`)}},
		"trailing json":    {"video_templates/manifest.json": {Data: []byte(`["a.json"]`)}, "video_templates/a.json": {Data: []byte(`{"id":"a"} {}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadVideoTemplateCatalog(files); err == nil {
				t.Fatal("accepted ambiguous catalog")
			}
		})
	}
}

func TestBrandKVSplitPreservesRecipe(t *testing.T) {
	raw, err := fs.ReadFile(videoTemplateFiles, "video_templates/brand-kv-unfold.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, frozen VideoTemplate
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	raw, err = fs.ReadFile(os.DirFS("testdata"), "brand-kv-unfold-v2.1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, frozen) {
		t.Fatal("split rewrote the approved KV method")
	}
}
