package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/vam12375/shucode-platform/server/internal/templatelibrary"
)

type TemplateProvenance struct {
	Platform    string `json:"platform"`
	URL         string `json:"url"`
	Observation string `json:"observation"`
	Adaptation  string `json:"adaptation"`
}

type ProductionSkill struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	PackageDigest string `json:"packageDigest"`
	Content       string `json:"content"`
}

// 制作单保存正文，不向工作区或用户技能目录安装文件，重试不查询最新技能。
type TemplateSnapshot struct {
	Template VideoTemplate     `json:"template"`
	Skills   []ProductionSkill `json:"skills"`
	Digest   string            `json:"digest"`
}

func cloneTemplateSnapshot(value *TemplateSnapshot) *TemplateSnapshot {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	var out TemplateSnapshot
	_ = json.Unmarshal(raw, &out)
	return &out
}

type resolvedTemplate struct {
	snapshot TemplateSnapshot
	enabled  bool
}

// 模板开关和技能正文来自同一次目录读取，防止创建时混用两次读取的开关与内容。
func (s *Service) resolveTemplateCatalog(ctx context.Context, models []Model) ([]resolvedTemplate, error) {
	definitions, err := allVideoTemplateDefinitions()
	if err != nil {
		return nil, err
	}
	seeds := make([]templatelibrary.Entry, 0, len(definitions))
	for _, t := range definitions {
		seeds = append(seeds, templatelibrary.Entry{ID: templatelibrary.VideoID(t.ID), Kind: "video", Source: "builtin-video", Enabled: true})
	}
	entries, err := (templatelibrary.Repository{Store: s.Repo.Store}).List(ctx, seeds)
	if err != nil {
		return nil, err
	}
	byID := map[string]templatelibrary.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	regularModels, formulaModels := []string{}, []string{}
	for _, m := range models {
		if m.Kind != "video" || m.ID == "" {
			continue
		}
		regularModels = append(regularModels, m.ID)
		// 测试公式固定交付 30 秒并引用商品图，按与新建制作单相同的模型能力和分段规则展示。
		caps := CanvasCapabilitiesFor(m)
		if caps != nil && slices.Contains(caps.Modes, "multimodal") && caps.MaxImages > 0 && len(planGenerationDurations(30, m)) > 0 {
			formulaModels = append(formulaModels, m.ID)
		}
	}
	out := make([]resolvedTemplate, 0, len(definitions))
	for _, t := range definitions {
		snapshot := TemplateSnapshot{Template: t, Skills: []ProductionSkill{}}
		enabled := byID[templatelibrary.VideoID(t.ID)].Enabled
		for _, id := range t.SkillIDs {
			e, ok := byID[id]
			// 首期只执行自包含纯指令包；有脚本/引用的包不能只取入口造成残缺执行。
			if !ok || !e.Enabled || e.Kind != "skill" || e.Package == nil || len(e.Package.Files) != 1 || e.Package.Files[0].Path != "SKILL.md" || e.Package.Files[0].Executable || e.PackageDigest != e.Package.SHA256 {
				enabled = false
				continue
			}
			sum := sha256.Sum256([]byte(e.Content))
			contentDigest := hex.EncodeToString(sum[:])
			if contentDigest != e.Package.Files[0].SHA256 {
				enabled = false
				continue
			}
			snapshot.Skills = append(snapshot.Skills, ProductionSkill{ID: id, Name: e.Name, Version: contentDigest, PackageDigest: e.PackageDigest, Content: e.Content})
		}
		raw, _ := json.Marshal(snapshot)
		sum := sha256.Sum256(raw)
		snapshot.Digest = hex.EncodeToString(sum[:])
		// 旧客户端用此字段决定模板能否选用。目录随视频模型发布，正文摘要不随模型变化。
		if enabled {
			models := regularModels
			if t.Experiment != nil {
				models = formulaModels
			}
			snapshot.Template.VerifiedModelIDs = slices.Clone(models)
		}
		if preview := byID[templatelibrary.VideoID(t.ID)].Preview; preview != nil {
			snapshot.Template.Preview = &TemplatePreview{CoverURL: templatelibrary.PreviewURL(s.Blobs.Origin, preview.Cover), VideoURL: templatelibrary.PreviewURL(s.Blobs.Origin, preview.Video)}
		}
		out = append(out, resolvedTemplate{snapshot: snapshot, enabled: enabled})
	}
	return out, nil
}

func (s *Service) freezeTemplate(ctx context.Context, b Brief, models []Model) (*TemplateSnapshot, error) {
	if !adaptiveResearch(b) || b.TemplateID == "" {
		return nil, nil
	}
	catalog, err := s.resolveTemplateCatalog(ctx, models)
	if err != nil {
		return nil, err
	}
	for _, entry := range catalog {
		if !entry.enabled || entry.snapshot.Template.ID != b.TemplateID || entry.snapshot.Template.Version != b.TemplateVersion {
			continue
		}
		if _, err = validateTemplateSelection(b, []VideoTemplate{entry.snapshot.Template}); err != nil {
			return nil, err
		}
		if err = validateFormulaTestModel(b, entry.snapshot.Template, models); err != nil {
			return nil, err
		}
		snapshot := cloneTemplateSnapshot(&entry.snapshot)
		// 制作单只需声明本单模型，避免把整个模型目录冻结进可分享的项目。
		snapshot.Template.VerifiedModelIDs = []string{b.VideoModel}
		return snapshot, nil
	}
	return nil, adaptiveResearchError()
}

func frozenTemplateMatches(b Brief, snapshot *TemplateSnapshot) bool {
	return snapshot != nil && snapshot.Template.ID == b.TemplateID && snapshot.Template.Version == b.TemplateVersion && len(snapshot.Skills) == len(snapshot.Template.SkillIDs)
}
