# 参与 OpenNovel

OpenNovel 由 [hansshaw](https://github.com/hansshaw) 维护，欢迎提交可复现的问题、功能建议和范围清楚的改进。使用说明见 [README](README.md)，桌面与 Android 开发细节分别见 [desktop/README.md](desktop/README.md) 和 [mobile/README.md](mobile/README.md)。

项目与仓库名称为 **OpenNovel**。为兼容现有安装和升级，`ainovel` 系列应用标识、包名、数据路径、安装包文件名及发行签名保持稳定，不随仓库重命名；修改这些标识前须单独评估升级和数据迁移影响。

当前 Go 源码模块使用 `github.com/hansshaw/OpenNovel`，手机绑定模块使用其下的 `/mobile/bind`。历史发行标签保留当时的模块路径；构建历史版本时以对应版本的 `go.mod` 为准。

## 反馈与讨论

- 提交前搜索已有 Issue，避免重复报告；使用仓库中的问题反馈或功能建议表单。
- 新功能、架构调整、数据格式修改或较大重构，请先开 Issue 说明场景、方案及兼容性影响，再开始实现。
- 日志、截图和示例文件请先脱敏，不要上传 API Key、访问令牌、私人小说正文、个人信息、整个配置目录或签名文件。优先使用虚构的最小示例。
- 安全漏洞请按 [SECURITY.md](SECURITY.md) 私密报告，不要在公开 Issue 或 PR 中披露细节。

## 提交改动

1. Fork 仓库，从最新 `main` 创建短期功能分支；一个 PR 处理一个明确的问题。
2. 保持现有代码风格，Go 代码执行 `gofmt`。需要时更新使用说明、示例配置和第三方声明。
3. 完成与改动相关的验证，向本仓库 `main` 提交 PR，说明问题、改动、测试结果和关联 Issue。界面改动附脱敏截图；未完成的工作可先开 Draft PR。
4. 处理 CI 失败与评审意见，由维护者决定是否合并。仓库使用 Squash merge，将一个 PR 合并为一个主线提交；请使用能说明改动的 PR 标题。

只提交项目需要的源码、测试和文档，不提交本机配置、密钥、作品、构建产物或依赖目录。请勿为修复小问题顺带进行无关的格式化或依赖升级。

## 按改动范围验证

开发环境以仓库配置为准：Go 版本见 `go.mod`，桌面使用 Node.js 26，Android 环境见手机开发文档。桌面依赖使用 `npm --prefix desktop ci` 安装。

| 改动范围 | 建议验证 |
| --- | --- |
| Go 引擎、存储、导入或接口 | 相关包测试，`go test ./...`、`go vet ./...`；涉及共享状态时运行相关包的 `go test -race` |
| 桌面界面、Electron 或阅读器 | `npm test`；按影响运行 `npm run smoke`、`npm run test:e2e`，并实际检查改动的界面 |
| Android 原生代码或移动交互 | `bash mobile/scripts/test-work-protection.sh`、相关 Go/JS 测试；涉及原生 API、绑定或打包时构建 APK，并在设备或模拟器验证相关行为 |
| 文档、Issue 表单或协作配置 | 检查链接、内容、YAML 语法和实际配置含义，无需重新打包应用 |

在 PR 中列出实际执行的命令和结果；无法执行的相关检查说明原因。无需为每次改动运行全部桌面与 Android 打包流程。Linux 无图形会话时，桌面验收命令使用 `xvfb-run -a`。

自动 CI 覆盖根 Go 模块（不含 `mobile/bind` 独立模块）、桌面/共享 JavaScript 和 Java 电源保护测试；**CI 通过不代表 Android APK 构建、签名或设备验收通过**。Android 发布前仍须完成 [手机开发文档](mobile/README.md#验证) 中适用的检查。安装、升级和数据保留行为的改动需做对应的打包验收。

## 依赖、离线资源与许可

依赖更新需同时提交相应锁文件。PDF.js 采用固定版本并随应用离线分发；更新 `pdfjs-dist` 时，还需同步 `desktop/scripts/sync-reader-vendor.js` 中的版本，执行：

```sh
npm --prefix desktop run vendor:reader
npm --prefix desktop run vendor:check
```

提交更新后的离线资源与清单，检查 PDF 阅读行为，并同步 `NOTICE`、`LICENSES/THIRD-PARTY.md` 及相应许可文件。其他第三方依赖的更新也应核对版权和分发声明。Dependabot 的更新 PR 同样需要这些检查。

提交的贡献沿用本项目 [AGPL-3.0-only](LICENSE) 许可，第三方代码须保留其原有版权与许可。

## 发行

Release 由维护者在验收后创建和发布。普通 PR、合并和日常 push 不自动发布安装包；桌面继续使用手动验收工作流，Android 继续按现有本地构建与签名流程发行。发行签名及其密码由维护者私密保存，不进入仓库或 Release 附件。

## 维护设置

日常工作通过 [项目看板](https://github.com/users/hansshaw/projects/1) 跟踪；已确认进入下一次发布的任务加入“下一版本” Milestone。平台问题使用 `platform:windows`、`platform:linux` 或 `platform:android` 标签，确认反馈后移除 `needs-triage`。

主分支规则定义保存在 [.github/rulesets/main.json](.github/rulesets/main.json)：要求 PR、三个自动 CI 检查通过并解决评审对话，禁止强制推送与删除；单人维护不要求另一人批准。GitHub 上的规则是实际生效配置，修改 JSON 不会自动更新远端，应在仓库 Settings → Rules → Rulesets 中同步。
