package media

import (
	"fmt"
	"unicode/utf8"
)

func finishingGenerationPlan(p *Project, plan ShotPlan) (ShotPlan, error) {
	if !hasCinematicFinishing(p) {
		return plan, nil
	}
	// 原声开关与后期 API 开关职责不同；旧护栏把后期关闭误写成全量禁声，违背保留模型原声的选择。
	if p.Brief.GenerationPolicy == "whole-first" {
		plan.Prompt += "\n完整生成段：按段内拍点完成故事，允许已规划景别与换景；不要字幕、标题、平台界面、角标或水印。保持主体数量与商品形状、颜色、材质、包装，自然运动；不擅加淡黑或转场，允许已规划的遮挡接切。"
	} else {
		plan.Prompt += "\n统一后期素材镜头：不要字幕、标题、平台界面、角标或水印。保持商品形状、颜色、材质、包装，接续光线、色温、轴线与运动方向；自然运动，无悬浮光效。不擅加淡黑或转场，允许已规划的遮挡接切。"
	}
	// 根因：把每个生成段都要求完成动作并停声，会制造重复结尾；接点观察窗不等于静帧或静音余量。
	// 实际生成任务数才代表是否拼接；单段、自由直出及冻结公式不能被追加接点清单。
	if len(p.Shots) > 1 && !directPromptBrief(p.Brief) && !p.Brief.SourceOnly && p.SourceAnalysis == nil && (p.TemplateSnapshot == nil || p.TemplateSnapshot.Template.Experiment == nil) {
		plan.Prompt += "\n分段交接：前后各0.1秒是观察窗，不增减时长、不强制静帧或静音。按已有接点匹配状态与声场；保留完整台词、尾音，避开字音和关键动作声。中间段交棒，不重启或终止音乐；仅全片最后收束。不承诺跨段J/L cut、混轨或同一音乐轨。"
	}
	// 后期开关曾追加禁声要求；原生旁白、对白、音乐与音效由模型按照用户prompt生成。
	plan.Prompt += "声音由视频模型原生生成：旁白、对白、音乐、环境声与音效服从用户故事，声画同步；用户静音或无音乐要求优先。"
	if brand := p.Brief.Finishing.Brand; brand != nil {
		if err := validateVideoBrand(brand); err != nil {
			return ShotPlan{}, err
		}
		plan.Prompt += fmt.Sprintf("\n全片统一品牌环境：强调色%s、背景参考色%s仅用于非商品场景；不要改变商品颜色、包装、材质或标识，接续光线与空间。Logo非商品外观参考；准确文字与Logo由后期绘制，不自行猜字。", brand.PrimaryColor, brand.BackgroundColor)
	}
	if utf8.RuneCountInString(plan.Prompt) > 4000 {
		return ShotPlan{}, fmt.Errorf("video_prompt_too_long")
	}
	return plan, nil
}
