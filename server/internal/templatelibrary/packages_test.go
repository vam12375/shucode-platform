package templatelibrary

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"reflect"
	"testing"
)

func TestPackageSeedsAreComplete(t *testing.T) {
	entries, err := legacyEntries()
	if err != nil {
		t.Fatal(err)
	}
	count, files := 0, 0
	for _, entry := range entries {
		if entry.Kind != "skill" {
			continue
		}
		count++
		raw, err := legacyFiles.ReadFile("packages/" + entry.PackageDigest + ".zip")
		if err != nil {
			t.Fatal(err)
		}
		info, _, content, err := inspectPackage(raw)
		if err != nil {
			t.Fatalf("%s: %v", entry.ID, err)
		}
		if !reflect.DeepEqual(&info, entry.Package) || content != entry.Content {
			t.Fatalf("%s: manifest or content mismatch", entry.ID)
		}
		files += len(info.Files)
	}
	if count != 13 || files != 44 {
		t.Fatalf("import incomplete: %d skills, %d files", count, files)
	}
}
func packageFixture(t *testing.T, files []packageContent) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, file := range files {
		h := zip.FileHeader{Name: file.Path, Method: zip.Deflate}
		mode := fs.FileMode(0644)
		if file.Executable {
			mode = 0755
		}
		h.SetMode(mode)
		f, err := w.CreateHeader(&h)
		if err != nil {
			t.Fatal(err)
		}
		f.Write(file.Content)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestPackageValidationAndPortablePaths(t *testing.T) {
	valid := []packageContent{{Path: "bundle/SKILL.md", Content: []byte("---\nname: sample\ndescription: sample\n---\n[Guide](references/guide.md)")}, {Path: "bundle/references/guide.md", Content: []byte("[asset](../assets/image.bin)")}, {Path: "bundle/assets/image.bin", Content: []byte{0, 255, 1}}, {Path: "bundle/scripts/run.sh", Content: []byte("#!/bin/sh\ntrue"), Executable: true}}
	info, _, _, err := inspectPackage(packageFixture(t, valid))
	if err != nil || len(info.Files) != 4 {
		t.Fatalf("valid: %+v %v", info, err)
	}
	if info.Files[3].Path != "scripts/run.sh" || !info.Files[3].Executable {
		t.Fatal("execution bit lost")
	}
	for name, files := range map[string][]packageContent{
		"missing dependency": valid[:1],
		"traversal":          {{Path: "../SKILL.md", Content: []byte("bad")}},
		"case collision":     {{Path: "SKILL.md", Content: []byte("a")}, {Path: "skill.md", Content: []byte("b")}},
		"multiple skills":    {{Path: "one/SKILL.md", Content: []byte("a")}, {Path: "two/SKILL.md", Content: []byte("b")}},
		"outside root":       append(slicesCopy(valid), packageContent{Path: "outside.txt", Content: []byte("x")}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := inspectPackage(packageFixture(t, files)); err == nil {
				t.Fatal("invalid package accepted")
			}
		})
	}
}
func slicesCopy(v []packageContent) []packageContent { return append([]packageContent(nil), v...) }
func TestClientSceneSeedKeepsAllCollectedPrompts(t *testing.T) {
	entries, err := sceneEntries()
	if err != nil || len(entries) != 50 {
		t.Fatalf("seeds: %d %v", len(entries), err)
	}
	seen := map[string]bool{}
	automations := 0
	for _, entry := range entries {
		if seen[entry.ID] {
			t.Fatal("duplicate identity")
		}
		seen[entry.ID] = true
		if entry.Kind == "automation" {
			automations++
			if entry.Scene == "scheduled-task" && !validCron(entry.CronExpr) {
				t.Fatalf("invalid seed cron %s", entry.CronExpr)
			}
		}
	}
	if automations != 7 {
		t.Fatal("automation seeds missing")
	}
}
