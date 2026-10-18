package contracts

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type fixturesFile struct {
	DeviceTokens []struct {
		Token string `json:"token"`
		Valid bool   `json:"valid"`
	} `json:"deviceTokens"`
	DeepLinks []struct {
		FlowID string `json:"flowId"`
		URL    string `json:"url"`
	} `json:"deepLinks"`
	DownloadURLs []struct {
		Base     string          `json:"base"`
		Revision json.RawMessage `json:"revision"`
		URL      string          `json:"url"`
	} `json:"downloadUrls"`
	BuiltinReleases []struct {
		Input    any     `json:"input"`
		Ok       bool    `json:"ok"`
		Revision float64 `json:"revision"`
		Error    string  `json:"error"`
	} `json:"builtinReleases"`
	ConfigsEnvelopes []struct {
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	} `json:"configsEnvelopes"`
	InitRequests []struct {
		Body map[string]any `json:"body"`
		Ok   bool           `json:"ok"`
	} `json:"initRequests"`
}

func loadFixtures(t *testing.T) fixturesFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatalf("读取契约 fixtures(先在仓库根目录跑 node scripts/export-contract-fixtures.ts): %v", err)
	}
	var f fixturesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析 fixtures: %v", err)
	}
	return f
}

func TestDeviceTokenFixtures(t *testing.T) {
	for _, tc := range loadFixtures(t).DeviceTokens {
		if got := ValidatePlatformDeviceToken(tc.Token); got != tc.Valid {
			t.Fatalf("设备 token 校验不符 %q: got %v want %v", tc.Token, got, tc.Valid)
		}
	}
}

func TestDeepLinkFixtures(t *testing.T) {
	for _, tc := range loadFixtures(t).DeepLinks {
		if got := BuildPlatformDeepLinkCallbackURL(tc.FlowID); got != tc.URL {
			t.Fatalf("deep link 不符 %q: got %s want %s", tc.FlowID, got, tc.URL)
		}
	}
}

func TestDownloadURLFixtures(t *testing.T) {
	for _, tc := range loadFixtures(t).DownloadURLs {
		var revision float64
		if err := json.Unmarshal(tc.Revision, &revision); err != nil {
			t.Fatalf("revision 解析: %v", err)
		}
		if got := BuildBuiltinReleaseDownloadURL(tc.Base, revision); got != tc.URL {
			t.Fatalf("下载 URL 不符: got %s want %s", got, tc.URL)
		}
	}
}

func TestBuiltinReleaseFixtures(t *testing.T) {
	for i, tc := range loadFixtures(t).BuiltinReleases {
		revision, _, err := ParseBuiltinReleaseLoose(tc.Input)
		if tc.Ok {
			if err != nil {
				t.Fatalf("样例 %d 应通过: %v", i, err)
			}
			if revision != tc.Revision {
				t.Fatalf("样例 %d revision 不符: got %v want %v", i, revision, tc.Revision)
			}
			continue
		}
		if err == nil {
			t.Fatalf("样例 %d 应拒绝", i)
		}
		// TS 导出的 error 字段本身就是完整文案(parseBuiltinReleaseLoose 抛出的 message)。
		if err.Error() != tc.Error {
			t.Fatalf("样例 %d 错误文案不符: got %q want %q", i, err.Error(), tc.Error)
		}
	}
}

func TestConfigsEnvelopeFixtures(t *testing.T) {
	normalize := func(t *testing.T, v any) any {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("序列化: %v", err)
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("反序列化: %v", err)
		}
		return out
	}
	for i, tc := range loadFixtures(t).ConfigsEnvelopes {
		var input BuildClientConfigsEnvelopeInput
		if err := json.Unmarshal(tc.Input, &input); err != nil {
			t.Fatalf("样本 %d 输入还原: %v", i, err)
		}
		got := normalize(t, BuildClientConfigsEnvelope(input))
		var want any
		if err := json.Unmarshal(tc.Output, &want); err != nil {
			t.Fatalf("样本 %d 期望还原: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("样本 %d 信封不符:\n got %s\nwant %s", i, mustDump(t, got), tc.Output)
		}
	}
}

func TestParsePlatformAuthInitRequestFixtures(t *testing.T) {
	for i, tc := range loadFixtures(t).InitRequests {
		_, err := ParsePlatformAuthInitRequest(tc.Body)
		if tc.Ok && err != nil {
			t.Fatalf("样本 %d 应通过: %v", i, err)
		}
		if !tc.Ok && err == nil {
			t.Fatalf("样本 %d 应拒绝: %+v", i, tc.Body)
		}
	}
}

// ValidatePlatformAuthFlowID 的边界用例直接内联(schema 规则简单且 change 少)。
func TestPlatformAuthFlowID(t *testing.T) {
	if id, ok := ValidatePlatformAuthFlowID("  abcdefghijklmnop  "); !ok || id != "abcdefghijklmnop" {
		t.Fatalf("合法 flow id 处理不符: %q %v", id, ok)
	}
	if _, ok := ValidatePlatformAuthFlowID("short"); ok {
		t.Fatal("过短 flow id 应拒绝")
	}
	if _, ok := ValidatePlatformAuthFlowID(string(make([]byte, 65))); ok {
		t.Fatal("过长 flow id 应拒绝")
	}
}

func mustDump(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
