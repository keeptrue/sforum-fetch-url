package main

import (
	"reflect"
	"testing"
)

func TestAllowlistDeniesEverythingWhenEmpty(t *testing.T) {
	list := ParseAllowlist("")
	if !list.Empty() {
		t.Fatalf("empty input produced %d entries", len(list.Entries()))
	}
	for _, host := range []string{"example.com", "a.example.com", "127.0.0.1"} {
		if list.Allows(host) {
			t.Fatalf("empty allowlist allowed %q", host)
		}
	}
}

func TestAllowlistMatchesHostAndSubdomains(t *testing.T) {
	list := ParseAllowlist("example.com")
	cases := map[string]bool{
		"example.com":      true,
		"www.example.com":  true,
		"a.b.example.com":  true,
		"EXAMPLE.COM":      true,
		"example.com.":     true,
		"notexample.com":   false,
		"example.com.evil": false,
		"example.org":      false,
	}
	for host, want := range cases {
		if got := list.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestAllowlistWildcardMatchesSubdomainsOnly(t *testing.T) {
	list := ParseAllowlist("*.example.com")
	cases := map[string]bool{
		"www.example.com":  true,
		"a.b.example.com":  true,
		"example.com":      false,
		"otherexample.com": false,
	}
	for host, want := range cases {
		if got := list.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestAllowlistNormalizesOperatorInput(t *testing.T) {
	list := ParseAllowlist(`
	  # 注释行会被忽略
	  Example.COM
	  https://docs.example.com/guide/intro?x=1
	  wiki.example.org:8443
	  *.mirror.example.net
	  example.com
	  , ,
	  docs.example.com，cn.example.com
	`)
	cases := map[string]bool{
		"example.com":             true,
		"docs.example.com":        true,
		"wiki.example.org":        true,
		"a.mirror.example.net":    true,
		"mirror.example.net":      false, // *.x 只匹配子域，不放行 apex
		"mirror.example.net.evil": false,
		"cn.example.com":          true,
	}
	for host, want := range cases {
		if got := list.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
	if got, want := list.Entries(), []string{"*.mirror.example.net", "cn.example.com", "docs.example.com", "example.com", "wiki.example.org"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries() = %v, want %v", got, want)
	}
}

func TestAllowlistDropsUnparsableEntries(t *testing.T) {
	list := ParseAllowlist("*.\n://\n#\n")
	if !list.Empty() {
		t.Fatalf("garbage produced entries: %v", list.Entries())
	}
}
