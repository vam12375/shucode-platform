package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/netguard"
	"github.com/vam12375/shucode-platform/server/internal/upstreamurl"
)

const maxNarrationBytes = 32 << 20
const narrationRequestTimeout = 2 * time.Minute

// NarrationConfig 只来自管理员配置；不得从制作单接收端点、密钥或任意声音 URL。
type NarrationConfig struct {
	BaseURL, Model, Voice, APIKey string
	Client                        *http.Client
	AllowInsecure                 bool
}

func (c *NarrationConfig) Ready() bool {
	if c == nil || strings.TrimSpace(c.Model) == "" || strings.TrimSpace(c.Voice) == "" || strings.TrimSpace(c.APIKey) == "" || len(c.Model) > 200 || len(c.Voice) > 200 {
		return false
	}
	u, err := url.Parse(c.BaseURL)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || (u.Scheme == "http" && c.AllowInsecure))
}

func validateFinishingOptions(b Brief) error {
	if b.Finishing == nil {
		return nil
	}
	if b.Mode != "auto" || b.ReferenceAssetID != "" || b.SourceOnly || b.GenerationMode == "video_reference" || (b.Finishing.Subtitles != "none" && b.Finishing.Subtitles != "short") {
		return ErrInvalid
	}
	return nil
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
	if b.Finishing.Voiceover {
		if strings.TrimSpace(joined.String()) == "" {
			return fmt.Errorf("narration_empty")
		}
		if b.Script != "" && joined.String() != b.Script {
			return fmt.Errorf("narration_script_mismatch")
		}
		if narrationCopyWidth(joined.String()) > math.Floor(float64(b.Duration)*4.5) {
			return fmt.Errorf("narration_too_long")
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
	if b.Finishing.Voiceover && !s.Narration.Ready() {
		return fmt.Errorf("narration_not_configured")
	}
	if b.Finishing.Music {
		file, err := os.Open(s.BackgroundMusicPath)
		if err != nil {
			return fmt.Errorf("background_music_not_configured")
		}
		info, statErr := file.Stat()
		file.Close()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128<<20 {
			return fmt.Errorf("background_music_not_configured")
		}
		seconds, probeErr := probeFinishingAudio(ctx, s.BackgroundMusicPath)
		if probeErr != nil || seconds < 1 {
			return fmt.Errorf("background_music_not_configured")
		}
	}
	return nil
}

func (s *Service) FinishingCapabilities(ctx context.Context) map[string]any {
	capabilities := make(map[string]any)
	for key, value := range Capabilities() {
		capabilities[key] = value
	}
	capabilities["researchPlatforms"] = []string{"douyin", "xiaohongshu", "bilibili"}
	available := FinishingCompositionAvailable()
	capabilities["cinematicComposition"], capabilities["subtitleStyle"] = available, available
	capabilities["voiceover"] = available && s.Narration.Ready()
	capabilities["bgm"] = available && s.validateFinishing(ctx, Brief{Mode: "auto", Finishing: &FinishingOptions{Music: true, Subtitles: "none"}}) == nil
	return capabilities
}

func narrationSignature(c *NarrationConfig, text string) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{c.BaseURL, c.Model, c.Voice, text}, "\x00")))
	return hex.EncodeToString(hash[:])
}

type narrationHTTPError struct{ unknown bool }

func (e narrationHTTPError) Error() string {
	if e.unknown {
		return "narration_submission_unknown"
	}
	return "narration_generation_failed"
}

func (c *NarrationConfig) synthesize(ctx context.Context, text, requestID string) ([]byte, error) {
	if !c.Ready() {
		return nil, fmt.Errorf("narration_not_configured")
	}
	requestCtx, cancel := context.WithTimeout(ctx, narrationRequestTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": c.Model, "voice": c.Voice, "input": text, "response_format": "wav"})
	req, err := http.NewRequestWithContext(requestCtx, "POST", upstreamurl.Join(c.BaseURL, "/v1/audio/speech"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("narration_not_configured")
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", requestID)
	client := http.Client{Transport: netguard.NewTransport(false), Timeout: narrationRequestTimeout}
	if c.Client != nil {
		client = *c.Client
	}
	// 供应商重定向不能携带配音凭据到另一来源，测试注入 client 也保持同一边界。
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, narrationHTTPError{unknown: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 仅明确的请求/权限/配额拒绝可视为未生成，5xx/超时等保持未知受理。
		rejected := resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 405 || resp.StatusCode == 413 || resp.StatusCode == 415 || resp.StatusCode == 422 || resp.StatusCode == 429
		return nil, narrationHTTPError{unknown: !rejected}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxNarrationBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxNarrationBytes {
		return nil, narrationHTTPError{unknown: true}
	}
	return raw, nil
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

func (s *Service) prepareNarration(ctx context.Context, p *Project, dir string) (string, error) {
	if p.Brief.Finishing == nil || !p.Brief.Finishing.Voiceover {
		return "", nil
	}
	if err := s.validateFinishing(ctx, p.Brief); err != nil {
		return "", err
	}
	plan := narrationPlan(p)
	if err := validateFinishingPlan(p.Brief, plan); err != nil {
		return "", err
	}
	text := narrationText(p.Brief, plan.Shots)
	signature := narrationSignature(s.Narration, text)
	current, err := s.Repo.Get(ctx, p.TeamID, p.ID)
	if err != nil {
		return "", err
	}
	if current == nil || current.CancelRequested || current.LeaseOwner != s.WorkerID || !current.LeaseUntil.After(time.Now()) || narrationText(current.Brief, narrationPlan(current).Shots) != text {
		return "", ErrConflict
	}
	if current.Narration != nil && (current.Narration.Status == "submitting" || current.Narration.Status == "unknown") {
		return "", fmt.Errorf("narration_submission_unknown")
	}
	// 配置变化只作用于新脚本；同制作单重试必须复用已经确认的音轨，不得再次付费。
	if current.Narration != nil && current.Narration.Text == text && current.Narration.Status == "ready" {
		assets, readErr := s.Repo.Assets(ctx, p.TeamID)
		if readErr != nil {
			return "", readErr
		}
		for _, asset := range assets {
			if asset.ID == current.Narration.AssetID && asset.Kind == "audio" && asset.Internal {
				path := s.Blobs.Path(p.TeamID, asset.ID)
				if _, probeErr := probeFinishingAudio(ctx, path); probeErr != nil {
					return "", fmt.Errorf("narration_audio_invalid")
				}
				return path, nil
			}
		}
		return "", fmt.Errorf("narration_audio_invalid")
	}
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.TeamID+"\x00"+p.ID+"\x00"+signature)).String()
	admitted := false
	_, err = s.workerUpdate(ctx, p, func(v *Project) {
		admitted = false
		if v.CancelRequested || narrationText(v.Brief, narrationPlan(v).Shots) != text || v.Narration != nil && (v.Narration.Status == "submitting" || v.Narration.Status == "unknown") {
			return
		}
		v.Narration = &NarrationState{Signature: signature, RequestID: requestID, Text: text, Status: "submitting"}
		admitted = true
	})
	if err != nil {
		return "", err
	}
	if !admitted {
		return "", ErrConflict
	}
	raw, err := s.Narration.synthesize(ctx, text, requestID)
	if err != nil {
		status := "failed"
		var failure narrationHTTPError
		if errors.As(err, &failure) && failure.unknown {
			status = "unknown"
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.workerUpdate(cleanup, p, func(v *Project) {
			if v.Narration != nil && v.Narration.RequestID == requestID {
				v.Narration.Status = status
			}
		})
		return "", err
	}
	// 收到完整响应后先落盘再验证；之后任何中断仍保留 submitting，不能再次付费合成。
	asset, err := s.persistNarrationAudio(ctx, p.TeamID, raw)
	if err != nil {
		return "", err
	}
	bound := false
	_, err = s.workerUpdate(ctx, p, func(v *Project) {
		bound = false
		if v.CancelRequested || v.Narration == nil || v.Narration.RequestID != requestID || narrationText(v.Brief, narrationPlan(v).Shots) != text {
			return
		}
		v.Narration.Status, v.Narration.AssetID, v.Narration.DurationSeconds = "ready", asset.ID, asset.DurationSeconds
		bound = true
	})
	if err != nil {
		return "", err
	}
	if !bound {
		return "", ErrConflict
	}
	return s.Blobs.Path(p.TeamID, asset.ID), nil
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
