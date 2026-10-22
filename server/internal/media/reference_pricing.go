package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

const ReferencePricingKey = "media:reference-pricing:v1"
const MaxReferencePriceMicro int64 = 9007199254740991

type ReferencePricing struct {
	SnapAnyImportMicro int64 `json:"snapAnyImportMicro"`
	AnalysisMicro      int64 `json:"analysisMicro"`
}

func ParseReferencePricing(raw []byte) (ReferencePricing, error) {
	var input struct {
		SnapAnyImportMicro *int64 `json:"snapAnyImportMicro"`
		AnalysisMicro      *int64 `json:"analysisMicro"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.SnapAnyImportMicro == nil || input.AnalysisMicro == nil {
		return ReferencePricing{}, fmt.Errorf("invalid_reference_pricing")
	}
	p := ReferencePricing{SnapAnyImportMicro: *input.SnapAnyImportMicro, AnalysisMicro: *input.AnalysisMicro}
	if p.SnapAnyImportMicro < 0 || p.AnalysisMicro < 0 || p.SnapAnyImportMicro > MaxReferencePriceMicro || p.AnalysisMicro > MaxReferencePriceMicro {
		return ReferencePricing{}, fmt.Errorf("invalid_reference_pricing")
	}
	return p, nil
}

func (r Repository) ReferencePricing(ctx context.Context) (ReferencePricing, error) {
	raw, err := r.Store.GetConfigValue(ctx, ReferencePricingKey)
	if err != nil {
		return ReferencePricing{}, fmt.Errorf("reference_pricing_unavailable")
	}
	if len(raw) == 0 {
		return ReferencePricing{SnapAnyImportMicro: ReferenceImportFeeMicro, AnalysisMicro: ReferenceAnalysisFeeMicro}, nil
	}
	p, err := ParseReferencePricing(raw)
	if err != nil {
		return ReferencePricing{}, fmt.Errorf("reference_pricing_unavailable")
	}
	return p, nil
}
