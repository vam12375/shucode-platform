package media

import (
	"fmt"
	"unicode/utf8"
)

func finishingGenerationPlan(p *Project, plan ShotPlan) (ShotPlan, error) {
	if !hasCinematicFinishing(p) {
		return plan, nil
	}
	// 根因是逐镜模型把独立音乐、字幕和淡黑烘焙进素材，后期拼接无法可靠去除。
	// 统一音轨与文字由平台后期唯一负责，生成端只提交干净、可组接的摄影画面。
	plan.Prompt += "\n统一后期素材镜头：只生成干净摄影画面，不要字幕、标题、平台界面、角标、水印、解说、音乐和音效。保持参考商品形状、颜色、材质和包装，保持跨镜头光线、色温、空间轴线和运动方向一致。自然物理运动，无悬浮光效；不要在首尾淡黑或自行转场。"
	if utf8.RuneCountInString(plan.Prompt) > 4000 {
		return ShotPlan{}, fmt.Errorf("video_prompt_too_long")
	}
	return plan, nil
}
