package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testPublicIP 是测试里假装的公网解析结果。真正的 httptest 监听在 127.0.0.1，
// 通过替换 dial 把逻辑主机名接到本机服务上——这样测试走的是完整真实路径
// （主机守卫 → 白名单 → 重定向复校验 → 抽取），而生产代码里没有任何
// 「跳过守卫」的开关。
const testPublicIP = "93.184.216.34"

func newTestEngine(t *testing.T, server *httptest.Server, config Config) *Engine {
	t.Helper()
	engine := NewEngine(config)
	engine.resolver = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP(testPublicIP)}, nil
	}
	engine.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: 2 * time.Second}
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	return engine
}

func testConfig(allowedDomains string) Config {
	config := DefaultConfig()
	config.AllowedDomains = allowedDomains
	config.AllowHTTP = true
	config.Timeout = 3 * time.Second
	return config
}

func TestFetchReturnsExtractedText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<html><head><title>示例页</title></head><body><h1>标题</h1><p>正文内容</p><script>bad()</script></body></html>`)
	}))
	defer server.Close()

	engine := newTestEngine(t, server, testConfig("example.com"))
	result, err := engine.RunModel(context.Background(), "http://example.com/page")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool failure: %s", result.Content)
	}
	for _, expected := range []string{"http://example.com/page", "HTTP: 200 OK", "text/html", "标题: 示例页", "正文内容"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("content missing %q:\n%s", expected, result.Content)
		}
	}
	if strings.Contains(result.Content, "bad()") {
		t.Fatalf("script content leaked into the model payload:\n%s", result.Content)
	}
}

func TestFetchDeniesEverythingWithoutAnAllowlist(t *testing.T) {
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), testConfig(""))
	result, err := engine.RunModel(context.Background(), "http://example.com/")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "白名单") {
		t.Fatalf("empty allowlist did not produce an explainable denial: %+v", result)
	}
}

func TestFetchDeniesHostOutsideAllowlist(t *testing.T) {
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), testConfig("example.com"))
	result, err := engine.RunModel(context.Background(), "http://other.org/page")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError {
		t.Fatalf("non-allowlisted host was fetched: %+v", result)
	}
	if !strings.Contains(result.Content, "other.org") || !strings.Contains(result.Content, "example.com") {
		t.Fatalf("denial should name both the target and the allowlist: %s", result.Content)
	}
}

func TestFetchBlocksPrivateTargetEvenWhenAllowlisted(t *testing.T) {
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), testConfig("127.0.0.1\nlocalhost"))
	for _, target := range []string{"http://127.0.0.1/", "http://localhost/", "http://169.254.169.254/latest/meta-data/"} {
		result, err := engine.RunModel(context.Background(), target)
		if err != nil {
			t.Fatalf("RunModel(%s): %v", target, err)
		}
		if !result.IsError {
			t.Fatalf("private target %s was fetched: %+v", target, result)
		}
	}
}

func TestFetchRevalidatesRedirectTarget(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "http://evil.org/landing", http.StatusFound)
			return
		}
		fmt.Fprint(writer, "should never be reached")
	}))
	defer server.Close()

	engine := newTestEngine(t, server, testConfig("example.com"))
	result, err := engine.RunModel(context.Background(), "http://example.com/start")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError {
		t.Fatalf("redirect to a non-allowlisted host was followed: %+v", result)
	}
	if !strings.Contains(result.Content, "evil.org") {
		t.Fatalf("failure should name the redirect target: %s", result.Content)
	}
}

func TestFetchFollowsRedirectInsideTheAllowlist(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "http://sub.example.com/next", http.StatusFound)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(writer, "redirected body")
	}))
	defer server.Close()

	engine := newTestEngine(t, server, testConfig("example.com"))
	result, err := engine.RunModel(context.Background(), "http://example.com/start")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if result.IsError {
		t.Fatalf("allowed redirect failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "sub.example.com/next") || !strings.Contains(result.Content, "redirected body") {
		t.Fatalf("final url or body missing:\n%s", result.Content)
	}
}

func TestFetchStopsAfterTooManyRedirects(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://example.com/loop", http.StatusFound)
	}))
	defer server.Close()

	engine := newTestEngine(t, server, testConfig("example.com"))
	result, err := engine.RunModel(context.Background(), "http://example.com/loop")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "重定向") {
		t.Fatalf("redirect loop was not reported: %+v", result)
	}
}

func TestFetchReportsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusNotFound)
		fmt.Fprint(writer, "<html><body>not here</body></html>")
	}))
	defer server.Close()

	engine := newTestEngine(t, server, testConfig("example.com"))
	result, err := engine.RunModel(context.Background(), "http://example.com/missing")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "404") {
		t.Fatalf("non-2xx was not reported: %+v", result)
	}
	if !strings.Contains(result.Content, "not here") {
		t.Fatalf("error page body was dropped: %s", result.Content)
	}
}

func TestFetchHonoursTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		fmt.Fprint(writer, "late")
	}))
	defer server.Close()

	config := testConfig("example.com")
	config.Timeout = 200 * time.Millisecond
	engine := newTestEngine(t, server, config)
	result, err := engine.RunModel(context.Background(), "http://example.com/slow")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "超时") {
		t.Fatalf("timeout was not reported: %+v", result)
	}
}

func TestFetchTruncatesOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(writer, strings.Repeat("字", 20000))
	}))
	defer server.Close()

	config := testConfig("example.com")
	config.MaxBytes = 4096
	engine := newTestEngine(t, server, config)
	result, err := engine.RunModel(context.Background(), "http://example.com/big")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected failure: %s", result.Content)
	}
	if !strings.Contains(result.Content, "已截断") {
		t.Fatalf("truncation was not disclosed:\n%s", result.Content)
	}
	if len(result.Content) > payloadBudget {
		t.Fatalf("payload %d bytes exceeds the %d byte budget", len(result.Content), payloadBudget)
	}
	// 宿主 v3.0.20 的单条工具结果截断线是 64000 字节；插件预算必须留在它下面，
	// 否则宿主会在结构中间切一刀。
	if len(result.Content) >= 64000 {
		t.Fatalf("payload must stay under the host's 64000 byte truncation point, got %d", len(result.Content))
	}
}

func TestFetchRejectsBinaryResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/pdf")
		writer.Write([]byte("%PDF-1.4"))
	}))
	defer server.Close()

	engine := newTestEngine(t, server, testConfig("example.com"))
	result, err := engine.RunModel(context.Background(), "http://example.com/file.pdf")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "application/pdf") {
		t.Fatalf("binary response was not rejected with its type: %+v", result)
	}
}

func TestFetchReportsInvalidProxyConfiguration(t *testing.T) {
	config := testConfig("example.com")
	config.ProxyURL = "ftp://127.0.0.1:21"
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), config)
	result, err := engine.RunModel(context.Background(), "http://example.com/")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "代理") {
		t.Fatalf("invalid proxy was not reported: %+v", result)
	}
}

func TestParseProxyURLAcceptsSupportedSchemes(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:7897", "https://proxy.example.com:8443", "socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080"} {
		if _, err := parseProxyURL(raw); err != nil {
			t.Errorf("parseProxyURL(%q) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []string{"ftp://127.0.0.1:21", "127.0.0.1:7897", "http://user:pass@127.0.0.1:7897", "http://"} {
		if _, err := parseProxyURL(raw); err == nil {
			t.Errorf("parseProxyURL(%q) accepted an unsupported proxy", raw)
		}
	}
}

func TestRunModelRespectsTheEnabledSwitch(t *testing.T) {
	config := testConfig("example.com")
	config.Enabled = false
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), config)
	result, err := engine.RunModel(context.Background(), "http://example.com/")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "关闭") {
		t.Fatalf("disabled switch did not short-circuit: %+v", result)
	}
}

func TestRunProbeIgnoresTheEnabledSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(writer, "probe body")
	}))
	defer server.Close()

	config := testConfig("example.com")
	config.Enabled = false
	config.ProbeURL = "http://example.com/"
	engine := newTestEngine(t, server, config)

	result, err := engine.RunProbe(context.Background())
	if err != nil {
		t.Fatalf("RunProbe: %v", err)
	}
	if result.IsError {
		t.Fatalf("probe failed while disabled: %s", result.Content)
	}
	for _, expected := range []string{"测试抓取成功", "example.com", "当前草稿配置", "probe body"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("probe output missing %q:\n%s", expected, result.Content)
		}
	}
}

// TestDescribeConfigurationReportsTheEffectiveDraft 检查探测结果自带的上下文。
// 它不发起任何请求：运营者看到失败时最需要知道的是「当前生效的到底是哪份配置」，
// 而这份说明本身必须是纯函数。
func TestDescribeConfigurationReportsTheEffectiveDraft(t *testing.T) {
	plain := testConfig("example.com\ndocs.example.com")
	plain.AllowHTTP = false
	direct := NewEngine(plain)
	description := direct.describeConfiguration()
	for _, expected := range []string{"example.com", "docs.example.com", "未配置（直连）", "仅 https", "3s", "524288"} {
		if !strings.Contains(description, expected) {
			t.Fatalf("description missing %q:\n%s", expected, description)
		}
	}

	config := testConfig("example.com")
	config.ProxyURL = "socks5://127.0.0.1:1080"
	config.AllowHTTP = true
	proxied := NewEngine(config)
	description = proxied.describeConfiguration()
	for _, expected := range []string{"socks5://127.0.0.1:1080（已启用）", "https 与明文 http"} {
		if !strings.Contains(description, expected) {
			t.Fatalf("proxied description missing %q:\n%s", expected, description)
		}
	}

	broken := NewEngine(func() Config {
		cfg := testConfig("example.com")
		cfg.ProxyURL = "ftp://127.0.0.1:21"
		return cfg
	}())
	if !strings.Contains(broken.describeConfiguration(), "配置无效") {
		t.Fatalf("invalid proxy was not reported:\n%s", broken.describeConfiguration())
	}
}

func TestRunProbeExplainsFailureWithConfiguration(t *testing.T) {
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), testConfig("example.com"))
	engine.config.ProbeURL = "http://outside.org/"

	result, err := engine.RunProbe(context.Background())
	if err != nil {
		t.Fatalf("RunProbe: %v", err)
	}
	if !result.IsError {
		t.Fatalf("probe should fail for a non-allowlisted target: %+v", result)
	}
	for _, expected := range []string{"测试抓取失败", "outside.org", "当前草稿配置", "白名单"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("probe failure missing %q:\n%s", expected, result.Content)
		}
	}
}

func TestRunProbeRequiresATarget(t *testing.T) {
	config := testConfig("example.com")
	config.ProbeURL = ""
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), config)
	result, err := engine.RunProbe(context.Background())
	if err != nil {
		t.Fatalf("RunProbe: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "测试目标地址") {
		t.Fatalf("empty probe target was not reported: %+v", result)
	}
}

func TestFetchSurfacesResolverFailures(t *testing.T) {
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), testConfig("example.com"))
	engine.resolver = func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("dns exploded")
	}
	result, err := engine.RunModel(context.Background(), "http://example.com/")
	if err != nil {
		t.Fatalf("RunModel: %v", err)
	}
	if !result.IsError {
		t.Fatalf("resolver failure was swallowed: %+v", result)
	}
}

// TestInternalErrorsStayInternal 钉死「插件内部故障不会借模型之口发布」：
// 非 toolFailure 的错误必须原样返回给宿主（宿主只给模型一句通用说明），
// 而不是被包装成工具结果。
func TestInternalErrorsStayInternal(t *testing.T) {
	engine := &Engine{config: testConfig("example.com")}
	engine.allowlist = ParseAllowlist("example.com")
	engine.resolver = systemResolver
	engine.dial = engine.dialDirect
	engine.now = time.Now

	_, err := engine.translate(errors.New("boom: internal detail"))
	if err == nil {
		t.Fatalf("internal error was converted into a tool result")
	}
	if _, translateErr := engine.translate(fail("explainable")); translateErr != nil {
		t.Fatalf("explainable failure was escalated: %v", translateErr)
	}
}

func TestMaxResponseCeilingExceedsTheConfigurableReadLimit(t *testing.T) {
	if maxResponseBytes <= maxMaxBytes {
		t.Fatalf("maxResponseBytes %d must exceed the configurable limit %d", maxResponseBytes, maxMaxBytes)
	}
}
