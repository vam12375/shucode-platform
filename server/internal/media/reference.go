package media

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/netguard"
)

const SnapAnyExtractURL = "https://api.snapany.com/openapi/v1/extract/post"
const MaxReferenceBytes int64 = 200 << 20
const MaxReferenceSeconds = 90

// Ark Seedance 参考视频单片段时长下限；上限由模型能力 VideoReference.MaxDurationSeconds 控制。
const MinReferenceClipSeconds = 2
const maxReferenceProbeOutput = 256 << 10

type boundedReferenceOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedReferenceOutput) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining < len(p) {
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		b.exceeded = true
		return max(remaining, 0), io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

func NewReferenceHTTPClient(timeout time.Duration) *http.Client {
	transport := netguard.NewTransport(false)
	transport.Proxy = nil
	return &http.Client{Transport: transport, Timeout: timeout}
}

type SourceParser struct {
	Key        string
	Client     *http.Client
	ResolveKey func(context.Context) (string, error)
	Preflight  func(context.Context) error
}

func NormalizeSourceParserKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", fmt.Errorf("source_parser_unconfigured")
	}
	if len(key) > 4096 {
		return "", fmt.Errorf("source_parser_auth_failed")
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return "", fmt.Errorf("source_parser_auth_failed")
		}
	}
	return key, nil
}

func (p *SourceParser) resolved(ctx context.Context) (*SourceParser, error) {
	if p == nil {
		return nil, fmt.Errorf("source_parser_unconfigured")
	}
	key := p.Key
	if p.ResolveKey != nil {
		var err error
		key, err = p.ResolveKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("source_parser_configuration_unavailable")
		}
	}
	key, err := NormalizeSourceParserKey(key)
	if err != nil {
		return nil, err
	}
	return &SourceParser{Key: key, Client: p.Client, Preflight: p.Preflight}, nil
}

type referenceDownload struct {
	URL      string
	AudioURL string
	Headers  http.Header
	Quality  int
}
type ReferenceMetadata struct {
	DurationSeconds float64
	Width, Height   int
}

func canonicalReferenceURL(raw string) (string, error) {
	if len(raw) > 4096 || strings.ContainsAny(raw, "\\\r\n\t") {
		return "", fmt.Errorf("invalid_source_url")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return "", fmt.Errorf("invalid_source_url")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return "", fmt.Errorf("invalid_source_url")
	}
	if ip, err := netip.ParseAddr(host); err == nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified()) {
		return "", fmt.Errorf("invalid_source_url")
	}
	u.Fragment = ""
	return u.String(), nil
}

func sourceParserError(status int) error {
	switch status {
	case 401, 403:
		return fmt.Errorf("source_parser_auth_failed")
	case 402:
		return fmt.Errorf("source_parser_credits_exhausted")
	case 429:
		return fmt.Errorf("source_parser_rate_limited")
	default:
		return fmt.Errorf("source_parse_failed")
	}
}

func (p *SourceParser) Extract(ctx context.Context, source string) (referenceDownload, error) {
	resolved, err := p.resolved(ctx)
	if err != nil {
		return referenceDownload{}, err
	}
	p = resolved
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"url": source})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SnapAnyExtractURL, bytes.NewReader(body))
	if err != nil {
		return referenceDownload{}, fmt.Errorf("source_parser_unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "zh")
	baseClient := p.Client
	if baseClient == nil {
		baseClient = NewReferenceHTTPClient(60 * time.Second)
	}
	client := *baseClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return referenceDownload{}, fmt.Errorf("source_parser_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return referenceDownload{}, sourceParserError(resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return referenceDownload{}, fmt.Errorf("source_parser_invalid_response")
	}
	var out struct {
		Medias []struct {
			MediaType   string            `json:"media_type"`
			ResourceURL string            `json:"resource_url"`
			Headers     map[string]string `json:"headers"`
			Variants    []struct {
				VideoURL  string `json:"video_url"`
				AudioURL  string `json:"audio_url"`
				Ext       string `json:"video_ext"`
				Size      int64  `json:"video_filesize"`
				AudioSize int64  `json:"audio_filesize"`
				Quality   int    `json:"quality"`
			} `json:"variants"`
		} `json:"medias"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Medias) == 0 || len(out.Medias) > 32 {
		return referenceDownload{}, fmt.Errorf("source_parser_invalid_response")
	}
	candidates := []referenceDownload{}
	for _, m := range out.Medias {
		if m.MediaType != "video" {
			continue
		}
		if len(m.Variants) > 64 {
			return referenceDownload{}, fmt.Errorf("source_parser_invalid_response")
		}
		headers := make(http.Header)
		for name, value := range m.Headers {
			canonical := http.CanonicalHeaderKey(name)
			if (canonical == "User-Agent" || canonical == "Referer" || canonical == "Accept" || canonical == "Accept-Language") && len(value) <= 2048 && !strings.ContainsAny(value, "\r\n") {
				headers.Set(canonical, value)
			}
		}
		excluded := map[string]bool{}
		hasSplitStreams := false
		for _, v := range m.Variants {
			if v.AudioURL != "" {
				// 分离流的 resource_url 可能仍是无声画面；必须作为一对验证，不能退回伪完整单流。
				hasSplitStreams = true
				excluded[v.VideoURL] = true
				if (v.Ext == "" || v.Ext == "mp4" || v.Ext == "mov" || v.Ext == "webm" || v.Ext == "m4s") && v.Size >= 0 && v.AudioSize >= 0 && v.Size <= MaxReferenceBytes && v.AudioSize <= MaxReferenceBytes-v.Size && v.VideoURL != v.AudioURL && validReferenceStreamURL(v.VideoURL, false) && validReferenceStreamURL(v.AudioURL, true) {
					candidates = append(candidates, referenceDownload{URL: v.VideoURL, AudioURL: v.AudioURL, Headers: headers.Clone(), Quality: v.Quality})
				}
				continue
			}
			if (v.Ext != "" && v.Ext != "mp4" && v.Ext != "mov" && v.Ext != "webm") || v.Size > MaxReferenceBytes || v.Size < 0 {
				excluded[v.VideoURL] = true
				continue
			}
			if validReferenceDownloadURL(v.VideoURL) {
				candidates = append(candidates, referenceDownload{URL: v.VideoURL, Headers: headers.Clone(), Quality: v.Quality})
			}
		}
		if !hasSplitStreams && !excluded[m.ResourceURL] && validReferenceDownloadURL(m.ResourceURL) {
			candidates = append(candidates, referenceDownload{URL: m.ResourceURL, Headers: headers.Clone()})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Quality > candidates[j].Quality })
	if len(candidates) == 0 {
		return referenceDownload{}, fmt.Errorf("source_video_unavailable")
	}
	return candidates[0], nil
}

func validReferenceDownloadURL(raw string) bool {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\\\r\n\t") || !ValidURL(raw) {
		return false
	}
	u, _ := url.Parse(raw)
	if u.Scheme != "https" {
		return false
	}
	switch strings.ToLower(filepath.Ext(u.Path)) {
	case ".m3u8", ".mpd", ".ts", ".m4s", ".mp3", ".m4a", ".aac", ".jpg", ".png", ".webp":
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified()) {
		return false
	}
	return true
}

type referenceOperation struct {
	Digest  string    `json:"digest"`
	Status  string    `json:"status"`
	AssetID string    `json:"assetId,omitempty"`
	Asset   *Asset    `json:"asset,omitempty"`
	Fee     *FixedFee `json:"fee,omitempty"`
}

func (s *Service) ImportReference(ctx context.Context, team, user, id, rawURL string) (Asset, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Asset{}, ErrInvalid
	}
	source, err := canonicalReferenceURL(rawURL)
	if err != nil {
		return Asset{}, err
	}
	hash := sha256.Sum256([]byte(source))
	digest := hex.EncodeToString(hash[:])
	key := "media:reference-import:" + team + ":" + user + ":" + id
	old, err := s.Repo.Store.GetConfigValue(ctx, key)
	if err != nil {
		return Asset{}, err
	}
	if len(old) != 0 {
		return s.replayReference(ctx, team, user, key, digest, old)
	}
	// 在付费 admission 前读取并冻结配置；配置读取失败不能占用本次 operation。
	parser, err := s.SourceParser.resolved(ctx)
	if err != nil {
		return Asset{}, err
	}
	preflight := parser.Preflight
	if preflight == nil {
		preflight = referenceStreamDependencies
	}
	// 解析响应才能揭晓是否分轨，因此依赖与媒体盘必须在所有付费解析前检查。
	if err = preflight(ctx); err != nil {
		return Asset{}, err
	}
	if err = s.referenceImportStoragePreflight(team); err != nil {
		return Asset{}, err
	}
	pricing, err := s.Repo.ReferencePricing(ctx)
	if err != nil {
		return Asset{}, err
	}
	fee := newFixedFee(team, user, "media:source-import:"+id, "media:source-import", pricing.SnapAnyImportMicro)
	started, _ := json.Marshal(referenceOperation{Digest: digest, Status: "started", Fee: &fee})
	claimed, err := s.Repo.Store.CompareAndSwapConfigValue(ctx, key, nil, started)
	if err != nil {
		return Asset{}, err
	}
	if !claimed {
		old, err = s.Repo.Store.GetConfigValue(ctx, key)
		if err != nil {
			return Asset{}, err
		}
		return s.replayReference(ctx, team, user, key, digest, old)
	}
	// started 是付费解析的持久 admission；未知响应或进程退出均禁止同 operation 再次付费。
	finished, intentAttempted := false, false
	defer func() {
		if finished || intentAttempted {
			return
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		failed, _ := json.Marshal(referenceOperation{Digest: digest, Status: "failed", Fee: &fee})
		if ok, err := s.Repo.Store.CompareAndSwapConfigValue(persistCtx, key, started, failed); err == nil && ok {
			_ = s.releaseFixedFee(persistCtx, team, user, fee)
		}
	}()
	if err = s.admitFixedFee(ctx, team, user, fee); err != nil {
		return Asset{}, err
	}
	media, err := parser.Extract(ctx, source)
	if err != nil {
		return Asset{}, err
	}
	var asset Asset
	if media.AudioURL != "" {
		asset, err = s.prepareReferenceStreams(ctx, team, source, parser.Client, media)
	} else {
		asset, err = s.downloadSingleReference(ctx, team, source, parser.Client, media)
	}
	if err != nil {
		return Asset{}, err
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer persistCancel()
	pending, _ := json.Marshal(referenceOperation{Digest: digest, Status: "settlement_pending", AssetID: asset.ID, Asset: &asset, Fee: &fee})
	// CAS 响应未知时，意图可能已经提交；不能关闭收费身份或删除恢复需要的原件。
	intentAttempted = true
	ok, err := s.Repo.Store.CompareAndSwapConfigValue(persistCtx, key, started, pending)
	if err != nil {
		return Asset{}, fmt.Errorf("source_import_settlement_pending")
	}
	if !ok {
		latest, readErr := s.Repo.Store.GetConfigValue(persistCtx, key)
		if readErr != nil {
			return Asset{}, fmt.Errorf("source_import_settlement_pending")
		}
		return s.replayReference(persistCtx, team, user, key, digest, latest)
	}
	finished = true
	return s.replayReference(persistCtx, team, user, key, digest, pending)
}

func (s *Service) downloadSingleReference(ctx context.Context, team, source string, baseClient *http.Client, media referenceDownload) (Asset, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(downloadCtx, http.MethodGet, media.URL, nil)
	req.Header = media.Headers
	if baseClient == nil {
		baseClient = NewReferenceHTTPClient(90 * time.Second)
	}
	client := *baseClient
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 3 || !validReferenceDownloadURL(next.URL.String()) {
			return fmt.Errorf("source_redirect_forbidden")
		}
		if len(via) > 0 && !strings.EqualFold(next.URL.Host, via[len(via)-1].URL.Host) {
			next.Header.Del("Referer")
		}
		next.Header.Del("Authorization")
		next.Header.Del("Cookie")
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return Asset{}, fmt.Errorf("source_video_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Asset{}, fmt.Errorf("source_video_unavailable")
	}
	if resp.ContentLength > MaxReferenceBytes {
		return Asset{}, fmt.Errorf("source_video_too_large")
	}
	return s.prepareReference(downloadCtx, team, "原片.mp4", resp.Body, source)
}

func (s *Service) replayReference(ctx context.Context, team, user, key, digest string, raw []byte) (Asset, error) {
	var op referenceOperation
	if json.Unmarshal(raw, &op) != nil || op.Digest != digest {
		return Asset{}, ErrConflict
	}
	if op.Status == "settlement_pending" {
		if op.Asset == nil || op.Fee == nil || op.Asset.ID == "" || op.Asset.ID != op.AssetID || op.Asset.Kind != "video" || op.Fee.Micro < 0 {
			return Asset{}, ErrConflict
		}
		file, err := os.Stat(s.Blobs.Path(team, op.AssetID))
		if err != nil || !file.Mode().IsRegular() || file.Size() != op.Asset.Size {
			return Asset{}, fmt.Errorf("source_import_unavailable")
		}
		// 文件准备阶段不发布团队资产；只有同一账本身份结算成功后才允许用户复用。
		if err := s.settleFixedFee(ctx, team, user, "", *op.Fee); err != nil {
			return Asset{}, fmt.Errorf("source_import_settlement_pending")
		}
		if err := s.Repo.AddAsset(ctx, team, *op.Asset); err != nil {
			return Asset{}, fmt.Errorf("source_import_settlement_pending")
		}
		op.Status = "succeeded"
		op.Asset = nil
		succeeded, _ := json.Marshal(op)
		ok, err := s.Repo.Store.CompareAndSwapConfigValue(ctx, key, raw, succeeded)
		if err != nil {
			return Asset{}, fmt.Errorf("source_import_settlement_pending")
		}
		if !ok {
			latest, err := s.Repo.Store.GetConfigValue(ctx, key)
			if err != nil || bytes.Equal(latest, raw) {
				return Asset{}, fmt.Errorf("source_import_settlement_pending")
			}
			return s.replayReference(ctx, team, user, key, digest, latest)
		}
	}
	if op.Status != "succeeded" {
		if op.Status == "failed" {
			if op.Fee != nil {
				if err := s.releaseFixedFee(ctx, team, user, *op.Fee); err != nil {
					return Asset{}, fmt.Errorf("source_import_failed")
				}
			}
			return Asset{}, fmt.Errorf("source_import_failed")
		}
		return Asset{}, fmt.Errorf("source_import_in_progress")
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return Asset{}, err
	}
	for _, a := range assets {
		if a.ID == op.AssetID && a.Kind == "video" {
			a.URL = s.Blobs.URL(team, a.ID)
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("source_import_unavailable")
}

func (s *Service) UploadReference(ctx context.Context, team, name string, reader io.Reader, source string) (Asset, error) {
	asset, err := s.prepareReference(ctx, team, name, reader, source)
	if err != nil {
		return Asset{}, err
	}
	// 本地上传免费，沿用验证后直接登记资产的路径。
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err = s.Repo.AddAsset(persistCtx, team, asset); err != nil {
		_ = os.Remove(s.Blobs.Path(team, asset.ID))
		return Asset{}, fmt.Errorf("source_import_save_failed")
	}
	asset.URL = s.Blobs.URL(team, asset.ID)
	return asset, nil
}

func (s *Service) prepareReference(ctx context.Context, team, name string, reader io.Reader, source string) (Asset, error) {
	if err := ctx.Err(); err != nil {
		return Asset{}, fmt.Errorf("source_import_cancelled")
	}
	buffered := bufio.NewReader(reader)
	head, _ := buffered.Peek(512)
	mime := http.DetectContentType(head)
	if mime != "video/mp4" && mime != "video/quicktime" && mime != "video/webm" {
		return Asset{}, fmt.Errorf("invalid_source_video")
	}
	id, size, err := s.Blobs.Put(team, buffered, MaxReferenceBytes)
	if err != nil {
		return Asset{}, fmt.Errorf("source_video_too_large")
	}
	path := s.Blobs.Path(team, id)
	saved := false
	defer func() {
		if !saved {
			_ = os.Remove(path)
		}
	}()
	probe := s.ReferenceProbe
	if probe == nil {
		probe = ProbeReference
	}
	metadata, err := probe(ctx, path)
	if err != nil {
		return Asset{}, err
	}
	if !finitePositive(metadata.DurationSeconds) || metadata.DurationSeconds > MaxReferenceSeconds || metadata.Width <= 0 || metadata.Height <= 0 || metadata.Width > 16384 || metadata.Height > 16384 {
		return Asset{}, fmt.Errorf("source_video_limits_exceeded")
	}
	f, err := os.Open(path)
	if err != nil {
		return Asset{}, fmt.Errorf("source_import_save_failed")
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	_ = f.Close()
	if err != nil {
		return Asset{}, fmt.Errorf("source_import_save_failed")
	}
	ext := ".mp4"
	if mime == "video/webm" {
		ext = ".webm"
	}
	if mime == "video/quicktime" {
		ext = ".mov"
	}
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if len(name) > 240 {
		name = name[:240]
	}
	if name == "." || name == "" {
		name = "原片" + ext
	}
	if strings.HasPrefix(name, "原片") {
		name = "原片" + ext
	}
	asset := Asset{ID: id, Name: name, Kind: "video", Mime: mime, Size: size, CreatedAt: time.Now().UTC(), DurationSeconds: metadata.DurationSeconds, Width: metadata.Width, Height: metadata.Height, SourceURL: source, Checksum: hex.EncodeToString(hash.Sum(nil))}
	saved = true
	return asset, nil
}

func ProbeReference(ctx context.Context, path string) (ReferenceMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "format=duration:stream=codec_type,width,height", "-of", "json", path)
	output := boundedReferenceOutput{limit: maxReferenceProbeOutput}
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || output.exceeded {
		return ReferenceMetadata{}, fmt.Errorf("source_video_probe_failed")
	}
	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return ReferenceMetadata{}, fmt.Errorf("source_video_probe_failed")
	}
	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return ReferenceMetadata{}, fmt.Errorf("source_video_probe_failed")
	}
	for _, stream := range result.Streams {
		if stream.CodecType == "video" {
			return ReferenceMetadata{DurationSeconds: duration, Width: stream.Width, Height: stream.Height}, nil
		}
	}
	return ReferenceMetadata{}, fmt.Errorf("invalid_source_video")
}
