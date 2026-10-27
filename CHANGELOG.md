# 变更记录

发布物的版本号与 `plugins/sforum.fetch-url/sforum.extension.json` 的 `version` 一致，
标签为 `v<version>`。发布说明由 `.github/workflows/release.yml` 从本文件对应小节生成。

## [1.1.0]

- 预算对齐宿主 `v3.0.20`：AI 工具超时上限从 5000ms 放宽到 30000ms，单条工具结果从
  8000 字节放宽到 64000 字节、单次回复累计 256000 字节。插件按新上限重定自己的预算：
  清单 `timeoutMs` 声明 30000（贴着宿主上限），设置页 `timeout_ms` 的可填上限从 4500
  提到 29000（留 1 秒给封装与返回），回给模型的正文预算从 7500 提到 50000 字节——
  单条在 64000 线下，默认 5 步 × 50000 = 250000 也在 256000 累计线下，两条截断线
  都不会由宿主来切。
- `sforumVersion` 从不可验证的 `>=0.1.0` 收紧到 `>=3.0.20`：1.1.0 声明了 30000ms
  超时，老宿主的上限是 5000，装上去只会在清单校验阶段失败；写明下限让拒绝发生在
  安装前，而不是启用时。
- 构建基线钉到宿主 `v3.0.20`（commit `9baabbf1c`），CI 与 Release 的 `SFORUM_REF`
  同步；`main_test.go` 新增断言：声明的 `timeoutMs` 必须等于 SDK 导出的
  `HostAIToolMaximumTimeoutMS`。宿主再动上限时测试先红，而不是等运营者发现。
- 设置页文案、README、NOTES 里「宿主 5000ms / 8000 字节」的旧说法一并改掉；
  `pluginVersion`（User-Agent）从 1.0.0 补到 1.1.0，与清单版本对齐。

## [1.0.3]

- 包内品牌资产三件套：`brand/icon.svg`（512 viewBox，自包含）、`brand/icon.png`（512×512）、
  `brand/social-preview.png`（1280×640 中文主卡）；仓库根与包内 README 首屏改用社交卡。
  品牌文件不进 `packageFiles`（不是运行时制品），但属于包树，会改变包摘要——走正常升级流程。
- `scripts/prepare-package.sh` 在 validate 之后、test 之前接入 `extension brand check`：
  图缺失或超出尺寸/体积上限就在契约阶段失败，而不是等到发版当天。
- 文档补齐摘要漂移的真实来源：本机用本地 SForum 检出与本地 Go 工具链构建，CI 用钉住的
  宿主 tag 构建，两边算出的摘要可以不同；CI 只以 notice 提示，发布包内写入的是 CI 自己那份。

## [1.0.2]

- 构建基线钉到宿主 `v3.0.13`：本插件依赖的扩展宿主面已合入宿主 main，CI / Release 从
  流动的 `feat/extension-host-v2-surfaces` 改为固定 tag，构建可复现；`backend/go.mod`
  的 Go 版本同步到 `1.26.10`（宿主该版本的 go 指令）。
- 发布流水线必须真的发布：已存在的 Release 草稿会被 `--draft=false` 转正，上传后断言
  「非草稿 + 四个平台包齐全」，不再出现「流水线全绿、Release 却对下载者不可见」。
- 打包时排除宿主旧入口名留下的 `backend/backend` 陈旧产物；发布前校验平台包数量，缺包直接失败。
- 仓库配套与 oauth2-provider 对齐：README 增加 ci / release 徽章，补 MIT `LICENSE`。

## [1.0.1]

- 后台设置页并入 Core 的「AI 能力」一级菜单（`admin.pages[].group = "ai-tools"`），
  不再平铺在「扩展管理」里。
- 构建参数与宿主 dev 构建对齐：`CGO_ENABLED=0`、`-buildvcs=false`，包摘要只由源码决定。
- 新增 GitHub Actions 流水线：`main`/PR 跑单测与宿主契约校验，`v*` 标签触发
  linux/darwin × amd64/arm64 四平台打包并发布 Release。

## [1.0.0]

- 首个版本：`fetch-url` AI 工具，按运营者域名白名单抓取站外页面并返回正文纯文本。
- 后台设置页：总开关、域名白名单、出站代理、超时、响应体上限；「测试抓取」用草稿值做真实探测。
- `sforum-fetch-url-probe` 工具仅供设置页 `ai_tool_probe` 派发，不进入任何用途白名单。
- 不执行 JavaScript、不做 robots.txt、不缓存；白名单为空即拒绝一切抓取。
