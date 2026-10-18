package gwapp

import "testing"

// 向量与 apps/gateway/src/client-ip.test.ts 完全一致。
func TestNormalizeIPVectors(t *testing.T) {
	valid := [][2]string{
		{" 203.0.113.7 ", "203.0.113.7"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"[::FFFF:203.0.113.7]", "203.0.113.7"},
		{"0:0:0:0:0:ffff:cb00:7107", "203.0.113.7"},
		{"::ffff:0:203.0.113.7", "::ffff:0:cb00:7107"},
		{"::ffff:0:cb00:7107", "::ffff:0:cb00:7107"},
		{"::ffff:1:cb00:7107", "::ffff:1:cb00:7107"},
		{"2001:0DB8:0:0:0:0:0:7", "2001:db8::7"},
		{"[2001:DB8::7]", "2001:db8::7"},
		{"::1", "::1"},
		{"fd00::7", "fd00::7"},
	}
	for _, tc := range valid {
		if got, ok := NormalizeIP(tc[0]); !ok || got != tc[1] {
			t.Fatalf("normalize(%q) = %q,%v want %q", tc[0], got, ok, tc[1])
		}
		if got, ok := NormalizeIP(tc[1]); !ok || got != tc[1] {
			t.Fatalf("幂等归一失败 %q", tc[1])
		}
	}
	invalid := []string{
		"", "unknown", "not-an-ip", "999.1.1.1", "010.0.0.1", "127.0.0.1.evil",
		"203.0.113.7:443", "[2001:db8::7]:443", "::ffff:not-an-ip", "fe80::1%eth0",
		"203.0.113.7, 203.0.113.8", `"203.0.113.7"`,
	}
	for _, input := range invalid {
		if got, ok := NormalizeIP(input); ok {
			t.Fatalf("应拒绝 %q,得到 %q", input, got)
		}
	}
}

func TestParseTrustedProxyIPs(t *testing.T) {
	for _, input := range []string{"", "  "} {
		if got, err := ParseTrustedProxyIPs(input); err != nil || len(got) != 0 {
			t.Fatalf("空配置应返回空且不报错: %v %v", got, err)
		}
	}
	got, err := ParseTrustedProxyIPs(" 10.0.0.2, ::ffff:10.0.0.2, [2001:0DB8::2] ")
	if err != nil || len(got) != 2 || got[0] != "10.0.0.2" || got[1] != "2001:db8::2" {
		t.Fatalf("归一化去重不符: %v %v", got, err)
	}
	for _, input := range []string{"*", "10.0.0.0/8", "localhost", "10.0.0.2,", ",10.0.0.2", "10.0.0.2,invalid"} {
		if _, err := ParseTrustedProxyIPs(input); err == nil {
			t.Fatalf("非法配置应报错: %q", input)
		}
	}
}

func TestResolveClientIP(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.2", "10.0.0.3", "2001:db8::2"})

	// 非可信对端:忽略全部转发头。
	for _, remote := range []string{"198.51.100.20", "::1234", "10.0.0.4", "127.0.0.1", "fd00::1"} {
		got := ResolveClientIP(remote, []string{"203.0.113.7"}, true, "203.0.113.8", trusted)
		if got == nil || *got != remote {
			t.Fatalf("非可信对端 %s 应为自身: %v", remote, got)
		}
		got = ResolveClientIP(remote, nil, false, "203.0.113.8", trusted)
		if got == nil || *got != remote {
			t.Fatalf("非可信对端(仅 real-ip) %s 应为自身", remote)
		}
	}
	// 未知/非法对端:不采信头,返回 nil。
	for _, remote := range []string{"", "invalid", "::ffff:invalid"} {
		if got := ResolveClientIP(remote, []string{"203.0.113.7"}, true, "203.0.113.8", trusted); got != nil {
			t.Fatalf("非法对端 %q 应为 nil,得到 %v", remote, *got)
		}
	}
	// 代理链:从右往左取首个非可信地址。
	chain := []struct {
		remote, forwarded, want string
	}{
		{"10.0.0.2", "203.0.113.7", "203.0.113.7"},
		{"10.0.0.2", "203.0.113.7, 10.0.0.3", "203.0.113.7"},
		{"10.0.0.2", "192.0.2.99, 203.0.113.7", "203.0.113.7"},
		{"10.0.0.2", "192.0.2.99, 10.0.0.4, 10.0.0.3", "10.0.0.4"},
		{"::ffff:10.0.0.2", "[::ffff:203.0.113.7], ::ffff:10.0.0.3", "203.0.113.7"},
		{"2001:0DB8:0:0:0:0:0:2", "2001:DB8::7, 10.0.0.3", "2001:db8::7"},
		{"10.0.0.2", "10.0.0.3", "10.0.0.3"},
		{"10.0.0.2", "unknown, 203.0.113.7", "203.0.113.7"},
	}
	for _, tc := range chain {
		got := ResolveClientIP(tc.remote, []string{tc.forwarded}, true, "192.0.2.98", trusted)
		if got == nil || *got != tc.want {
			t.Fatalf("代理链 %s / %s: got %v want %s", tc.remote, tc.forwarded, got, tc.want)
		}
	}
	// XFF 非法:保留对端,不降级 X-Real-IP。
	for _, forwarded := range []string{"", "unknown", "999.1.1.1", "203.0.113.7:443", "[::1]:443", "fe80::1%eth0", "203.0.113.7,", "203.0.113.7, unknown, 10.0.0.3"} {
		got := ResolveClientIP("10.0.0.2", []string{forwarded}, true, "203.0.113.8", trusted)
		if got == nil || *got != "10.0.0.2" {
			t.Fatalf("XFF %q 非法应保留对端,得到 %v", forwarded, got)
		}
	}
	// 无 XFF 时使用校验过的 X-Real-IP。
	if got := ResolveClientIP("10.0.0.2", nil, false, "[::ffff:203.0.113.7]", trusted); got == nil || *got != "203.0.113.7" {
		t.Fatalf("X-Real-IP 回退失败: %v", got)
	}
	// 无有效转发头:保留对端。
	for _, realIP := range []string{"", "unknown", "203.0.113.7, 203.0.113.8"} {
		if got := ResolveClientIP("10.0.0.2", nil, false, realIP, trusted); got == nil || *got != "10.0.0.2" {
			t.Fatalf("real-ip %q 无效应保留对端", realIP)
		}
	}
	// "::ffff:0:203.0.113.7" 作为可信代理地址时,代理链仍解析出客户端。
	set := NewTrustedProxies([]string{"::ffff:0:cb00:7107"})
	if got := ResolveClientIP("::ffff:0:203.0.113.7", []string{"198.51.100.20"}, true, "", set); got == nil || *got != "198.51.100.20" {
		t.Fatalf("非 mapped 的 ::ffff: 可信代理处理失败: %v", got)
	}
}
