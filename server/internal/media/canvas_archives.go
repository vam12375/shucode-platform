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

func canvasArchiveSources(p Project) []string {
	var sources []string
	for _, shot := range p.Shots {
		if shot.Status == "succeeded" && shot.OutputAssetID != "" && !slices.Contains(sources, shot.OutputAssetID) {
			sources = append(sources, shot.OutputAssetID)
			if len(sources) == 4 {
				break
			}
		}
	}
	return sources
}

func canvasCurrentArchives(p Project) []CanvasTeamAssetArchive {
	var out []CanvasTeamAssetArchive
	for _, source := range canvasArchiveSources(p) {
		for _, archive := range p.TeamAssetArchives {
			if archive.SourceAssetID == source {
				out = append(out, archive)
				break
			}
		}
	}
	return out
}

func (s *Service) saveCanvasArchive(ctx context.Context, p Project, archive CanvasTeamAssetArchive, expected *CanvasTeamAssetArchive) (Project, error) {
	for range 10 {
		current, err := s.Repo.Get(ctx, p.TeamID, p.ID)
		if err != nil {
			return p, err
		}
		if current == nil {
			return p, ErrConflict
		}
		if !slices.Contains(canvasArchiveSources(*current), archive.SourceAssetID) {
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
	if p.Canvas == nil || s.ArchiveCanvasOutput == nil {
		return p
	}
	// 回执仅对应当前成功输出；旧/损坏快照不能突破公共数组上限或误认失败输出。
	if current := canvasCurrentArchives(p); !slices.Equal(current, p.TeamAssetArchives) {
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
	for _, sourceID := range canvasArchiveSources(p) {
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
