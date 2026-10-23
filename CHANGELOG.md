# 变更记录

发布物的版本号与 `plugins/sforum.fetch-url/sforum.extension.json` 的 `version` 一致，
标签为 `v<version>`。发布说明由 `.github/workflows/release.yml` 从本文件对应小节生成。

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
