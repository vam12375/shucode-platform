package teamassets

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
)

var ErrThumbnailUnsupported = errors.New("team_asset_thumbnail_unsupported")
var thumbnailSlots = make(chan struct{}, 2)

func (s Service) Thumbnail(ctx context.Context, team, id string) ([]byte, error) {
	asset, file, err := s.Open(ctx, team, id)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if asset.Kind != "image" {
		return nil, ErrThumbnailUnsupported
	}
	// 原因：CDN回源和旧Host缩略图请求曾重复解码原件；派生按不可变版本持久复用。
	// 原件存在性已由Open确认，不能因为派生文件存在而复活已删除资产。
	cachePath := filepath.Join(s.Blobs.Root, ".previews", "jpeg-256-v1", team, asset.ID+"-"+asset.ContentHash+".jpg")
	if raw, ok := readThumbnailCache(cachePath); ok {
		return raw, nil
	}
	select {
	case thumbnailSlots <- struct{}{}:
		defer func() { <-thumbnailSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// 排队期间其他请求可能已完成同版本派生，取到slot后再次读取可省一次原图解码。
	if raw, ok := readThumbnailCache(cachePath); ok {
		return raw, nil
	}
	config, _, err := image.DecodeConfig(file)
	// 原图解码也需有界；不能用小图的输出尺寸掩盖压缩图片造成的解码内存峰值。
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 16000000 {
		return nil, ErrThumbnailUnsupported
	}
	if _, err = file.Seek(0, 0); err != nil {
		return nil, err
	}
	source, _, err := image.Decode(file)
	if err != nil {
		return nil, ErrThumbnailUnsupported
	}
	width, height := config.Width, config.Height
	if width > 256 || height > 256 {
		if width >= height {
			height = max(1, height*256/width)
			width = 256
		} else {
			width = max(1, width*256/height)
			height = 256
		}
	}
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < width; x++ {
			target.Set(x, y, source.At(bounds.Min.X+x*config.Width/width, bounds.Min.Y+y*config.Height/height))
		}
	}
	for _, quality := range []int{80, 60, 40} {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, target, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if out.Len() <= 65536 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := writeThumbnailCache(cachePath, out.Bytes()); err != nil {
				return nil, err
			}
			return out.Bytes(), nil
		}
	}
	return nil, ErrThumbnailUnsupported
}

func readThumbnailCache(path string) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 65536 {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) != int(info.Size()) {
		return nil, false
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 256 || config.Height > 256 {
		return nil, false
	}
	if _, err := jpeg.Decode(bytes.NewReader(raw)); err != nil {
		return nil, false
	}
	return raw, true
}

func writeThumbnailCache(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".thumbnail-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
