package teamassets

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
)

var ErrThumbnailUnsupported = errors.New("team_asset_thumbnail_unsupported")
var thumbnailSlots = make(chan struct{}, 2)

func (s Service) Thumbnail(ctx context.Context, team, id string) ([]byte, error) {
	select {
	case thumbnailSlots <- struct{}{}:
		defer func() { <-thumbnailSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	asset, file, err := s.Open(ctx, team, id)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if asset.Kind != "image" {
		return nil, ErrThumbnailUnsupported
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
			return out.Bytes(), nil
		}
	}
	return nil, ErrThumbnailUnsupported
}
