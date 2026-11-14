package store

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func (s *memoryStore) QueryTeamAssets(ctx context.Context, teamID, actorID string, q TeamAssetQuery) (TeamAssetQueryPage, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssetQueryPage{}, err
	}
	if !validTeamAssetQuery(teamID, actorID, q) {
		return TeamAssetQueryPage{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.teamAssetActorLocked(teamID, actorID); err != nil {
		return TeamAssetQueryPage{}, err
	}
	page := TeamAssetQueryPage{Revision: strconv.FormatInt(s.teamAssetRevisions[teamID], 10), UsedBytes: s.teamAssetUsedBytes[teamID], Records: []TeamAssetManagedRecord{}, Facets: TeamAssetFacets{Kinds: []TeamAssetKindFacet{}, Uploaders: []TeamAssetUploaderFacet{}, Tags: []string{}}}
	if q.ExpectedRevision != "" && q.ExpectedRevision != page.Revision {
		return TeamAssetQueryPage{}, ErrTeamAssetSnapshotChanged
	}
	if q.KnownRevision == page.Revision {
		page.NotModified = true
		return page, nil
	}
	kinds := map[string]TeamAssetKindFacet{}
	uploaders := map[string]TeamAssetUploaderFacet{}
	allTags := map[string]bool{}
	for _, r := range s.teamAssets[teamID] {
		tags := s.teamAssetTags[teamID+":"+r.ID]
		favorite := s.teamAssetFavorites[teamID+":"+actorID+":"+r.ID]
		page.Facets.Total++
		kind := kinds[r.Kind]
		kind.Kind = r.Kind
		kind.Count++
		kind.Bytes += r.Size
		kinds[r.Kind] = kind
		uploader := uploaders[r.UploadedByID]
		uploader.ID = r.UploadedByID
		if uploader.Count == 0 || r.UploadedByName < uploader.Name {
			uploader.Name = r.UploadedByName
		}
		uploader.Count++
		uploaders[r.UploadedByID] = uploader
		for _, tag := range tags {
			allTags[tag] = true
		}
		if q.Kind != "" && q.Kind != r.Kind || q.UploadedBy != "" && q.UploadedBy != r.UploadedByID || q.FavoritesOnly && !favorite || q.Tag != "" && !slices.Contains(tags, q.Tag) || !strings.Contains(strings.ToLower(r.Name+" "+r.UploadedByName), strings.ToLower(q.Q)) {
			continue
		}
		page.Records = append(page.Records, TeamAssetManagedRecord{TeamAssetRecord: r, Tags: append([]string{}, tags...), Favorite: favorite})
	}
	for _, kind := range kinds {
		page.Facets.Kinds = append(page.Facets.Kinds, kind)
	}
	for _, uploader := range uploaders {
		page.Facets.Uploaders = append(page.Facets.Uploaders, uploader)
	}
	for tag := range allTags {
		page.Facets.Tags = append(page.Facets.Tags, tag)
	}
	slices.SortFunc(page.Facets.Kinds, func(a, b TeamAssetKindFacet) int { return strings.Compare(a.Kind, b.Kind) })
	slices.SortFunc(page.Facets.Uploaders, func(a, b TeamAssetUploaderFacet) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(page.Facets.Tags)
	boundTeamAssetFacets(&page.Facets)
	sort.Slice(page.Records, func(i, j int) bool {
		a, b := page.Records[i], page.Records[j]
		switch q.Sort {
		case "name":
			if a.Name != b.Name {
				return a.Name < b.Name
			}
		case "size":
			if a.Size != b.Size {
				return a.Size > b.Size
			}
		case "oldest":
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
		default:
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
		}
		return a.ID > b.ID
	})
	start := min(q.Offset, len(page.Records))
	page.Records = page.Records[start:min(start+q.Limit, len(page.Records))]
	return page, nil
}

func (s *memoryStore) SetTeamAssetFavorite(ctx context.Context, teamID, actorID, id string, favorite bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(id) {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.teamAssetActorLocked(teamID, actorID); err != nil {
		return err
	}
	if _, ok := s.teamAssets[teamID][id]; !ok {
		return ErrTeamAssetNotFound
	}
	key := teamID + ":" + actorID + ":" + id
	if s.teamAssetFavorites[key] != favorite {
		if favorite {
			s.teamAssetFavorites[key] = true
		} else {
			delete(s.teamAssetFavorites, key)
		}
		s.teamAssetRevisions[teamID]++
	}
	return nil
}

func (s *memoryStore) SetTeamAssetTags(ctx context.Context, teamID, actorID, id string, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !canonicalUUID(teamID) || !canonicalUUID(actorID) || !canonicalUUID(id) {
		return ErrInvalidInput
	}
	normalized, err := normalizeTeamAssetTags(tags)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	actor, err := s.teamAssetActorLocked(teamID, actorID)
	if err != nil {
		return err
	}
	asset, ok := s.teamAssets[teamID][id]
	if !ok {
		return ErrTeamAssetNotFound
	}
	if asset.UploadedByID != actorID && !IsTeamAdmin(actor.Role) {
		return ErrTeamAssetForbidden
	}
	key := teamID + ":" + id
	if !slices.Equal(s.teamAssetTags[key], normalized) {
		s.teamAssetTags[key] = normalized
		s.teamAssetRevisions[teamID]++
	}
	return nil
}
