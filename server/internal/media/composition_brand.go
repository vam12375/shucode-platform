package media

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

var brandCompositionCheck struct {
	sync.Once
	available bool
}

// 包装能力只在真实滤镜和固定中日韩字体可用时开放，不能让客户端先提交再借任意字体兜底。
func BrandCompositionAvailable() bool {
	brandCompositionCheck.Do(func() {
		if !FinishingCompositionAvailable() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-filters").Output()
		if err != nil {
			return
		}
		for _, filter := range []string{"overlay", "scale", "trim", "setpts", "fps", "blackdetect", "silencedetect"} {
			if !strings.Contains(string(raw), " "+filter+" ") {
				return
			}
		}
		for _, font := range []string{"Noto Sans CJK SC", "Noto Serif CJK SC"} {
			matched, err := exec.CommandContext(ctx, "fc-match", "-f", "%{family}", font).Output()
			if err != nil || !strings.Contains(string(matched), strings.TrimSuffix(font, " SC")) {
				return
			}
		}
		brandCompositionCheck.available = true
	})
	return brandCompositionCheck.available
}

func brandFont(brand *VideoBrand) string {
	if brand != nil && brand.FontFamily == "serif" {
		return "Noto Serif CJK SC"
	}
	return "Noto Sans CJK SC"
}

func assBrandColor(hex string) string {
	// 仅供通过严格#RRGGBB校验的品牌字段，ASS使用反向BGR字节序。
	if len(hex) != 7 {
		return "&H00FFFFFF"
	}
	return "&H00" + hex[5:7] + hex[3:5] + hex[1:3]
}

func compositionHasTail(brand *VideoBrand) bool {
	return brand != nil && (brand.LogoAssetID != "" || strings.TrimSpace(brand.EndTitle) != "" || strings.TrimSpace(brand.EndSubtitle) != "")
}

func compositionTotalFrames(segments []compositionSegment) int {
	frames := 0
	for _, segment := range segments {
		frames += segment.frames
	}
	return frames
}

// 准入、ASS和QA共用实际可见时窗；尾卡遮挡不能按原clip时长虚增可读预算。
func finishingCaptionWindow(p *Project, segments []compositionSegment, index int) (start, end float64) {
	segment := segments[index]
	margin := min(.15, segment.seconds()/5)
	start, end = segment.start+margin, segment.end-margin
	if compositionHasTail(effectiveFinishingOptions(p).Brand) {
		end = min(end, float64(max(0, compositionTotalFrames(segments)-30))/30)
	}
	return start, end
}

func compositionASSScript(p *Project, segments []compositionSegment, w, h int) string {
	options := effectiveFinishingOptions(p)
	brand := options.Brand
	fontSize, marginLR, marginV := subtitleMetrics(w, h)
	primary, background, accent := "&H00FFFFFF", "&H00000000", "&H00C07000"
	backColor := "&H80000000"
	preset := "clean"
	if brand != nil {
		primary, background, accent = assBrandColor(brand.TextColor), assBrandColor(brand.BackgroundColor), assBrandColor(brand.PrimaryColor)
		backColor = background
		preset = brand.Preset
	}
	align, border, outline := 2, 1, 1
	if preset == "editorial" {
		align = 1
	}
	if preset == "bold" {
		border, outline = 3, max(3, min(w, h)/120)
	}
	font := brandFont(brand)
	ass := fmt.Sprintf("[Script Info]\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\nWrapStyle: 0\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,%s,%d,%s,%s,%s,%s,0,0,0,0,100,100,0,0,%d,%d,0,%d,%d,%d,%d,1\nStyle: Tail,%s,%d,%s,%s,%s,%s,-1,0,0,0,100,100,0,0,1,0,0,5,%d,%d,0,1\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n", w, h, font, max(20, fontSize*3/4), primary, primary, background, backColor, border, outline, align, marginLR, marginLR, marginV, font, max(18, min(w, h)/14), primary, primary, background, background, marginLR, marginLR)
	totalFrames := compositionTotalFrames(segments)
	tailStart := float64(max(0, totalFrames-30)) / 30
	for i := range segments {
		text := finishingSubtitle(p, i)
		if options.Subtitles != "short" || text == "" {
			continue
		}
		start, end := finishingCaptionWindow(p, segments, i)
		if end > start {
			ass += fmt.Sprintf("Dialogue: 1,%s,%s,Default,,0,0,0,,%s\n", subtitleTime(start), subtitleTime(end), assText(text))
		}
	}
	if !compositionHasTail(brand) {
		return ass
	}
	start, end := subtitleTime(tailStart), subtitleTime(float64(totalFrames)/30)
	// 背景和文字共享末30帧，尾卡不额外追加时长或移动原镜头边界。
	ass += fmt.Sprintf("Dialogue: 2,%s,%s,Tail,,0,0,0,,{\\an7\\pos(0,0)\\p1\\bord0\\1c%s}m 0 0 l %d 0 l %d %d l 0 %d{\\p0}\n", start, end, background, w, w, h, h)
	x, y, anchor := w/2, h*46/100, 5
	if preset == "editorial" {
		x, anchor = max(w*8/100, 12), 4
		ass += fmt.Sprintf("Dialogue: 3,%s,%s,Tail,,0,0,0,,{\\an7\\pos(0,0)\\p1\\bord0\\1c%s}m %d %d l %d %d l %d %d l %d %d{\\p0}\n", start, end, accent, w*4/100, h*30/100, w*5/100, h*30/100, w*5/100, h*72/100, w*4/100, h*72/100)
	}
	if preset == "bold" {
		ass += fmt.Sprintf("Dialogue: 3,%s,%s,Tail,,0,0,0,,{\\an7\\pos(0,0)\\p1\\bord0\\1c%s}m 0 0 l %d 0 l %d %d l 0 %d{\\p0}\n", start, end, accent, w, w, max(3, h/20), max(3, h/20))
	}
	if brand.EndTitle != "" {
		ass += fmt.Sprintf("Dialogue: 4,%s,%s,Tail,,0,0,0,,{\\an%d\\pos(%d,%d)}%s\n", start, end, anchor, x, y, assText(brand.EndTitle))
	}
	if brand.EndSubtitle != "" {
		ass += fmt.Sprintf("Dialogue: 4,%s,%s,Tail,,0,0,0,,{\\an%d\\pos(%d,%d)\\fs%d\\b0}%s\n", start, end, anchor, x, h*65/100, max(12, min(w, h)/25), assText(brand.EndSubtitle))
	}
	return ass
}

func (s *Service) compositionBrandLogoPath(ctx context.Context, p *Project, brand *VideoBrand) (string, error) {
	if brand == nil || brand.LogoAssetID == "" {
		return "", nil
	}
	if !slices.Contains(p.Brief.AssetIDs, brand.LogoAssetID) {
		return "", fmt.Errorf("composition_logo_invalid")
	}
	assets, err := s.Repo.Assets(ctx, p.TeamID)
	if err != nil {
		return "", err
	}
	var mime string
	for _, asset := range assets {
		if asset.ID == brand.LogoAssetID && asset.Kind == "image" && !asset.Internal {
			mime = asset.Mime
			break
		}
	}
	if !slices.Contains([]string{"image/png", "image/jpeg", "image/webp", "image/gif"}, mime) {
		return "", fmt.Errorf("composition_logo_invalid")
	}
	path := s.Blobs.Path(p.TeamID, brand.LogoAssetID)
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("composition_logo_invalid")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxUpload {
		return "", fmt.Errorf("composition_logo_invalid")
	}
	head := make([]byte, 512)
	n, _ := file.Read(head)
	if http.DetectContentType(head[:n]) != mime {
		return "", fmt.Errorf("composition_logo_invalid")
	}
	if !compositionImageReadable(ctx, path) {
		return "", fmt.Errorf("composition_logo_invalid")
	}
	return path, nil
}

func (s *Service) prepareCompositionBrandLogo(ctx context.Context, p *Project, brand *VideoBrand, dir string, w, h int) (string, error) {
	path, err := s.compositionBrandLogoPath(ctx, p, brand)
	if err != nil || path == "" {
		return "", err
	}
	// GIF等格式不是image2输入，直接-loop会失败；先取已校验图像的首帧并限制到尾卡Logo槽位。
	normalized := filepath.Join(dir, "brand-logo.png")
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,format=rgba", max(2, w/5), max(2, h/6))
	if err := runFinishingCommand(ctx, dir, "composition_logo_invalid", "-protocol_whitelist", "file,pipe", "-i", path, "-map", "0:v:0", "-vf", filter, "-frames:v", "1", "-an", "-sn", "-dn", "-threads", "1", normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

func writeCompositionCaptions(p *Project, segments []compositionSegment, dir string, w, h int) error {
	return os.WriteFile(filepath.Join(dir, "captions.ass"), []byte(compositionASSScript(p, segments, w, h)), 0600)
}
