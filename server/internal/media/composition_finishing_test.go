package media

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFinishingTransitionsPreserveDeliveryClock(t *testing.T) {
	p := &Project{Brief: Brief{Mode: "auto", Duration: 6, Finishing: &FinishingOptions{Subtitles: "none"}}, Shots: []Shot{
		{Plan: ShotPlan{Duration: 2}},
		{Plan: ShotPlan{Duration: 2, Transition: "fade"}},
		{Plan: ShotPlan{Duration: 2, Transition: "cut"}},
	}}
	segments, err := compositionTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	if segments[0].frames+segments[1].frames+segments[2].frames != 180 || segments[1].start != 2 || segments[2].end != 6 {
		t.Fatalf("delivery clock changed: %+v", segments)
	}
	if got := finishingOverlapFrames(p, segments, 0); got != 9 {
		t.Fatalf("fade overlap=%d want 9 tail frames", got)
	}
	if got := finishingOverlapFrames(p, segments, 1); got != 0 {
		t.Fatalf("cut must not overlap: %d", got)
	}
	filter := finishingPictureFilter(p, segments)
	if !strings.Contains(filter, "xfade=transition=fade:duration=0.300000000:offset=2.000000000") || !strings.Contains(filter, "concat=n=2:v=1:a=0") || strings.Contains(filter, "fade=t=") {
		t.Fatalf("unexpected picture filter: %s", filter)
	}
}

func TestFinishingGenerationGuardDoesNotMutateStoredPlan(t *testing.T) {
	p := &Project{Brief: Brief{Mode: "auto", Finishing: &FinishingOptions{Subtitles: "none"}}}
	plan := ShotPlan{Prompt: "阳光下商品的近景", Narration: "日常的一点从容。", Subtitle: "持久字幕"}
	guarded, err := finishingGenerationPlan(p, plan)
	if err != nil || !strings.Contains(guarded.Prompt, "不要字幕") || !strings.Contains(guarded.Prompt, "音乐") || !strings.Contains(guarded.Prompt, plan.Prompt) || plan.Prompt != "阳光下商品的近景" {
		t.Fatalf("guarded=%+v err=%v original=%+v", guarded, err, plan)
	}
	legacy, err := finishingGenerationPlan(&Project{}, plan)
	if err != nil || legacy.Prompt != plan.Prompt {
		t.Fatalf("legacy prompt changed: %+v %v", legacy, err)
	}
	if _, err = finishingGenerationPlan(p, ShotPlan{Prompt: strings.Repeat("字", 3999)}); err == nil {
		t.Fatal("oversize guarded prompt was silently truncated")
	}
}

func TestBrandGenerationGuardKeepsProductColorsAndReservesExactArtForPost(t *testing.T) {
	p := &Project{Brief: Brief{Mode: "auto", Finishing: &FinishingOptions{Brand: &VideoBrand{Preset: "clean", PrimaryColor: "#223344", BackgroundColor: "#556677", TextColor: "#FFFFFF", FontFamily: "sans", LogoAssetID: "brand-logo"}}}}
	original := ShotPlan{Prompt: "红色包装商品，柔和侧光"}
	guarded, err := finishingGenerationPlan(p, original)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"#223344", "#556677", "不要改变商品", "Logo", "后期", original.Prompt} {
		if !strings.Contains(guarded.Prompt, required) {
			t.Fatalf("missing deterministic brand guard %q: %s", required, guarded.Prompt)
		}
	}
	if original.Prompt != "红色包装商品，柔和侧光" {
		t.Fatal("stored user prompt mutated")
	}
}

// 根因：统一要求每段首尾完成和停顿，会让中间段形成重复收尾；护栏只能保留完整台词与接点意图。
func TestWholeFirstGenerationGuardPreservesSegmentHandoff(t *testing.T) {
	p := &Project{Brief: Brief{Mode: "auto", GenerationPolicy: "whole-first", Finishing: &FinishingOptions{Subtitles: "none"}}, Shots: []Shot{{}, {}}}
	plan := ShotPlan{Prompt: "风格基因：暖光纪实。末尾手部从左向右经过镜头，下一段同方向揭开；旁白先完整收句，环境声保持同一空间。", Sound: "同一暖声线，音乐母题保持进行态"}
	guarded, err := finishingGenerationPlan(p, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"首尾保留动作完成态和声音停顿", "acrossfade", "自动跨轨混音"} {
		if strings.Contains(guarded.Prompt, forbidden) {
			t.Fatalf("guard introduced a conflicting seam requirement %q", forbidden)
		}
	}
	for _, required := range []string{plan.Prompt, "前后各0.1秒", "观察窗", "完整台词", "中间段", "全片最后", "不承诺跨段J/L cut", "不增减时长"} {
		if !strings.Contains(guarded.Prompt, required) {
			t.Errorf("missing handoff guard %q: %s", required, guarded.Prompt)
		}
	}
	if plan.Sound != guarded.Sound || plan.Prompt == guarded.Prompt || strings.Contains(plan.Prompt, "观察窗") {
		t.Fatal("stored plan or sound was rewritten")
	}
}

func TestSegmentHandoffGuardOnlyAppliesToOrdinaryMultipleGenerations(t *testing.T) {
	for _, item := range []struct {
		name   string
		mutate func(*Project)
		want   bool
	}{
		{"ordinary-multiple", func(*Project) {}, true},
		{"whole-first-multiple", func(p *Project) { p.Brief.GenerationPolicy = "whole-first" }, true},
		{"15-second-single", func(p *Project) { p.Shots = p.Shots[:1]; p.Brief.GenerationPolicy = "whole-first" }, false},
		{"direct", func(p *Project) { p.Brief.PromptPolicy = "direct" }, false},
		{"formula", func(p *Project) {
			p.TemplateSnapshot = &TemplateSnapshot{Template: VideoTemplate{Experiment: &TemplateExperiment{}}}
		}, false},
		{"source-only", func(p *Project) { p.Brief.SourceOnly = true }, false},
		{"reference", func(p *Project) { p.Brief.ReferenceAssetID = "reference" }, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			p := &Project{Brief: Brief{Mode: "auto", Duration: 15, Finishing: &FinishingOptions{Subtitles: "none"}}, Shots: []Shot{{}, {}}}
			item.mutate(p)
			plan := ShotPlan{Prompt: "用户原文：角色说“你好”，前景经过后继续行动。"}
			guarded, err := finishingGenerationPlan(p, plan)
			if err != nil || strings.Contains(guarded.Prompt, "分段交接：") != item.want {
				t.Fatalf("guarded=%q err=%v wantHandoff=%v", guarded.Prompt, err, item.want)
			}
			if !strings.HasPrefix(guarded.Prompt, plan.Prompt) || strings.Contains(guarded.Prompt, "首尾保留动作完成态和声音停顿") {
				t.Fatal("guard rewrote user intent or forced boundary pauses")
			}
		})
	}
}

// 客户端预留400字给平台；最坏组合（多段、品牌）也不能拒绝合法3600字提示词。
func TestGenerationGuardFitsReservedPromptBudgetIncludingBrand(t *testing.T) {
	for _, policy := range []string{"", "whole-first"} {
		p := &Project{Brief: Brief{Mode: "auto", GenerationPolicy: policy, Finishing: &FinishingOptions{Brand: &VideoBrand{Preset: "clean", PrimaryColor: "#223344", BackgroundColor: "#556677", TextColor: "#FFFFFF", FontFamily: "sans", LogoAssetID: "brand-logo"}}}, Shots: []Shot{{}, {}}}
		plan := ShotPlan{Prompt: strings.Repeat("字", 3600)}
		guarded, err := finishingGenerationPlan(p, plan)
		if err != nil || utf8.RuneCountInString(guarded.Prompt) > 4000 || !strings.HasPrefix(guarded.Prompt, plan.Prompt) {
			t.Fatalf("policy=%q guardedLength=%d err=%v", policy, utf8.RuneCountInString(guarded.Prompt), err)
		}
	}
}
