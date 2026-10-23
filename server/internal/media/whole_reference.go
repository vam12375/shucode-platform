package media

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

func referenceReviewPlan(project *Project, plan Plan) (Plan, error) {
	if project.Canvas != nil || project.Brief.ReferenceAssetID == "" || project.Brief.GenerationMode != "video_reference" || len(project.Shots) <= 1 {
		return plan, nil
	}
	if project.Status != "review" || project.CancelRequested || project.ChargedMicro != 0 || project.OutputAssetID != "" || project.SourceAnalysis == nil {
		return Plan{}, ErrConflict
	}
	for _, shot := range project.Shots {
		if shot.Status != "pending" || shot.Attempt > 1 || shot.VideoTaskID != "" || shot.ProviderError != nil || shot.ImageAssetID != "" || shot.OutputAssetID != "" || shot.ResultURL != "" || shot.ReservationID != "" || shot.Stage != "" || shot.ChargedMicro != 0 || shot.PendingChargeMicro != 0 || shot.ReservedMicro != 0 || shot.ReleasedMicro != 0 || shot.ProviderUsageTokens != 0 || shot.TokenChargeMicro != 0 {
			return Plan{}, ErrConflict
		}
	}
	// 只有用户显式保存尚未执行的旧审阅方案才丢弃逐镜裁片；不能把局部片段冒充完整参考。
	// 复制请求切片，避免重建执行计划时修改调用方或旧记录中的原片边界。
	plan.Shots = append([]ShotPlan(nil), plan.Shots...)
	for i := range plan.Shots {
		plan.Shots[i].ReferenceVideoAssetID = ""
	}
	return plan, nil
}

// 审阅镜头保留原片边界；执行镜头按完整参考视频派生，避免把闪切误当成独立供应商输入。
func buildWholeReferenceExecution(b Brief, plan Plan, model Model, models []Model) ([]Shot, int64, error) {
	if model.VideoReference == nil {
		return nil, 0, fmt.Errorf("video_reference_unsupported")
	}
	if b.ReferenceDurationSeconds < float64(MinReferenceClipSeconds)-.001 || b.ReferenceDurationSeconds > float64(model.VideoReference.MaxDurationSeconds)+.001 {
		return nil, 0, fmt.Errorf("video_reference_duration_unsupported")
	}
	duration := 0
	for _, candidate := range model.AllowedDurations() {
		if float64(candidate)+.001 >= b.ReferenceDurationSeconds && (duration == 0 || candidate < duration) {
			duration = candidate
		}
	}
	if duration == 0 {
		return nil, 0, fmt.Errorf("video_reference_duration_unsupported")
	}
	var script strings.Builder
	if b.ReplacementInstructions != "" {
		script.WriteString("替换目标：" + b.ReplacementInstructions + "\n")
	}
	firstImage, firstImagePrompt, clipID := "", plan.Shots[0].ImagePrompt, ""
	images := map[string]bool{}
	for _, shot := range plan.Shots {
		if shot.AssetID != "" {
			images[shot.AssetID] = true
			if firstImage == "" {
				firstImage = shot.AssetID
			}
		}
		if shot.ReferenceVideoAssetID != "" {
			if clipID != "" && clipID != shot.ReferenceVideoAssetID {
				return nil, 0, fmt.Errorf("video_reference_plan_invalid")
			}
			clipID = shot.ReferenceVideoAssetID
		}
		script.WriteString("[" + strconv.FormatFloat(shot.SourceStart, 'f', -1, 64) + "–" + strconv.FormatFloat(shot.SourceEnd, 'f', -1, 64) + "秒]\n")
		// 审阅编辑的身份、场景和转场必须进入实际供应商脚本，不能只更新展示字段。
		if shot.Subject != "" {
			script.WriteString("主体：" + shot.Subject + "\n")
		}
		if shot.Scene != "" {
			script.WriteString("场景：" + shot.Scene + "\n")
		}
		if shot.Transition != "" {
			script.WriteString("转场方式：" + shot.Transition + "\n")
		}
		if shot.TransitionDescription != "" {
			script.WriteString("转场描述：" + shot.TransitionDescription + "\n")
		}
		script.WriteString(shot.Prompt + "\n")
		if shot.Sound != "" {
			script.WriteString("声音：" + shot.Sound + "\n")
		}
		if shot.Subtitle != "" {
			script.WriteString("字幕：" + shot.Subtitle + "\n")
		}
	}
	if b.ImageModel == "" && len(images) > model.VideoReference.MaxImages {
		return nil, 0, fmt.Errorf("video_reference_image_limit")
	}
	// 客户端 Zod 字符串上限按 UTF-16 计数，表情符号不能让派生执行镜头超出同一接收合同。
	if len(utf16.Encode([]rune(script.String()))) > 4000 {
		return nil, 0, fmt.Errorf("video_reference_prompt_limit")
	}
	quote := videoReferenceQuoteMicro(model.VideoReference, b.Resolution, b.AspectRatio, duration)
	if b.ImageModel != "" {
		if firstImagePrompt == "" {
			return nil, 0, fmt.Errorf("first_frame_required")
		}
		imageModel, err := FindModel(models, b.ImageModel, "image")
		if err != nil {
			return nil, 0, err
		}
		quote += imageModel.PriceMicro
	} else if firstImage == "" {
		return nil, 0, fmt.Errorf("first_frame_required")
	}
	if quote > b.MaxCreditsMicro {
		return nil, 0, fmt.Errorf("duration_or_budget_exceeded")
	}
	execution := plan.Shots[0]
	execution.SourceStart, execution.SourceEnd, execution.Duration = 0, b.ReferenceDurationSeconds, duration
	execution.AssetID, execution.ReferenceVideoAssetID = firstImage, clipID
	execution.Prompt, execution.Subtitle = strings.TrimSpace(script.String()), ""
	// 历史方案携带首帧描述不代表用户选择生图；显式模型才拥有执行及计费开关。
	execution.ImagePrompt = ""
	if b.ImageModel != "" {
		execution.ImagePrompt = firstImagePrompt
	}
	return []Shot{{Plan: execution, Status: "pending", Attempt: 1, QuoteMicro: quote}}, quote, nil
}
