package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 主机守卫：这一层**不可配置、不可关闭**，因为它的调用者不是运营者，而是任意
// 论坛用户——模型会照着用户给的链接去抓。一个可以被配置关掉的 SSRF 边界等于没有
// 边界，所以它不进入设置页。
//
// 两层：
//
//  1. 字面量层（任何模式下都生效）：拒绝私有/回环/保留地址、单标签主机名、
//     localhost / *.local / *.internal / *.home.arpa 这类内部名字。
//  2. 解析层（只在直连时生效）：域名解析出的每个 IP 都必须是公网地址，并且在
//     建立连接时再校验一次（防 DNS rebinding）。走代理时 DNS 由代理解析，
//     本机拿不到真实出口 IP，因此这一层必然降级——设置页的 callout 与 README
//     都写明了这一点，而不是让它在暗处失效。

// forbiddenHostSuffixes 是内部专用域名后缀（RFC 6761/6762/8375 及常见内网约定）。
var forbiddenHostSuffixes = []string{
	".localhost", ".local", ".localdomain", ".internal", ".intranet", ".corp",
	".home", ".lan", ".private", ".test", ".example", ".invalid", ".onion",
	".home.arpa", ".in-addr.arpa", ".ip6.arpa",
}

// forbiddenExactHosts 是永远不放行的主机名。
var forbiddenExactHosts = map[string]struct{}{
	"localhost": {}, "localhost.localdomain": {}, "ip6-localhost": {}, "ip6-loopback": {},
}

// reservedNetworks 覆盖 Go 标准库判定之外的保留段。
// 标准库的 IsPrivate/IsLoopback/IsLinkLocal* 已经处理了 10/8、172.16/12、
// 192.168/16、127/8、169.254/16、fc00::/7、fe80::/10、::1；这里补上
// 「看起来像公网但其实不是」的那些段。
var reservedNetworks = mustParseCIDRs(
	"0.0.0.0/8",          // 本网络
	"100.64.0.0/10",      // CGNAT
	"192.0.0.0/24",       // IETF 协议分配
	"192.0.2.0/24",       // TEST-NET-1
	"198.18.0.0/15",      // 基准测试
	"198.51.100.0/24",    // TEST-NET-2
	"203.0.113.0/24",     // TEST-NET-3
	"224.0.0.0/4",        // 组播
	"240.0.0.0/4",        // 保留
	"255.255.255.255/32", // 广播
	"64:ff9b::/96",       // NAT64 翻译前缀
	"100::/64",           // 丢弃前缀
	"2001:db8::/32",      // 文档用
	"2002::/16",          // 6to4（出口不可预测）
	"3fff::/20",          // 文档用（RFC 9637）
)

func mustParseCIDRs(values ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, block, err := net.ParseCIDR(value)
		if err != nil {
			panic(fmt.Sprintf("invalid reserved network %q: %v", value, err))
		}
		nets = append(nets, block)
	}
	return nets
}

// checkURLShape 校验 scheme、userinfo 与主机形态，并施加字面量层守卫。
// 它不解析 DNS，因此可以廉价地对每一跳重定向重复调用。
func checkURLShape(raw string, allowHTTP bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fail("地址为空。请给出完整的 http(s) 网址。")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fail("这不是一个合法的网址：%s", trimmed)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !allowHTTP {
			return nil, fail("只允许 https 地址。若确实需要抓取明文 http 站点，请让管理员在插件设置里打开「允许明文 http」。")
		}
	default:
		return nil, fail("只支持 http 与 https 地址，当前是 %q。", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, fail("地址里不允许携带用户名或密码。")
	}
	if parsed.Host == "" {
		return nil, fail("网址缺少主机名：%s", trimmed)
	}
	host := normalizeHost(parsed.Hostname())
	if host == "" {
		return nil, fail("网址缺少主机名：%s", trimmed)
	}
	if reason, forbidden := forbiddenLiteralHost(host); forbidden {
		return nil, fail("%s", reason)
	}
	return parsed, nil
}

// forbiddenLiteralHost 是字面量层判定：不查 DNS，只看主机名本身。
func forbiddenLiteralHost(host string) (string, bool) {
	if _, ok := forbiddenExactHosts[host]; ok {
		return fmt.Sprintf("拒绝访问本机地址 %s。", host), true
	}
	for _, suffix := range forbiddenHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return fmt.Sprintf("拒绝访问内部域名 %s。", host), true
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPublicIP(ip) {
			return "", false
		}
		return fmt.Sprintf("拒绝访问非公网地址 %s。", host), true
	}
	// 单标签主机名（intranet、wiki）永远不是公网站点；它们只能由内部 DNS 解析。
	if !strings.Contains(host, ".") {
		return fmt.Sprintf("拒绝访问单标签主机名 %s（只能是内部地址）。", host), true
	}
	return "", false
}

// isPublicIP 判定一个已解析的 IP 是否可以出现在公网抓取路径上。
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsPrivate() {
		return false
	}
	for _, block := range reservedNetworks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

// resolvePublicIPs 解析主机名并确认每一个结果都是公网地址。
// 任一结果落在保留段就整体拒绝：只放行部分结果会留下「解析顺序决定安全性」的口子。
func resolvePublicIPs(ctx context.Context, resolver resolverFunc, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, fail("拒绝访问非公网地址 %s。", host)
		}
		return []net.IP{ip}, nil
	}
	addresses, err := resolver(ctx, host)
	if err != nil {
		// 解析失败是外部条件，不是插件故障：模型应该读到一条可解释的说明，
		// 而不是让整次工具调用以「内部错误」的姿态消失。
		return nil, fail("无法解析域名 %s（%s）。", host, err)
	}
	if len(addresses) == 0 {
		return nil, fail("域名 %s 没有解析到任何地址。", host)
	}
	resolved := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if !isPublicIP(address) {
			return nil, fail("域名 %s 解析到了非公网地址，已拒绝访问。", host)
		}
		resolved = append(resolved, address)
	}
	return resolved, nil
}

type resolverFunc func(ctx context.Context, host string) ([]net.IP, error)

// systemResolver 是生产解析器；测试可替换它以避免真实 DNS。
func systemResolver(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	resolved := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		resolved = append(resolved, address.IP)
	}
	return resolved, nil
}
