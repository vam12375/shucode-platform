package media

import (
	"bytes"
	"context"
	"testing"
)

func TestCanvasUploadSniffsAndProbesPublicVideoAndAudio(t *testing.T) {
	f := setup(t)
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 3, Width: 720, Height: 1280}, nil
	}
	video := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...)
	mov := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'q', 't', ' ', ' '}, make([]byte, 512)...)
	audio := append([]byte("RIFF0000WAVE"), make([]byte, 512)...)
	for _, tc := range []struct {
		name, kind string
		data       []byte
	}{{"reference.mp4", "video", video}, {"reference.wav", "audio", audio}, {"reference.mov", "video", mov}} {
		a, err := f.s.UploadCanvasMedia(context.Background(), f.team, tc.name, "canvas", bytes.NewReader(tc.data))
		if err != nil || a.Kind != tc.kind || a.Internal || a.DurationSeconds != 3 || a.Checksum == "" {
			t.Fatalf("asset=%+v err=%v", a, err)
		}
	}
	if _, err := f.s.UploadCanvasMedia(context.Background(), f.team, "fake.mp4", "canvas", bytes.NewBufferString("not a video")); err == nil {
		t.Fatal("extension trusted without media signature")
	}
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 16, Width: 720, Height: 1280}, nil
	}
	if _, err := f.s.UploadCanvasMedia(context.Background(), f.team, "long.wav", "canvas", bytes.NewReader(audio)); err == nil {
		t.Fatal("overlong reference accepted")
	}
}
