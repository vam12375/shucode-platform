package media

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

var replicaImageMention = regexp.MustCompile(`@图片([-+]?[0-9]+)`)
var replicaVideoMention = regexp.MustCompile(`@视频([-+]?[0-9]+)`)

func replicaExecutionImageIDs(b Brief, facts []ShotPlan) []string {
	if b.ReferenceMode == "multimodal" {
		return uniqueReferenceImageIDs(b.AssetIDs)
	}
	if b.ImageModel != "" {
		if len(facts) == 0 {
			return nil
		}
		return []string{facts[0].AssetID}
	}
	ids := []string{}
	for _, fact := range facts {
		ids = append(ids, fact.AssetID)
	}
	return uniqueReferenceImageIDs(ids)
}

// 审阅使用制作单图片序号，分段提交只包含真实局部输入；不能让 @图片3 指向只含一张图的供应商请求。
func replicaExecutionReferenceText(b Brief, ids []string, video bool, text string) (string, error) {
	if b.ReferenceVideoPurpose == "" {
		return text, nil
	}
	invalid := false
	text = replicaImageMention.ReplaceAllStringFunc(text, func(mention string) string {
		index, err := strconv.Atoi(replicaImageMention.FindStringSubmatch(mention)[1])
		if err != nil || index < 1 || index > len(b.AssetIDs) || mention != "@图片"+strconv.Itoa(index) {
			invalid = true
			return mention
		}
		local := slices.Index(ids, b.AssetIDs[index-1])
		if local < 0 {
			invalid = true
			return mention
		}
		return "@图片" + strconv.Itoa(local+1)
	})
	text = replicaVideoMention.ReplaceAllStringFunc(text, func(mention string) string {
		if !video || mention != "@视频1" {
			invalid = true
		}
		return mention
	})
	if invalid {
		return "", fmt.Errorf("video_replica_reference_mapping_invalid")
	}
	return text, nil
}

func replicaExecutionFacts(b Brief, facts []ShotPlan, video bool) ([]ShotPlan, error) {
	if b.ReferenceVideoPurpose == "" {
		return facts, nil
	}
	ids := replicaExecutionImageIDs(b, facts)
	out := append([]ShotPlan(nil), facts...)
	for i := range out {
		shot := &out[i]
		// 首帧生图只收到静态图片，不能借用视频请求的参考资格。
		imagePrompt, err := replicaExecutionReferenceText(b, ids, false, shot.ImagePrompt)
		if err != nil {
			return nil, err
		}
		shot.ImagePrompt = imagePrompt
		for _, field := range []*string{&shot.Prompt, &shot.Framing, &shot.Camera, &shot.Action, &shot.Purpose, &shot.Subject, &shot.Scene, &shot.Sound, &shot.Subtitle, &shot.TransitionDescription} {
			text, err := replicaExecutionReferenceText(b, ids, video, *field)
			if err != nil {
				return nil, err
			}
			*field = text
		}
		shot.Anchors = append([]ReferenceAnchor(nil), shot.Anchors...)
		for j := range shot.Anchors {
			anchor := &shot.Anchors[j]
			for _, field := range []*string{&anchor.Framing, &anchor.Camera, &anchor.Action, &anchor.RequiredDetails} {
				text, err := replicaExecutionReferenceText(b, ids, video, *field)
				if err != nil {
					return nil, err
				}
				*field = text
			}
		}
	}
	return out, nil
}
