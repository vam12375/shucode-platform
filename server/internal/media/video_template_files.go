package media

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

//go:embed video_templates/*.json
var videoTemplateFiles embed.FS

// 单文件目录容易混入重复版本；manifest 唯一拥有顺序，严格解码避免新字段被悄悄丢弃。
func loadVideoTemplateCatalog(files fs.FS) ([]VideoTemplate, error) {
	fail := func() ([]VideoTemplate, error) { return nil, fmt.Errorf("video_templates_unavailable") }
	decode := func(name string, value any) error {
		raw, err := fs.ReadFile(files, "video_templates/"+name)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(value); err != nil {
			return err
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("trailing_json")
		}
		return nil
	}
	var names []string
	if err := decode("manifest.json", &names); err != nil || len(names) == 0 {
		return fail()
	}
	seen := map[string]bool{"manifest.json": true}
	out := make([]VideoTemplate, 0, len(names))
	for _, name := range names {
		if !fs.ValidPath(name) || strings.Contains(name, "/") || !strings.HasSuffix(name, ".json") || seen[name] {
			return fail()
		}
		var recipe VideoTemplate
		if err := decode(name, &recipe); err != nil || name != recipe.ID+".json" {
			return fail()
		}
		seen[name] = true
		out = append(out, recipe)
	}
	entries, err := fs.ReadDir(files, "video_templates")
	if err != nil || len(entries) != len(seen) {
		return fail()
	}
	for _, entry := range entries {
		if entry.IsDir() || !seen[entry.Name()] {
			return fail()
		}
	}
	return out, nil
}
