# SForum AI URL Fetch

SForum 的 AI 网页抓取插件：给模型一个 `fetch-url` 工具，让它能读到运营者白名单内
页面的正文。外部扩展包，不属于 SForum 内置插件。

- 仓库：`~/Code/github/sforum-fetch-url`（独立仓库，仓库根即 SForum 的外部扩展源根）
- 框架检出：`~/Code/github/SForum`（**同级目录**，插件通过本地 `replace` 解析宿主 SDK）
- 扩展 id：`sforum.fetch-url`；版本在 `plugins/sforum.fetch-url/sforum.extension.json`
- 面向运营者的说明见仓库根 `README.md`

## 目录布局

```text
sforum-fetch-url/
├── plugins/sforum.fetch-url/          ← 扩展包：manifest、settings、langs、backend
├── scripts/prepare-package.sh         ← build → digest --write → validate → test
├── README.md  NOTES.md  AGENTS.md
└── .gitignore                         ← 忽略 backend/plugin
```

宿主扫描规则是 `<EXTERNAL_EXTENSION_ROOTS>/plugins/<包目录>`，**同一扩展 id 只能出现在
一个源**。宿主 `DigestTree` 禁止符号链接，不要用软链挂载，也不要保留第二份副本。

## 品牌资产（必须）

包内 `brand/` 是产品身份的一部分，不是可选项：

| 文件 | 尺寸 | 上限 |
| --- | --- | --- |
| `brand/icon.svg` | `viewBox="0 0 512 512"`，自包含 | 40 KiB |
| `brand/icon.png` | 512×512 PNG | 120 KiB |
| `brand/social-preview.png` | 1280×640 PNG（中文主卡） | 600 KiB |

- 生成：`node ~/Code/github/SForum/scripts/brand/generate.mjs --only <包目录名>`；
  先加 `--draft /tmp/x --sheet /tmp/x` 看样张。文案与字形改
  `~/Code/github/SForum/scripts/brand/spec.json`，不手改 PNG。
- 校验：`scripts/prepare-package.sh` 已含 `extension brand check`；单独跑
  `cd "$SFORUM_DIR/apps/api" && go run ./cmd/sforum extension brand check "$PKG"`。
- 规则（模板参数、错误码、GitHub 手动上传步骤）：
  `~/Code/github/SForum/docs/extensions/brand-assets.md`。
- 品牌文件不进 `packageFiles`，但属于包树：改图会改变包摘要，走正常升级流程；
  不要为了消掉摘要漂移去改版本号。

## 开发闭环

```bash
# 1. 改 plugins/sforum.fetch-url/ 下的后端代码
cd plugins/sforum.fetch-url/backend && go vet ./... && go test ./...

# 2. 构建二进制 + 刷新摘要 + 宿主校验（SFORUM_DIR=… 可覆盖框架路径）
cd ../../.. && ./scripts/prepare-package.sh

# 3. 让站点加载：重启 API 扫描到候选 → 后台「插件管理 → 升级 → 启用」
```

**改任何包内文件都必须重跑第 2 步**：宿主以 exact-artifact 校验哈希，摘要不匹配的包
会被拒绝启用。同版本号只改内容时，卡片上的「重启」不会切换制品，必须走
「升级（授权当前精确制品 → 确认并升级）」。

## 验证标准（不要只跑单测就报完成）

1. `cd plugins/sforum.fetch-url/backend && go test ./...`
2. `cd ~/Code/github/SForum/apps/api && go run ./cmd/sforum extension validate|test <包路径>`
3. 真实抓取冒烟：在站内发一条 @ 机器人的主题带上白名单内 URL，确认回复引用了该页
   正文里的事实（而不是模型记忆）；再发一条白名单外 URL，确认回复是「不在白名单」
   的可解释拒绝，而不是凭记忆作答。
4. 设置页「测试抓取」按钮要能出成功与失败两种就地解释（依赖宿主已实现
   `ai_tool_probe` settings action）。

## 宿主约束速查（实测）

1. **AI 工具超时上限 5000ms**，且运行期截止时间必须逐字等于清单 `timeoutMs`；
   插件自己的抓取预算要更小（本插件上限 4500ms），否则宿主会在插件返回结果前掐断，
   运营者看到的是「工具无响应」而不是一条可解释的超时说明。
2. **工具结果被宿主截断到 8000 字节/条**（单次回复累计 16000）。所以本插件自己把
   payload 压到 7500 字节以内，让切点由插件决定。
3. **登记 ≠ 暴露**：模型只看到用途白名单里的工具名。装上插件后必须在
   AI 控制台 → 用途 → `forum.reply` → 模型可见工具 → 自定义 里点名 `fetch-url`。
   未注册的名字会被服务端跳过，所以可以先写配置后装插件。
4. **工具表在 API 装配期一次性登记**：启用/停用插件要重启 API 才反映到工具列表。
5. **插件设置以 `SFORUM_SETTING_<KEY>` 环境变量注入子进程**（大写、非字母数字转下划线），
   而且从未保存过的设置不会被注入——插件必须自带与清单 `default` 一致的默认值
   （本插件用 `config_test.go` 的 `TestManifestDefaultsMatchRuntimeDefaults` 钉死）。
6. **插件子进程的环境是白名单**（`PATH/HOME/TMPDIR/TEMP/TMP/LANG/LC_ALL/LC_CTYPE/TZ/
   SFORUM_PLUGIN_HELPER/APP_ENV` + `SFORUM_SETTING_*`）。`HTTP_PROXY`/`HTTPS_PROXY`
   **不会**透传，所以插件的出站代理只能来自它自己的设置项。
7. **保存插件设置会重启该插件的精确制品**，因此 env 形式的配置改完即生效，不需要
   重启 API。设置页的「测试抓取」也是用一份带草稿值的短命进程完成的。
8. **信任确认是两步**：先「授权当前精确制品」，再「确认并启用/升级」。任何字节变化
   都会作废旧的授权。
9. **`admin.pages[].label` / `description` 是纯字符串**，不是本地化对象；扩展的
   name/description 本地化走 `includes.langs`。
10. **Go 的 RE2 不支持 `\uXXXX` 转义**，必须写 `\x{...}`。写错的后果是
    `regexp.MustCompile` 在包初始化时 panic，插件连 go-plugin 握手都进不去，
    宿主只报一句 `Unrecognized remote plugin message` + stderr 尾巴。
    所以任何包级正则都要有一个能跑到该包的测试。
11. **外部扩展源只在 API 启动时扫描**；`extensions/dev/` 从不被扫描（只是脚手架区）。
12. **`air` 在自身启动时缓存 env**：改了 SForum 的 `.env` 必须重启 `air` 才生效。

## 发布

推 `v*` 标签触发 `.github/workflows/release.yml`：verify（`go vet` / `go test` + 宿主
`extension validate|test` + 标签与清单版本一致性）→ 四平台矩阵交叉编译
（linux/darwin × amd64/arm64，每平台各自 `digest --write` 后打包 zip + SBOM）→
publish（`SHA256SUMS` + Release）。

- **发版前把 `sforum.extension.json` 的 version 提到与标签一致**，并在 `CHANGELOG.md`
  写同名小节——发布说明就从那里生成。
- CI 与 Release 共用 `SFORUM_REF`（当前钉 `v3.0.13`，扩展宿主面自该版本起已在宿主 main）。
  升宿主基线时同步改两个 workflow 和 `backend/go.mod` 的 `go` 指令。
- publish 会断言「Release 非草稿 + 四个平台包齐全」：草稿会被自动转正，缺包直接失败。
- `workflow_dispatch` 只跑 verify + build，不创建 Release，用来演练构建。
- Windows 未发布：宿主按 `backend/entry` 直接执行、不做 `.exe` 归一化，未经实机验证。

## 当前环境事实（2026-10-15）

- SForum `.env`：`EXTERNAL_EXTENSION_ROOTS=…/SForum/extensions/dev,~/Code/github/sforum-oauth2-provider,~/Code/github/sforum-fetch-url`
- dev API 由 `air` 后台托管，日志 `/tmp/sforum-api-dev.log`
- **同一个浏览器与同一个 dev API 可能被其他会话同时使用**（例如 oauth2-provider 任务）：
  air 会被别人重启、浏览器标签会被别人导航。遇到 502 先确认 API 是否正在重建，
  不要盲目再杀一次进程。
- 站点 `http://127.0.0.1:3000`；插件设置页
  `/control-panel/extensions/sforum.fetch-url/pages/settings`
- 本机直连外网不通，出站必须走 `http://127.0.0.1:7897` 代理；插件已按此配置。

## 设计取舍（NOTES.md 有完整记录）

- **没有「全部放行」模式**：白名单为空即拒绝一切。抓取工具的实际调用者是任意用户，
  一个默认放行的开关等于把站点变成匿名抓取代理。
- **白名单不做路径前缀匹配**：看起来更精确，实际更容易让人以为自己限制了范围。
- **代理模式下放弃本机 IP 校验**：DNS 由代理解析，本机拿不到真实出口 IP；
  这是明确写出来的降级，不是隐藏行为。
- **不执行 JavaScript、不做 robots.txt、不缓存**：每多一项能力都要多一份边界；
  本插件的目标是「读一篇静态文章」，不是做一个爬虫框架。
