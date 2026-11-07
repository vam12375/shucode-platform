package templatelibrary

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

const MaxPreviewSize = 64 << 20

var previewName = regexp.MustCompile(`^[a-f0-9]{64}\.(png|jpg|webp|mp4|webm)$`)

type Preview struct {
	Cover string `json:"cover"`
	Video string `json:"video"`
}

func ValidPreviewName(name string) bool { return previewName.MatchString(name) }
func (p Preview) Valid() bool {
	return (p.Cover == "" || ValidPreviewName(p.Cover) && (filepath.Ext(p.Cover) == ".png" || filepath.Ext(p.Cover) == ".jpg" || filepath.Ext(p.Cover) == ".webp")) &&
		(p.Video == "" || ValidPreviewName(p.Video) && (filepath.Ext(p.Video) == ".mp4" || filepath.Ext(p.Video) == ".webm"))
}
func PreviewPath(root, name string) string {
	return filepath.Join(root, "template-library-public", name)
}
func PreviewURL(origin, name string) string {
	if !ValidPreviewName(name) {
		return ""
	}
	return origin + "/api/v1/media/template-assets/" + name
}

// 公开模板素材使用独立目录与内容寻址；不复用团队资产及其私有签名地址。
func SavePreview(root string, raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > MaxPreviewSize {
		return "", ErrInvalid
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "video/mp4": "mp4", "video/webm": "webm"}[http.DetectContentType(raw)]
	if ext == "" {
		return "", fmt.Errorf("%w：仅支持 PNG、JPEG、WebP、MP4、WebM", ErrInvalid)
	}
	sum := sha256.Sum256(raw)
	name := hex.EncodeToString(sum[:]) + "." + ext
	dir := filepath.Dir(PreviewPath(root, name))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".upload-")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(raw)
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Rename(file.Name(), PreviewPath(root, name)); err != nil {
		return "", err
	}
	return name, nil
}
