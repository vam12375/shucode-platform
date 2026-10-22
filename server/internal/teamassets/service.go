package teamassets

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

type Service struct {
	Repository Repository
	Blobs      media.Blobs
	Now        func() time.Time
	Logger     *slog.Logger
}

func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && len(id) == 36 && parsed.String() == id
}

func validSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

func validName(name string) bool {
	if !utf8.ValidString(name) || !validSegment(name) || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 255 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func assetKind(mime, name string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/") || mime == "application/ogg":
		return "audio"
	case strings.HasPrefix(mime, "text/html"):
		return "website"
	case strings.HasPrefix(mime, "text/"):
		switch strings.ToLower(filepath.Ext(name)) {
		case ".html", ".htm":
			return "website"
		case ".js", ".jsx", ".ts", ".tsx", ".py", ".go", ".rs", ".java", ".c", ".h", ".cpp", ".css", ".json", ".yaml", ".yml", ".sh", ".sql", ".xml":
			return "code"
		default:
			return "document"
		}
	case mime == "application/pdf", mime == "application/postscript":
		return "document"
	default:
		return "other"
	}
}

func (s Service) Upload(ctx context.Context, team, userID, userName, id, name string, reader io.Reader) (Asset, error) {
	if !validSegment(team) || userID == "" || !validID(id) || !validName(name) || reader == nil {
		return Asset{}, ErrInvalid
	}
	buffer := bufio.NewReader(contextReader{ctx, reader})
	head, err := buffer.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return Asset{}, err
	}
	if len(head) == 0 {
		return Asset{}, ErrInvalid
	}
	mime := http.DetectContentType(head)
	digest := sha256.New()
	var count int64
	counted := &countReader{reader: io.TeeReader(buffer, digest), count: &count}
	blobID, size, err := s.Blobs.Put(team, counted, MaxAssetBytes)
	if err != nil {
		if count > MaxAssetBytes {
			return Asset{}, ErrSizeLimit
		}
		return Asset{}, err
	}
	record := Record{Asset: Asset{
		ID: id, Name: name, Kind: assetKind(mime, name), Mime: mime, Size: size,
		CreatedAt: s.Now().UTC(), UploadedBy: Uploader{ID: userID, Name: userName},
	}, SHA256: hex.EncodeToString(digest.Sum(nil)), BlobID: blobID}
	committed, commitErr := s.Repository.Commit(ctx, team, record)
	if commitErr == nil {
		if committed.BlobID != blobID {
			if err := os.Remove(s.Blobs.Path(team, blobID)); err != nil {
				return Asset{}, fmt.Errorf("clean unused team asset: %w", err)
			}
		}
		return committed.Asset, nil
	}
	// 提交返回错误不代表数据库未提交；只有重新读取确认未被引用，才能删本次 blob。
	// 取消后的请求仍做有界确认，无法确认时保留文件，避免把已提交内容误删。
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	existing, verifyErr := s.Repository.Get(verifyCtx, team, id)
	if verifyErr == nil {
		if existing == nil || existing.BlobID != blobID {
			if err := os.Remove(s.Blobs.Path(team, blobID)); err != nil {
				return Asset{}, errors.Join(commitErr, fmt.Errorf("clean unused team asset: %w", err))
			}
		} else if sameUpload(*existing, record) {
			return existing.Asset, nil
		}
	}
	return Asset{}, commitErr
}

type countReader struct {
	reader io.Reader
	count  *int64
}

func (r *countReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	*r.count += int64(n)
	return n, err
}

func (s Service) Get(ctx context.Context, team, id string) (*Asset, error) {
	if !validSegment(team) || !validID(id) {
		return nil, ErrInvalid
	}
	record, err := s.Repository.Get(ctx, team, id)
	if err != nil || record == nil {
		return nil, err
	}
	return &record.Asset, nil
}

func (s Service) Open(ctx context.Context, team, id string) (*Asset, *os.File, error) {
	if !validSegment(team) || !validID(id) {
		return nil, nil, ErrInvalid
	}
	record, err := s.Repository.Get(ctx, team, id)
	if err != nil {
		return nil, nil, err
	}
	if record == nil {
		return nil, nil, ErrNotFound
	}
	if !validID(record.BlobID) {
		return nil, nil, fmt.Errorf("invalid team asset blob reference")
	}
	file, err := os.Open(s.Blobs.Path(team, record.BlobID))
	if err != nil {
		return nil, nil, fmt.Errorf("open team asset content: %w", err)
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != record.Size {
		_ = file.Close()
		return nil, nil, fmt.Errorf("invalid team asset content")
	}
	return &record.Asset, file, nil
}

type cursorPosition struct {
	Team      string    `json:"team"`
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func (s Service) List(ctx context.Context, team, cursor string) (Page, error) {
	if !validSegment(team) {
		return Page{}, ErrInvalid
	}
	var position cursorPosition
	if cursor != "" {
		if len(cursor) > 4096 {
			return Page{}, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil {
			return Page{}, ErrInvalid
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&position); err != nil {
			return Page{}, ErrInvalid
		}
		if decoder.Decode(new(any)) != io.EOF || position.Team != team || position.CreatedAt.IsZero() || !validID(position.ID) {
			return Page{}, ErrInvalid
		}
	}
	var beforeAt *time.Time
	var beforeID string
	if cursor != "" {
		beforeAt, beforeID = &position.CreatedAt, position.ID
	}
	records, used, err := s.Repository.ListPage(ctx, team, beforeAt, beforeID, PageSize+1)
	if err != nil {
		return Page{}, err
	}
	page := Page{Assets: make([]Asset, 0, min(len(records), PageSize)), Storage: Storage{UsedBytes: used, LimitBytes: TeamStorageLimitBytes}}
	hasNext := len(records) > PageSize
	if hasNext {
		records = records[:PageSize]
	}
	for _, record := range records {
		page.Assets = append(page.Assets, record.Asset)
	}
	if hasNext {
		last := records[len(records)-1]
		raw, err := json.Marshal(cursorPosition{Team: team, CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return Page{}, err
		}
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
	}
	return page, nil
}
