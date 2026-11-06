package templatelibrary

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"slices"
)

//go:embed client_scenes_catalog.json
var sceneCatalogJSON []byte

// 与客户端 services/client-scenes 的传输字段保持一致。
type Scene struct {
	Namespace string                  `json:"namespace"`
	Scene     string                  `json:"scene"`
	Options   map[string]*SceneOption `json:"options"`
	CreatedAt int64                   `json:"created_at,omitempty"`
	UpdatedAt int64                   `json:"updated_at,omitempty"`
}
type SceneOption struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Contents  map[string]string      `json:"contents"`
	Prompts   map[string]string      `json:"prompts,omitempty"`
	Items     []SceneItem            `json:"items,omitempty"`
	Refer     string                 `json:"refer,omitempty"`
	Cascades  map[string][]SceneItem `json:"cascades,omitempty"`
	Templates map[string]string      `json:"templates,omitempty"`
}
type SceneItem struct {
	ID        string              `json:"id"`
	Type      string              `json:"type"`
	Contents  map[string]string   `json:"contents"`
	Labels    map[string]string   `json:"labels"`
	Descs     map[string]string   `json:"descs,omitempty"`
	OnFinish  *string             `json:"on_finish,omitempty"`
	Img       *string             `json:"img,omitempty"`
	Imgs      map[string]string   `json:"imgs,omitempty"`
	ShareURLs map[string]string   `json:"share_urls,omitempty"`
	Defaults  map[string][]string `json:"defaults,omitempty"`
}

func seedScenes() ([]Scene, error) {
	out := []Scene{}
	err := json.Unmarshal(sceneCatalogJSON, &out)
	return out, err
}
func sceneID(scene Scene, cascade, id string) string {
	raw, _ := json.Marshal([]string{scene.Namespace, scene.Scene, cascade, id})
	sum := sha256.Sum256(raw)
	return "scene:" + hex.EncodeToString(sum[:16])
}
func walkPrompts(scene *Scene, visit func(string, []SceneItem) []SceneItem) {
	option := scene.Options["prompts"]
	if option == nil {
		return
	}
	option.Items = visit("", option.Items)
	keys := []string{}
	for key := range option.Cascades {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		option.Cascades[key] = visit(key, option.Cascades[key])
	}
}
func itemEntry(scene Scene, cascade string, item SceneItem) Entry {
	kind := "prompt"
	cron := ""
	if scene.Scene == "scheduled-task" || scene.Scene == "off-peak-task" {
		kind = "automation"
	}
	if option := scene.Options["cronExpr"]; option != nil {
		for _, id := range item.Defaults["cronExpr"] {
			for _, candidate := range option.Items {
				if candidate.ID == id {
					cron = candidate.Contents["en"]
					if cron == "" {
						cron = candidate.Contents["cn"]
					}
					break
				}
			}
			if cron != "" {
				break
			}
		}
	}
	name := item.Labels["cn"]
	if name == "" {
		name = item.Labels["en"]
	}
	content := item.Contents["cn"]
	if content == "" {
		content = item.Contents["en"]
	}
	return Entry{ID: sceneID(scene, cascade, item.ID), Kind: kind, Source: "client-scenes", Enabled: true, Fields: Fields{Name: name, NameEN: item.Labels["en"], Description: item.Descs["cn"], DescriptionEN: item.Descs["en"], Content: content, ContentEN: item.Contents["en"], Tags: []string{scene.Scene}, Scene: scene.Scene, CronExpr: cron}}
}
func sceneEntries() ([]Entry, error) {
	scenes, err := seedScenes()
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	for i := range scenes {
		scene := &scenes[i]
		walkPrompts(scene, func(cascade string, items []SceneItem) []SceneItem {
			for _, item := range items {
				entries = append(entries, itemEntry(*scene, cascade, item))
			}
			return items
		})
	}
	return entries, nil
}
func applyItem(scene *Scene, item SceneItem, e Entry) SceneItem {
	if item.Labels == nil {
		item.Labels = map[string]string{}
	}
	if item.Contents == nil {
		item.Contents = map[string]string{}
	}
	if item.Descs == nil {
		item.Descs = map[string]string{}
	}
	item.Labels["cn"] = e.Name
	item.Labels["en"] = e.NameEN
	item.Contents["cn"] = e.Content
	item.Contents["en"] = e.ContentEN
	item.Descs["cn"] = e.Description
	item.Descs["en"] = e.DescriptionEN
	if e.Kind == "automation" && e.Scene == "scheduled-task" {
		if item.Defaults == nil {
			item.Defaults = map[string][]string{}
		}
		id := "library-cron-" + e.ID
		item.Defaults["cronExpr"] = []string{id}
		option := scene.Options["cronExpr"]
		if option == nil {
			option = &SceneOption{ID: "library-cron", Type: "cronExpr", Contents: map[string]string{}}
			scene.Options["cronExpr"] = option
		}
		option.Items = append(option.Items, SceneItem{ID: id, Type: "cronExpr", Labels: map[string]string{}, Contents: map[string]string{"cn": e.CronExpr, "en": e.CronExpr}})
	}
	return item
}
func (r Repository) Scenes(ctx context.Context) ([]Scene, error) {
	scenes, err := seedScenes()
	if err != nil {
		return nil, err
	}
	overrides, _, err := r.read(ctx)
	if err != nil {
		return nil, err
	}
	for i := range scenes {
		scene := &scenes[i]
		walkPrompts(scene, func(cascade string, items []SceneItem) []SceneItem {
			out := []SceneItem{}
			for _, item := range items {
				entry, exists := overrides[sceneID(*scene, cascade, item.ID)]
				if exists {
					if !entry.Enabled || entry.Deleted {
						continue
					}
					item = applyItem(scene, item, entry)
				}
				out = append(out, item)
			}
			return out
		})
	}
	for _, entry := range catalog(nil, overrides) {
		if !entry.Enabled || entry.Kind == "skill" {
			continue
		}
		for i := range scenes {
			scene := &scenes[i]
			if scene.Scene != entry.Scene {
				continue
			}
			option := scene.Options["prompts"]
			if option == nil {
				continue
			}
			item := applyItem(scene, SceneItem{ID: entry.ID, Type: "prompt"}, entry)
			option.Items = append(option.Items, item)
			break
		}
	}
	return scenes, nil
}

type Skill struct {
	Package     *PackageInfo `json:"package,omitempty"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Content     string       `json:"content"`
}

func (r Repository) Skills(ctx context.Context) ([]Skill, error) {
	entries, err := r.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, entry := range entries {
		if entry.Kind == "skill" && entry.Enabled {
			out = append(out, Skill{ID: entry.ID, Name: entry.Name, Description: entry.Description, Content: entry.Content, Package: entry.Package})
		}
	}
	return out, nil
}
