package templatelibrary

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

const MaxPackageSize = 4 << 20
const maxPackageExpandedSize = 16 << 20

//go:embed packages/*.zip legacy_catalog.json
var legacyFiles embed.FS

type PackageFile struct {
	Path       string `json:"path"`
	Size       int    `json:"size"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable"`
}
type PackageInfo struct {
	SHA256 string        `json:"sha256"`
	Size   int           `json:"size"`
	Files  []PackageFile `json:"files"`
}
type packageContent struct {
	Path       string
	Content    []byte
	Executable bool
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func legacyEntries() ([]Entry, error) {
	raw, err := legacyFiles.ReadFile("legacy_catalog.json")
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	err = json.Unmarshal(raw, &out)
	return out, err
}
func builtinEntries() ([]Entry, error) {
	entries, err := sceneEntries()
	if err != nil {
		return nil, err
	}
	legacy, err := legacyEntries()
	return append(entries, legacy...), err
}
func safePackagePath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	return true
}

var markdownLink = regexp.MustCompile(`\]\(([^)]+)\)`)

func inspectPackage(raw []byte) (PackageInfo, []packageContent, string, error) {
	invalid := func(message string) (PackageInfo, []packageContent, string, error) {
		return PackageInfo{}, nil, "", fmt.Errorf("%w：%s", ErrInvalid, message)
	}
	if len(raw) == 0 || len(raw) > MaxPackageSize {
		return invalid("ZIP 文件须不超过 4 MiB")
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return invalid("不是有效 ZIP 文件")
	}
	if len(reader.File) > 512 {
		return invalid("文件数不得超过 512")
	}
	files := []packageContent{}
	seen := map[string]bool{}
	total := 0
	skillPath := ""
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() || !safePackagePath(file.Name) {
			return invalid("文件路径或类型不安全")
		}
		lower := strings.ToLower(file.Name)
		if seen[lower] {
			return invalid("包中存在重复路径")
		}
		seen[lower] = true
		if file.UncompressedSize64 > maxPackageExpandedSize {
			return invalid("解压后内容过大")
		}
		total += int(file.UncompressedSize64)
		if total > maxPackageExpandedSize {
			return invalid("解压后内容不得超过 16 MiB")
		}
		stream, err := file.Open()
		if err != nil {
			return invalid("文件无法读取")
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, int64(maxPackageExpandedSize+1)))
		stream.Close()
		if readErr != nil || len(data) != int(file.UncompressedSize64) {
			return invalid("文件完整性校验失败")
		}
		files = append(files, packageContent{file.Name, data, file.Mode()&0111 != 0})
		if path.Base(file.Name) == "SKILL.md" {
			if skillPath != "" {
				return invalid("每个包须只有一个 SKILL.md")
			}
			skillPath = file.Name
		}
	}
	if skillPath == "" {
		return invalid("缺少 SKILL.md")
	}
	prefix := strings.TrimSuffix(skillPath, "SKILL.md")
	normalized := map[string][]byte{}
	content := ""
	for i := range files {
		if !strings.HasPrefix(files[i].Path, prefix) {
			return invalid("所有文件须位于 Skill 根目录内")
		}
		name := strings.TrimPrefix(files[i].Path, prefix)
		files[i].Path = name
		normalized[name] = files[i].Content
		if name == "SKILL.md" {
			content = string(files[i].Content)
		}
	}
	if !utf8.ValidString(content) || !validText(content, 1, 60000) {
		return invalid("SKILL.md 须为有效文本")
	}
	frontmatter := strings.SplitN(strings.ReplaceAll(content, "\r\n", "\n"), "---\n", 3)
	if len(frontmatter) != 3 || frontmatter[0] != "" {
		return invalid("SKILL.md 缺少 YAML frontmatter")
	}
	var meta map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter[1]), &meta); err != nil {
		return invalid("Skill 元数据不合法")
	}
	name, nameOK := meta["name"].(string)
	description, descriptionOK := meta["description"].(string)
	if !nameOK || !descriptionOK || !validText(name, 1, 300) || !validText(description, 1, 2000) {
		return invalid("Skill 元数据须包含 name 和 description")
	}
	for _, file := range files {
		if !strings.HasSuffix(strings.ToLower(file.Path), ".md") {
			continue
		}
		for _, link := range markdownLink.FindAllSubmatch(file.Content, -1) {
			target := strings.Trim(string(link[1]), "<>")
			target = strings.SplitN(target, " \"", 2)[0]
			parsed, err := url.Parse(target)
			if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Path == "" {
				continue
			}
			local := parsed.Path
			resolved := path.Clean(path.Join(path.Dir(file.Path), local))
			if !safePackagePath(resolved) {
				return invalid("引用超出 Skill 包")
			}
			if _, ok := normalized[resolved]; !ok {
				directory := false
				for name := range normalized {
					if strings.HasPrefix(name, resolved+"/") {
						directory = true
						break
					}
				}
				if !directory {
					return invalid("缺少引用文件 " + resolved)
				}
			}
		}
	}
	info := PackageInfo{SHA256: digest(raw), Size: len(raw), Files: []PackageFile{}}
	slices.SortFunc(files, func(a, b packageContent) int { return strings.Compare(a.Path, b.Path) })
	for _, file := range files {
		info.Files = append(info.Files, PackageFile{file.Path, len(file.Content), digest(file.Content), file.Executable})
	}
	return info, files, content, nil
}
func (r Repository) Package(ctx context.Context, hash string) ([]byte, error) {
	if len(hash) != 64 {
		return nil, ErrInvalid
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return nil, ErrInvalid
	}
	raw, err := legacyFiles.ReadFile("packages/" + hash + ".zip")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		stored, err := r.Store.GetConfigValue(ctx, "template-library.package:"+hash)
		if err != nil {
			return nil, err
		}
		if len(stored) == 0 {
			return nil, ErrNotFound
		}
		if err = json.Unmarshal(stored, &raw); err != nil {
			return nil, err
		}
	}
	if digest(raw) != hash {
		return nil, errors.New("Skill 包完整性校验失败")
	}
	return raw, nil
}
func (r Repository) ImportPackage(ctx context.Context, raw []byte) (PackageInfo, string, error) {
	info, _, content, err := inspectPackage(raw)
	if err != nil {
		return PackageInfo{}, "", err
	}
	stored, err := json.Marshal(raw)
	if err != nil {
		return PackageInfo{}, "", err
	}
	_, err = r.Store.CompareAndSwapConfigValue(ctx, "template-library.package:"+info.SHA256, nil, stored)
	return info, content, err
}
func (r Repository) saveSkillPackage(ctx context.Context, fields *Fields) (*PackageInfo, error) {
	files := []packageContent{{Path: "SKILL.md", Content: []byte(fields.Content)}}
	if fields.PackageDigest != "" {
		raw, err := r.Package(ctx, fields.PackageDigest)
		if err != nil {
			return nil, err
		}
		info, previous, content, err := inspectPackage(raw)
		if err != nil {
			return nil, err
		}
		if content == fields.Content {
			return &info, nil
		}
		files = previous
		for i := range files {
			if files[i].Path == "SKILL.md" {
				files[i].Content = []byte(fields.Content)
			}
		}
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range files {
		header := zip.FileHeader{Name: file.Path, Method: zip.Deflate}
		mode := fs.FileMode(0644)
		if file.Executable {
			mode = 0755
		}
		header.SetMode(mode)
		out, err := writer.CreateHeader(&header)
		if err != nil {
			return nil, err
		}
		if _, err = out.Write(file.Content); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	info, _, err := r.ImportPackage(ctx, buffer.Bytes())
	if err != nil {
		return nil, err
	}
	fields.PackageDigest = info.SHA256
	return &info, nil
}
