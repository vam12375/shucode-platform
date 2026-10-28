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
	// 当前平台派生的多段执行允许 Host 在事实计划上按段回填绑定；只有旧逐镜执行才迁移。
	if project.Plan != nil {
		withoutBindings := *project.Plan
		withoutBindings.Shots = append([]ShotPlan(nil), project.Plan.Shots...)
		for i := range withoutBindings.Shots {
			withoutBindings.Shots[i].ReferenceVideoAssetID = ""
			withoutBindings.Shots[i].ReferenceAudioAssetID = ""
		}
		derived, _, err := BuildShots(project.Brief, withoutBindings, project.Models)
		if err == nil && len(derived) == len(project.Shots) {
			matches := true
			for i := range derived {
				current := project.Shots[i].Plan
				current.ReferenceVideoAssetID = ""
				current.ReferenceAudioAssetID = ""
				if current != derived[i].Plan {
					matches = false
					break
				}
			}
			if matches {
				return plan, nil
			}
		}
	}
	if project.Status != "review" || project.CancelRequested || project.ChargedMicro != 0 || project.OutputAssetID != "" || project.SourceAnalysis == nil {
		return Plan{}, ErrConflict
	}
	for _, shot := range project.Shots {
		// 路由绑定也是执行证据；复用统一判定，避免审阅重建丢掉已冻结的供应商归属。
		if shot.Status != "pending" || shot.Attempt > 1 || referenceExecutionHasEvidence(shot) {
			return Plan{}, ErrConflict
		}
	}
	// 只有用户显式保存尚未执行的审阅方案才丢弃旧裁片；复制切片避免修改调用方数据。
	plan.Shots = append([]ShotPlan(nil), plan.Shots...)
	for i := range plan.Shots {
		plan.Shots[i].ReferenceVideoAssetID = ""
		plan.Shots[i].ReferenceAudioAssetID = ""
	}
	return plan, nil
}

type wholeReferenceSegment struct{ first, last int }

func videoReferenceMaxSegments(capability *VideoReferenceCapability) int {
	if capability == nil || capability.MaxSegments <= 1 {
		return 1
	}
	return capability.MaxSegments
}

// 分段只能落在事实镜头边界。动态规划求最少连续段，并优先选择最远的相同最优边界。
func partitionWholeReference(shots []ShotPlan, capability *VideoReferenceCapability) ([]wholeReferenceSegment, error) {
	if capability == nil || len(shots) == 0 {
		return nil, fmt.Errorf("video_reference_unsupported")
	}
	n := len(shots)
	const unreachable = int(^uint(0) >> 1)
	dp := make([]int, n+1)
	next := make([]int, n)
	for i := range dp {
		dp[i] = unreachable
	}
	dp[n] = 0
	for i := n - 1; i >= 0; i-- {
		for j := i + 1; j <= n; j++ {
			duration := shots[j-1].SourceEnd - shots[i].SourceStart
			if duration > float64(capability.MaxDurationSeconds)+.001 {
				break
			}
			if duration < float64(MinReferenceClipSeconds)-.001 || dp[j] == unreachable {
				continue
			}
			candidate := 1 + dp[j]
			if candidate <= dp[i] {
				dp[i], next[i] = candidate, j
			}
		}
	}
	if dp[0] == unreachable || dp[0] > videoReferenceMaxSegments(capability) {
		return nil, fmt.Errorf("video_reference_duration_unsupported")
	}
	out := make([]wholeReferenceSegment, 0, dp[0])
	for i := 0; i < n; i = next[i] {
		if next[i] <= i {
			return nil, fmt.Errorf("video_reference_duration_unsupported")
		}
		out = append(out, wholeReferenceSegment{first: i, last: next[i]})
	}
	return out, nil
}

func wholeReferenceOutputDuration(model Model, sourceDuration float64) int {
	duration := 0
	for _, candidate := range model.AllowedDurations() {
		if float64(candidate)+.001 >= sourceDuration && (duration == 0 || candidate < duration) {
			duration = candidate
		}
	}
	return duration
}

func appendWholeReferenceScript(script *strings.Builder, shot ShotPlan) {
	script.WriteString("[" + strconv.FormatFloat(shot.SourceStart, 'f', -1, 64) + "–" + strconv.FormatFloat(shot.SourceEnd, 'f', -1, 64) + "秒]\n")
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

// 审阅镜头保留原片边界；执行镜头按能力在事实边界派生最少连续参考段。
func buildWholeReferenceExecution(b Brief, plan Plan, model Model, models []Model) ([]Shot, int64, error) {
	segments, err := partitionWholeReference(plan.Shots, model.VideoReference)
	if err != nil {
		return nil, 0, err
	}
	executions := make([]Shot, 0, len(segments))
	var total int64
	for _, segment := range segments {
		facts := plan.Shots[segment.first:segment.last]
		sourceDuration := facts[len(facts)-1].SourceEnd - facts[0].SourceStart
		duration := wholeReferenceOutputDuration(model, sourceDuration)
		if duration == 0 {
			return nil, 0, fmt.Errorf("video_reference_duration_unsupported")
		}
		var script strings.Builder
		if b.ReplacementInstructions != "" {
			script.WriteString("替换目标：" + b.ReplacementInstructions + "\n")
		}
		// 段首图的语义是执行段第一个事实镜头的起始画面。不能从段内后续镜头借图，
		// 否则 Logo 或局部素材会被错误地提升为整段首帧，造成每个任务首帧重复。
		firstImage, firstImagePrompt, clipID, audioID := facts[0].AssetID, facts[0].ImagePrompt, "", ""
		images := map[string]bool{}
		boundClips, boundAudios := 0, 0
		for _, fact := range facts {
			if fact.AssetID != "" {
				images[fact.AssetID] = true
			}
			if fact.ReferenceVideoAssetID != "" {
				boundClips++
				if clipID != "" && clipID != fact.ReferenceVideoAssetID {
					return nil, 0, fmt.Errorf("video_reference_plan_invalid")
				}
				clipID = fact.ReferenceVideoAssetID
			}
			if fact.ReferenceAudioAssetID != "" {
				boundAudios++
				if audioID != "" && audioID != fact.ReferenceAudioAssetID {
					return nil, 0, fmt.Errorf("video_reference_plan_invalid")
				}
				audioID = fact.ReferenceAudioAssetID
			}
			appendWholeReferenceScript(&script, fact)
		}
		if boundClips != 0 && boundClips != len(facts) || boundAudios != 0 && boundAudios != len(facts) {
			return nil, 0, fmt.Errorf("video_reference_plan_invalid")
		}
		if b.ImageModel == "" && len(images) > model.VideoReference.MaxImages {
			return nil, 0, fmt.Errorf("video_reference_image_limit")
		}
		if len(utf16.Encode([]rune(script.String()))) > 4000 {
			return nil, 0, fmt.Errorf("video_reference_prompt_limit")
		}
		quote := videoReferenceQuoteMicro(model.VideoReference, b.Resolution, b.AspectRatio, duration)
		if b.ImageModel != "" {
			if firstImagePrompt == "" {
				return nil, 0, fmt.Errorf("first_frame_required")
			}
			imageModel, findErr := FindModel(models, b.ImageModel, "image")
			if findErr != nil {
				return nil, 0, findErr
			}
			quote += imageModel.PriceMicro
		} else if firstImage == "" {
			return nil, 0, fmt.Errorf("first_frame_required")
		}
		execution := facts[0]
		execution.SourceStart, execution.SourceEnd, execution.Duration = facts[0].SourceStart, facts[len(facts)-1].SourceEnd, duration
		execution.AssetID, execution.ReferenceVideoAssetID, execution.ReferenceAudioAssetID = firstImage, clipID, audioID
		execution.Prompt, execution.Subtitle = strings.TrimSpace(script.String()), ""
		execution.ImagePrompt = ""
		if b.ImageModel != "" {
			execution.ImagePrompt = firstImagePrompt
		}
		executions = append(executions, Shot{Plan: execution, Status: "pending", Attempt: 1, QuoteMicro: quote})
		total += quote
	}
	if total > b.MaxCreditsMicro {
		return nil, 0, fmt.Errorf("duration_or_budget_exceeded")
	}
	return executions, total, nil
}
