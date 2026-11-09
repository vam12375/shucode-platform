package media

import (
	"bytes"
	"context"
	"fmt"
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
	for _, duration := range []float64{30, 30.001} {
		f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
			return ReferenceMetadata{DurationSeconds: duration, Width: 720, Height: 1280}, nil
		}
		for _, tc := range []struct {
			name string
			data []byte
		}{{"reference.mp4", video}, {"reference.mov", mov}, {"reference.wav", audio}} {
			t.Run(fmt.Sprintf("%s/%g", tc.name, duration), func(t *testing.T) {
				a, err := f.s.UploadCanvasMedia(context.Background(), f.team, tc.name, "canvas", bytes.NewReader(tc.data))
				if duration == 30 && (err != nil || a.DurationSeconds != duration) {
					t.Fatalf("30s reference rejected: asset=%+v err=%v", a, err)
				}
				if duration > 30 && err == nil {
					t.Fatal("overlong reference accepted")
				}
			})
		}
	}
}
