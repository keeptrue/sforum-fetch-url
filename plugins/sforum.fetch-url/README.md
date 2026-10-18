# sforum.fetch-url

SForum 扩展包：给 AI 回复一个 `fetch-url` 工具，让它能读到运营者白名单内页面的正文。

面向使用者的完整说明（安装、设置、安全边界、已知降级）见仓库根 `README.md`；
宿主约束速查与实测证据见仓库根 `AGENTS.md` 与 `NOTES.md`。

## 包内结构

```text
sforum.extension.json      Manifest V3：两条 aiTools[] 声明 + 设置页 + backend
manifest/settings.json     设置文档（schemaVersion 1，Schema UI）
manifest/langs/            zh-CN / en-US 的扩展名与描述
backend/                   Protocol V2 服务（独立 Go module）
```

## 后端文件

| 文件 | 职责 |
| --- | --- |
| `main.go` | 两条 AI 工具声明与 handler；配置装载入口 |
| `config.go` | 从 `SFORUM_SETTING_*` 环境变量装载配置，含默认值与 clamp |
| `allowlist.go` | 域名白名单解析与匹配（主机名 + 子域继承 + `*.` 通配） |
| `guard.go` | 主机守卫：字面量层 + 解析层，不进入设置页 |
| `extract.go` | 响应体 → 纯文本（剥标签、解实体、压缩空白、字符集解码） |
| `fetch.go` | 抓取引擎：重定向逐跳复校验、代理、体积上限、失败分类 |
| `*_test.go` | 单测；`httptest` 路径通过注入 resolver/dial 走完整真实流程 |

## 两条 AI 工具

| id / handler | 模型可见名 | 用途 |
| --- | --- | --- |
| `sforum.fetch-url.tool.fetch_url` | `fetch-url` | 给模型用 |
| `sforum.fetch-url.tool.probe_fetch` | `sforum-fetch-url-probe` | 只给设置页「测试抓取」按钮用 |

第二条被登记但永不进入用途白名单，因此模型看不到它。`main_test.go` 的
`TestDeclarationsMatchManifest` 钉死清单与代码之间的 id / name / timeout / 描述一致。

## 快速验证

```bash
cd backend && go test ./...
cd ../../.. && ./scripts/prepare-package.sh
```
