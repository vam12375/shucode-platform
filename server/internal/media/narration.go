package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maxNarrationBytes = 32 << 20

func validateFinishingOptions(b Brief) error {
	if b.Finishing == nil {
		return nil
	}
	if b.Mode != "auto" || b.ReferenceAssetID != "" || b.SourceOnly || b.GenerationMode == "video_reference" || (b.Finishing.Subtitles != "none" && b.Finishing.Subtitles != "short") {
		return ErrInvalid
	}
	return validateVideoBrand(b.Finishing.Brand)
}

// copyWidth 与客户端等效字宽一致，避免英文按字节计费式的过严限制。
func narrationCopyWidth(text string) float64 {
	width := 0.0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if r >= 0x2e80 && r <= 0x9fff || r >= 0xf900 && r <= 0xfaff || r >= 0xff00 && r <= 0xffef {
			width++
		} else {
			width += .5
		}
	}
	return width
}

func narrationText(b Brief, shots []ShotPlan) string {
	parts := make([]string, len(shots))
	for i, shot := range shots {
		parts[i] = shot.Narration
	}
	// 全片脚本由分镜按序精确拼接；空白与标点由作者决定，不能在边界擅自增字。
	return strings.Join(parts, "")
}

func validateFinishingPlan(b Brief, plan Plan) error {
	if err := validateFinishingOptions(b); err != nil {
		return err
	}
	if b.Finishing == nil {
		return nil
	}
	var joined strings.Builder
	terms := strings.FieldsFunc(strings.ToLower(b.AvoidClaims), func(r rune) bool { return strings.ContainsRune(";；,，、\n", r) })
	for _, shot := range plan.Shots {
		joined.WriteString(shot.Narration)
		haystack := strings.ToLower(strings.Join([]string{shot.Subtitle, shot.Narration, shot.Prompt, shot.ImagePrompt, shot.Sound, shot.Subject, shot.Scene, shot.Action}, "\n"))
		for _, term := range terms {
			if value := strings.TrimSpace(term); value != "" && strings.Contains(haystack, value) {
				return fmt.Errorf("video_plan_copy_invalid")
			}
		}
	}
	for _, term := range terms {
		if value := strings.TrimSpace(term); value != "" && strings.Contains(strings.ToLower(joined.String()), value) {
			return fmt.Errorf("video_plan_copy_invalid")
		}
	}

	return nil
}

func probeFinishingAudio(ctx context.Context, file string) (float64, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "format=duration:stream=codec_type", "-of", "json", file).Output()
	var out struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Kind string `json:"codec_type"`
		} `json:"streams"`
	}
	if err != nil || json.Unmarshal(raw, &out) != nil || len(out.Streams) != 1 || out.Streams[0].Kind != "audio" {
		return 0, fmt.Errorf("finishing_audio_invalid")
	}
	var duration float64
	if _, err = fmt.Sscanf(out.Format.Duration, "%f", &duration); err != nil || !finitePositive(duration) {
		return 0, fmt.Errorf("finishing_audio_invalid")
	}
	return duration, nil
}

func (s *Service) validateFinishing(ctx context.Context, b Brief) error {
	if err := validateFinishingOptions(b); err != nil {
		return err
	}
	if b.Finishing == nil {
		return nil
	}
	if !FinishingCompositionAvailable() {
		return fmt.Errorf("composition_dependencies_missing")
	}
	// 已移除独立配音与授权音乐，不能再因旧配置缺服务阻断付费视频。

	return nil
}

func (s *Service) FinishingCapabilities(ctx context.Context) map[string]any {
	capabilities := make(map[string]any)
	for key, value := range Capabilities() {
		capabilities[key] = value
	}
	capabilities["researchPlatforms"] = []string{"douyin", "xiaohongshu", "bilibili"}
	available := FinishingCompositionAvailable()
	capabilities["wholeDurationPlanning"] = available
	capabilities["imageDrivenCreation"] = available
	capabilities["stagedImageAnalysis"] = available
	capabilities["stagedTemplateSelection"] = available
	capabilities["templateAutoGeneration"] = available
	capabilities["stagedCreativeDirection"] = available
	capabilities["planningResults"] = true
	// 直出复用图片优先入口与方案原文接收，只在两者都可用时声明。
	capabilities["directPromptCreation"] = available
	capabilities["retryModelSelection"] = available
	// 客户端只有看到该声明才会提交超过 20 张图片的制作单。
	capabilities["maxBriefImages"] = MaxBriefImages
	capabilities["cinematicComposition"], capabilities["subtitleStyle"] = available, available
	branded := BrandCompositionAvailable()
	capabilities["brandComposition"], capabilities["editableComposition"], capabilities["compositionQuality"] = branded, branded, branded
	capabilities["voiceover"] = false
	capabilities["bgm"] = false
	return capabilities
}

func narrationPlan(p *Project) Plan {
	if p.Plan != nil {
		return *p.Plan
	}
	plan := Plan{Shots: make([]ShotPlan, len(p.Shots))}
	for i, shot := range p.Shots {
		plan.Shots[i] = shot.Plan
	}
	return plan
}

func (s *Service) persistNarrationAudio(ctx context.Context, team string, raw []byte) (Asset, error) {
	id, size, err := s.Blobs.Put(team, bytes.NewReader(raw), maxNarrationBytes)
	if err != nil {
		return Asset{}, fmt.Errorf("narration_storage_failed")
	}
	path := s.Blobs.Path(team, id)
	duration, err := probeFinishingAudio(ctx, path)
	if err != nil {
		return Asset{}, fmt.Errorf("narration_audio_invalid")
	}
	decodeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	decoded, decodeErr := exec.CommandContext(decodeCtx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "info", "-xerror", "-protocol_whitelist", "file,pipe", "-i", path, "-map", "0:a:0", "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
	peak := regexp.MustCompile(`max_volume:\s*(-?[0-9.]+) dB`).FindSubmatch(decoded)
	peakDB := math.Inf(-1)
	if len(peak) == 2 {
		peakDB, _ = strconv.ParseFloat(string(peak[1]), 64)
	}
	// 原实现只看音轨是否存在，静音也会被算作成功配音；完整解码后拒绝不可听音轨。
	if decodeErr != nil || peakDB < -65 {
		return Asset{}, fmt.Errorf("narration_audio_invalid")
	}
	asset := Asset{ID: id, Name: "narration.wav", Kind: "audio", Mime: http.DetectContentType(raw), Size: size, CreatedAt: time.Now().UTC(), Internal: true, DurationSeconds: duration}
	if err = s.Repo.AddAsset(ctx, team, asset); err != nil {
		return Asset{}, fmt.Errorf("narration_storage_failed")
	}
	return asset, nil
}

func finishingFailureCode(err error) string {
	if err == nil {
		return ""
	}
	switch err.Error() {
	case "composition_dependencies_missing", "narration_not_configured", "background_music_not_configured", "narration_empty", "narration_too_long", "narration_script_mismatch", "narration_submission_unknown", "narration_generation_failed", "narration_storage_failed", "narration_audio_invalid", "finishing_audio_invalid", "video_plan_copy_invalid":
		return err.Error()
	}
	return ""
}
