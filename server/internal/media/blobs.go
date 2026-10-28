package media

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const MaxUpload = 20 << 20
const MaxVideo = 512 << 20

type Blobs struct {
	Root, Origin, SigningKey string
	Client                   *http.Client
}

func (b Blobs) Path(team, id string) string { return filepath.Join(b.Root, team, id) }
func (b Blobs) Put(team string, reader io.Reader, limit int64) (string, int64, error) {
	return b.PutWithID(team, uuid.NewString(), reader, limit)
}

// PutWithID writes a blob under a caller-owned stable identity. A complete
// existing blob is reused so a worker crash after the media write cannot
// create a second final asset on the next lease.
func (b Blobs) PutWithID(team, id string, reader io.Reader, limit int64) (string, int64, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", 0, ErrInvalid
	}
	dir := filepath.Join(b.Root, team)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", 0, e
	}
	if info, e := os.Stat(b.Path(team, id)); e == nil {
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
			return "", 0, fmt.Errorf("media_size_limit")
		}
		return id, info.Size(), nil
	} else if !os.IsNotExist(e) {
		return "", 0, e
	}
	f, e := os.CreateTemp(dir, ".upload-*")
	if e != nil {
		return "", 0, e
	}
	defer os.Remove(f.Name())
	n, e := io.Copy(f, io.LimitReader(reader, limit+1))
	closeErr := f.Close()
	if e != nil {
		return "", 0, e
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	if n == 0 || n > limit {
		return "", 0, fmt.Errorf("media_size_limit")
	}
	e = os.Rename(f.Name(), b.Path(team, id))
	return id, n, e
}
func (b Blobs) signature(team, id, expires string) string {
	m := hmac.New(sha256.New, []byte(b.SigningKey))
	m.Write([]byte(team + "/" + id + "/" + expires))
	return hex.EncodeToString(m.Sum(nil))
}
func (b Blobs) URL(team, id string) string {
	expires := strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	return b.Origin + "/api/v1/media/blobs/" + team + "/" + id + "?expires=" + expires + "&signature=" + b.signature(team, id, expires)
}
func (b Blobs) Verify(team, id, expires, signature string) bool {
	if _, e := uuid.Parse(team); e != nil {
		return false
	}
	if _, e := uuid.Parse(id); e != nil {
		return false
	}
	t, e := strconv.ParseInt(expires, 10, 64)
	return e == nil && t >= time.Now().Unix() && t <= time.Now().Add(25*time.Hour).Unix() && hmac.Equal([]byte(signature), []byte(b.signature(team, id, expires)))
}
func (b Blobs) Download(ctx context.Context, team, rawURL, kind string) (Asset, error) {
	if !ValidURL(rawURL) {
		return Asset{}, ErrInvalid
	}
	req, e := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if e != nil {
		return Asset{}, e
	}
	resp, e := b.Client.Do(req)
	if e != nil {
		return Asset{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Asset{}, fmt.Errorf("media_download_failed")
	}
	reader := bufio.NewReader(resp.Body)
	head, _ := reader.Peek(512)
	mime := http.DetectContentType(head)
	limit := int64(MaxVideo)
	name := "generated.mp4"
	if kind == "image" {
		limit = MaxUpload
		switch mime {
		case "image/png":
			name = "generated.png"
		case "image/jpeg":
			name = "generated.jpg"
		case "image/webp":
			name = "generated.webp"
		default:
			return Asset{}, fmt.Errorf("invalid_image_result")
		}
	} else if mime != "video/mp4" {
		return Asset{}, fmt.Errorf("invalid_video_result")
	}
	id, n, e := b.Put(team, reader, limit)
	if e != nil {
		return Asset{}, e
	}
	return Asset{ID: id, Kind: kind, Mime: mime, Name: name, Size: n, CreatedAt: time.Now().UTC()}, nil
}
