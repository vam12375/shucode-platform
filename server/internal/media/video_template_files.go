package media

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"
)

//go:embed video_templates/*.json
var videoTemplateFiles embed.FS

//go:embed video_formula_tests/*.json
var videoFormulaTestFiles embed.FS

const (
	productAdDirectorSuite   = "product-ad-director-v2"
	productAdDirectorVersion = "4.0.0-test.1"
	productAdDirectorCount   = 31 // auto + F01–F30；F31 是公共执行保底，不是独立创意。
	legacyFormulaSuite       = "single-image-dual-anchor-v3"
	legacyFormulaVersion     = "3.0.0-test.2"
)

func VideoFormulaTestDefinitions() ([]VideoTemplate, error) {
	out, err := loadVideoTemplateCatalogAt(videoFormulaTestFiles, "video_formula_tests")
	if err != nil || len(out) != productAdDirectorCount {
		return nil, fmt.Errorf("video_templates_unavailable")
	}
	for i, t := range out {
		formula, id := "auto", "formula-test-auto"
		if i > 0 {
			formula, id = fmt.Sprintf("F%02d", i), fmt.Sprintf("formula-test-f%02d", i)
		}
		e := t.Experiment
		if t.ID != id || t.Version != productAdDirectorVersion || e == nil || e.SuiteID != productAdDirectorSuite || e.FormulaID != formula || utf8.RuneCountInString(e.Instructions) > 40000 || !strings.Contains(e.Instructions, "## 每镜落实记录与验收") || !strings.Contains(e.Instructions, "## 公共执行保底 F31") || t.DurationRange.Min != 30 || t.DurationRange.Max != 30 || t.MinAssets != 1 || len(t.Beats) == 0 {
			return nil, fmt.Errorf("video_templates_unavailable")
		}
		out[i].VerifiedModelIDs = []string{}
	}
	return out, nil
}

func allVideoTemplateDefinitions() ([]VideoTemplate, error) {
	regular, err := VideoTemplateDefinitions()
	if err != nil {
		return nil, err
	}
	experiments, err := VideoFormulaTestDefinitions()
	if err != nil {
		return nil, err
	}
	return append(regular, experiments...), nil
}

func AllVideoTemplateDefinitions() ([]VideoTemplate, error) {
	return allVideoTemplateDefinitions()
}

// 单文件目录容易混入重复版本；manifest 唯一拥有顺序，严格解码避免新字段被悄悄丢弃。
func loadVideoTemplateCatalog(files fs.FS) ([]VideoTemplate, error) {
	return loadVideoTemplateCatalogAt(files, "video_templates")
}

func loadVideoTemplateCatalogAt(files fs.FS, root string) ([]VideoTemplate, error) {
	fail := func() ([]VideoTemplate, error) { return nil, fmt.Errorf("video_templates_unavailable") }
	decode := func(name string, value any) error {
		raw, err := fs.ReadFile(files, root+"/"+name)
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
	entries, err := fs.ReadDir(files, root)
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
