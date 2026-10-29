package media

import (
	"context"
	"errors"
	"slices"
)

type CanvasTeamAssetArchive struct {
	SourceAssetID string `json:"sourceAssetId"`
	TeamAssetID   string `json:"teamAssetId,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

// CanvasArchiveInput 只传原生成者与已落盘媒体；适配器在组合层连接资产域，避免循环依赖。
type CanvasArchiveInput struct {
	TeamID, UserID, ProjectID string
	Source                    Asset
	Previous                  CanvasTeamAssetArchive
}

func generatedArchiveSources(p Project) []string {
	var sources []string
	if isDirectGeneration(p) {
		for _, shot := range p.Shots[:min(len(p.Shots), 4)] {
			if shot.Status != "succeeded" || shot.OutputAssetID == "" {
				continue
			}
			// 仅 Canvas 可明确请求尾帧；Agent 与普通制作的内部帧不归档。
			ids := []string{shot.OutputAssetID}
			if p.Canvas != nil {
				ids = append(ids, shot.LastFrameAssetID)
			}
			for _, id := range ids {
				if id != "" && !slices.Contains(sources, id) {
					sources = append(sources, id)
				}
			}
		}
	} else if p.Status == "completed" && p.OutputAssetID != "" {
		// 普通视频制作的成片是唯一公开交付物，镜头输出只用于服务端合成。
		sources = append(sources, p.OutputAssetID)
	}
	return sources
}

func currentGeneratedArchives(p Project) []CanvasTeamAssetArchive {
	var out []CanvasTeamAssetArchive
	for _, source := range generatedArchiveSources(p) {
		for _, archive := range p.TeamAssetArchives {
			if archive.SourceAssetID == source {
				out = append(out, archive)
				break
			}
		}
	}
	return out
}

// canvasCurrentArchives 保留旧测试与兼容调用名；状态现在覆盖所有平台最终生成物。
func canvasCurrentArchives(p Project) []CanvasTeamAssetArchive { return currentGeneratedArchives(p) }

func (s *Service) saveCanvasArchive(ctx context.Context, p Project, archive CanvasTeamAssetArchive, expected *CanvasTeamAssetArchive) (Project, error) {
	for range 10 {
		current, err := s.Repo.Get(ctx, p.TeamID, p.ID)
		if err != nil {
			return p, err
		}
		if current == nil {
			return p, ErrConflict
		}
		if !slices.Contains(generatedArchiveSources(*current), archive.SourceAssetID) {
			return *current, ErrConflict
		}
		index := -1
		for i, old := range current.TeamAssetArchives {
			if old.SourceAssetID == archive.SourceAssetID {
				// 并发worker的旧IO结果不能覆盖后来确认的归档事实；deleted不可逆。
				if old == archive || old.Status == "deleted" || expected == nil || old != *expected {
					return *current, nil
				}
				index = i
				break
			}
		}
		if index < 0 && expected != nil {
			return *current, ErrConflict
		}
		next, err := s.Mutate(ctx, current.TeamID, current.ID, current.Version, func(v *Project) error {
			if index < 0 {
				v.TeamAssetArchives = append(v.TeamAssetArchives, archive)
			} else {
				v.TeamAssetArchives[index] = archive
			}
			return nil
		})
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return *current, err
		}
		return *next, nil
	}
	return p, ErrConflict
}

func (s *Service) reconcileCanvasArchives(ctx context.Context, p Project) Project {
	if s.ArchiveCanvasOutput == nil || len(generatedArchiveSources(p)) == 0 {
		return p
	}
	// 回执仅对应当前成功输出；旧/损坏快照不能突破公共数组上限或误认失败输出。
	if current := currentGeneratedArchives(p); !slices.Equal(current, p.TeamAssetArchives) {
		next, err := s.Mutate(ctx, p.TeamID, p.ID, p.Version, func(v *Project) error {
			v.TeamAssetArchives = current
			return nil
		})
		if err != nil {
			return p
		}
		p = *next
	}
	assets, err := s.Repo.Assets(ctx, p.TeamID)
	if err != nil {
		return p
	}
	sources := map[string]Asset{}
	for _, asset := range assets {
		sources[asset.ID] = asset
	}
	for _, sourceID := range generatedArchiveSources(p) {
		if ctx.Err() != nil {
			break
		}
		archive := CanvasTeamAssetArchive{SourceAssetID: sourceID, Status: "pending"}
		found := false
		for _, previous := range p.TeamAssetArchives {
			if previous.SourceAssetID == sourceID {
				archive, found = previous, true
				break
			}
		}
		if archive.Status == "deleted" {
			continue
		}
		if !found {
			p, err = s.saveCanvasArchive(ctx, p, archive, nil)
			if err != nil {
				continue
			}
			for _, stored := range p.TeamAssetArchives {
				if stored.SourceAssetID == archive.SourceAssetID {
					archive = stored
					break
				}
			}
			if archive.Status == "deleted" {
				continue
			}
		}
		result := CanvasTeamAssetArchive{SourceAssetID: sourceID, Status: "failed", Error: "canvas_archive_retrying"}
		if source, ok := sources[sourceID]; ok {
			result = s.ArchiveCanvasOutput(ctx, CanvasArchiveInput{TeamID: p.TeamID, UserID: p.UserID, ProjectID: p.ID, Source: source, Previous: archive})
		}
		if result != archive {
			if next, err := s.saveCanvasArchive(ctx, p, result, &archive); err == nil {
				p = next
			}
		}
	}
	return p
}
