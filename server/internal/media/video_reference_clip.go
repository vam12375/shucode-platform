package media

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
)

type videoReferenceSource struct {
	Asset        Asset
	StartSeconds float64
	EndSeconds   float64
	Checksum     string
}

func (s *Service) resolveVideoReference(ctx context.Context, team, assetID string, start, end *float64, maxDuration int) (videoReferenceSource, error) {
	if assetID == "" || (start == nil) != (end == nil) {
		return videoReferenceSource{}, ErrInvalid
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return videoReferenceSource{}, err
	}
	var source *Asset
	for i := range assets {
		if assets[i].ID == assetID && assets[i].Kind == "video" && !assets[i].Internal {
			source = &assets[i]
			break
		}
	}
	if source == nil {
		return videoReferenceSource{}, fmt.Errorf("reference_asset_not_found")
	}
	if _, err = uuid.Parse(source.ID); err != nil {
		return videoReferenceSource{}, ErrInvalid
	}
	path := s.Blobs.Path(team, source.ID)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || (source.Size > 0 && info.Size() != source.Size) || info.Size() > MaxVideo {
		return videoReferenceSource{}, fmt.Errorf("reference_asset_not_ready")
	}
	probe := s.ReferenceProbe
	if probe == nil {
		probe = ProbeReference
	}
	metadata, err := probe(ctx, path)
	if err != nil || !finitePositive(metadata.DurationSeconds) || metadata.DurationSeconds > MaxReferenceSeconds {
		return videoReferenceSource{}, fmt.Errorf("reference_asset_not_ready")
	}
	checksum, err := checksumFile(path)
	if err != nil {
		return videoReferenceSource{}, fmt.Errorf("reference_asset_not_ready")
	}
	if source.Checksum != "" && source.Checksum != checksum {
		return videoReferenceSource{}, fmt.Errorf("video_source_changed")
	}
	if source.DurationSeconds > 0 && math.Abs(source.DurationSeconds-metadata.DurationSeconds) > .05 {
		return videoReferenceSource{}, fmt.Errorf("video_source_changed")
	}
	clipStart, clipEnd := 0.0, metadata.DurationSeconds
	if start != nil {
		clipStart, clipEnd = *start, *end
	}
	if math.IsNaN(clipStart) || math.IsInf(clipStart, 0) || math.IsNaN(clipEnd) || math.IsInf(clipEnd, 0) || clipStart < 0 || clipEnd <= clipStart || clipEnd > metadata.DurationSeconds+.001 {
		return videoReferenceSource{}, ErrInvalid
	}
	if maxDuration < MinReferenceClipSeconds || clipEnd-clipStart < float64(MinReferenceClipSeconds)-.001 || clipEnd-clipStart > float64(maxDuration)+.001 {
		return videoReferenceSource{}, fmt.Errorf("video_reference_duration_unsupported")
	}
	source.DurationSeconds = metadata.DurationSeconds
	source.Checksum = checksum
	return videoReferenceSource{Asset: *source, StartSeconds: clipStart, EndSeconds: clipEnd, Checksum: checksum}, nil
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func clipIdentity(team, sourceID, sourceChecksum string, start, end float64, clipChecksum string) string {
	name := fmt.Sprintf("mendao-reference-clip:v1:%s:%s:%s:%s:%s:%s", team, sourceID, sourceChecksum,
		strconv.FormatFloat(start, 'f', 9, 64), strconv.FormatFloat(end, 'f', 9, 64), clipChecksum)
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(name)).String()
}

func (s *Service) UploadReferenceClip(ctx context.Context, team, sourceID, sourceChecksum string, start, end float64, reader io.Reader) (Asset, error) {
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
		return Asset{}, fmt.Errorf("video_reference_clip_store_failed")
	}
	buffered := bufio.NewReader(reader)
	head, peekErr := buffered.Peek(512)
	if peekErr != nil && peekErr != io.EOF {
		return Asset{}, fmt.Errorf("video_reference_clip_invalid")
	}
	if http.DetectContentType(head) != "video/mp4" {
		return Asset{}, fmt.Errorf("video_reference_clip_invalid")
	}
	temp, err := os.CreateTemp(teamDir, ".reference-clip-upload-*")
	if err != nil {
		return Asset{}, fmt.Errorf("video_reference_clip_store_failed")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(temp, h), io.LimitReader(buffered, MaxVideo+1))
	closeErr := temp.Close()
	if copyErr != nil || closeErr != nil || n <= 0 || n > MaxVideo {
		return Asset{}, fmt.Errorf("video_reference_clip_size_limit")
	}
	if err = ctx.Err(); err != nil {
		return Asset{}, err
	}
	clipChecksum := hex.EncodeToString(h.Sum(nil))
	probe := s.ReferenceProbe
	if probe == nil {
		probe = ProbeReference
	}
	metadata, err := probe(ctx, tempPath)
	if err != nil || !finitePositive(metadata.DurationSeconds) || metadata.DurationSeconds > MaxReferenceSeconds || metadata.Width < 1 || metadata.Height < 1 || metadata.Width > 16384 || metadata.Height > 16384 || math.Abs(metadata.DurationSeconds-(end-start)) > .1 {
		return Asset{}, fmt.Errorf("video_reference_clip_invalid")
	}
	id := clipIdentity(team, sourceID, sourceChecksum, start, end, clipChecksum)
	asset := Asset{ID: id, Name: "reference-clip.mp4", Kind: "video", Mime: "video/mp4", Size: n, CreatedAt: time.Now().UTC(), DurationSeconds: metadata.DurationSeconds, Width: metadata.Width, Height: metadata.Height, Checksum: clipChecksum, Internal: true}
	finalPath := s.Blobs.Path(team, id)
	linkErr := os.Link(tempPath, finalPath)
	if linkErr != nil && !os.IsExist(linkErr) {
		return Asset{}, fmt.Errorf("video_reference_clip_store_failed")
	}
	if linkErr != nil {
		info, statErr := os.Stat(finalPath)
		existingChecksum, hashErr := checksumFile(finalPath)
		if statErr != nil || hashErr != nil || info.Size() != n || existingChecksum != clipChecksum {
			return Asset{}, ErrConflict
		}
	}
	if err = s.Repo.AddInternalAssetIfSame(ctx, team, asset); err != nil {
		return Asset{}, err
	}
	asset.URL = s.Blobs.URL(team, id)
	return asset, nil
}

func (s *Service) validateUploadedReferenceClip(ctx context.Context, team string, source videoReferenceSource, clipID string) (Asset, error) {
	if _, err := uuid.Parse(clipID); err != nil {
		return Asset{}, ErrInvalid
	}
	assets, err := s.Repo.Assets(ctx, team)
	if err != nil {
		return Asset{}, err
	}
	var clip *Asset
	for i := range assets {
		if assets[i].ID == clipID && assets[i].Internal && assets[i].Kind == "video" {
			clip = &assets[i]
			break
		}
	}
	if clip == nil || clip.Mime != "video/mp4" || !validSourceChecksum(clip.Checksum) || clip.ID != clipIdentity(team, source.Asset.ID, source.Checksum, source.StartSeconds, source.EndSeconds, clip.Checksum) || !finitePositive(clip.DurationSeconds) || math.Abs(clip.DurationSeconds-(source.EndSeconds-source.StartSeconds)) > .1 {
		return Asset{}, fmt.Errorf("video_reference_clip_invalid")
	}
	path := s.Blobs.Path(team, clip.ID)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != clip.Size {
		return Asset{}, fmt.Errorf("video_reference_clip_invalid")
	}
	checksum, err := checksumFile(path)
	if err != nil || checksum != clip.Checksum {
		return Asset{}, fmt.Errorf("video_reference_clip_invalid")
	}
	clip.URL = s.Blobs.URL(team, clip.ID)
	return *clip, nil
}

func (s *Service) ensureVideoReferenceClip(ctx context.Context, p *Project, shot Shot, limit int) (Asset, error) {
	if p.Canvas == nil && (p.Brief.GenerationMode != "video_reference" || p.SourceAnalysis == nil || p.SourceChecksum == "" || p.SourceChecksum != p.SourceAnalysis.SourceChecksum) {
		return Asset{}, fmt.Errorf("video_reference_source_analysis_required")
	}
	var sourceID string
	var start, end *float64
	if p.Canvas != nil {
		if len(p.Canvas.VideoReferences) != 1 {
			return Asset{}, fmt.Errorf("video_reference_required")
		}
		ref := p.Canvas.VideoReferences[0]
		sourceID, start, end = ref.AssetID, ref.StartSeconds, ref.EndSeconds
	} else {
		sourceID = p.Brief.ReferenceAssetID
		start, end = &shot.Plan.SourceStart, &shot.Plan.SourceEnd
	}
	clipID := shot.ReferenceVideoAssetID
	if clipID == "" {
		clipID = shot.Plan.ReferenceVideoAssetID
	}
	if clipID == "" {
		return Asset{}, fmt.Errorf("video_reference_clip_required")
	}
	source, err := s.resolveVideoReference(ctx, p.TeamID, sourceID, start, end, limit)
	if err != nil {
		return Asset{}, err
	}
	if p.SourceChecksum != "" && source.Checksum != p.SourceChecksum {
		return Asset{}, fmt.Errorf("video_source_changed")
	}
	return s.validateUploadedReferenceClip(ctx, p.TeamID, source, clipID)
}

// AddInternalAssetIfSame lets concurrent byte-identical retries share one metadata row.
func (r Repository) AddInternalAssetIfSame(ctx context.Context, team string, asset Asset) error {
	if !asset.Internal || asset.ID == "" || asset.Kind != "video" || !validSourceChecksum(asset.Checksum) || asset.Size <= 0 {
		return ErrInvalid
	}
	return updateJSON(ctx, r.Store, assetKey(team), []Asset{}, func(assets *[]Asset) error {
		for _, existing := range *assets {
			if existing.ID == asset.ID {
				if !existing.Internal || existing.Kind != asset.Kind || existing.Mime != asset.Mime || existing.Size != asset.Size || existing.Checksum != asset.Checksum || math.Abs(existing.DurationSeconds-asset.DurationSeconds) > .001 || existing.Width != asset.Width || existing.Height != asset.Height {
					return ErrConflict
				}
				return nil
			}
		}
		if len(*assets) >= 10000 {
			return fmt.Errorf("asset_limit")
		}
		*assets = append(*assets, asset)
		return nil
	})
}
