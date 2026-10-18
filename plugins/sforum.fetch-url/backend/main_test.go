package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginv2 "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2"
)

func callTool(t *testing.T, handler pluginv2.AIToolHandler, arguments string) *pluginv2.AIToolResult {
	t.Helper()
	result, err := handler(context.Background(), &pluginv2.AIToolCall{Arguments: arguments})
	if err != nil {
		t.Fatalf("tool handler returned an internal error: %v", err)
	}
	if result == nil {
		t.Fatalf("tool handler returned no result")
	}
	return result
}

func TestFetchHandlerRejectsMalformedArguments(t *testing.T) {
	engine := newTestEngine(t, httptest.NewServer(http.NotFoundHandler()), testConfig("example.com"))
	handler := fetchHandler(engine)

	for _, arguments := range []string{``, `not json`, `{"url": 42}`, `{"url": ""}`, `{"url": "   "}`} {
		result := callTool(t, handler, arguments)
		if !result.IsError {
			t.Errorf("arguments %q were accepted: %+v", arguments, result)
		}
	}
}

func TestFetchHandlerForwardsToTheEngine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(writer, "handler body")
	}))
	defer server.Close()

	engine := newTestEngine(t, server, testConfig("example.com"))
	result := callTool(t, fetchHandler(engine), `{"url": "http://example.com/"}`)
	if result.IsError || !strings.Contains(result.Content, "handler body") {
		t.Fatalf("handler did not reach the engine: %+v", result)
	}
}

func TestProbeHandlerUsesProcessConfigurationNotArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(writer, "probe body")
	}))
	defer server.Close()

	config := testConfig("example.com")
	config.ProbeURL = "http://example.com/"
	engine := newTestEngine(t, server, config)

	// 参数里放一个不该被采用的地址：探测目标只能来自本进程（即草稿）配置。
	result := callTool(t, probeHandler(engine), `{"probe_url": "http://evil.org/"}`)
	if result.IsError {
		t.Fatalf("probe failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "probe body") {
		t.Fatalf("probe did not use the process configuration:\n%s", result.Content)
	}
}

// TestDeclarationsMatchManifest 是清单与代码之间的漂移钉死。
// 宿主按清单里的 id/name/timeoutMs 派发与计时，插件按同样的 id 解析 handler；
// 两边分叉的表现是「模型调用了工具但永远失败」，非常难排查，所以用测试钉住。
func TestDeclarationsMatchManifest(t *testing.T) {
	path := filepath.Join("..", "sforum.extension.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var manifest struct {
		ID      string `json:"id"`
		Backend struct {
			Entry           string `json:"entry"`
			RPC             string `json:"rpc"`
			ProtocolVersion int    `json:"protocolVersion"`
		} `json:"backend"`
		AITools []struct {
			ID              string          `json:"id"`
			ContractVersion string          `json:"contractVersion"`
			Name            string          `json:"name"`
			Description     string          `json:"description"`
			Parameters      json.RawMessage `json:"parameters"`
			Handler         string          `json:"handler"`
			TimeoutMS       int             `json:"timeoutMs"`
		} `json:"aiTools"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if manifest.Backend.RPC != "hashicorp-go-plugin" || manifest.Backend.ProtocolVersion != 2 {
		t.Fatalf("manifest backend must be Protocol V2 over hashicorp-go-plugin: %+v", manifest.Backend)
	}

	definitions := toolDefinitions(NewEngine(DefaultConfig()))
	if len(definitions) != len(manifest.AITools) {
		t.Fatalf("code declares %d tools, manifest declares %d", len(definitions), len(manifest.AITools))
	}
	byID := map[string]pluginv2.AIToolDefinition{}
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}
	for _, declared := range manifest.AITools {
		definition, ok := byID[declared.ID]
		if !ok {
			t.Errorf("manifest declares %q but the code has no such tool", declared.ID)
			continue
		}
		if definition.Name != declared.Name {
			t.Errorf("%s: code name %q, manifest name %q", declared.ID, definition.Name, declared.Name)
		}
		if definition.TimeoutMS != declared.TimeoutMS {
			t.Errorf("%s: code timeout %dms, manifest timeout %dms", declared.ID, definition.TimeoutMS, declared.TimeoutMS)
		}
		if definition.ContractVersion != declared.ContractVersion {
			t.Errorf("%s: code contract %q, manifest contract %q", declared.ID, definition.ContractVersion, declared.ContractVersion)
		}
		if definition.Description != declared.Description {
			t.Errorf("%s: code description drifted from the manifest", declared.ID)
		}
		// handler 必须与 id 同名：宿主按 id 派发，插件按 id 解析，两边用的是同一个键。
		if declared.Handler != declared.ID {
			t.Errorf("%s: manifest handler %q must equal the declaration id", declared.ID, declared.Handler)
		}
		if !strings.HasPrefix(declared.ID, manifest.ID+".") {
			t.Errorf("%s: declaration id must be namespaced under %q", declared.ID, manifest.ID)
		}
		if strings.TrimSpace(declared.Description) == "" {
			t.Errorf("%s: manifest description is required", declared.ID)
		}
		if len(declared.Parameters) == 0 {
			t.Errorf("%s: manifest parameters are required", declared.ID)
		}
	}
}

func TestFetchToolParametersStayUnderHostLimits(t *testing.T) {
	limits := struct {
		schema      int
		description int
	}{schema: 8192, description: 1024}
	if len(fetchToolParameters) > limits.schema || len(probeToolParameters) > limits.schema {
		t.Fatalf("tool schema exceeds the host limit of %d bytes", limits.schema)
	}
	if len(fetchToolDescription) > limits.description || len(probeToolDescription) > limits.description {
		t.Fatalf("tool description exceeds the host limit of %d bytes", limits.description)
	}
	if declaredTimeoutMS <= 0 || declaredTimeoutMS > 5000 {
		t.Fatalf("declared timeout %dms is outside the host's 1..5000 range", declaredTimeoutMS)
	}
}
