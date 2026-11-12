package media

import (
	"fmt"
	"reflect"
	"testing"
)

func seedance25CapacityModel() Model {
	m := fixtureVideoModel()
	m.ID, m.UpstreamModel = "seedance-2.5-720p", "doubao-seedance-2-5-260628"
	m.MinDuration, m.MaxDuration = 4, 30
	return m
}

func capacityAssets(count int) ([]string, []Asset) {
	ids := make([]string, count)
	assets := make([]Asset, count)
	for i := range count {
		ids[i] = fmt.Sprintf("asset-%02d", i)
		assets[i] = Asset{ID: ids[i], Kind: "image"}
	}
	return ids, assets
}

// 与客户端 shared/videoImageCapacity 同一组用例，保证两端对同一目录得出相同上限与段长。
func TestBriefImageCapacityMatchesClient(t *testing.T) {
	s20, s25 := fixtureVideoModel(), seedance25CapacityModel()
	for _, tc := range []struct {
		name     string
		model    Model
		mode     string
		duration int
		want     int
	}{
		{"seedance20-15s", s20, "multimodal", 15, 30},
		{"seedance20-10s", s20, "multimodal", 10, 27},
		{"frames-15s", s20, "frames", 15, 4},
		{"seedance25-30s", s25, "multimodal", 30, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := briefImageCapacity(Brief{ReferenceMode: tc.mode, Duration: tc.duration}, tc.model)
			if !ok || got != tc.want {
				t.Fatalf("capacity=%d ok=%v, want %d", got, ok, tc.want)
			}
		})
	}
	for count, want := range map[int][]int{9: {15}, 10: {8, 7}, 18: {8, 7}, 19: {5, 5, 5}, 28: {4, 4, 4, 4}} {
		ids, _ := capacityAssets(count)
		b := Brief{ImageAnalysisFirst: true, ReferenceMode: "multimodal", Duration: 15, AssetIDs: ids}
		if got := planImageGenerationDurations(b, s20); !reflect.DeepEqual(got, want) {
			t.Fatalf("%d images: durations=%v, want %v", count, got, want)
		}
	}
	if got := planImageGenerationDurations(Brief{ImageAnalysisFirst: true, ReferenceMode: "multimodal", Duration: 15, AssetIDs: make([]string, 10)}, s25); !reflect.DeepEqual(got, []int{15}) {
		t.Fatalf("seedance 2.5 should fit 10 images in one pass, got %v", got)
	}
}

func TestValidateBriefRejectsImagesBeyondModelCapacity(t *testing.T) {
	m := fixtureVideoModel()
	brief := func(count, duration int, mode string) (Brief, []Asset) {
		ids, assets := capacityAssets(count)
		// 与客户端图片优先入口实际提交的 brief 一致（whole-first 需要故事规划与后期字段）。
		return Brief{Mode: "auto", ImageAnalysisFirst: true, GenerationPolicy: "whole-first", CreativeExpansion: "related", ResearchPolicy: "disabled", ReferenceURLs: []string{}, Finishing: &FinishingOptions{Subtitles: "none"}, ReferenceMode: mode, Product: "产品", AssetIDs: ids, Platforms: []string{}, VideoModel: m.ID, Resolution: "720p", AspectRatio: "16:9", Duration: duration}, assets
	}
	capacityError := "video_generation_image_capacity_exceeded"
	for _, tc := range []struct {
		count, duration int
		mode            string
		reject          bool
	}{
		{27, 10, "multimodal", false},
		{28, 10, "multimodal", true},
		{30, 15, "multimodal", false},
		{4, 15, "frames", false},
		{5, 15, "frames", true},
	} {
		b, assets := brief(tc.count, tc.duration, tc.mode)
		err := ValidateBrief(b, []Model{m}, assets)
		if rejected := err != nil && err.Error() == capacityError; rejected != tc.reject {
			t.Fatalf("%+v: err=%v", tc, err)
		}
	}
	// 协议上限 30；超过时属于非法参数而不是模型容量。
	b, assets := brief(MaxBriefImages+1, 15, "multimodal")
	if err := ValidateBrief(b, []Model{m}, assets); err != ErrInvalid {
		t.Fatalf("31 images err=%v, want ErrInvalid", err)
	}
	// 旧流程（非图片优先）不受新容量准入影响。
	b, assets = brief(5, 15, "frames")
	b.ImageAnalysisFirst = false
	if err := ValidateBrief(b, []Model{m}, assets); err != nil {
		t.Fatalf("legacy brief should not use image capacity admission: %v", err)
	}
}

func TestAutoMatchedProductAssetsStayAtTwenty(t *testing.T) {
	_, assets := capacityAssets(25)
	for i := range assets {
		assets[i].Product = "杯子"
	}
	if got := matchProductAssets("杯子", assets); len(got) != autoMatchedBriefImages {
		t.Fatalf("auto matched=%d, want %d", len(got), autoMatchedBriefImages)
	}
}
