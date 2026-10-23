# AI Novel Studio 桌面开发

桌面应用使用 Electron 管理窗口与 Go 引擎进程。界面资源嵌入 Go 程序并随安装包分发，主进程通过本次启动的私有凭证访问随机回环端口。内部进程不提供独立产品入口。

窗口启用上下文隔离和 Chromium 沙箱，关闭 Node 集成；文件导出使用系统“另存为”对话框。应用退出时，主进程通知 Go 引擎取消任务、保存状态并释放作品锁；超时后强制结束进程，下次启动按已有检查点恢复。

## 开发启动

需要 `go.mod` 指定版本或更新的 Go、Node.js 26 和 npm。在仓库根目录执行：

```sh
npm --prefix desktop ci
node desktop/node_modules/electron/install.js
npm start
```

也可进入 `desktop/` 后执行 `npm ci`、`node node_modules/electron/install.js` 和 `npm start`。启动命令先构建当前系统和架构的 Go 内部进程，再打开 Electron。

只编译内部进程：

```sh
npm run build:sidecar
```

内部可执行文件在开发环境放置于 `desktop/bin/ainovel-sidecar`，Windows 扩展名为 `.exe`。它的生命周期与访问凭证由桌面主进程管理。

## 验证

以下命令可在仓库根目录执行：

```sh
go test ./...
go vet ./...
npm test
npm run smoke
npm run test:e2e
```

单元测试覆盖桌面数据库、进程生命周期、IPC 信任边界和界面交互。`smoke` 启动真实 Electron 隐藏窗口与 Go 进程，检查页面加载、内部接口、SQLite，以及退出后的进程、端口和数据库完整性。

外观验收覆盖五种皮肤、4.5:1 文字对比度、SVG 随包加载、切换时保留输入、动效开关、系统减少动态效果及恢复默认。`test:e2e` 还检查外观偏好跨进程重启持久化。需要截图时，可设置 `AINOVEL_APPEARANCE_SCREENSHOTS` 为输出目录；测试将保存每种皮肤与外观面板的 PNG，使用独立的测试工作区。

`test:e2e` 使用独立临时用户目录、会话、HOME、配置、缓存和本地模型 mock，验证多连接模型配置、流式共创、项目复制、资料独立修改和重启持久化。测试不读取现有模型配置，也不调用真实模型。

完整创作流程另由 Go 集成测试覆盖：

```sh
go test -count=1 -run 'TestCreative(Desktop|Background)Workflow' -v ./internal/entry/desktopui
```

该流程覆盖共创、规划、审批、正文、反馈重写、验收、TXT/EPUB 导出和后台项目隔离；它验证工作流与恢复边界，不评估真实模型的文学质量。

报告默认写入 `desktop/dist/reports/`。在 `desktop/` 内执行 `npm run test:e2e -- --report /path/to/report.json` 可以指定位置。报告中的 mock token 是测试固定值，不代表真实消耗。

Linux 无图形会话时使用：

```sh
xvfb-run -a npm run smoke
xvfb-run -a npm run test:e2e
```

测试默认保留 Chromium 沙箱。仅当容器明确不支持沙箱时，在 `desktop/` 目录使用显式 `-- --no-sandbox` 参数。

## 安装包

安装开发依赖后，在仓库根目录执行：

```sh
npm run dist
npm run dist:win -- --x64
npm run dist:linux -- --x64
npm run dist -- --dir
```

`dist` 默认构建当前系统和架构，产物保存在 `desktop/dist/`；`--dir` 仅生成未压缩应用目录。每次打包按目标系统和架构重新编译 Go 进程，无需提前启动应用。

| 系统 | 产物 |
| --- | --- |
| Windows | NSIS `.exe` 安装向导，可选择安装位置 |
| Linux | `.AppImage` 与 `.deb` |

打包产物仅包含相应目标的 Go 可执行文件，放在应用 `resources/bin/` 中。建议分别在 Windows 和 Linux 上构建各自安装包；Linux 交叉构建 Windows 安装包需另有 Wine 等构建依赖。

```sh
npm run smoke:packaged
```

该命令直接启动未压缩应用目录中的桌面程序，验证随包 Go 进程、界面、SQLite 与退出清理。Linux 无图形会话时加 `xvfb-run -a`。默认报告为 `desktop/dist/reports/smoke-packaged.json`。

CI 在 Windows 和 Linux 运行隐藏窗口与打包验收。推送 `v*` 标签时，同一流程生成版本对应的安装包，全部验收通过后发布至本仓库 Release。

### Windows 原目录升级

首次安装运行 Release 中的 `ainovel-desktop-<版本>-win-x64-setup.exe`，可以选择安装目录。后续版本保存作品并退出应用后，直接运行新版安装包；安装向导会读取已安装版本记录，默认沿用原目录和原安装范围，再替换程序文件，无需先卸载。升级当前用户安装时使用原 Windows 账户；原来选择“所有用户”的安装，升级时可能需要管理员权限。

作品、SQLite、外观偏好和模型配置位于用户数据/配置目录，升级保留这些数据。安装目录用于程序文件，不应存放手动添加的作品或个人文件。后续升级方式为下载并运行新版安装包。

维护发布时保持 `name=ainovel-desktop`、`appId=com.ainovel.desktop`、`productName=AI Novel Studio` 和 NSIS GUID `e8db1337-b330-59d5-a41f-5283be9b2bcf` 稳定，仅递增版本号。这个 GUID 与旧配置按 appId 自动派生的值完全相同，兼容未显式声明 GUID 的安装包。NSIS 根据注册表 `Software\<GUID>` 的 `InstallLocation` 查找旧目录，使用标准升级卸载流程保留用户数据，`deleteAppDataOnUninstall` 显式关闭。

Windows CI 额外生成 `0.0.0` 测试安装包（省略显式 GUID，模拟旧配置），先安装到包含空格的自定义目录，再运行待发布安装包且不指定目录，验证注册表身份、版本、原目录、用户数据及安装后启动。此测试只允许在临时 Windows GitHub runner 中执行，测试包不会发布。

发布新版本时更新 `desktop/package.json` / lockfile 的版本号与 `desktop/release-notes.md`，提交代码，再推送对应 `v<版本>` 标签。标签流水线在 Linux 和 Windows 验收通过后创建 Release，上传安装包和 SHA-256 校验值；已发布版本不会被覆盖。预发布标签发布为 prerelease，不替换稳定版。当前提供运行新版安装包的升级方式，没有启用应用内自动下载更新。

## 数据位置

工作区位于 Electron 的 `userData` 目录：

```text
<userData>/
├── ainovel-desktop.sqlite
├── logs/
└── output/
    ├── novel/                  # 默认项目
    ├── web-books/              # 历史命名，项目中心的作品与副本
    ├── style-library/          # 各项目共享的风格语料库
    └── workbench-jobs.json     # 任务队列与并发设置
```

SQLite 保存项目索引、共创快照、界面设置和运行状态。正文、规划、章节版本、评审和连续性事实保存在各项目目录中。模型 API Key 由 Go 配置管理，不写入桌面 SQLite，也不通过 preload 暴露给界面。

外观偏好使用 `desktop_settings` 的 `appearance` 项，经受信主窗口 IPC 读取和写入，仅接受约定主题名与布尔动效值。渲染端 localStorage 只是首次绘制缓存；随机回环端口和非持久 Chromium partition 不影响跨启动保存。样式和原创 SVG 位于 `internal/entry/desktopui/static/appearance.*` 与 `static/themes/`，由 Go embed 随应用分发。

旧版曾将默认作品直接保存在 `output/web-books` 根目录。检测到这类项目时，应用继续打开原位置；不会为了更改技术架构而自动搬移或覆盖作品。该项目可在“项目中心”创建独立副本，复制只包含项目自身资料，不递归包含其他作品。

“版本与恢复”保存命名快照和差异预览，恢复前自动备份；异常退出后会先回滚未完成的恢复事务。快照、复制和媒体导入均遵守项目租约，避免与其他实例交叉写入。

## 并发与退出

默认允许 3 个项目同时创作；项目名额与模型请求总并发、每连接请求并发分别设置。打开另一项目后原项目继续运行，暂停只影响当前项目。共享风格分析使用独立任务，可以与创作同时进行。

后台任务计入项目名额，同一项目不会重复执行任务。队列不保存密钥；重启后排队任务继续，异常中断的运行任务标为 `aborted`，检查项目状态后可重新提交。工作区由单个桌面实例管理，退出应用会停止其中所有创作任务。

## 媒体模块

`internal/mediaworkspace/` 集中放置媒体界面资源与中性章节配图包格式，`internal/chaptermedia/` 负责本机图片文件和元数据。桌面接口仅承担项目访问与读写协调，可在不改动创作引擎的情况下替换媒体界面或接入新的生成实现。

当前支持图片导入、说明与位置编辑、正文阅读配图和配图包导出。图片、视频与语音连接可保存配置，尚未执行媒体生成请求。
