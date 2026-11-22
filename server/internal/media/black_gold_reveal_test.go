package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

func readBlackGoldRevealSnapshot(t *testing.T) TemplateSnapshot {
	t.Helper()
	raw, err := os.ReadFile("testdata/black-gold-reveal-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot TemplateSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestBlackGoldRevealDesktopSnapshotContract(t *testing.T) {
	snapshot := readBlackGoldRevealSnapshot(t)
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := findVideoTemplate(templates, "black-gold-reveal", "3.0.7")
	if err != nil || !reflect.DeepEqual(recipe, snapshot.Template) {
		t.Fatal("desktop snapshot differs from the production recipe", err)
	}
	unsigned := snapshot
	unsigned.Digest = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if snapshot.Digest != hex.EncodeToString(digest[:]) {
		t.Fatal("desktop snapshot digest differs from Go serialization")
	}
	f := setup(t)
	catalog, err := f.s.resolveTemplateCatalog(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range catalog {
		if entry.snapshot.Template.ID == recipe.ID {
			if !entry.enabled || !reflect.DeepEqual(entry.snapshot, snapshot) {
				t.Fatal("desktop snapshot differs from the resolved production template and skills")
			}
			return
		}
	}
	t.Fatal("production recipe is missing")
}

func TestBlackGoldRevealTimingPreservesCoreAtEveryDuration(t *testing.T) {
	recipe := readBlackGoldRevealSnapshot(t).Template
	if !validTemplateTiming(recipe) {
		t.Fatal("desktop fixture is not a valid timed recipe")
	}
	core := []int{0, 1, 3, 6}
	for target := recipe.DurationRange.Min; target <= recipe.DurationRange.Max; target++ {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
		if !ok || len(kept) != len(seconds) {
			t.Fatalf("%ds: invalid timing plan", target)
		}
		total := 0
		var retainedCore []int
		for i, index := range kept {
			total += seconds[i]
			if seconds[i] < 2 {
				t.Fatalf("%ds: beat %d is below two seconds", target, index)
			}
			if recipe.Beats[index].Tier == TemplateTierCore {
				retainedCore = append(retainedCore, index)
			}
		}
		if total != target || !slices.Equal(retainedCore, core) {
			t.Fatalf("%ds: total=%d core=%v", target, total, retainedCore)
		}
	}
	for _, tc := range []struct {
		target  int
		kept    []int
		seconds []int
	}{
		{12, core, []int{2, 3, 4, 3}},
		{15, []int{0, 1, 2, 3, 6}, []int{2, 3, 2, 5, 3}},
		{20, []int{0, 1, 2, 3, 4, 5, 6}, []int{2, 3, 2, 4, 3, 3, 3}},
		{30, []int{0, 1, 2, 3, 4, 5, 6}, []int{3, 4, 3, 6, 4, 5, 5}},
		{60, []int{0, 1, 2, 3, 4, 5, 6}, []int{6, 9, 6, 12, 9, 9, 9}},
	} {
		kept, seconds, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, tc.target)
		if !ok || !slices.Equal(kept, tc.kept) || !slices.Equal(seconds, tc.seconds) {
			t.Fatalf("%ds: kept=%v seconds=%v", tc.target, kept, seconds)
		}
	}
	for _, target := range []int{recipe.DurationRange.Min - 1, recipe.DurationRange.Max + 1} {
		if _, _, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target); ok {
			t.Fatalf("out-of-range duration %d was accepted", target)
		}
	}
}
