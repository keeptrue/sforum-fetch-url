package main

import (
	"net/url"
	"sort"
	"strings"
)

// Allowlist 是解析后的域名白名单。
//
// 语义刻意只有两条，因为运营者写错配置的代价是「抓取静默失效」或「放行了不该放行的站点」：
//
//   - example.com      → 放行 example.com 本身，以及它的任意子域（a.example.com）；
//   - *.example.com    → 只放行子域，不放行 example.com 本身。
//
// 不做路径前缀匹配：路径规则看起来更精确，实际上更容易让人以为自己限制了范围而
// 其实没有（重定向、大小写、百分号编码都能绕过）。
type Allowlist struct {
	// exact 是被显式写出的域名（含子域继承权）。
	exact map[string]struct{}
	// wildcard 是 *.domain 形式，只匹配子域。
	wildcard map[string]struct{}
	// entries 是解析后用于回显给模型/运营者的规范化条目（已排序、去重）。
	entries []string
}

// ParseAllowlist 解析多行/逗号分隔的白名单原文。
//
// 宽容处理运营者会真实写下的形态：`#` 注释行、前后空白、带 scheme 或路径的 URL、
// 末尾点、端口、大小写。宽容是有意的——白名单写错的表现是「什么都没发生」，
// 比报错更难排查。
func ParseAllowlist(raw string) Allowlist {
	list := Allowlist{exact: map[string]struct{}{}, wildcard: map[string]struct{}{}}
	seen := map[string]struct{}{}
	for _, token := range splitEntries(raw) {
		entry := normalizeEntry(token)
		if entry == "" {
			continue
		}
		if _, duplicate := seen[entry]; duplicate {
			continue
		}
		seen[entry] = struct{}{}
		if strings.HasPrefix(entry, "*.") {
			base := strings.TrimPrefix(entry, "*.")
			if base == "" {
				continue
			}
			list.wildcard[base] = struct{}{}
			list.entries = append(list.entries, entry)
			continue
		}
		list.exact[entry] = struct{}{}
		list.entries = append(list.entries, entry)
	}
	sort.Strings(list.entries)
	return list
}

// Allows 判断一个主机名是否被放行。host 可以是域名或 IP 字面量。
func (a Allowlist) Allows(host string) bool {
	candidate := normalizeHost(host)
	if candidate == "" {
		return false
	}
	if _, ok := a.exact[candidate]; ok {
		return true
	}
	// 逐级去掉最左标签，检查每一级是否命中「精确条目（含子域继承）」或「通配条目」。
	for index := 0; index < len(candidate); index++ {
		if candidate[index] != '.' {
			continue
		}
		rest := candidate[index+1:]
		if _, ok := a.exact[rest]; ok {
			return true
		}
		if _, ok := a.wildcard[rest]; ok {
			return true
		}
	}
	return false
}

// Empty 表示「没有任何条目」——此时一切抓取都被拒绝。
func (a Allowlist) Empty() bool { return len(a.entries) == 0 }

// Entries 返回规范化后的条目，用于回显（让模型和运营者看到的是同一份事实）。
func (a Allowlist) Entries() []string { return append([]string(nil), a.entries...) }

func splitEntries(raw string) []string {
	replaced := strings.NewReplacer("\r\n", "\n", "\r", "\n", ",", "\n", "，", "\n").Replace(raw)
	tokens := make([]string, 0, 8)
	for _, line := range strings.Split(replaced, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		tokens = append(tokens, trimmed)
	}
	return tokens
}

// normalizeEntry 把一个条目收敛成主机名或 *.主机名；无法识别时返回空串。
func normalizeEntry(token string) string {
	value := strings.ToLower(strings.TrimSpace(token))
	// 通配前缀必须在剥末尾点之前判断：`*.` 先剥点会变成 `*`，于是通配语义
	// 静默退化成「一个叫 * 的精确条目」。
	wildcard := strings.HasPrefix(value, "*.")
	if wildcard {
		value = strings.TrimPrefix(value, "*.")
	}
	if value == "" {
		return ""
	}
	// 运营者常常直接粘贴一个 URL。剥掉 scheme、userinfo、路径、查询与端口。
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
			value = parsed.Host
		} else {
			_, after, _ := strings.Cut(value, "://")
			value = after
		}
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	if slash := strings.IndexAny(value, "/?#"); slash >= 0 {
		value = value[:slash]
	}
	value = stripPort(value)
	value = strings.TrimSuffix(value, ".")
	value = normalizeHost(value)
	// 条目里不允许再有 *：`a.*.com` 这种写法只会让人以为它匹配了什么。
	if value == "" || strings.Contains(value, "*") {
		return ""
	}
	if wildcard {
		return "*." + value
	}
	return value
}

// stripPort 只剥掉「显然是端口」的后缀：IPv6 字面量（带冒号）原样返回。
func stripPort(host string) string {
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end >= 0 {
			return host[:end+1]
		}
		return host
	}
	if strings.Count(host, ":") != 1 {
		return host
	}
	name, port, _ := strings.Cut(host, ":")
	if port == "" || !isDigits(port) {
		return host
	}
	return name
}

func isDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return len(value) > 0
}

// normalizeHost 统一主机名形态：小写、去末尾点、去 IPv6 方括号。
func normalizeHost(host string) string {
	value := strings.ToLower(strings.TrimSpace(host))
	value = strings.TrimSuffix(value, ".")
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = value[1 : len(value)-1]
	}
	return value
}
