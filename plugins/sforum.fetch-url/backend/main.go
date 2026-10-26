package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

// 本插件贡献两个 AI 工具声明，与 sforum.extension.json 的 aiTools[] 一一对应：
//
//   - fetch-url 给模型用；
//   - sforum-fetch-url-probe 只给设置页的「测试抓取」按钮用（Core 的
//     ai_tool_probe settings action）。它永远不进任何用途白名单，因此模型看不到它。
//
// 声明是静态事实：宿主在插件进程启动前就知道这些工具存在，派发只认 id。
const (
	toolIDFetch = "sforum.fetch-url.tool.fetch_url"
	toolIDProbe = "sforum.fetch-url.tool.probe_fetch"

	toolNameFetch = "fetch-url"
	toolNameProbe = "sforum-fetch-url-probe"

	// declaredTimeoutMS 必须与 manifest 的 timeoutMs 逐字一致：宿主会拿清单里
	// 的数字作为每次调用的截止时间，并在派发前校验它没有漂移。
	declaredTimeoutMS = 5000
)

const fetchToolDescription = "抓取一个外部网页并返回它的正文纯文本。当用户给出链接、或需要引用站外页面内容时使用。" +
	"只有运营者已把该域名加入白名单的网址才能抓取；不在白名单、非文本响应或超时都会返回一条可解释的说明。"

const probeToolDescription = "仅供管理员在插件设置页执行「测试抓取」使用，模型不应调用它。" +
	"它用设置页当前草稿值（白名单、代理、超时等）对 probe_url 做一次真实抓取并返回诊断信息。"

// fetchToolParameters 是模型看到的参数 schema。宿主只校验它是合法 JSON 对象、
// 从不重新解释它，因此这份声明就是模型与插件之间唯一的参数契约。
var fetchToolParameters = mustCompactJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"url": map[string]any{
			"type":        "string",
			"description": "要抓取的完整 http(s) URL，例如 https://example.com/docs/intro。",
		},
	},
	"required": []string{"url"},
})

// probeToolParameters 与设置页草稿字段同名。草稿值全部以字符串形态存在
// （extension_settings 存的就是字符串），所以这里一律声明为 string。
var probeToolParameters = mustCompactJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"allowed_domains": map[string]any{"type": "string", "description": "设置页草稿中的域名白名单原文。"},
		"allow_http":      map[string]any{"type": "string", "description": "设置页草稿中的「允许明文 http」值。"},
		"max_bytes":       map[string]any{"type": "string", "description": "设置页草稿中的响应体读取上限。"},
		"timeout_ms":      map[string]any{"type": "string", "description": "设置页草稿中的单次抓取超时。"},
		"proxy_url":       map[string]any{"type": "string", "description": "设置页草稿中的出站代理地址。"},
		"probe_url":       map[string]any{"type": "string", "description": "设置页草稿中的测试目标地址。"},
	},
})

func main() {
	registry, err := pluginv2.NewAIToolRegistry(toolDefinitions(NewEngine(LoadConfig(os.Getenv)))...)
	if err != nil {
		log.Fatalf("configure sforum.fetch-url: %v", err)
	}
	server := pluginv2.NewServer().WithAIToolRegistry(registry)
	// 声明 lifecycle（manifest lifecycle.*）后宿主会走 V2 升级路径,
	// dev auto 才能自动 promote;步骤实现见 lifecycle.go。
	server.WithRuntimeStreams(pluginv2.RuntimeStreams{Lifecycle: runLifecycle})
	pluginv2.Serve(server)
}

// toolDefinitions 把两条声明集中在一处，让 main 与测试看到同一份事实
// （declarations_test.go 会拿它与 sforum.extension.json 对照）。
func toolDefinitions(engine *Engine) []pluginv2.AIToolDefinition {
	return []pluginv2.AIToolDefinition{
		{
			ID: toolIDFetch, ContractVersion: toolIDFetch + "@1",
			Name: toolNameFetch, Description: fetchToolDescription,
			Parameters: fetchToolParameters, TimeoutMS: declaredTimeoutMS,
			Execute: fetchHandler(engine),
		},
		{
			ID: toolIDProbe, ContractVersion: toolIDProbe + "@1",
			Name: toolNameProbe, Description: probeToolDescription,
			Parameters: probeToolParameters, TimeoutMS: declaredTimeoutMS,
			Execute: probeHandler(engine),
		},
	}
}

// fetchHandler 是模型-facing 的 fetch-url：参数只有一个 url，配置来自本进程环境。
func fetchHandler(engine *Engine) pluginv2.AIToolHandler {
	return func(ctx context.Context, call *pluginv2.AIToolCall) (*pluginv2.AIToolResult, error) {
		var arguments struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
			return explainable("参数必须是 JSON 对象，且包含字符串字段 url。"), nil
		}
		if strings.TrimSpace(arguments.URL) == "" {
			return explainable("缺少必填参数 url。请给出完整的 http(s) 地址。"), nil
		}
		return engine.RunModel(ctx, strings.TrimSpace(arguments.URL))
	}
}

// probeHandler 是设置页探测入口。宿主用草稿值启动一个短命进程，所以本进程的
// 环境里就是运营者正在编辑的那份配置——工具参数只是同一份事实的冗余副本，
// 因此这里不读参数，只读本进程配置。
func probeHandler(engine *Engine) pluginv2.AIToolHandler {
	return func(ctx context.Context, _ *pluginv2.AIToolCall) (*pluginv2.AIToolResult, error) {
		return engine.RunProbe(ctx)
	}
}

// explainable 构造「工具可解释的失败」：模型会读到 Content 并可能换一种方式重试。
// 它与 handler 返回 Go error 是两回事——后者表示插件内部故障，宿主只给模型一句
// 通用说明，插件内部错误串永远不会借模型之口发布到社区。
func explainable(message string) *pluginv2.AIToolResult {
	return &pluginv2.AIToolResult{Content: message, IsError: true}
}

func mustCompactJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode AI tool parameters: %v", err))
	}
	return string(encoded)
}
