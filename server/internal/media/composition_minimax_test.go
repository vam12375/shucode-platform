package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func h3CompositionProject(upstream, resolution, ratio string) *Project {
	return &Project{
		TeamID: "fixture-team",
		Brief:  Brief{VideoModel: "h3-model", Resolution: resolution, AspectRatio: ratio},
		Models: []Model{{ID: "h3-model", Kind: "video", Adapter: "toapis", UpstreamModel: upstream}},
		Shots:  []Shot{{Status: "succeeded", OutputAssetID: "saved-video"}},
	}
}

func TestMiniMaxCompositionUsesPersistedOutputDimensions(t *testing.T) {
	for _, tc := range []struct {
		upstream, resolution, ratio    string
		sourceW, sourceH, wantW, wantH int
	}{
		{"MiniMax-H3", "2K", "9:16", 2048, 1152, 1152, 2048},
		{"MiniMax-H3", "768p", "16:9", 768, 1366, 1366, 768},
		{"MiniMax-H3-Max", "480p", "1:1", 854, 480, 480, 480},
		{"MiniMax-H3-Max-Turbo", "768p", "21:9", 1366, 769, 1792, 768},
	} {
		t.Run(tc.upstream+"/"+tc.resolution+"/"+tc.ratio, func(t *testing.T) {
			p := h3CompositionProject(tc.upstream, tc.resolution, tc.ratio)
			calls := 0
			s := &Service{Blobs: Blobs{Root: t.TempDir()}, ReferenceProbe: func(ctx context.Context, path string) (ReferenceMetadata, error) {
				calls++
				if ctx == nil || filepath.Base(path) != "saved-video" || filepath.Base(filepath.Dir(path)) != p.TeamID {
					t.Fatalf("probe must read the persisted same-team output: %s", path)
				}
				return ReferenceMetadata{DurationSeconds: 5, Width: tc.sourceW, Height: tc.sourceH}, nil
			}}
			w, h, err := s.compositionOutputSize(context.Background(), p)
			if err != nil || calls != 1 || w != tc.wantW || h != tc.wantH || w%2 != 0 || h%2 != 0 {
				t.Fatalf("size=%dx%d calls=%d err=%v, want %dx%d", w, h, calls, err, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestMiniMaxCompositionRejectsMissingOrInvalidOutputMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata ReferenceMetadata
		probeErr error
		change   func(*Project)
	}{
		{name: "probe-failure", probeErr: fmt.Errorf("private diagnostics")},
		{name: "no-video", metadata: ReferenceMetadata{DurationSeconds: 5}},
		{name: "one-pixel", metadata: ReferenceMetadata{DurationSeconds: 5, Width: 1, Height: 768}},
		{name: "excessive-source", metadata: ReferenceMetadata{DurationSeconds: 5, Width: 32768, Height: 768}},
		{name: "excessive-output", metadata: ReferenceMetadata{DurationSeconds: 5, Width: 16384, Height: 16384}, change: func(p *Project) { p.Brief.AspectRatio = "21:9" }},
		{name: "missing-output", change: func(p *Project) { p.Shots[0].OutputAssetID = "" }},
		{name: "missing-shot", change: func(p *Project) { p.Shots = nil }},
		{name: "auto", change: func(p *Project) { p.Brief.AspectRatio = "auto" }},
		{name: "unknown-ratio", change: func(p *Project) { p.Brief.AspectRatio = "3:2" }},
		{name: "missing-model", change: func(p *Project) { p.Models = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := h3CompositionProject("MiniMax-H3", "2K", "9:16")
			if tc.change != nil {
				tc.change(p)
			}
			s := &Service{Blobs: Blobs{Root: t.TempDir()}, ReferenceProbe: func(context.Context, string) (ReferenceMetadata, error) { return tc.metadata, tc.probeErr }}
			w, h, err := s.compositionOutputSize(context.Background(), p)
			if err == nil || w != 0 || h != 0 {
				t.Fatalf("invalid output metadata accepted: %dx%d %v", w, h, err)
			}
		})
	}
}

func TestMiniMaxCompositionDoesNotChangeArkSizesOrTokenQuotes(t *testing.T) {
	s := &Service{ReferenceProbe: func(context.Context, string) (ReferenceMetadata, error) {
		t.Fatal("Ark sizes must not probe H3 metadata")
		return ReferenceMetadata{}, nil
	}}
	p := h3CompositionProject("doubao-seedance-2-0-mini-260615", "480p", "16:9")
	p.Models[0].Adapter = "ark"
	w, h, err := s.compositionOutputSize(context.Background(), p)
	if err != nil || w != 864 || h != 496 {
		t.Fatalf("Ark size changed: %dx%d %v", w, h, err)
	}
	if estimateVideoTokens("2K", "16:9", 15, 5) != 0 || estimateVideoTokens("768p", "16:9", 15, 5) != 0 {
		t.Fatal("H3 dimensions must not be guessed by Ark token pricing")
	}
}

func TestMiniMaxMultiClipCompositionUsesRealAdaptiveOutput(t *testing.T) {
	if !CompositionAvailable() {
		t.Skip("ffmpeg unavailable")
	}
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	video, err := os.ReadFile("testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(f.s.Blobs.Path(f.team, "generated")), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.s.Blobs.Path(f.team, "generated"), video, 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := ProbeReference(ctx, f.s.Blobs.Path(f.team, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.Resolution, v.Brief.AspectRatio = "768p", "9:16"
		v.Models[0].Adapter, v.Models[0].UpstreamModel = "toapis", "MiniMax-H3"
		for i := range v.Shots {
			v.Shots[i].Status, v.Shots[i].OutputAssetID = "succeeded", "generated"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Status != "completed" || p.OutputAssetID == "" || f.submits.Load() != 0 {
		t.Fatalf("composition did not complete from existing outputs: %+v", p)
	}
	output, err := ProbeReference(ctx, f.s.Blobs.Path(f.team, p.OutputAssetID))
	if err != nil {
		t.Fatal(err)
	}
	short := min(metadata.Width, metadata.Height) / 2 * 2
	if output.Width != short || output.Height%2 != 0 || output.Height <= output.Width || output.DurationSeconds < 9.9 || output.DurationSeconds > 10.2 {
		t.Fatalf("composition lost source dimensions, delivery ratio or timeline: source=%+v output=%+v", metadata, output)
	}
}
