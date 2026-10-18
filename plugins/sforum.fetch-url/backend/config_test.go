package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLoadConfigUsesSafeDefaultsWhenNothingIsSaved(t *testing.T) {
	config := LoadConfig(func(string) string { return "" })

	if !config.Enabled {
		t.Fatalf("enabled default = %v, want true", config.Enabled)
	}
	if config.AllowedDomains != "" {
		t.Fatalf("allowed_domains default = %q, want empty (deny everything)", config.AllowedDomains)
	}
	if config.AllowHTTP {
		t.Fatalf("allow_http default = true, want false")
	}
	if config.ProxyURL != "" {
		t.Fatalf("proxy_url default = %q, want empty (direct)", config.ProxyURL)
	}
	if config.Timeout != 4*time.Second {
		t.Fatalf("timeout default = %s, want 4s", config.Timeout)
	}
	if config.MaxBytes != 512*1024 {
		t.Fatalf("max_bytes default = %d, want 524288", config.MaxBytes)
	}
	if config.ProbeURL != "https://example.com/" {
		t.Fatalf("probe_url default = %q", config.ProbeURL)
	}
}

func TestLoadConfigReadsInjectedSettings(t *testing.T) {
	env := map[string]string{
		"SFORUM_SETTING_ENABLED":         "false",
		"SFORUM_SETTING_ALLOWED_DOMAINS": "example.com\n# 注释\n*.wikipedia.org",
		"SFORUM_SETTING_ALLOW_HTTP":      "true",
		"SFORUM_SETTING_PROXY_URL":       " socks5://127.0.0.1:1080 ",
		"SFORUM_SETTING_TIMEOUT_MS":      "2500",
		"SFORUM_SETTING_MAX_BYTES":       "65536",
		"SFORUM_SETTING_PROBE_URL":       "https://example.org/",
	}
	config := LoadConfig(func(key string) string { return env[key] })

	if config.Enabled {
		t.Fatalf("enabled = true, want false")
	}
	if config.AllowedDomains != "example.com\n# 注释\n*.wikipedia.org" {
		t.Fatalf("allowed_domains = %q", config.AllowedDomains)
	}
	if !config.AllowHTTP {
		t.Fatalf("allow_http = false, want true")
	}
	if config.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy_url = %q, want trimmed value", config.ProxyURL)
	}
	if config.Timeout != 2500*time.Millisecond {
		t.Fatalf("timeout = %s, want 2.5s", config.Timeout)
	}
	if config.MaxBytes != 65536 {
		t.Fatalf("max_bytes = %d, want 65536", config.MaxBytes)
	}
	if config.ProbeURL != "https://example.org/" {
		t.Fatalf("probe_url = %q", config.ProbeURL)
	}
}

func TestLoadConfigClampsOutOfRangeNumbers(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		wantTime  time.Duration
		wantBytes int64
	}{
		{
			name:      "too small",
			env:       map[string]string{"SFORUM_SETTING_TIMEOUT_MS": "1", "SFORUM_SETTING_MAX_BYTES": "1"},
			wantTime:  minTimeoutMS * time.Millisecond,
			wantBytes: minMaxBytes,
		},
		{
			name:      "too large",
			env:       map[string]string{"SFORUM_SETTING_TIMEOUT_MS": "60000", "SFORUM_SETTING_MAX_BYTES": "99999999"},
			wantTime:  maxTimeoutMS * time.Millisecond,
			wantBytes: maxMaxBytes,
		},
		{
			name:      "not a number falls back to default",
			env:       map[string]string{"SFORUM_SETTING_TIMEOUT_MS": "soon", "SFORUM_SETTING_MAX_BYTES": "lots"},
			wantTime:  defaultTimeoutMS * time.Millisecond,
			wantBytes: defaultMaxBytes,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := LoadConfig(func(key string) string { return testCase.env[key] })
			if config.Timeout != testCase.wantTime {
				t.Fatalf("timeout = %s, want %s", config.Timeout, testCase.wantTime)
			}
			if config.MaxBytes != testCase.wantBytes {
				t.Fatalf("max_bytes = %d, want %d", config.MaxBytes, testCase.wantBytes)
			}
		})
	}
}

// TestTimeoutStaysUnderDeclaredToolTimeout 钉死一个很容易被改坏的边界：
// 插件自己的抓取预算必须严格小于清单声明的 timeoutMs，否则宿主会在插件还
// 没来得及返回结果时就掐掉整次调用，运营者看到的会是「工具无响应」而不是
// 一条可解释的超时说明。
func TestTimeoutStaysUnderDeclaredToolTimeout(t *testing.T) {
	if maxTimeoutMS >= declaredTimeoutMS {
		t.Fatalf("max timeout %dms must stay under declared tool timeout %dms", maxTimeoutMS, declaredTimeoutMS)
	}
}

func TestSettingEnvNameMatchesHostMapping(t *testing.T) {
	cases := map[string]string{
		"enabled":         "SFORUM_SETTING_ENABLED",
		"allowed_domains": "SFORUM_SETTING_ALLOWED_DOMAINS",
		"allow_http":      "SFORUM_SETTING_ALLOW_HTTP",
		"proxy_url":       "SFORUM_SETTING_PROXY_URL",
		"timeout_ms":      "SFORUM_SETTING_TIMEOUT_MS",
		"max_bytes":       "SFORUM_SETTING_MAX_BYTES",
		"probe_url":       "SFORUM_SETTING_PROBE_URL",
	}
	for key, want := range cases {
		if got := settingEnvName(key); got != want {
			t.Fatalf("settingEnvName(%q) = %q, want %q", key, got, want)
		}
	}
}

// TestManifestDefaultsMatchRuntimeDefaults 是跨文件的漂移钉死：清单里的 default
// 是「运营者什么都没改」时宿主展示的值，代码里的默认值是「宿主没注入任何设置」
// 时实际生效的值。两者分叉过一次就再也不会被发现，所以用测试钉住。
func TestManifestDefaultsMatchRuntimeDefaults(t *testing.T) {
	path := filepath.Join("..", "manifest", "settings.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document struct {
		Fields []struct {
			Key     string `json:"key"`
			Default string `json:"default"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	defaults := DefaultConfig()
	want := map[string]string{
		"enabled":         strconv.FormatBool(defaults.Enabled),
		"allowed_domains": defaults.AllowedDomains,
		"allow_http":      strconv.FormatBool(defaults.AllowHTTP),
		"proxy_url":       defaults.ProxyURL,
		"timeout_ms":      strconv.FormatInt(defaults.Timeout.Milliseconds(), 10),
		"max_bytes":       strconv.FormatInt(defaults.MaxBytes, 10),
		"probe_url":       defaults.ProbeURL,
	}
	seen := map[string]bool{}
	for _, field := range document.Fields {
		expected, ok := want[field.Key]
		if !ok {
			continue
		}
		seen[field.Key] = true
		if field.Default != expected {
			t.Errorf("manifest default for %q = %q, runtime default = %q", field.Key, field.Default, expected)
		}
	}
	for key := range want {
		if !seen[key] {
			t.Errorf("manifest/settings.json no longer declares %q, but the runtime reads it", key)
		}
	}
}
