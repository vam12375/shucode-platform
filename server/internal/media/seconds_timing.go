package media

import (
	"bytes"
	"encoding/json"
	"math"
)

// 秒级创作在准入边界转换为历史编辑刻度；该刻度不代表供应商编码帧率。
func normalizeSecondsRangeJSON(raw []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	startRaw, hasStart := fields["startSeconds"]
	endRaw, hasEnd := fields["endSeconds"]
	if !hasStart && !hasEnd {
		return raw, nil
	}
	if !hasStart || !hasEnd || fields["inFrame"] != nil || fields["outFrame"] != nil || bytes.Equal(bytes.TrimSpace(startRaw), []byte("null")) || bytes.Equal(bytes.TrimSpace(endRaw), []byte("null")) {
		return nil, ErrInvalid
	}
	var start, end float64
	if json.Unmarshal(startRaw, &start) != nil || json.Unmarshal(endRaw, &end) != nil || math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) || start < 0 || end > 30 || end-start < .5 {
		return nil, ErrInvalid
	}
	delete(fields, "startSeconds")
	delete(fields, "endSeconds")
	fields["inFrame"], _ = json.Marshal(int(math.Round(start * 30)))
	fields["outFrame"], _ = json.Marshal(int(math.Round(end * 30)))
	return json.Marshal(fields)
}

// 以累计秒数分配各段刻度，避免逐段取整让合法的整片时长多/少一个刻度。
func (p *Plan) UnmarshalJSON(raw []byte) error {
	type plain Plan
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	var shots []map[string]json.RawMessage
	if err := json.Unmarshal(fields["shots"], &shots); err != nil {
		return err
	}
	ranges := make([]map[string]json.RawMessage, len(shots))
	allSeconds := len(shots) > 0
	for i, shot := range shots {
		if json.Unmarshal(shot["creative"], &ranges[i]) != nil || ranges[i]["startSeconds"] == nil || ranges[i]["endSeconds"] == nil {
			allSeconds = false
			break
		}
	}
	if allSeconds {
		elapsed := 0.0
		for i, shot := range shots {
			c := ranges[i]
			normalized, err := normalizeSecondsRangeJSON(shot["creative"])
			if err != nil {
				return err
			}
			var start, end, duration float64
			_ = json.Unmarshal(c["startSeconds"], &start)
			_ = json.Unmarshal(c["endSeconds"], &end)
			_ = json.Unmarshal(shot["duration"], &duration)
			origin := elapsed
			elapsed += end - start
			length := int(math.Round(elapsed*30) - math.Round(origin*30))
			in := int(math.Round(start * 30))
			out := in + length
			if end <= duration && out > int(math.Round(duration*30)) {
				out = int(math.Round(duration * 30))
				in = out - length
			}
			_ = json.Unmarshal(normalized, &c)
			delete(c, "startSeconds")
			delete(c, "endSeconds")
			c["inFrame"], _ = json.Marshal(in)
			c["outFrame"], _ = json.Marshal(out)
			shot["creative"], _ = json.Marshal(c)
			var beats []map[string]json.RawMessage
			if len(shot["beats"]) > 0 && json.Unmarshal(shot["beats"], &beats) == nil {
				for _, beat := range beats {
					if beat["startSeconds"] == nil && beat["endSeconds"] == nil {
						continue
					}
					braw, _ := json.Marshal(beat)
					if _, err := normalizeSecondsRangeJSON(braw); err != nil {
						return err
					}
					var from, to float64
					_ = json.Unmarshal(beat["startSeconds"], &from)
					_ = json.Unmarshal(beat["endSeconds"], &to)
					delete(beat, "startSeconds")
					delete(beat, "endSeconds")
					beat["inFrame"], _ = json.Marshal(in + int(math.Round((origin+from-start)*30)-math.Round(origin*30)))
					beat["outFrame"], _ = json.Marshal(in + int(math.Round((origin+to-start)*30)-math.Round(origin*30)))
				}
				shot["beats"], _ = json.Marshal(beats)
			}
		}
		fields["shots"], _ = json.Marshal(shots)
		raw, _ = json.Marshal(fields)
	}
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*p = Plan(value)
	return nil
}
