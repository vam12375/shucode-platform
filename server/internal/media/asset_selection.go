package media

import (
	"slices"
	"strings"
)

// 平台创建制作单时冻结素材；客户端只表达手选 ID 或自动检索意图，不能自行维护匹配结果。
func matchProductAssets(product string, assets []Asset) []string {
	query := strings.ToLower(strings.TrimSpace(product))
	if query == "" {
		return nil
	}
	best := 2
	matches := []Asset{}
	for _, asset := range assets {
		if asset.Kind != "image" {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(asset.Product))
		name := strings.ToLower(asset.Name)
		rank := 2
		if label == query {
			rank = 0
		} else if strings.Contains(label, query) || strings.Contains(name, query) {
			rank = 1
		}
		if rank < best {
			best = rank
			matches = nil
		}
		if rank == best && rank < 2 {
			matches = append(matches, asset)
		}
	}
	slices.SortFunc(matches, func(a, b Asset) int {
		if n := b.CreatedAt.Compare(a.CreatedAt); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	ids := make([]string, 0, min(20, len(matches)))
	for _, asset := range matches[:min(20, len(matches))] {
		ids = append(ids, asset.ID)
	}
	return ids
}
