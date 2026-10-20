# AI Novel Studio 桌面版

桌面版使用 Electron 承载与 Web 工作台完全相同的 Go 引擎和界面。Electron
主进程启动随机回环端口的 Go sidecar，窗口加载 sidecar 页面；退出应用时关闭
sidecar。

关闭应用时，主进程先使用仅本次启动有效的私有凭证发送本机退出请求，等待前后台
任务取消、费用和队列保存、作品锁释放后正常退出。30 秒后仍未退出才强制停止，
此时记录非正常退出，不写入“正常关闭”时间；下次启动仍按检查点恢复。

## 开发启动

需要 Go 1.25+、Node.js 24+ 和 npm。

```powershell
cd desktop
npm ci
node node_modules/electron/install.js
npm start
```

`npm start` 会先构建 `desktop/bin/ainovel-sidecar.exe`，再启动 Electron。
界面、创作流程、项目管理、模型设置和 TXT/EPUB 导出与 Web 工作台一致；
桌面版导出会打开系统“另存为”对话框。

## 测试与打包

```powershell
npm test
npm run smoke
npm run test:e2e
npm run dist
```

`npm run smoke` 会启动真实 Electron 隐藏窗口与 Go sidecar，检查界面加载、
preload IPC、Web API、SQLite 和退出后的进程、监听端口与数据库完整性。
测试使用独立临时 `userData`、Chromium session、HOME、配置、缓存与临时目录，
会清除继承的凭证变量，完成后删除临时数据，不读取现有模型配置。

`npm run test:e2e` 使用同样的隔离环境和确定性的本地 OpenAI 兼容 mock，
验证多连接/多模型配置、模型选择、流式共创、确认后取消挂起的规则生成、
默认项目复制、原项目与副本独立修改、新建项目、SQLite 同步、退出与重启持久化。
这些桌面回归通过真实窗口内的正式 HTTP API 操作。完整规划、正文工具调用、
章节反馈/验收和导出由补充的 Go 集成测试覆盖：

```sh
go test -count=1 -run 'TestCreative(Desktop|Background)Workflow' -v ./internal/entry/web
```

该流程通过本地模型 mock 调用真实引擎和工具，覆盖共创、规则、规划、审批、
写作、反馈重写、验收、TXT/EPUB 导出，以及复制后后台独立写作、事件与新增费用隔离。
它验证工作流与恢复边界，不评估真实模型的文学质量。CI 同时运行 Linux/Windows
隐藏窗口验收、完整创作流程和关键 Go 状态路径的 race 检查。

报告默认写入 `desktop/dist/reports/smoke.json` 和 `regression.json`，包含应用、
Electron/Node/Chromium 版本，各步骤耗时、模型调用、mock token 数和失败次数。
可用 `npm run test:e2e -- --report /path/to/report.json` 指定报告位置。
mock token 是测试固定数据，不代表真实模型用量或成本。

Linux CI 需要图形服务，例如 `xvfb-run -a npm run smoke` 和
`xvfb-run -a npm run test:e2e`。测试保留 Chromium 沙箱；仅容器明确不支持沙箱时，
才使用显式 `-- --no-sandbox` 选项。某些 npm 版本不自动执行 Electron 安装脚本，
因此上面的 `node node_modules/electron/install.js` 需要显式执行一次。

`npm run dist` 使用 electron-builder 生成安装包。

如果安装或打包时访问 GitHub 下载 Electron 运行时超时，可在当前 PowerShell
会话使用镜像：

```powershell
$env:ELECTRON_MIRROR = "https://npmmirror.com/mirrors/electron/"
$env:ELECTRON_BUILDER_BINARIES_MIRROR = "https://npmmirror.com/mirrors/electron-builder-binaries/"
npm install
```

## 数据位置

桌面工作区位于 Electron `userData` 目录：

```text
<userData>/
├── ainovel-desktop.sqlite
├── logs/
└── output/
    ├── novel/       # 独立默认项目，可创建副本
    └── web-books/   # 项目中心创建的项目与副本
```

SQLite 用于桌面项目索引、共创会话快照、界面设置和最近运行事件。小说正文、
规划、评审和连续性事实继续保存在 `output/novel` 或 `output/web-books/<项目>` 中，保证 Web、
桌面和命令行可以读取同一套版本化数据。

旧版曾将默认作品直接保存在 `output/web-books` 根目录。检测到这类作品时，
桌面版继续打开原目录并显示一次说明，不在没有源项目锁的情况下自动迁移作品。
旧版根项目暂不支持直接复制；在项目中心新建的独立项目支持复制。
带备份及锁保护的旧作品迁移仍待实现。

已归档项目可从界面永久删除。后端删除整个项目目录后，桌面同步会清理 SQLite
中对应的项目索引、共创会话和运行快照，并保留不属于具体项目的全局运行快照。

模型 API Key 由 Go 引擎保存到其既有配置位置，不写入桌面 SQLite，也不会通过
Electron preload 暴露给渲染进程。

## 版本与后台创作

“版本与恢复”支持命名快照、逐行比较和预览后恢复，恢复前自动备份，异常退出后
下次打开项目会先回滚未完成的恢复事务。锁、日志、后台队列与版本历史不参与复制。
Windows 与 Linux 删除项目时均先取得作品锁，再以持久删除标记阻止新写入，释放
句柄后移至隐藏临时目录清理，避免目录仍被其他进程写入。

后台创作只接受已审批、处于写作阶段的非当前项目。从项目中心点击“后台创作”，
选择批次/恢复、已保存的连接与模型、章节数及可选预算。每个任务使用独立 Host、
事件和新增费用记录。任务页的“并发设置”支持总并发、默认每连接并发和连接单独
覆盖，均可设置 1–4096，默认总并发 2、每连接 1；连接覆盖留空则继承默认。
保存后重启仍保留，运行期间也可调整：提高上限立即补派，降低上限等待已有任务结束，
不会强制取消。同一项目仍串行，同一任务所有角色和备用模型须来自同一连接。
4096 是配置上限，实际并发能力取决于本机资源和上游额度。
后台运行时前台模型调用暂时禁用，可继续阅读其他项目。
任务可以单独停止；安全退出前仍占用配额。重启后排队任务继续，原运行任务标为
`aborted`，检查项目后再重新提交，避免重复生成。

队列位于 `output/workbench-jobs.json`，只保存任务参数，不保存 API Key。预算按
实际回报用量累计，已经发出的请求可能超过预算；无定价模型不能提供精确费用控制。
任务中的项目禁止切换打开、复制、归档、删除与恢复版本。每个工作区使用一个桌面实例。

高并发事件和费用报告采用有界批量持久化：批内报告写盘成功后才返回，不降低
费用与事件的持久性。调度器优先处理取消、关闭和配置请求，费用轮询只读内存计数。
单条后台诊断会截断超长内容并标记，避免大响应无限放大日志；完整作品资料仍在项目中。
