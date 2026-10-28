package media

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// 参考音频用于保证复刻卡点与原片一致。Ark 只接受 wav/mp3，
// Desktop 侧统一编码为 PCM WAV，因此这里只认 RIFF/WAVE 头。
const MaxAudio int64 = 16 << 20

func isWAVHeader(head []byte) bool {
	return len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE"
}

func audioClipIdentity(team, sourceID, sourceChecksum string, start, end float64, clipChecksum string) string {
	// 与视频片段使用不同命名空间，避免同源同区间同字节时派生相同 UUID。
	name := fmt.Sprintf("mendao-reference-audio:v1:%s:%s:%s:%s:%s:%s", team, sourceID, sourceChecksum,
		strconv.FormatFloat(start, 'f', 9, 64), strconv.FormatFloat(end, 'f', 9, 64), clipChecksum)
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(name)).String()
}

func (s *Service) UploadReferenceAudio(ctx context.Context, team, sourceID, sourceChecksum string, start, end float64, reader io.Reader) (Asset, error) {
	if !validSourceChecksum(sourceChecksum) || !finitePositive(end-start) {
		return Asset{}, ErrInvalid
	}
	source, err := s.resolveVideoReference(ctx, team, sourceID, &start, &end, MaxReferenceSeconds)
	if err != nil {
		return Asset{}, err
	}
	if source.Checksum != sourceChecksum {
		return Asset{}, fmt.Errorf("video_source_changed")
	}
	if err = ctx.Err(); err != nil {
		return Asset{}, err
	}
	teamDir := filepath.Dir(s.Blobs.Path(team, "placeholder"))
	if err = os.MkdirAll(teamDir, 0700); err != nil {
		return Asset{}, fmt.Errorf("video_reference_audio_store_failed")
	}
	buffered := bufio.NewReader(reader)
	head, peekErr := buffered.Peek(512)
	if peekErr != nil && peekErr != io.EOF {
		return Asset{}, fmt.Errorf("video_reference_audio_invalid")
	}
	if !isWAVHeader(head) {
		return Asset{}, fmt.Errorf("video_reference_audio_invalid")
	}
	temp, err := os.CreateTemp(teamDir, ".reference-audio-upload-*")
	if err != nil {
		return Asset{}, fmt.Errorf("video_reference_audio_store_failed")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(temp, h), io.LimitReader(buffered, MaxAudio+1))
	closeErr := temp.Close()
	if copyErr != nil || closeErr != nil || n <= 0 || n > MaxAudio {
		return Asset{}, fmt.Errorf("video_reference_audio_size_limit")
	}
	if err = ctx.Err(); err != nil {
		return Asset{}, err
	}
	audioChecksum := hex.EncodeToString(h.Sum(nil))
	probe := s.ReferenceProbe
	if probe == nil {
		// WAV 参考音频没有 video stream；复用视频探针会把每次合法音频上传
		// 都判成 invalid_source_video，Host 随后只能静默降级为无音频参考。
		probe = ProbeReferenceAudio
	}
	metadata, err := probe(ctx, tempPath)
	if err != nil || !finitePositive(metadata.DurationSeconds) || metadata.DurationSeconds > MaxReferenceSeconds || math.Abs(metadata.DurationSeconds-(end-start)) > .1 {
		return Asset{}, fmt.Errorf("video_reference_audio_invalid")
	}
	id := audioClipIdentity(team, sourceID, sourceChecksum, start, end, audioChecksum)
	asset := Asset{ID: id, Name: "reference-audio.wav", Kind: "audio", Mime: "audio/wav", Size: n, CreatedAt: time.Now().UTC(), DurationSeconds: metadata.DurationSeconds, Checksum: audioChecksum, Internal: true}
	finalPath := s.Blobs.Path(team, id)
	linkErr := os.Link(tempPath, finalPath)
	if linkErr != nil && !os.IsExist(linkErr) {
		return Asset{}, fmt.Errorf("video_reference_audio_store_failed")
	}
	if linkErr != nil {
		info, statErr := os.Stat(finalPath)
		existingChecksum, hashErr := checksumFile(finalPath)
		if statErr != nil || hashErr != nil || info.Size() != n || existingChecksum != audioChecksum {
			return Asset{}, ErrConflict
		}
	}
	if err = s.Repo.AddInternalAssetIfSame(ctx, team, asset); err != nil {
		return Asset{}, err
	}
	asset.URL = s.Blobs.URL(team, id)
	return asset, nil
}

func (s *Service) validateUploadedReferenceAudio(ctx context.Context, team string, source videoReferenceSource, audioID string) (Asset, error) {
	if _, err := uuid.Parse(audioID); err != nil {
		return Asset{}, ErrInvalid
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return Asset{}, err
	}
	var audio *Asset
	for i := range assets {
		if assets[i].ID == audioID && assets[i].Internal && assets[i].Kind == "audio" {
			audio = &assets[i]
			break
		}
	}
	if audio == nil || audio.Mime != "audio/wav" || !validSourceChecksum(audio.Checksum) || audio.ID != audioClipIdentity(team, source.Asset.ID, source.Checksum, source.StartSeconds, source.EndSeconds, audio.Checksum) || !finitePositive(audio.DurationSeconds) || math.Abs(audio.DurationSeconds-(source.EndSeconds-source.StartSeconds)) > .1 {
		return Asset{}, fmt.Errorf("video_reference_audio_invalid")
	}
	path := s.Blobs.Path(team, audio.ID)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != audio.Size {
		return Asset{}, fmt.Errorf("video_reference_audio_invalid")
	}
	checksum, err := checksumFile(path)
	if err != nil || checksum != audio.Checksum {
		return Asset{}, fmt.Errorf("video_reference_audio_invalid")
	}
	audio.URL = s.Blobs.URL(team, audio.ID)
	return *audio, nil
}

// ensureReferenceAudio 解析已绑定的参考音频。未绑定表示降级（原片无音轨或超出上限），
// 返回零值 Asset 而不是错误——缺少参考音频不能阻断付费生成。
func (s *Service) ensureReferenceAudio(ctx context.Context, p *Project, shot Shot) (Asset, error) {
	audioID := shot.ReferenceAudioAssetID
	if audioID == "" {
		audioID = shot.Plan.ReferenceAudioAssetID
	}
	if audioID == "" {
		return Asset{}, nil
	}
	start, end := shot.Plan.SourceStart, shot.Plan.SourceEnd
	source, err := s.resolveVideoReference(ctx, p.TeamID, p.Brief.ReferenceAssetID, &start, &end, MaxReferenceSeconds)
	if err != nil {
		return Asset{}, err
	}
	if p.SourceChecksum != "" && source.Checksum != p.SourceChecksum {
		return Asset{}, fmt.Errorf("video_source_changed")
	}
	return s.validateUploadedReferenceAudio(ctx, p.TeamID, source, audioID)
}

// ProbeReferenceAudio 只读时长；音频文件没有视频流，不能复用 ProbeReference。
func ProbeReferenceAudio(ctx context.Context, path string) (ReferenceMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "format=duration:stream=codec_type", "-of", "json", path)
	output := boundedReferenceOutput{limit: maxReferenceProbeOutput}
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || output.exceeded {
		return ReferenceMetadata{}, fmt.Errorf("video_reference_audio_invalid")
	}
	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if json.Unmarshal(output.Bytes(), &result) != nil {
		return ReferenceMetadata{}, fmt.Errorf("video_reference_audio_invalid")
	}
	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return ReferenceMetadata{}, fmt.Errorf("video_reference_audio_invalid")
	}
	for _, stream := range result.Streams {
		if stream.CodecType == "audio" {
			return ReferenceMetadata{DurationSeconds: duration}, nil
		}
	}
	return ReferenceMetadata{}, fmt.Errorf("video_reference_audio_invalid")
}
