package apiapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
	"github.com/vam12375/shucode-platform/server/internal/teamassets"
)

func canvasTeamAssetID(input media.CanvasArchiveInput) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("mendao:canvas-team-asset:v1:"+input.TeamID+":"+input.ProjectID+":"+input.Source.ID)).String()
}

func canvasSourceChecksum(blobs media.Blobs, team string, source media.Asset) (string, error) {
	file, err := os.Open(blobs.Path(team, source.ID))
	if err != nil {
		return "", err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != source.Size || source.Size < 1 || source.Size > teamassets.MaxAssetBytes {
		return "", teamassets.ErrInvalid
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, teamassets.MaxAssetBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func newCanvasArchivePublisher(deps *Deps, blobs media.Blobs) func(context.Context, media.CanvasArchiveInput) media.CanvasTeamAssetArchive {
	assets := teamassets.Service{Repository: teamassets.Repository{Store: deps.Store, RequireActiveMember: true}, Blobs: media.Blobs{Root: filepath.Join(blobs.Root, "team-assets")}, Now: deps.Now, Logger: deps.Logger}
	return func(ctx context.Context, input media.CanvasArchiveInput) media.CanvasTeamAssetArchive {
		id := canvasTeamAssetID(input)
		out := media.CanvasTeamAssetArchive{SourceAssetID: input.Source.ID, TeamAssetID: id, Status: "failed", Error: "canvas_archive_retrying"}
		if uuid.Validate(input.TeamID) != nil || uuid.Validate(input.ProjectID) != nil || uuid.Validate(input.Source.ID) != nil || uuid.Validate(input.UserID) != nil {
			return out
		}
		deleted := func() (bool, error) { return deps.Store.IsTeamAssetDeleted(ctx, input.TeamID, id) }
		if retired, err := deleted(); err != nil {
			return out
		} else if retired {
			out.Status, out.Error = "deleted", ""
			return out
		}
		// 已保存回执不再复制文件；查询仍以平台元数据与墓碑为准，避免删除后复活。
		existing, err := assets.Repository.Get(ctx, input.TeamID, id)
		if err != nil {
			return out
		}
		if existing != nil {
			if existing.UploadedBy.ID != input.UserID || existing.Name != input.Source.Name || existing.Kind != input.Source.Kind || existing.Mime != input.Source.Mime || existing.Size != input.Source.Size {
				out.Error = "canvas_archive_conflict"
				return out
			}
			if input.Previous.Status != "saved" || input.Previous.TeamAssetID != id {
				checksum, err := canvasSourceChecksum(blobs, input.TeamID, input.Source)
				if err != nil {
					return out
				}
				if checksum != existing.SHA256 {
					out.Error = "canvas_archive_conflict"
					return out
				}
			}
			out.Status, out.Error = "saved", ""
			return out
		}
		if input.Source.Size > teamassets.MaxAssetBytes {
			out.Error = "canvas_archive_size_limit"
			return out
		}
		if input.Source.Size < 1 || (input.Source.Kind != "image" && input.Source.Kind != "video") {
			return out
		}
		user, err := deps.Store.FindUserByID(ctx, input.UserID)
		if err != nil {
			return out
		}
		member, err := deps.Store.GetTeamMember(ctx, input.TeamID, input.UserID)
		if err != nil {
			return out
		}
		team, err := deps.Store.GetTeam(ctx, input.TeamID)
		if err != nil {
			return out
		}
		if user == nil || user.DisabledAt != nil || member == nil || member.MemberDisabledAt != nil || team == nil || team.DisabledAt != nil {
			out.Error = "canvas_archive_forbidden"
			return out
		}
		page, err := deps.Store.ListTeamAssetPage(ctx, input.TeamID, nil, "", 1)
		if err != nil {
			return out
		}
		if page.UsedBytes > teamassets.TeamStorageLimitBytes || input.Source.Size > teamassets.TeamStorageLimitBytes-page.UsedBytes {
			out.Error = "canvas_archive_storage_limit"
			return out
		}
		file, err := os.Open(blobs.Path(input.TeamID, input.Source.ID))
		if err != nil {
			return out
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() != input.Source.Size {
			return out
		}
		_, err = assets.Upload(ctx, input.TeamID, input.UserID, user.DisplayName, id, input.Source.Name, file)
		if err != nil {
			if retired, lookupErr := deleted(); lookupErr == nil && retired {
				out.Status, out.Error = "deleted", ""
				return out
			}
			switch {
			case errors.Is(err, teamassets.ErrStorageLimit):
				out.Error = "canvas_archive_storage_limit"
			case errors.Is(err, teamassets.ErrSizeLimit):
				out.Error = "canvas_archive_size_limit"
			case errors.Is(err, teamassets.ErrForbidden):
				out.Error = "canvas_archive_forbidden"
			case errors.Is(err, teamassets.ErrConflict):
				out.Error = "canvas_archive_conflict"
			}
			return out
		}
		out.Status, out.Error = "saved", ""
		return out
	}
}
