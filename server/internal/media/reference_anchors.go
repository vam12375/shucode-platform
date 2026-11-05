package media

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode/utf16"
)

type ReferenceAnchor struct {
	SourceTime      float64 `json:"sourceTime"`
	Framing         string  `json:"framing"`
	Camera          string  `json:"camera"`
	Action          string  `json:"action"`
	RequiredDetails string  `json:"requiredDetails,omitempty"`
}

func (a *ReferenceAnchor) UnmarshalJSON(raw []byte) error {
	type plain ReferenceAnchor
	var value plain
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"sourceTime", "framing", "camera", "action"} {
		if v, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return ErrInvalid
		}
	}
	if v, ok := fields["requiredDetails"]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	value.Framing = strings.TrimSpace(value.Framing)
	value.Camera = strings.TrimSpace(value.Camera)
	value.Action = strings.TrimSpace(value.Action)
	value.RequiredDetails = strings.TrimSpace(value.RequiredDetails)
	*a = ReferenceAnchor(value)
	return nil
}

func validReferenceAnchors(anchors []ReferenceAnchor, start, end float64) bool {
	if anchors == nil {
		return true
	}
	if len(anchors) == 0 || len(anchors) > 8 || math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) || start < 0 || end <= start {
		return false
	}
	textOK := func(s string, max int, required bool) bool {
		return (!required || strings.TrimSpace(s) != "") && len(utf16.Encode([]rune(s))) <= max
	}
	for i, a := range anchors {
		if math.IsNaN(a.SourceTime) || math.IsInf(a.SourceTime, 0) || a.SourceTime < start || a.SourceTime > end || (i > 0 && a.SourceTime <= anchors[i-1].SourceTime) || !textOK(a.Framing, 160, true) || !textOK(a.Camera, 160, true) || !textOK(a.Action, 240, true) || !textOK(a.RequiredDetails, 240, false) {
			return false
		}
	}
	return true
}

func sameReferenceAnchorTimes(source, target []ReferenceAnchor) bool {
	if source == nil {
		return true
	}
	if len(source) != len(target) {
		return false
	}
	for i := range source {
		if source[i].SourceTime != target[i].SourceTime {
			return false
		}
	}
	return true
}
