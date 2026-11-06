package templatelibrary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const ConfigKey = "template-library.entries"

var (
	ErrInvalid  = errors.New("模板字段不合法")
	ErrConflict = errors.New("模板已被其他管理员修改，请刷新后重试")
	ErrNotFound = errors.New("模板不存在")
)

type ConfigStore interface {
	GetConfigValue(context.Context, string) ([]byte, error)
	CompareAndSwapConfigValue(context.Context, string, []byte, []byte) (bool, error)
}
type Fields struct {
	PackageDigest string   `json:"packageDigest,omitempty"`
	Name          string   `json:"name"`
	NameEN        string   `json:"nameEn"`
	Description   string   `json:"description"`
	DescriptionEN string   `json:"descriptionEn"`
	Content       string   `json:"content"`
	ContentEN     string   `json:"contentEn"`
	Tags          []string `json:"tags"`
	Scene         string   `json:"scene"`
	CronExpr      string   `json:"cronExpr"`
}
type Entry struct {
	Package *PackageInfo `json:"package,omitempty"`
	Fields
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Source          string `json:"source"`
	Enabled         bool   `json:"enabled"`
	Deleted         bool   `json:"deleted,omitempty"`
	ExpectedVersion string `json:"expectedVersion"`
}
type Mutation struct {
	ExpectedVersion string  `json:"expectedVersion"`
	Enabled         *bool   `json:"enabled,omitempty"`
	Fields          *Fields `json:"fields,omitempty"`
	Kind            string  `json:"kind,omitempty"`
}
type Repository struct{ Store ConfigStore }

func VideoID(id string) string { return "video:" + id }
func version(e Entry) string {
	e.ExpectedVersion = ""
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (r Repository) read(ctx context.Context) (map[string]Entry, []byte, error) {
	raw, err := r.Store.GetConfigValue(ctx, ConfigKey)
	if err != nil {
		return nil, nil, err
	}
	entries := map[string]Entry{}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &entries); err != nil {
			return nil, nil, err
		}
	}
	if entries == nil {
		return nil, nil, errors.New("模板库配置不可用")
	}
	return entries, raw, nil
}
func (r Repository) DisabledVideoIDs(ctx context.Context) (map[string]bool, error) {
	overrides, _, err := r.read(ctx)
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for id, e := range overrides {
		if strings.HasPrefix(id, "video:") && (!e.Enabled || e.Deleted) {
			result[strings.TrimPrefix(id, "video:")] = true
		}
	}
	return result, nil
}
func catalog(seeds []Entry, overrides map[string]Entry) []Entry {
	entries := make([]Entry, 0, len(seeds)+len(overrides))
	seen := map[string]bool{}
	for _, seed := range seeds {
		seen[seed.ID] = true
		entry := seed
		if override, ok := overrides[seed.ID]; ok {
			if override.Deleted {
				continue
			}
			if seed.Kind == "video" {
				entry.Enabled = override.Enabled
				entry.ExpectedVersion = override.ExpectedVersion
			} else {
				entry = override
			}
		}
		if entry.ExpectedVersion == "" {
			entry.ExpectedVersion = version(entry)
		}
		entries = append(entries, entry)
	}
	ids := []string{}
	for id, e := range overrides {
		if !seen[id] && e.Source == "custom" && !e.Deleted {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		entries = append(entries, overrides[id])
	}
	return entries
}
func (r Repository) List(ctx context.Context, videos []Entry) ([]Entry, error) {
	seeds, err := builtinEntries()
	if err != nil {
		return nil, err
	}
	overrides, _, err := r.read(ctx)
	if err != nil {
		return nil, err
	}
	return catalog(append(videos, seeds...), overrides), nil
}
func validText(s string, min, max int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= min && n <= max && !strings.ContainsRune(s, 0)
}
func validateFields(kind string, f *Fields) error {
	f.Name = strings.TrimSpace(f.Name)
	if !validText(f.Name, 1, 300) || !validText(f.NameEN, 0, 500) || !validText(f.Description, 0, 2000) || !validText(f.DescriptionEN, 0, 4000) || !validText(f.Content, 1, 60000) || !validText(f.ContentEN, 0, 60000) || len(f.Tags) > 20 {
		return ErrInvalid
	}
	for _, tag := range f.Tags {
		if !validText(tag, 1, 80) {
			return ErrInvalid
		}
	}
	if f.Tags == nil {
		f.Tags = []string{}
	}
	if kind == "automation" {
		if f.Scene != "scheduled-task" && f.Scene != "off-peak-task" {
			return ErrInvalid
		}
		if f.Scene == "scheduled-task" {
			if !validCron(f.CronExpr) {
				return ErrInvalid
			}
		} else if f.CronExpr != "" {
			return ErrInvalid
		}
	} else if f.CronExpr != "" {
		return ErrInvalid
	}
	if kind == "skill" && f.Scene != "" {
		return ErrInvalid
	}
	return nil
}

// 定时模板只接收客户端编辑器可无损呈现的每日/每周五段表达式。
func validCron(value string) bool {
	fields := strings.Fields(value)
	if len(fields) != 5 || fields[2] != "*" || fields[3] != "*" {
		return false
	}
	validNumber := func(s string, max int) bool {
		if s == "" {
			return false
		}
		n := 0
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
			n = n*10 + int(c-'0')
			if n > max {
				return false
			}
		}
		return true
	}
	if !validNumber(fields[0], 59) || !validNumber(fields[1], 23) {
		return false
	}
	if fields[4] == "*" {
		return true
	}
	for _, day := range strings.Split(fields[4], ",") {
		bounds := strings.Split(day, "-")
		if len(bounds) > 2 {
			return false
		}
		for _, b := range bounds {
			if !validNumber(b, 6) {
				return false
			}
		}
		if len(bounds) == 2 && bounds[0] > bounds[1] {
			return false
		}
	}
	return true
}
func (r Repository) Save(ctx context.Context, videos []Entry, id string, input Mutation, remove bool) (Entry, error) {
	seeds, err := builtinEntries()
	if err != nil {
		return Entry{}, err
	}
	seeds = append(videos, seeds...)
	creating := id == ""
	if creating {
		id = "custom:" + uuid.NewString()
	}
	for range 20 {
		overrides, raw, err := r.read(ctx)
		if err != nil {
			return Entry{}, err
		}
		var current Entry
		for _, entry := range catalog(seeds, overrides) {
			if entry.ID == id {
				current = entry
				break
			}
		}
		if creating {
			if input.ExpectedVersion != "" || input.Fields == nil || input.Enabled == nil || !slices.Contains([]string{"prompt", "automation", "skill", "template"}, input.Kind) || len(overrides) >= 1000 {
				return Entry{}, ErrInvalid
			}
			current = Entry{ID: id, Kind: input.Kind, Source: "custom"}
		} else {
			if current.ID == "" {
				return Entry{}, ErrNotFound
			}
			if input.ExpectedVersion == "" || current.ExpectedVersion != input.ExpectedVersion {
				return Entry{}, ErrConflict
			}
			if input.Kind != "" {
				return Entry{}, ErrInvalid
			}
		}
		if remove {
			if creating || input.Fields != nil || input.Enabled != nil {
				return Entry{}, ErrInvalid
			}
			current.Enabled = false
			if current.Source == "custom" {
				delete(overrides, id)
			} else {
				// 内置目录每次从种子重建，删除须持久化标记，避免刷新或重启后重新出现。
				overrides[id] = Entry{ID: id, Kind: current.Kind, Source: current.Source, Deleted: true}
			}
		} else {
			if input.Fields == nil && input.Enabled == nil {
				return Entry{}, ErrInvalid
			}
			if input.Fields != nil {
				if current.Kind == "video" {
					return Entry{}, ErrInvalid
				}
				fields := *input.Fields
				if current.Kind == "skill" && fields.PackageDigest == "" {
					fields.PackageDigest = current.PackageDigest
				}
				if !creating && fields.Scene != current.Scene {
					return Entry{}, ErrInvalid
				}
				if current.Source == "custom" && current.Kind == "prompt" && fields.Scene != "draft-suggestion" {
					return Entry{}, ErrInvalid
				}
				if err = validateFields(current.Kind, &fields); err != nil {
					return Entry{}, err
				}
				if current.Kind == "skill" {
					info, packageErr := r.saveSkillPackage(ctx, &fields)
					if packageErr != nil {
						return Entry{}, packageErr
					}
					current.Package = info
				} else if fields.PackageDigest != "" {
					return Entry{}, ErrInvalid
				}
				current.Fields = fields
			}
			if input.Enabled != nil {
				current.Enabled = *input.Enabled
			}
			current.ExpectedVersion = uuid.NewString()
			stored := current
			if current.Kind == "video" {
				stored.Fields = Fields{}
			}
			overrides[id] = stored
		}
		next, err := json.Marshal(overrides)
		if err != nil {
			return Entry{}, err
		}
		swapped, err := r.Store.CompareAndSwapConfigValue(ctx, ConfigKey, raw, next)
		if err != nil {
			return Entry{}, err
		}
		if swapped {
			return current, nil
		}
	}
	return Entry{}, ErrConflict
}
