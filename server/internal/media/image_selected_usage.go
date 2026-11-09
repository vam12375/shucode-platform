package media

import "fmt"

// 仅在新方案与生成准入使用；不能让新增覆盖规则阻断历史成片的读取。
func validateSelectedImageUsage(b Brief, p Plan) error {
	if !b.ImageAnalysisFirst {
		return nil
	}
	roles, used := map[string]bool{}, map[string]bool{}
	if p.Story != nil {
		for _, role := range p.Story.AssetRoles {
			if role.Role == "unused" {
				return fmt.Errorf("video_story_selected_asset_unused")
			}
			roles[role.AssetID] = true
		}
	}
	for _, shot := range p.Shots {
		if shot.Creative != nil && shot.Creative.Source == "generated" {
			continue
		}
		if b.ReferenceMode != "multimodal" {
			if shot.AssetID != "" {
				used[shot.AssetID] = true
			}
		} else if shot.Creative != nil {
			for _, id := range shot.Creative.AssetIDs {
				used[id] = true
			}
		}
	}
	for _, id := range b.AssetIDs {
		if !roles[id] || !used[id] {
			return fmt.Errorf("video_story_selected_asset_missing")
		}
	}
	return nil
}
