package media

import (
	"context"
	"strings"
	"testing"
)

func TestImageCreativeDirectionAtomicReplay(t *testing.T) {
	f, p, a, selection := imageTemplateFixture(t)
	ctx := context.Background()
	direction := "突出窗边雨景与咖啡香气，结尾留一个安静的停顿"
	bound, err := f.s.BindWithDirection(ctx, f.team, p.ID, p.Version, a, selection, &direction)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Brief.SellingPoints != direction || bound.TemplateSnapshot == nil || bound.Analysis.TaskID != a.TaskID || bound.Version != p.Version+1 {
		t.Fatal("direction/template/task were not frozen atomically")
	}
	replay, err := f.s.BindWithDirection(ctx, f.team, p.ID, p.Version, a, selection, &direction)
	if err != nil || replay.Version != bound.Version {
		t.Fatal("replay changed version", err)
	}
	other := "改成另一种方向"
	if _, err = f.s.BindWithDirection(ctx, f.team, p.ID, p.Version, a, selection, &other); err == nil {
		t.Fatal("changed direction accepted under same input")
	}
	if f.submits.Load() != 0 {
		t.Fatal("binding triggered paid generation")
	}
}
func TestImageCreativeDirectionValidationAndClear(t *testing.T) {
	for _, direction := range []string{"", strings.Repeat("文", 4000), strings.Repeat("文", 4001)} {
		f, p, a, selection := imageTemplateFixture(t)
		bound, err := f.s.BindWithDirection(context.Background(), f.team, p.ID, p.Version, a, selection, &direction)
		if len([]rune(direction)) > 4000 {
			if err == nil {
				t.Fatal("oversized direction accepted")
			}
			continue
		}
		if err != nil || bound.Brief.SellingPoints != direction {
			t.Fatal("valid or empty direction lost", err)
		}
	}
	f, p, a, selection := imageTemplateFixture(t)
	a.Kind = "images"
	direction := "invalid stage"
	if _, err := f.s.BindWithDirection(context.Background(), f.team, p.ID, p.Version, a, selection, &direction); err == nil {
		t.Fatal("wrong phase accepted")
	}
}
