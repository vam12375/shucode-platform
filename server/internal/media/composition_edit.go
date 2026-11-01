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
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var compositionIdentity = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var compositionColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func decodeCompositionFields(raw []byte, value any, required, optional []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, ErrInvalid
	}
	// encoding/json按大小写不敏感匹配struct，额外核对原始key以阻止Voiceover/EndTitle别名覆盖。
	for key := range fields {
		if !slices.Contains(required, key) && !slices.Contains(optional, key) {
			return nil, ErrInvalid
		}
	}
	for _, key := range required {
		if v, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, ErrInvalid
		}
	}
	for _, key := range optional {
		if v, ok := fields[key]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, ErrInvalid
		}
	}
	return fields, nil
}

// 镜头0和入点0都是合法值，不能用Go零值替代客户端必填/非null约束。
func (c *VideoCompositionClip) UnmarshalJSON(raw []byte) error {
	type plain VideoCompositionClip
	var value plain
	if _, err := decodeCompositionFields(raw, &value, []string{"sourceShotIndex", "inFrame", "outFrame", "speed"}, []string{"subtitle"}); err != nil {
		return err
	}
	*c = VideoCompositionClip(value)
	return nil
}

func (b *VideoBrand) UnmarshalJSON(raw []byte) error {
	type plain VideoBrand
	var value plain
	fields, err := decodeCompositionFields(raw, &value, []string{"preset", "primaryColor", "backgroundColor", "textColor", "fontFamily"}, []string{"logoAssetId", "endTitle", "endSubtitle"})
	if err != nil {
		return err
	}
	value.LogoAssetID = strings.TrimSpace(value.LogoAssetID)
	if _, present := fields["logoAssetId"]; present && !compositionIdentity.MatchString(value.LogoAssetID) {
		return ErrInvalid
	}
	value.EndTitle, value.EndSubtitle = strings.TrimSpace(value.EndTitle), strings.TrimSpace(value.EndSubtitle)
	*b = VideoBrand(value)
	return nil
}

// Go 的 bool 零值不能代表字段已提交；严格记录 required 字段，保持客户端 schema 同构。
func (e *VideoCompositionEdit) UnmarshalJSON(raw []byte) error {
	type plain VideoCompositionEdit
	var value plain
	if _, err := decodeCompositionFields(raw, &value, []string{"fps", "clips", "voiceover", "music", "subtitles"}, []string{"brand", "musicVolume"}); err != nil {
		return err
	}
	*e = VideoCompositionEdit(value)
	return nil
}

func validateVideoBrand(b *VideoBrand) error {
	if b == nil {
		return nil
	}
	if !slices.Contains([]string{"clean", "editorial", "bold"}, b.Preset) || !slices.Contains([]string{"sans", "serif"}, b.FontFamily) || !compositionColor.MatchString(b.PrimaryColor) || !compositionColor.MatchString(b.BackgroundColor) || !compositionColor.MatchString(b.TextColor) || utf8.RuneCountInString(strings.TrimSpace(b.EndTitle)) > 80 || utf8.RuneCountInString(strings.TrimSpace(b.EndSubtitle)) > 160 || b.LogoAssetID != "" && !compositionIdentity.MatchString(b.LogoAssetID) {
		return ErrInvalid
	}
	return nil
}

func validateCompositionEdit(e VideoCompositionEdit) error {
	if e.FPS != 30 || len(e.Clips) < 1 || len(e.Clips) > 30 || !slices.Contains([]string{"none", "short"}, e.Subtitles) {
		return ErrInvalid
	}
	if e.MusicVolume != nil && (math.IsNaN(*e.MusicVolume) || math.IsInf(*e.MusicVolume, 0) || *e.MusicVolume < 0 || *e.MusicVolume > 1) {
		return ErrInvalid
	}
	if err := validateVideoBrand(e.Brand); err != nil {
		return err
	}
	seen := map[int]bool{}
	total := 0
	for _, c := range e.Clips {
		if c.SourceShotIndex < 0 || c.SourceShotIndex > 29 || seen[c.SourceShotIndex] || c.InFrame < 0 || c.InFrame > 899 || c.OutFrame < 1 || c.OutFrame > 900 || c.OutFrame <= c.InFrame || !slices.Contains([]float64{.5, 1, 1.5, 2}, c.Speed) || c.Subtitle != nil && utf8.RuneCountInString(*c.Subtitle) > 300 {
			return ErrInvalid
		}
		seen[c.SourceShotIndex] = true
		frames := int(math.Round(float64(c.OutFrame-c.InFrame) / c.Speed))
		if frames < 15 {
			return ErrInvalid
		}
		total += frames
	}
	if total < 150 || total > 3600 {
		return ErrInvalid
	}
	return nil
}

func (s *Service) validateBrandAsset(ctx context.Context, p *Project, b *VideoBrand) error {
	if err := validateVideoBrand(b); err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	if err := validateCompositionCopy(p, VideoCompositionEdit{Subtitles: "none", Brand: b}); err != nil {
		return err
	}
	if !BrandCompositionAvailable() {
		return fmt.Errorf("composition_dependencies_missing")
	}
	if b.LogoAssetID == "" {
		return nil
	}
	if !slices.Contains(p.Brief.AssetIDs, b.LogoAssetID) {
		return fmt.Errorf("composition_logo_unavailable")
	}
	assets, err := s.Repo.Assets(ctx, p.TeamID)
	if err != nil {
		return err
	}
	for _, a := range assets {
		if a.ID == b.LogoAssetID && a.Kind == "image" && !a.Internal {
			file, openErr := os.Open(s.Blobs.Path(p.TeamID, a.ID))
			if openErr != nil {
				return fmt.Errorf("composition_logo_unavailable")
			}
			defer file.Close()
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxUpload {
				return fmt.Errorf("composition_logo_unavailable")
			}
			if !compositionImageReadable(ctx, s.Blobs.Path(p.TeamID, a.ID)) {
				return fmt.Errorf("composition_logo_unavailable")
			}
			return nil
		}
	}
	return fmt.Errorf("composition_logo_unavailable")
}

func compositionImageReadable(ctx context.Context, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "stream=codec_name,width,height", "-of", "json", path)
	output := boundedReferenceOutput{limit: 32 << 10}
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || output.exceeded {
		return false
	}
	var meta struct {
		Streams []struct {
			Codec  string `json:"codec_name"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(output.Bytes(), &meta) != nil || len(meta.Streams) != 1 {
		return false
	}
	v := meta.Streams[0]
	return v.Width > 0 && v.Height > 0 && v.Width <= 8192 && v.Height <= 8192 && int64(v.Width)*int64(v.Height) <= 16<<20 && slices.Contains([]string{"png", "mjpeg", "webp", "gif"}, v.Codec)
}

func videoProductAssetIDs(b Brief) []string {
	ids := make([]string, 0, len(b.AssetIDs))
	logo := ""
	if b.Finishing != nil && b.Finishing.Brand != nil {
		logo = b.Finishing.Brand.LogoAssetID
	}
	for _, id := range b.AssetIDs {
		if id != logo {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Service) narrationAvailable(ctx context.Context, p *Project) bool {
	if p.Narration == nil || p.Narration.Status != "ready" || !compositionIdentity.MatchString(p.Narration.AssetID) {
		return false
	}
	assets, err := s.Repo.Assets(ctx, p.TeamID)
	if err != nil {
		return false
	}
	for _, a := range assets {
		if a.ID == p.Narration.AssetID && a.Kind == "audio" && a.Internal {
			file, err := os.Open(s.Blobs.Path(p.TeamID, a.ID))
			if err != nil {
				return false
			}
			info, err := file.Stat()
			file.Close()
			return err == nil && info.Mode().IsRegular() && info.Size() > 0
		}
	}
	return false
}

func (s *Service) ProjectView(ctx context.Context, p *Project) ProjectView {
	view := p.View()
	view.NarrationAvailable = s.narrationAvailable(ctx, p)
	return view
}

func originalCompositionTimeline(p *Project, e VideoCompositionEdit) bool {
	if len(e.Clips) != len(p.Shots) {
		return false
	}
	for i, c := range e.Clips {
		if c.SourceShotIndex != i || c.InFrame != 0 || c.OutFrame != p.Shots[i].Plan.Duration*30 || c.Speed != 1 {
			return false
		}
	}
	return true
}

func (s *Service) validateRecompose(ctx context.Context, p *Project, e VideoCompositionEdit) error {
	if p.Canvas != nil || p.Agent != nil || p.Brief.Mode != "auto" || p.Brief.Finishing == nil || p.Brief.ReferenceAssetID != "" || p.Brief.GenerationMode == "video_reference" || p.CancelRequested || len(p.Shots) == 0 || (p.Status != "completed" && p.Status != "failed") || p.LeaseOwner != "" && p.LeaseUntil.After(time.Now()) {
		return ErrConflict
	}
	return s.validateCompositionMedia(ctx, p, e)
}

func (s *Service) validateCompositionMedia(ctx context.Context, p *Project, e VideoCompositionEdit) error {
	if !FinishingCompositionAvailable() {
		return fmt.Errorf("composition_dependencies_missing")
	}
	if err := validateCompositionEdit(e); err != nil {
		return err
	}
	if err := validateCompositionCopy(p, e); err != nil {
		return err
	}
	if p.Narration != nil && (p.Narration.Status == "submitting" || p.Narration.Status == "unknown") {
		return fmt.Errorf("narration_submission_unknown")
	}
	assets, err := s.Repo.Assets(ctx, p.TeamID)
	if err != nil {
		return err
	}
	for _, shot := range p.Shots {
		if shot.Status != "succeeded" || !compositionIdentity.MatchString(shot.OutputAssetID) {
			return ErrConflict
		}
	}
	for _, clip := range e.Clips {
		if clip.SourceShotIndex >= len(p.Shots) {
			return ErrInvalid
		}
		shot := p.Shots[clip.SourceShotIndex]
		found := false
		for _, asset := range assets {
			if asset.ID == shot.OutputAssetID && asset.Kind == "video" {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("composition_source_unavailable")
		}
		frames, probeErr := probeCompositionSourceFrames(ctx, s.Blobs.Path(p.TeamID, shot.OutputAssetID))
		if probeErr != nil || clip.OutFrame > frames {
			return fmt.Errorf("composition_source_unavailable")
		}
	}
	if e.Voiceover {
		if !originalCompositionTimeline(p, e) {
			return fmt.Errorf("composition_voiceover_timeline_changed")
		}
		if !s.narrationAvailable(ctx, p) {
			return fmt.Errorf("composition_narration_unavailable")
		}
	}
	if e.Music {
		if err := s.validateFinishing(ctx, Brief{Mode: "auto", Finishing: &FinishingOptions{Music: true, Subtitles: "none"}}); err != nil {
			return err
		}
	}
	return s.validateBrandAsset(ctx, p, e.Brand)
}

// 文案约束按实际交付时窗计算；关闭字幕不检查不烧录的旧文字，品牌尾卡始终可见。
func validateCompositionCopy(p *Project, e VideoCompositionEdit) error {
	terms := strings.FieldsFunc(strings.ToLower(p.Brief.AvoidClaims), func(r rune) bool { return strings.ContainsRune(";；,，、\n", r) })
	forbidden := func(text string) bool {
		for _, term := range terms {
			if value := strings.TrimSpace(term); value != "" && strings.Contains(strings.ToLower(text), value) {
				return true
			}
		}
		return false
	}
	if e.Brand != nil && forbidden(e.Brand.EndTitle+"\n"+e.Brand.EndSubtitle) {
		return fmt.Errorf("composition_caption_invalid")
	}
	if e.Subtitles != "short" {
		return nil
	}
	timelineProject := *p
	timelineProject.Composition = &VideoCompositionState{Edit: e}
	segments, err := editableCompositionTimeline(&timelineProject)
	if err != nil {
		return ErrInvalid
	}
	for index, clip := range e.Clips {
		if clip.SourceShotIndex < 0 || clip.SourceShotIndex >= len(p.Shots) {
			return ErrInvalid
		}
		text := p.Shots[clip.SourceShotIndex].Plan.Subtitle
		if clip.Subtitle != nil {
			text = *clip.Subtitle
		}
		text = strings.TrimSpace(text)
		// 文案预算依据真实ASS窗口：片头片尾留白和尾卡遮挡不能计作可读时间。
		start, end := finishingCaptionWindow(&timelineProject, segments, index)
		if forbidden(text) || text != "" && (end <= start || narrationCopyWidth(text) > math.Floor((end-start)*5.5)) {
			return fmt.Errorf("composition_caption_invalid")
		}
		lines := 0
		for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lines++
			if narrationCopyWidth(line) > 18 {
				return fmt.Errorf("composition_caption_invalid")
			}
		}
		if lines > 2 {
			return fmt.Errorf("composition_caption_invalid")
		}
	}
	return nil
}

// Recompose 只接受同制作单的已有成功镜头；持久化身份先于worker，永不进入付费准入。
func (s *Service) Recompose(ctx context.Context, team, user, id string, version int64, requestID string, edit VideoCompositionEdit) (*Project, error) {
	if version <= 0 || !compositionIdentity.MatchString(requestID) {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(edit)
	if err != nil {
		return nil, ErrInvalid
	}
	var frozen VideoCompositionEdit
	if json.Unmarshal(raw, &frozen) != nil {
		return nil, ErrInvalid
	}
	if frozen.Brand != nil {
		frozen.Brand.EndTitle = strings.TrimSpace(frozen.Brand.EndTitle)
		frozen.Brand.EndSubtitle = strings.TrimSpace(frozen.Brand.EndSubtitle)
	}
	raw, _ = json.Marshal(frozen)
	hash := sha256.Sum256(raw)
	signature := hex.EncodeToString(hash[:])
	p, err := s.Repo.Get(ctx, team, id)
	if err != nil {
		return nil, err
	}
	if p == nil || p.UserID != user {
		return nil, fmt.Errorf("composition_project_unavailable")
	}
	if receipt, ok := p.CompositionReceipts[requestID]; ok {
		if receipt.Signature != signature {
			return nil, ErrConflict
		}
		return p, nil
	}
	if p.Version != version {
		return nil, ErrConflict
	}
	if err = s.validateRecompose(ctx, p, frozen); err != nil {
		return nil, err
	}
	next, err := s.Mutate(ctx, team, id, version, func(v *Project) error {
		if v.CancelRequested || v.UserID != user || v.LeaseOwner != "" && v.LeaseUntil.After(time.Now()) {
			return ErrConflict
		}
		if v.CompositionReceipts == nil {
			v.CompositionReceipts = map[string]compositionReceipt{}
		}
		v.CompositionReceipts[requestID] = compositionReceipt{Signature: signature, SourceVersion: version}
		v.Composition = &VideoCompositionState{RequestID: requestID, SourceVersion: version, Edit: frozen}
		v.CompositionAssetID = ""
		v.Status = "composing"
		v.Error = ""
		return nil
	})
	if errors.Is(err, ErrConflict) {
		// 并发同requestId只有一个CAS胜者；输家复用已接受事实，避免重投成新操作。
		if latest, readErr := s.Repo.Get(ctx, team, id); readErr == nil && latest != nil && latest.UserID == user {
			if receipt, ok := latest.CompositionReceipts[requestID]; ok && receipt.Signature == signature {
				return latest, nil
			}
		}
	}
	if err == nil && p.CompositionAssetID != "" {
		_ = s.cleanupRetiredComposition(ctx, p, p.CompositionAssetID)
	}
	return next, err
}
