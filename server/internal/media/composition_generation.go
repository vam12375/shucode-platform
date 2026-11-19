package media

import (
	"fmt"
	"unicode/utf8"
)

func finishingGenerationPlan(p *Project, plan ShotPlan) (ShotPlan, error) {
	if !hasCinematicFinishing(p) {
		return plan, nil
	}
	// 固定光线和禁用悬浮光效曾覆盖分镜选定的转场与抽象镜；连续性只约束分镜中的连续动作。
	if p.Brief.GenerationPolicy == "whole-first" {
		plan.Prompt += "\n完整生成段：按段内拍点完成故事，执行分镜指定的景别、换景和转场。"
	} else {
		plan.Prompt += "\n统一后期素材镜头：按分镜取景，连续动作接续轴线与运动方向。"
	}
	plan.Prompt += "实物出镜时保持主体数量、外形、颜色、材质和包装。不要字幕、标题、平台界面、角标或水印，不擅加淡黑或转场。"
	// 根因：把每个生成段都要求完成动作并停声，会制造重复结尾；接点观察窗不等于静帧或静音余量。
	// 实际生成任务数才代表是否拼接；单段、自由直出及冻结公式不能被追加接点清单。
	if len(p.Shots) > 1 && !directPromptBrief(p.Brief) && !p.Brief.SourceOnly && p.SourceAnalysis == nil && (p.TemplateSnapshot == nil || p.TemplateSnapshot.Template.Experiment == nil) {
		plan.Prompt += "\n分段交接：前后各0.1秒是观察窗，不增减时长、不强制静帧或静音。按已有接点匹配状态与声场。保留已安排的完整台词与关键动作声。有配乐时中间段延续，全片最后收束。不承诺跨段J/L cut、混轨或同一音乐轨。"
	}
	// 逐项点名所有声部会诱导供应商给纯器乐或环境声镜头加配音，最终提交只执行本段选定的声音。
	plan.Prompt += "\n声音由视频模型原生生成：按本段已写明的声音与画面同步，保留留白。没有安排人声就保持无人声，没有安排音乐就不加配乐。用户指定的静音或其他声音要求优先。"
	if brand := p.Brief.Finishing.Brand; brand != nil {
		if err := validateVideoBrand(brand); err != nil {
			return ShotPlan{}, err
		}
		plan.Prompt += fmt.Sprintf("\n全片统一品牌环境：强调色%s、背景参考色%s仅用于非商品场景。不要改变商品颜色、包装、材质或标识，连续场景接续光线。Logo非商品外观参考，准确文字与Logo由后期绘制，不自行猜字。", brand.PrimaryColor, brand.BackgroundColor)
	}
	if utf8.RuneCountInString(plan.Prompt) > 4000 {
		return ShotPlan{}, fmt.Errorf("video_prompt_too_long")
	}
	return plan, nil
}
