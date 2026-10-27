package media

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 先识别真实文件头再按媒体类型限额，扩展名和客户端MIME都不构成上传授权。
func (s *Service) UploadCanvasMedia(ctx context.Context, team, name, product string, reader io.Reader) (Asset, error) {
	buffered := bufio.NewReader(reader)
	head, _ := buffered.Peek(512)
	mime := http.DetectContentType(head)
	kind := "image"
	limit := int64(MaxUpload)
	switch {
	case mime == "image/png" || mime == "image/jpeg" || mime == "image/webp":
	case mime == "video/mp4" || isQuickTimeHeader(head):
		if mime != "video/mp4" {
			mime = "video/quicktime"
		}
		kind = "video"
		limit = MaxCanvasVideoBytes
	case isWAVHeader(head):
		kind = "audio"
		mime = "audio/wav"
		limit = MaxCanvasAudioBytes
	case mime == "audio/mpeg" || len(head) >= 3 && string(head[:3]) == "ID3" || len(head) >= 3 && head[0] == 0xff && head[1]&0xe0 == 0xe0 && head[1]&0x06 == 0x02 && head[2]&0x0c != 0x0c:
		kind = "audio"
		mime = "audio/mpeg"
		limit = MaxCanvasAudioBytes
	default:
		return Asset{}, fmt.Errorf("canvas_reference_format_invalid")
	}
	id, size, err := s.Blobs.Put(team, buffered, limit)
	if err != nil {
		return Asset{}, err
	}
	path := s.Blobs.Path(team, id)
	saved := false
	defer func() {
		if !saved {
			_ = os.Remove(path)
		}
	}()
	a := Asset{ID: id, Name: filepath.Base(strings.ReplaceAll(name, "\\", "/")), Product: product, Kind: kind, Mime: mime, Size: size, CreatedAt: time.Now().UTC()}
	if kind != "image" {
		probe := s.ReferenceProbe
		if probe == nil {
			if kind == "audio" {
				probe = ProbeReferenceAudio
			} else {
				probe = ProbeReference
			}
		}
		metadata, e := probe(ctx, path)
		if e != nil || !finitePositive(metadata.DurationSeconds) || metadata.DurationSeconds < 2 || metadata.DurationSeconds > 15 {
			return Asset{}, fmt.Errorf("canvas_reference_duration_limit")
		}
		if kind == "video" && (metadata.Width <= 0 || metadata.Height <= 0 || metadata.Width > 16384 || metadata.Height > 16384) {
			return Asset{}, fmt.Errorf("canvas_reference_asset_invalid")
		}
		a.DurationSeconds = metadata.DurationSeconds
		if kind == "video" {
			a.Width = metadata.Width
			a.Height = metadata.Height
		}
	}
	a.Checksum, err = checksumFile(path)
	if err != nil {
		return Asset{}, err
	}
	if err = s.Repo.AddAsset(ctx, team, a); err != nil {
		return Asset{}, err
	}
	saved = true
	a.URL = s.Blobs.URL(team, id)
	return a, nil
}

// Go的通用MIME探测不识别QuickTime；仅容器头还不足够，后续ffprobe必须确认真实视频流。
func isQuickTimeHeader(head []byte) bool {
	if len(head) < 12 {
		return false
	}
	size := binary.BigEndian.Uint32(head[:4])
	if size < 8 {
		return false
	}
	switch string(head[4:8]) {
	case "ftyp":
		return string(head[8:12]) == "qt  "
	case "moov", "wide", "mdat":
		return true
	default:
		return false
	}
}
