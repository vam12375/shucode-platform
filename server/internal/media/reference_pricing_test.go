package media

import (
	"context"
	"testing"
)

func TestReferencePricingDefaultsAndRejectsCorruption(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, err := f.s.Repo.ReferencePricing(ctx)
	if err != nil || p.SnapAnyImportMicro != 500000 || p.AnalysisMicro != 1000000 {
		t.Fatalf("default reference pricing unavailable: %+v %v", p, err)
	}
	for _, raw := range []string{`{}`, `null`, `{"snapAnyImportMicro":-1,"analysisMicro":1}`, `{"snapAnyImportMicro":0,"analysisMicro":0,"extra":true}`} {
		if err := f.s.Repo.Store.SetConfigValue(ctx, ReferencePricingKey, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Repo.ReferencePricing(ctx); err == nil {
			t.Fatal("invalid stored pricing silently accepted")
		}
	}
	if err := f.s.Repo.Store.SetConfigValue(ctx, ReferencePricingKey, []byte(`{"snapAnyImportMicro":0,"analysisMicro":2500000}`)); err != nil {
		t.Fatal(err)
	}
	if p, err := f.s.Repo.ReferencePricing(ctx); err != nil || p.SnapAnyImportMicro != 0 || p.AnalysisMicro != 2500000 {
		t.Fatalf("free import / updated analysis price unavailable: %+v %v", p, err)
	}
}
