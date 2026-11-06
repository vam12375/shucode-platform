package templatelibrary

import (
	"slices"
	"strings"
	"testing"
)

func TestLegacyVideoOriginalsRemainCompleteAndUnpublished(t *testing.T) {
	// 冻结导出清单中的原件哈希，防止收录时截断正文或改写换行。
	expected := map[string]string{
		"template-e1a44b99bce72581ba54464f": "e1a44b99bce72581ba54464fd80910b38d8ca18e4d2f5cf4fa0447b1b02e840f",
		"template-bce6ca1f533cd3857b985bc9": "bce6ca1f533cd3857b985bc93b51887792e124d4091d4435857266c745d8a0f0",
		"template-d706d98d9a30bff660a60613": "d706d98d9a30bff660a60613b0ba2b3797e0f97b2d9404a1fc38d7cc6187f10d",
		"template-4e764364769d48b4ca353656": "4e764364769d48b4ca3536560b32a7068d35c8ed949f7675eaddc42267cc253e",
		"template-fe76f0b5c0477ab445a323d1": "fe76f0b5c0477ab445a323d133e5f85132055a85e5f47e11d699550b96d1eeda",
		"template-f8c7f157a95aedf11f029b87": "f8c7f157a95aedf11f029b8756c28300ff7b1ff093270709680cd2af03bb6447",
		"template-933c6f8433a39a25b06955b0": "933c6f8433a39a25b06955b0251bad7f5ac0eeed71fd2d64423cc41685128659",
		"template-73b492f742c90fda6efebfd8": "73b492f742c90fda6efebfd8629badc318750a7eee5d921f65de2f72175e43cf",
	}
	entries, err := legacyEntries()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	published, drafts := 0, 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.ID, "dineops:video-template:") {
			continue
		}
		slug := strings.TrimPrefix(entry.ID, "dineops:video-template:")
		if seen[slug] || expected[slug] == "" || digest([]byte(entry.Content)) != expected[slug] {
			t.Fatalf("duplicate, unknown or modified original: %s", entry.ID)
		}
		seen[slug] = true
		if entry.Enabled || entry.Kind != "template" || entry.Source != "dineops" || !slices.Contains(entry.Tags, "视频提示词模板") {
			t.Fatalf("legacy original must remain an unpublished text template: %s", entry.ID)
		}
		if err := validateFields(entry.Kind, &entry.Fields); err != nil {
			t.Fatalf("original cannot be edited: %s: %v", entry.ID, err)
		}
		if slices.Contains(entry.Tags, "旧版已发布") {
			published++
		}
		if slices.Contains(entry.Tags, "旧版草稿") {
			drafts++
		}
	}
	if len(seen) != len(expected) || published != 3 || drafts != 5 {
		t.Fatalf("incomplete export: %d originals, %d published, %d drafts", len(seen), published, drafts)
	}
}
