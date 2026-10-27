package main

import (
	"strconv"
	"strings"
	"time"
)

// Config 是插件运行期配置。
//
// 来源只有一个：宿主在启动插件子进程时把已保存的设置注入成 SFORUM_SETTING_*
// 环境变量（host: Support/Extensions/protocol.go 的 pluginSettingEnvName）。
// 从未保存过的设置不会被注入，所以下面的默认值必须与 manifest/settings.json 的
// default 一致——否则「运营者什么都没改」和「运营者点了重置」会得到两种不同的
// 行为。这个一致性由 manifest_drift_test.go 钉死。
//
// allowed_domains 是唯一例外：它的 default 为空（拒绝一切抓取），recommendedValue
// 是一份供运营者改成自己站点的示例域名列表。
//
// 设置保存会让宿主重启本插件的精确制品，因此改完设置不需要重启 API。
type Config struct {
	Enabled        bool
	AllowedDomains string
	AllowHTTP      bool
	ProxyURL       string
	Timeout        time.Duration
	MaxBytes       int64
	ProbeURL       string
}

const (
	// 单次抓取总预算。宿主 v3.0.20 的 AIToolMaximumTimeoutMS 是 30000，清单按
	// 上限声明，插件内部上限留 1000 毫秒给封装、序列化与返回：宿主拿声明的
	// timeoutMs 当调用截止时间，预算打满声明值就一定会被先掐断。
	minTimeoutMS     = 1000
	maxTimeoutMS     = 29000
	defaultTimeoutMS = 4000

	minMaxBytes     = 16 * 1024
	maxMaxBytes     = 1024 * 1024
	defaultMaxBytes = 512 * 1024

	defaultProbeURL = "https://example.com/"
)

// settingsEnvPrefix 与宿主的 pluginSettingEnvName 同源：SFORUM_SETTING_ +
// 大写的 key，非 [A-Z0-9] 字符替换成下划线。
const settingsEnvPrefix = "SFORUM_SETTING_"

// DefaultConfig 返回「什么都没配」时的安全默认：开关打开、白名单为空。
// 白名单为空意味着拒绝一切抓取——这是刻意的，本插件没有「全部放行」模式。
func DefaultConfig() Config {
	return Config{
		Enabled:        true,
		AllowedDomains: "",
		AllowHTTP:      false,
		ProxyURL:       "",
		Timeout:        defaultTimeoutMS * time.Millisecond,
		MaxBytes:       defaultMaxBytes,
		ProbeURL:       defaultProbeURL,
	}
}

// LoadConfig 从环境变量装载配置，缺项与非法项一律回落到默认值。
//
// 「非法值回落而不是报错」是有意的：一个写错的数字不该让工具变成不可用，
// 而应该让它按默认值继续工作——运营者在设置页看到的说明里写明了取值范围。
func LoadConfig(getenv func(string) string) Config {
	config := DefaultConfig()
	if getenv == nil {
		return config
	}
	if raw, ok := readSetting(getenv, "enabled"); ok {
		config.Enabled = parseBool(raw, config.Enabled)
	}
	if raw, ok := readSetting(getenv, "allowed_domains"); ok {
		config.AllowedDomains = raw
	}
	if raw, ok := readSetting(getenv, "allow_http"); ok {
		config.AllowHTTP = parseBool(raw, config.AllowHTTP)
	}
	if raw, ok := readSetting(getenv, "proxy_url"); ok {
		config.ProxyURL = strings.TrimSpace(raw)
	}
	if raw, ok := readSetting(getenv, "timeout_ms"); ok {
		config.Timeout = time.Duration(clampInt(parseInt(raw, defaultTimeoutMS), minTimeoutMS, maxTimeoutMS)) * time.Millisecond
	}
	if raw, ok := readSetting(getenv, "max_bytes"); ok {
		config.MaxBytes = int64(clampInt(parseInt(raw, defaultMaxBytes), minMaxBytes, maxMaxBytes))
	}
	if raw, ok := readSetting(getenv, "probe_url"); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			config.ProbeURL = trimmed
		}
	}
	return config
}

// settingEnvName 复刻宿主的键名映射，供测试钉死两边的约定不漂移。
func settingEnvName(key string) string {
	var builder strings.Builder
	builder.WriteString(settingsEnvPrefix)
	for _, char := range strings.ToUpper(strings.TrimSpace(key)) {
		switch {
		case char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func readSetting(getenv func(string) string, key string) (string, bool) {
	value := getenv(settingEnvName(key))
	// 宿主对「从未保存」与「保存为空串」注入的都是空值，两者在这里无法区分。
	// 这不影响任何字段的语义：每个字段的默认值都与空值等价，唯一的例外是
	// probe_url（空值回落成默认目标地址），它由 handler 自己再校验一次。
	if value == "" {
		return "", false
	}
	return value, true
}

func parseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
