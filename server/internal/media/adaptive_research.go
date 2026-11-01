package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

type AssetObservation struct {
	AssetID     string `json:"assetId"`
	Description string `json:"description"`
}
type ResearchDecision struct {
	Mode              string             `json:"mode"`
	Reason            string             `json:"reason"`
	IntentSummary     string             `json:"intentSummary"`
	AssetObservations []AssetObservation `json:"assetObservations"`
	CreativeGaps      []string           `json:"creativeGaps"`
	TemplateID        string             `json:"templateId,omitempty"`
	TemplateVersion   string             `json:"templateVersion,omitempty"`
}

// 源时间零秒是有效事实，不能用omitempty把它与原创镜头的缺省混为一谈。
func (p *ShotPlan) UnmarshalJSON(raw []byte) error {
	type plain ShotPlan
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*p = ShotPlan(value)
	for i, key := range []string{"sourceUrl", "sourceStart", "sourceEnd"} {
		if data, ok := fields[key]; ok {
			if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
				return ErrInvalid
			}
			p.sourceFields |= 1 << i
		}
	}
	// 完整合法来源与Go直接构造值规范化为同一结构，避免恢复重放因内部标记误判变化。
	if p.sourceFields == 7 && (p.SourceURL != "" || p.SourceStart != 0 || p.SourceEnd != 0) {
		p.sourceFields = 0
	}
	return nil
}
func (p ShotPlan) MarshalJSON() ([]byte, error) {
	type plain ShotPlan
	raw, err := json.Marshal(plain(p))
	if err != nil {
		return nil, err
	}
	if p.sourceFields != 0 || p.SourceURL != "" || p.SourceStart != 0 || p.SourceEnd != 0 {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "sourceUrl")
	delete(fields, "sourceStart")
	delete(fields, "sourceEnd")
	return json.Marshal(fields)
}
func (p ShotPlan) hasAnySource() bool {
	return p.sourceFields != 0 || p.SourceURL != "" || p.SourceStart != 0 || p.SourceEnd != 0
}
func (p ShotPlan) hasCompleteSource() bool { return p.sourceFields == 0 }
func adaptiveResearchError() error         { return fmt.Errorf("video_research_invalid") }
func adaptiveResearch(b Brief) bool        { return b.ResearchPolicy != "" }
func validateAdaptiveBrief(b Brief) error {
	if !adaptiveResearch(b) {
		if b.CreativeIntent != "" || b.DistributionPlatform != "" || b.TemplateID != "" || b.TemplateVersion != "" {
			return adaptiveResearchError()
		}
		return nil
	}
	if b.Mode != "auto" || b.ReferenceAssetID != "" || b.SourceOnly || b.Platforms == nil || b.ReferenceURLs == nil || !slices.Contains([]string{"auto", "required", "disabled"}, b.ResearchPolicy) || utf8.RuneCountInString(b.CreativeIntent) > 2000 {
		return adaptiveResearchError()
	}
	if b.DistributionPlatform != "" && !slices.Contains([]string{"douyin", "xiaohongshu", "bilibili"}, b.DistributionPlatform) {
		return adaptiveResearchError()
	}
	if (b.TemplateID == "") != (b.TemplateVersion == "") || utf8.RuneCountInString(b.TemplateID) > 100 || utf8.RuneCountInString(b.TemplateVersion) > 100 {
		return adaptiveResearchError()
	}
	if b.ResearchPolicy == "disabled" && len(b.ReferenceURLs) > 0 || b.ResearchPolicy == "required" && len(b.ReferenceURLs) == 0 && len(b.Platforms) == 0 {
		return adaptiveResearchError()
	}
	return nil
}
func boundedNonempty(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
func validateResearchPlan(b Brief, p Plan) error {
	if err := validateAdaptiveBrief(b); err != nil {
		return err
	}
	r := p.Research
	if !adaptiveResearch(b) {
		if r != nil {
			return adaptiveResearchError()
		}
		return nil
	}
	if r == nil || !slices.Contains([]string{"reference", "template", "original"}, r.Mode) || !boundedNonempty(r.Reason, 1000) || !boundedNonempty(r.IntentSummary, 1000) || len(r.AssetObservations) < 1 || len(r.AssetObservations) > 20 || r.CreativeGaps == nil || len(r.CreativeGaps) > 5 || p.References == nil {
		return adaptiveResearchError()
	}
	seen := map[string]bool{}
	for _, a := range r.AssetObservations {
		if !slices.Contains(videoProductAssetIDs(b), a.AssetID) || seen[a.AssetID] || !boundedNonempty(a.Description, 1000) {
			return adaptiveResearchError()
		}
		seen[a.AssetID] = true
	}
	for _, gap := range r.CreativeGaps {
		if !boundedNonempty(gap, 300) {
			return adaptiveResearchError()
		}
	}
	if (r.TemplateID == "") != (r.TemplateVersion == "") || utf8.RuneCountInString(r.TemplateID) > 100 || utf8.RuneCountInString(r.TemplateVersion) > 100 {
		return adaptiveResearchError()
	}
	if b.TemplateID != "" && (b.TemplateID != r.TemplateID || b.TemplateVersion != r.TemplateVersion) {
		return adaptiveResearchError()
	}
	if r.Mode == "template" && r.TemplateID == "" {
		return adaptiveResearchError()
	}
	if b.ResearchPolicy == "disabled" && r.Mode == "reference" || (b.ResearchPolicy == "required" || len(b.ReferenceURLs) > 0) && r.Mode != "reference" {
		return adaptiveResearchError()
	}
	if r.Mode != "reference" {
		if len(p.References) != 0 {
			return adaptiveResearchError()
		}
		for _, shot := range p.Shots {
			if shot.hasAnySource() {
				return adaptiveResearchError()
			}
		}
		return nil
	}
	if len(p.References) == 0 {
		return adaptiveResearchError()
	}
	refs := map[string]bool{}
	for _, ref := range p.References {
		if refs[ref.URL] {
			return adaptiveResearchError()
		}
		refs[ref.URL] = true
		if len(b.ReferenceURLs) > 0 {
			if !slices.Contains(b.ReferenceURLs, ref.URL) {
				return adaptiveResearchError()
			}
		} else if !researchURLAllowed(ref.URL, b.Platforms) {
			return adaptiveResearchError()
		}
	}
	for _, u := range b.ReferenceURLs {
		if !refs[u] {
			return adaptiveResearchError()
		}
	}
	for _, shot := range p.Shots {
		if !shot.hasCompleteSource() {
			return adaptiveResearchError()
		}
	}
	return nil
}
func researchURLAllowed(raw string, platforms []string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if len(platforms) == 0 {
		platforms = []string{"douyin", "xiaohongshu"}
	}
	domains := map[string][]string{"douyin": {"douyin.com", "iesdouyin.com"}, "xiaohongshu": {"xiaohongshu.com", "xhslink.com"}, "bilibili": {"bilibili.com", "b23.tv"}}
	for _, platform := range platforms {
		for _, domain := range domains[platform] {
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return true
			}
		}
	}
	return false
}
