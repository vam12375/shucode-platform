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
		plan.Prompt += "\n完整生成段：按段内叙事拍点完成连贯故事，允许有依据的自然景别或场景变化。不要字幕、标题、平台界面、角标或水印；保持参考商品形状、颜色、材质、包装及主体数量一致，自然物理运动；首尾保留动作完成态和声音停顿，不自行淡黑。"
	} else {
		plan.Prompt += "\n统一后期素材镜头：只生成干净摄影画面，不要字幕、标题、平台界面、角标、水印。保持参考商品形状、颜色、材质和包装，保持跨镜头光线、色温、空间轴线和运动方向一致。自然物理运动，无悬浮光效；不要在首尾淡黑或自行转场。"
	}
	if p.Brief.Finishing.NativeAudio {
		plan.Prompt += "模型原声开启：按完整故事自然生成并保留人声、对白、音乐、环境声和音效，保持声画同步及前后声音连贯。后期独白或配乐开关不限制模型原声。"
	} else {
		plan.Prompt += "用户未保留模型原声：不要解说、音乐和音效，后期按明确选择的配置处理。"
	}
	if brand := p.Brief.Finishing.Brand; brand != nil {
		if err := validateVideoBrand(brand); err != nil {
			return ShotPlan{}, err
		}
		plan.Prompt += fmt.Sprintf("\n全片统一品牌环境：强调色%s、背景参考色%s，仅用于非商品场景与克制光色参考；不要改变商品真实颜色、包装、材质或标识，固定既有光线色温与空间关系。Logo与准确文字由平台后期绘制，品牌Logo不是商品外观参考，画面不自行生成Logo、字卡或猜测字样。", brand.PrimaryColor, brand.BackgroundColor)
	}
	if utf8.RuneCountInString(plan.Prompt) > 4000 {
		return ShotPlan{}, fmt.Errorf("video_prompt_too_long")
	}
	return plan, nil
}
