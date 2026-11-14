package store

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrTeamAssetSnapshotChanged = errors.New("team asset snapshot changed")

type TeamAssetQuery struct {
	Q, Kind, UploadedBy, Tag, Sort string
	FavoritesOnly                  bool
	Offset, Limit                  int
	ExpectedRevision               string
	KnownRevision                  string
}
type TeamAssetManagedRecord struct {
	TeamAssetRecord
	Tags     []string
	Favorite bool
}
type TeamAssetKindFacet struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
	Bytes int64  `json:"bytes"`
}
type TeamAssetUploaderFacet struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}
type TeamAssetFacets struct {
	TagsTruncated      bool                     `json:"tagsTruncated"`
	UploadersTruncated bool                     `json:"uploadersTruncated"`
	Total              int64                    `json:"total"`
	Kinds              []TeamAssetKindFacet     `json:"kinds"`
	Uploaders          []TeamAssetUploaderFacet `json:"uploaders"`
	Tags               []string                 `json:"tags"`
}
type TeamAssetQueryPage struct {
	Records     []TeamAssetManagedRecord
	UsedBytes   int64
	Revision    string
	NotModified bool
	Facets      TeamAssetFacets
}

func normalizeTeamAssetTags(tags []string) ([]string, error) {
	if len(tags) > 20 {
		return nil, ErrInvalidInput
	}
	normalized := []string{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if !utf8.ValidString(tag) || tag == "" || utf8.RuneCountInString(tag) > 32 || strings.ContainsFunc(tag, unicode.IsControl) {
			return nil, ErrInvalidInput
		}
		if !slices.Contains(normalized, tag) {
			normalized = append(normalized, tag)
		}
	}
	slices.Sort(normalized)
	return normalized, nil
}

func validTeamAssetQuery(teamID, actorID string, q TeamAssetQuery) bool {
	return canonicalUUID(teamID) && canonicalUUID(actorID) && q.Limit > 0 && q.Limit <= 101 && q.Offset >= 0 &&
		q.Offset <= 1000000 && utf8.ValidString(q.Q) && utf8.RuneCountInString(q.Q) <= 200 && !strings.ContainsFunc(q.Q, unicode.IsControl) &&
		(q.Kind == "" || validTeamAssetKind(q.Kind)) && (q.UploadedBy == "" || canonicalUUID(q.UploadedBy)) &&
		utf8.ValidString(q.Tag) && !strings.ContainsFunc(q.Tag, unicode.IsControl) && utf8.RuneCountInString(q.Tag) <= 32 && slices.Contains([]string{"", "newest", "oldest", "name", "size"}, q.Sort)
}

// 建议集合有界，计数与容量仍来自整个团队，不把截断建议伪装成完整目录。
func boundTeamAssetFacets(f *TeamAssetFacets) {
	f.TagsTruncated = len(f.Tags) > 1000
	f.UploadersTruncated = len(f.Uploaders) > 1000
	f.Tags = f.Tags[:min(len(f.Tags), 1000)]
	f.Uploaders = f.Uploaders[:min(len(f.Uploaders), 1000)]
}
