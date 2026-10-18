package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	pluginv2 "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2"
)

const (
	// pluginVersion 只用于 User-Agent；发布时与 manifest 的 version 保持一致。
	pluginVersion = "1.0.0"
	// maxRedirects 是跟随跳数上限。每一跳都会重新过 scheme、主机守卫与白名单，
	// 否则一个开放重定向就能把「只允许 example.com」变成「允许任何站点」。
	maxRedirects = 3
	// maxResponseBytes 是「连读都不读」的硬上限：Content-Length 超过它就说明
	// 目标根本不是一篇文章，没必要为它消耗带宽与时间。
	maxResponseBytes = 8 * 1024 * 1024
	// payloadBudget 是回给模型的正文上限，刻意小于宿主 8000 字节的工具结果截断，
	// 这样切在哪里由插件决定（可读的边界），而不是由宿主在结构中间切一刀。
	payloadBudget = 7500
	// probePreviewBytes 是设置页探测回显的正文预览长度。
	probePreviewBytes = 400
)

// toolFailure 表示「工具可解释的失败」：模型读到它并可能换一种方式重试。
// 它与 handler 返回 Go error 是两回事——后者表示插件内部故障，宿主只会给模型
// 一句通用说明，插件内部错误串不会借模型之口发布到社区。
type toolFailure struct{ message string }

func (f toolFailure) Error() string { return f.message }

func fail(format string, args ...any) error {
	return toolFailure{message: fmt.Sprintf(format, args...)}
}

// Engine 持有进程生命周期内不变的配置与 HTTP 传输。
//
// 配置变化（运营者保存设置）会让宿主重启本插件的精确制品，因此这里不需要热重载；
// 设置页的「测试抓取」也是用一个带草稿环境的短命进程完成的。
type Engine struct {
	config    Config
	allowlist Allowlist
	client    *http.Client
	proxy     *url.URL
	proxyErr  error
	// resolver 与 dial 是可注入的依赖：测试用它们把「example.com」指到本机
	// httptest 服务，从而走完整条真实路径（守卫 → 白名单 → 重定向 → 抽取）。
	// 生产路径不提供任何「跳过守卫」的开关——守卫是给任意论坛用户用的边界。
	resolver resolverFunc
	dial     func(ctx context.Context, network, address string) (net.Conn, error)
	now      func() time.Time
}

// NewEngine 构造引擎。代理地址非法不会让进程起不来：它会被记下来，在每次调用时
// 变成一条可解释的失败——插件启动失败会让整个插件（包括设置页）不可用，
// 那对运营者是最难排查的形态。
func NewEngine(config Config) *Engine {
	engine := &Engine{
		config:    config,
		allowlist: ParseAllowlist(config.AllowedDomains),
		resolver:  systemResolver,
		now:       time.Now,
	}
	engine.dial = engine.dialDirect
	if trimmed := strings.TrimSpace(config.ProxyURL); trimmed != "" {
		parsed, err := parseProxyURL(trimmed)
		if err != nil {
			engine.proxyErr = err
		} else {
			engine.proxy = parsed
		}
	}
	engine.client = engine.newClient()
	return engine
}

// parseProxyURL 只接受 http / https / socks5 三种代理 scheme。
func parseProxyURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("无法解析代理地址")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("代理 scheme %q 不受支持（只支持 http、https、socks5）", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("代理地址缺少主机与端口")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("暂不支持代理认证（地址里带了用户名或密码）")
	}
	return parsed, nil
}

func (e *Engine) newClient() *http.Client {
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   4 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// 连接建立后到响应头之间的等待也受总预算约束，这里只做一次保险。
		ResponseHeaderTimeout: e.config.Timeout,
	}
	if e.proxy != nil {
		transport.Proxy = http.ProxyURL(e.proxy)
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if e.proxy != nil {
			// 这里连的是代理自身（常常就是 127.0.0.1），不能套用公网校验。
			// 代价是真实出口 IP 不可知——设置页的 callout 与 README 都写明了。
			return e.dial(ctx, network, address)
		}
		// 直连：解析并逐个 IP 校验，然后连到具体 IP（防 DNS rebinding：
		// 校验用的解析结果就是实际连接的目标）。
		ips, err := resolvePublicIPs(ctx, e.resolver, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, dialErr := e.dial(ctx, network, net.JoinHostPort(ip.String(), portOf(address)))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no dialable address for %s", host)
		}
		return nil, lastErr
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fail("重定向次数超过 %d 次，已停止。", maxRedirects)
			}
			// 每一跳都重新过守卫与白名单：重定向目标是目标站点给出的，
			// 不能因为「起点被允许」就继承信任。
			if _, err := checkURLShape(request.URL.String(), e.config.AllowHTTP); err != nil {
				return err
			}
			if !e.allowlist.Allows(request.URL.Hostname()) {
				return fail("重定向到了不在白名单里的域名 %s，已停止。", normalizeHost(request.URL.Hostname()))
			}
			return nil
		},
	}
}

// dialDirect 是生产用的连接方式：解析到 IP 之后按 IP 连接。
func (e *Engine) dialDirect(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 15 * time.Second}
	return dialer.DialContext(ctx, network, address)
}

// portOf 从 host:port 里取端口；取不到时返回 80（http 默认端口）。
func portOf(address string) string {
	if _, port, err := net.SplitHostPort(address); err == nil && port != "" {
		return port
	}
	return "80"
}

// FetchResult 是一次成功抓取的完整事实。
type FetchResult struct {
	FinalURL    string
	Status      int
	StatusText  string
	ContentType string
	Title       string
	Text        string
	BytesRead   int64
	Truncated   bool
	Elapsed     time.Duration
}

// RunModel 服务模型-facing 的 fetch-url 工具。
func (e *Engine) RunModel(ctx context.Context, rawURL string) (*pluginv2.AIToolResult, error) {
	if !e.config.Enabled {
		return explainable("网页抓取功能当前已被管理员关闭。"), nil
	}
	result, err := e.fetch(ctx, rawURL)
	if err != nil {
		return e.translate(err)
	}
	if result.Status < 200 || result.Status > 299 {
		return explainable(renderNonSuccess(result)), nil
	}
	return &pluginv2.AIToolResult{Content: renderForModel(result)}, nil
}

// RunProbe 服务设置页的「测试抓取」。它不受 enabled 开关影响：探测要回答的是
// 「按当前草稿值能不能抓」，而不是「现在生效了吗」。
func (e *Engine) RunProbe(ctx context.Context) (*pluginv2.AIToolResult, error) {
	target := strings.TrimSpace(e.config.ProbeURL)
	if target == "" {
		return explainable("测试目标地址为空。请先在「测试目标地址」里填一个网址。"), nil
	}
	result, err := e.fetch(ctx, target)
	if err != nil {
		translated, translateErr := e.translate(err)
		if translateErr != nil {
			return nil, translateErr
		}
		return explainable("测试抓取失败：" + translated.Content + "\n\n" + e.describeConfiguration()), nil
	}
	if result.Status < 200 || result.Status > 299 {
		return explainable("测试抓取返回非成功状态。\n\n" + renderNonSuccess(result) + "\n\n" + e.describeConfiguration()), nil
	}
	return &pluginv2.AIToolResult{Content: renderForProbe(result, e.describeConfiguration())}, nil
}

// translate 把内部错误分流成「可解释失败」或「插件故障」。
func (e *Engine) translate(err error) (*pluginv2.AIToolResult, error) {
	var failure toolFailure
	if errors.As(err, &failure) {
		return explainable(failure.message), nil
	}
	return nil, err
}

func (e *Engine) fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	if e.proxyErr != nil {
		return nil, fail("出站代理配置无效：%v。请让管理员检查插件设置里的「出站代理」。", e.proxyErr)
	}
	if e.allowlist.Empty() {
		return nil, fail("管理员还没有配置域名白名单，因此不允许抓取任何外部地址。请让管理员在插件设置里至少填写一个域名。")
	}
	target, err := checkURLShape(rawURL, e.config.AllowHTTP)
	if err != nil {
		return nil, err
	}
	if !e.allowlist.Allows(target.Hostname()) {
		return nil, fail("域名 %s 不在允许抓取的白名单里。当前白名单：%s。",
			normalizeHost(target.Hostname()), e.describeAllowlist())
	}
	// 直连时先解析一次：让「解析到内网」在发出请求之前就失败，错误信息也更清楚。
	// 走代理时这一层必然降级（DNS 由代理解析），由设置页 callout 与 README 说明。
	if e.proxy == nil {
		if _, err := resolvePublicIPs(ctx, e.resolver, normalizeHost(target.Hostname())); err != nil {
			return nil, err
		}
	}

	requestCtx, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fail("无法构造请求：%v。", err)
	}
	request.Header.Set("User-Agent", e.userAgent())
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,text/plain;q=0.7,*/*;q=0.1")
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	started := e.now()
	response, err := e.client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		var failure toolFailure
		if errors.As(err, &failure) {
			return nil, failure
		}
		return nil, fail("抓取 %s 失败：%s。", target.Redacted(), describeNetworkError(err))
	}
	defer response.Body.Close()

	if response.ContentLength > maxResponseBytes {
		return nil, fail("目标返回的内容过大（%d 字节），已跳过。", response.ContentLength)
	}

	body, err := readBounded(response.Body, e.config.MaxBytes)
	if err != nil {
		return nil, fail("读取 %s 的响应失败：%s。", target.Redacted(), describeNetworkError(err))
	}
	truncated := int64(len(body)) > e.config.MaxBytes
	if truncated {
		body = body[:e.config.MaxBytes]
	}

	contentType := response.Header.Get("Content-Type")
	text, title, err := extractText(contentType, body)
	if err != nil {
		return nil, err
	}
	return &FetchResult{
		FinalURL:    response.Request.URL.Redacted(),
		Status:      response.StatusCode,
		StatusText:  strings.TrimSpace(response.Status),
		ContentType: normalizeMediaType(contentType),
		Title:       title,
		Text:        text,
		BytesRead:   int64(len(body)),
		Truncated:   truncated,
		Elapsed:     e.now().Sub(started),
	}, nil
}

// readBounded 读取至多 limit+1 字节，多出的那一个字节用来判断是否被截断。
func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, limit+1))
}

func (e *Engine) userAgent() string {
	return fmt.Sprintf("SForum-FetchURL/%s (+https://github.com/zhuchunshu/sforum-fetch-url)", pluginVersion)
}

func (e *Engine) describeAllowlist() string {
	entries := e.allowlist.Entries()
	if len(entries) == 0 {
		return "（空）"
	}
	return strings.Join(entries, ", ")
}

// describeConfiguration 让探测结果自带上下文：运营者看到失败时，最需要知道的是
// 「当前生效的到底是哪份配置」。
func (e *Engine) describeConfiguration() string {
	proxy := "未配置（直连）"
	if e.proxy != nil {
		proxy = e.proxy.Redacted() + "（已启用）"
	} else if e.proxyErr != nil {
		proxy = "配置无效：" + e.proxyErr.Error()
	}
	httpNote := "仅 https"
	if e.config.AllowHTTP {
		httpNote = "https 与明文 http"
	}
	return strings.Join([]string{
		"当前草稿配置：",
		"- 白名单：" + e.describeAllowlist(),
		"- 代理：" + proxy,
		"- 协议：" + httpNote,
		"- 超时：" + e.config.Timeout.String(),
		fmt.Sprintf("- 响应体上限：%d 字节", e.config.MaxBytes),
	}, "\n")
}

// describeNetworkError 把 Go 的网络错误翻译成运营者与模型都能读懂的一句话。
// 原始错误串只保留在括号里：它是排查线索，不是给模型的答案。
func describeNetworkError(err error) string {
	var failure toolFailure
	if errors.As(err, &failure) {
		return failure.message
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "请求超时"
	case errors.Is(err, context.Canceled):
		return "请求被取消"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return "请求超时"
		}
		var netErr net.Error
		if errors.As(urlErr.Err, &netErr) && netErr.Timeout() {
			return "请求超时"
		}
		return fmt.Sprintf("网络错误（%s）", urlErr.Err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "请求超时"
	}
	return fmt.Sprintf("网络错误（%s）", err)
}

// renderForModel 生成给模型的正文：元信息一行行给出，正文用分隔线隔开。
// 元信息先行的原因是模型经常只需要回答「这个链接讲了什么」，状态码与最终地址
// 恰恰是它判断可信度的依据。
func renderForModel(result *FetchResult) string {
	lines := []string{
		"URL: " + result.FinalURL,
		fmt.Sprintf("HTTP: %d %s · %s", result.Status, http.StatusText(result.Status), orUnknown(result.ContentType)),
	}
	if result.Title != "" {
		lines = append(lines, "标题: "+result.Title)
	}
	if result.Truncated {
		lines = append(lines, fmt.Sprintf("已截断：仅返回前 %d 字节。", result.BytesRead))
	}
	header := strings.Join(lines, "\n") + "\n---\n"
	return clampPayload(header, result.Text)
}

// renderNonSuccess 描述一次非 2xx 的结果。它仍然是「可解释失败」：模型可以据此
// 换一个地址，或者把状态如实告诉提问者。
func renderNonSuccess(result *FetchResult) string {
	lines := []string{
		fmt.Sprintf("目标返回 HTTP %d %s。", result.Status, http.StatusText(result.Status)),
		"URL: " + result.FinalURL,
	}
	if result.Title != "" {
		lines = append(lines, "标题: "+result.Title)
	}
	excerpt := result.Text
	if len(excerpt) > 600 {
		excerpt = truncateUTF8(excerpt, 600) + "…"
	}
	if excerpt != "" {
		lines = append(lines, "---", excerpt)
	}
	return strings.Join(lines, "\n")
}

// renderForProbe 生成设置页探测的可读诊断。
func renderForProbe(result *FetchResult, configuration string) string {
	size := fmt.Sprintf("读取 %d 字节", result.BytesRead)
	if result.Truncated {
		size += "（已达上限，正文被截断）"
	}
	lines := []string{
		"测试抓取成功。",
		"- 最终地址：" + result.FinalURL,
		fmt.Sprintf("- 状态：%d %s", result.Status, http.StatusText(result.Status)),
		"- 类型：" + orUnknown(result.ContentType),
		"- 大小：" + size,
		"- 耗时：" + result.Elapsed.Round(time.Millisecond).String(),
	}
	if result.Title != "" {
		lines = append(lines, "- 标题："+result.Title)
	}
	preview := result.Text
	if preview == "" {
		preview = "（抽取到的正文为空：页面可能由 JavaScript 渲染，本插件只读静态响应体）"
	} else if len(preview) > probePreviewBytes {
		preview = truncateUTF8(preview, probePreviewBytes) + "…"
	}
	lines = append(lines, "", "正文开头：", preview, "", configuration)
	return strings.Join(lines, "\n")
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "未知类型"
	}
	return value
}

// clampPayload 把 header + 正文压进 payloadBudget 字节。
func clampPayload(header string, text string) string {
	budget := payloadBudget - len(header)
	if budget <= 0 {
		return truncateUTF8(header, payloadBudget)
	}
	if len(text) <= budget {
		return header + text
	}
	return header + truncateUTF8(text, budget) + "\n…（正文过长，已截断）"
}

// truncateUTF8 在不超过 limit 字节的前提下切到最后一个完整 rune 边界，
// 避免把一个中文字符切成半个。
func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !isUTF8Start(value[cut]) {
		cut--
	}
	return value[:cut]
}

func isUTF8Start(char byte) bool { return char&0xC0 != 0x80 }
