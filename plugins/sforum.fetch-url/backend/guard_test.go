package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestCheckURLShapeEnforcesSchemeAndUserinfo(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		allowHTTP bool
		wantFail  bool
	}{
		{name: "https ok", raw: "https://example.com/page", wantFail: false},
		{name: "http rejected by default", raw: "http://example.com/", allowHTTP: false, wantFail: true},
		{name: "http allowed when enabled", raw: "http://example.com/", allowHTTP: true, wantFail: false},
		{name: "file scheme rejected", raw: "file:///etc/passwd", allowHTTP: true, wantFail: true},
		{name: "gopher scheme rejected", raw: "gopher://example.com/", allowHTTP: true, wantFail: true},
		{name: "userinfo rejected", raw: "https://user:pass@example.com/", wantFail: true},
		{name: "missing host rejected", raw: "https:///path", wantFail: true},
		{name: "empty rejected", raw: "   ", wantFail: true},
		{name: "not a url rejected", raw: "example.com", wantFail: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := checkURLShape(testCase.raw, testCase.allowHTTP)
			if testCase.wantFail && err == nil {
				t.Fatalf("expected failure for %q", testCase.raw)
			}
			if !testCase.wantFail && err != nil {
				t.Fatalf("unexpected failure for %q: %v", testCase.raw, err)
			}
			if err != nil {
				var failure toolFailure
				if !errors.As(err, &failure) {
					t.Fatalf("error %v is not an explainable tool failure", err)
				}
				if strings.TrimSpace(failure.message) == "" {
					t.Fatalf("explainable failure carries no message")
				}
			}
		})
	}
}

func TestForbiddenLiteralHostBlocksInternalTargets(t *testing.T) {
	blocked := []string{
		"localhost", "ip6-localhost",
		"foo.local", "printer.internal", "wiki.intranet", "box.corp", "svc.home.arpa",
		"intranet", // 单标签主机名
		"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.5.4",
		"169.254.169.254", // 云元数据端点
		"100.64.0.1",      // CGNAT
		"192.0.2.10",      // TEST-NET-1
		"198.18.0.1",      // 基准测试段
		"0.0.0.0", "255.255.255.255",
		"::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1",
		"2001:db8::1", // 文档用段
	}
	for _, host := range blocked {
		if reason, forbidden := forbiddenLiteralHost(host); !forbidden {
			t.Errorf("forbiddenLiteralHost(%q) allowed it (reason=%q)", host, reason)
		}
	}

	allowed := []string{"example.com", "docs.example.com", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, host := range allowed {
		if reason, forbidden := forbiddenLiteralHost(host); forbidden {
			t.Errorf("forbiddenLiteralHost(%q) blocked it: %s", host, reason)
		}
	}
}

func TestResolvePublicIPsRejectsMixedAnswers(t *testing.T) {
	// 只要有一个答案是内网地址就整体拒绝：否则「解析顺序」会决定安全性。
	mixed := func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}, nil
	}
	if _, err := resolvePublicIPs(context.Background(), mixed, "example.com"); err == nil {
		t.Fatalf("mixed public/private answers were accepted")
	}

	allPublic := func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	resolved, err := resolvePublicIPs(context.Background(), allPublic, "example.com")
	if err != nil {
		t.Fatalf("public answers rejected: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved = %v", resolved)
	}
}

func TestResolvePublicIPsRejectsPrivateLiteral(t *testing.T) {
	noLookup := func(context.Context, string) ([]net.IP, error) {
		t.Fatalf("literal IP must not hit the resolver")
		return nil, nil
	}
	if _, err := resolvePublicIPs(context.Background(), noLookup, "10.1.2.3"); err == nil {
		t.Fatalf("private literal was accepted")
	}
}

func TestResolvePublicIPsReportsEmptyAnswers(t *testing.T) {
	empty := func(context.Context, string) ([]net.IP, error) { return nil, nil }
	if _, err := resolvePublicIPs(context.Background(), empty, "example.com"); err == nil {
		t.Fatalf("empty resolution was accepted")
	}
}
