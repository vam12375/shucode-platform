package media

import (
	"fmt"
	"math"
)

// 后期时间线只派生交付帧，不改写供应商原镜头、合法秒数或计费身份。
func editableCompositionTimeline(p *Project) ([]compositionSegment, error) {
	if p.Composition == nil || p.Composition.Edit.FPS != 30 {
		return nil, fmt.Errorf("composition_edit_invalid")
	}
	edit := p.Composition.Edit
	if len(edit.Clips) < 1 || len(edit.Clips) > 30 {
		return nil, fmt.Errorf("composition_edit_invalid")
	}
	segments := make([]compositionSegment, 0, len(edit.Clips))
	boundary := 0
	seen := map[int]bool{}
	for _, clip := range edit.Clips {
		if clip.SourceShotIndex < 0 || seen[clip.SourceShotIndex] || clip.InFrame < 0 || clip.OutFrame <= clip.InFrame || (clip.Speed != .5 && clip.Speed != 1 && clip.Speed != 1.5 && clip.Speed != 2) {
			return nil, fmt.Errorf("composition_edit_invalid")
		}
		seen[clip.SourceShotIndex] = true
		frames := int(math.Round(float64(clip.OutFrame-clip.InFrame) / clip.Speed))
		if frames < 15 || frames > 3600 || boundary+frames > 3600 {
			return nil, fmt.Errorf("composition_edit_invalid")
		}
		segments = append(segments, compositionSegment{start: float64(boundary) / 30, end: float64(boundary+frames) / 30, frames: frames})
		boundary += frames
	}
	if boundary < 150 {
		return nil, fmt.Errorf("composition_edit_invalid")
	}
	return segments, nil
}

func finishingSourceClip(p *Project, index int) VideoCompositionClip {
	if p.Composition != nil {
		return p.Composition.Edit.Clips[index]
	}
	if storyPlanning(p.Brief) && index < len(p.Shots) {
		if creative := p.Shots[index].Plan.Creative; creative != nil {
			return VideoCompositionClip{SourceShotIndex: index, InFrame: creative.InFrame, OutFrame: creative.OutFrame, Speed: 1}
		}
	}
	return VideoCompositionClip{SourceShotIndex: index, InFrame: 0, OutFrame: p.Shots[index].Plan.Duration * 30, Speed: 1}
}

func finishingSubtitle(p *Project, index int) string {
	clip := finishingSourceClip(p, index)
	if clip.Subtitle != nil {
		return *clip.Subtitle
	}
	return p.Shots[clip.SourceShotIndex].Plan.Subtitle
}

func effectiveFinishingOptions(p *Project) FinishingOptions {
	var options FinishingOptions
	if p.Brief.Finishing != nil {
		options = *p.Brief.Finishing
	}
	if p.Composition != nil {
		edit := p.Composition.Edit
		options.Voiceover, options.Music, options.Subtitles, options.Brand = edit.Voiceover, edit.Music, edit.Subtitles, edit.Brand
	}
	return options
}
