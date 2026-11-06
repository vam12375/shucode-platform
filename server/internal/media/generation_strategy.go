package media

import (
	"fmt"
	"slices"
)

type GenerationBeat struct {
	InFrame     int    `json:"inFrame"`
	OutFrame    int    `json:"outFrame"`
	Description string `json:"description"`
}
type GenerationCut struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
}
type Segmentation struct {
	Reason string          `json:"reason"`
	Cuts   []GenerationCut `json:"cuts"`
}

func (v *GenerationBeat) UnmarshalJSON(raw []byte) error {
	type plain GenerationBeat
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"inFrame", "outFrame", "description"}, nil); err != nil {
		return err
	}
	*v = GenerationBeat(x)
	return nil
}
func (v *GenerationCut) UnmarshalJSON(raw []byte) error {
	type plain GenerationCut
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"kind", "description"}, nil); err != nil {
		return err
	}
	*v = GenerationCut(x)
	return nil
}
func (v *Segmentation) UnmarshalJSON(raw []byte) error {
	type plain Segmentation
	var x plain
	if _, err := decodeCompositionFields(raw, &x, []string{"reason", "cuts"}, nil); err != nil {
		return err
	}
	*v = Segmentation(x)
	return nil
}

// 拍点不能决定生成请求数；与客户端同序比较任务数、生成余量、段长均衡。
func planGenerationDurations(total int, model Model) []int {
	if total < 1 || total > 300 {
		return nil
	}
	options := []int{}
	for _, d := range model.AllowedDurations() {
		if d >= 2 && d <= 30 {
			options = append(options, d)
		}
	}
	slices.Sort(options)
	options = slices.Compact(options)
	if len(options) == 0 {
		return nil
	}
	longest := options[len(options)-1]
	count := (total + longest - 1) / longest
	if count > 30 {
		return nil
	}
	type candidate struct {
		score     int
		durations []int
	}
	states := map[int]candidate{0: {}}
	for range count {
		next := map[int]candidate{}
		sums := make([]int, 0, len(states))
		for sum := range states {
			sums = append(sums, sum)
		}
		slices.Sort(sums)
		for _, sum := range sums {
			previous := states[sum]
			for _, d := range options {
				length, score := sum+d, previous.score+d*d
				if length > total+longest {
					continue
				}
				if existing, ok := next[length]; ok && score >= existing.score {
					continue
				}
				next[length] = candidate{score, append(slices.Clone(previous.durations), d)}
			}
		}
		states = next
	}
	for sum := total; sum <= total+longest; sum++ {
		if result, ok := states[sum]; ok {
			slices.Sort(result.durations)
			slices.Reverse(result.durations)
			return result.durations
		}
	}
	return nil
}

func validateGenerationPlan(b Brief, p Plan, m Model) error {
	if b.GenerationPolicy != "whole-first" {
		return nil
	}
	minimum := planGenerationDurations(b.Duration, m)
	if len(minimum) == 0 {
		return fmt.Errorf("video_generation_capability_missing")
	}
	if len(minimum) == 1 && len(p.Shots) != 1 {
		return fmt.Errorf("video_generation_single_required")
	}
	if len(p.Shots) > 1 {
		if p.Segmentation == nil || len(p.Segmentation.Cuts) != len(p.Shots)-1 || !storyText(p.Segmentation.Reason, 1000, true) {
			return fmt.Errorf("video_generation_cuts_required")
		}
		for _, cut := range p.Segmentation.Cuts {
			if !slices.Contains([]string{"action", "scene", "sentence", "dialogue-turn"}, cut.Kind) || !storyText(cut.Description, 500, true) {
				return fmt.Errorf("video_generation_cuts_invalid")
			}
		}
	} else if p.Segmentation != nil {
		return fmt.Errorf("video_generation_cuts_invalid")
	}
	frames := 0
	for _, shot := range p.Shots {
		if !m.DurationSupported(shot.Duration) {
			return fmt.Errorf("video_generation_duration_unsupported")
		}
		c := shot.Creative
		if c == nil || c.OutFrame > shot.Duration*30 || len(shot.Beats) == 0 || len(shot.Beats) > 30 {
			return fmt.Errorf("video_generation_beats_invalid")
		}
		cursor := c.InFrame
		for _, beat := range shot.Beats {
			if beat.InFrame != cursor || beat.OutFrame-beat.InFrame < 15 || !storyText(beat.Description, 500, true) {
				return fmt.Errorf("video_generation_beats_invalid")
			}
			cursor = beat.OutFrame
		}
		if cursor != c.OutFrame {
			return fmt.Errorf("video_generation_beats_invalid")
		}
		frames += c.OutFrame - c.InFrame
	}
	if frames != b.Duration*30 {
		return fmt.Errorf("video_generation_delivery_mismatch")
	}
	return nil
}
