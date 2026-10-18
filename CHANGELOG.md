# 变更记录

发布物的版本号与 `plugins/sforum.fetch-url/sforum.extension.json` 的 `version` 一致，
标签为 `v<version>`。发布说明由 `.github/workflows/release.yml` 从本文件对应小节生成。

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
