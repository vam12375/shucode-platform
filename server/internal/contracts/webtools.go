package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
)

const WebToolsCapabilitiesPath = "/v1/tools/capabilities"

// WebToolsCapabilities mirrors shared/platform-web-tools.ts.
type WebToolsCapabilities struct {
	SchemaVersion   int64  `json:"schemaVersion"`
	Enabled         bool   `json:"enabled"`
	SearchEnabled   bool   `json:"searchEnabled"`
	FetchEnabled    bool   `json:"fetchEnabled"`
	SearchSaleMicro *int64 `json:"searchSaleMicro,omitempty"`
	FetchSaleMicro  *int64 `json:"fetchSaleMicro,omitempty"`
}

func ParseWebToolsCapabilities(raw []byte) (WebToolsCapabilities, error) {
	invalid := errors.New("invalid_web_tools_capabilities")
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return WebToolsCapabilities{}, invalid
	}
	for _, key := range []string{"schemaVersion", "enabled", "searchEnabled", "fetchEnabled"} {
		value, exists := fields[key]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return WebToolsCapabilities{}, invalid
		}
	}
	for _, key := range []string{"searchSaleMicro", "fetchSaleMicro"} {
		if value, exists := fields[key]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return WebToolsCapabilities{}, invalid
		}
	}
	var wire struct {
		SchemaVersion   float64  `json:"schemaVersion"`
		Enabled         bool     `json:"enabled"`
		SearchEnabled   bool     `json:"searchEnabled"`
		FetchEnabled    bool     `json:"fetchEnabled"`
		SearchSaleMicro *float64 `json:"searchSaleMicro,omitempty"`
		FetchSaleMicro  *float64 `json:"fetchSaleMicro,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF {
		return WebToolsCapabilities{}, invalid
	}
	if wire.SchemaVersion != 1 || (!wire.Enabled && (wire.SearchEnabled || wire.FetchEnabled)) {
		return WebToolsCapabilities{}, invalid
	}
	for _, price := range []*float64{wire.SearchSaleMicro, wire.FetchSaleMicro} {
		if price != nil && (*price < 0 || *price > 9_007_199_254_740_991 || math.Trunc(*price) != *price) {
			return WebToolsCapabilities{}, invalid
		}
	}
	value := WebToolsCapabilities{SchemaVersion: 1, Enabled: wire.Enabled, SearchEnabled: wire.SearchEnabled, FetchEnabled: wire.FetchEnabled}
	if wire.SearchSaleMicro != nil {
		price := int64(*wire.SearchSaleMicro)
		value.SearchSaleMicro = &price
	}
	if wire.FetchSaleMicro != nil {
		price := int64(*wire.FetchSaleMicro)
		value.FetchSaleMicro = &price
	}
	return value, nil
}
