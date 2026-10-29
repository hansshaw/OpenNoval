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

外观验收遍历八种皮肤，检查新皮肤主要文字与基础表面达到 4.5:1 对比度、SVG 随包加载、切换时保留输入、动效开关、系统减少动态效果及恢复默认。`test:e2e` 还检查已暂停的星空皮肤跨进程重启后仍保持关闭。需要截图时，可设置 `AINOVEL_APPEARANCE_SCREENSHOTS` 为输出目录；测试将保存每种皮肤与外观面板的 PNG，使用独立的测试工作区。这些断言验证指定配色与交互条件，不等同于对所有设备的动画性能测量。

仅做阅读器视觉验收时，可设置 `AINOVEL_READER_SCREENSHOTS` 为截图目录，保存书架、排版、专注阅读与 PDF 等画面；阅读器优先使用此目录，无需开启全局截图。

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

基础 CI 在 Windows 和 Linux 执行 Go 测试，并在 Linux 执行桌面界面单元测试。隐藏窗口、打包与安装升级验收只通过 Desktop acceptance 手动触发。为控制 GitHub 存储占用，两个工作流都关闭依赖缓存，不上传 Actions artifacts。维护发布时，可在手动工作流中指定与当前版本、完整提交 SHA 对应的未发布 Release 草稿 ID；验收通过后直接上传安装包及校验报告到该草稿。未指定草稿时只执行验收，运行结束不保留安装包。普通 push、标签和此上传步骤都不会自动发布 Release。

### Windows 原目录升级

首次安装运行 Release 中的 `ainovel-desktop-<版本>-win-x64-setup.exe`，可以选择安装目录。后续版本保存作品并退出应用后，直接运行新版安装包；安装向导会读取已安装版本记录，默认沿用原目录和原安装范围，再替换程序文件，无需先卸载。升级当前用户安装时使用原 Windows 账户；原来选择“所有用户”的安装，升级时可能需要管理员权限。

作品、SQLite、外观偏好和模型配置位于用户数据/配置目录，升级保留这些数据。安装目录用于程序文件，不应存放手动添加的作品或个人文件。后续升级方式为下载并运行新版安装包。

维护发布时保持 `name=ainovel-desktop`、`appId=com.ainovel.desktop`、`productName=AI Novel Studio` 和 NSIS GUID `e8db1337-b330-59d5-a41f-5283be9b2bcf` 稳定，仅递增版本号。这个 GUID 与旧配置按 appId 自动派生的值完全相同，兼容未显式声明 GUID 的安装包。NSIS 根据注册表 `Software\<GUID>` 的 `InstallLocation` 查找旧目录，使用标准升级卸载流程保留用户数据，`deleteAppDataOnUninstall` 显式关闭。

Windows CI 额外生成 `0.0.0` 测试安装包（省略显式 GUID，模拟旧配置），先安装到包含空格的自定义目录，再运行待发布安装包且不指定目录，验证注册表身份、版本、原目录、用户数据及安装后启动。此测试只允许在临时 Windows GitHub runner 中执行，测试包不会发布。

后续确需发布时，单独确认版本号、源码与安装包，再手动创建 Release。日常开发只提交并 push；当前工作流没有自动发布步骤。应用提供运行新版安装包的升级方式，没有启用应用内自动下载更新。

## 数据位置

工作区位于 Electron 的 `userData` 目录：

```text
<userData>/
├── ainovel-desktop.sqlite
├── logs/
└── output/
    ├── novel/                  # 稳定工作区入口；兼容旧版在此保存的作品
    ├── web-books/              # 历史命名，项目中心的作品与副本
    ├── style-library/          # 各项目共享的风格语料库
    └── workbench-jobs.json     # 任务队列与并发设置
```

首次启动进入空项目中心，不自动创建作品。可以手动创建或导入项目，归档后也可删除最后一个项目。SQLite 保存上次选择的项目、项目索引、共创快照、界面设置和运行状态；重启恢复最后选择，无有效项目时返回项目中心。正文、规划、章节版本、评审和连续性事实保存在各项目目录中。模型 API Key 由 Go 配置管理，不写入桌面 SQLite，也不通过 preload 暴露给界面。

外观偏好使用 `desktop_settings` 的 `appearance` 项，经受信主窗口 IPC 读取和写入，仅接受八个约定主题名与布尔动效值。渲染端 localStorage 只是首次绘制缓存；随机回环端口和非持久 Chromium partition 不影响跨启动保存。启动恢复遵循已保存的动效值；只有用户主动切换到另一款星空、极光或丝绸流光皮肤时默认开启动效。原始皮肤始终静态。

样式、原创 SVG 与 Canvas 场景位于 `internal/entry/desktopui/static/appearance.*`、`static/themes/` 和 `static/dynamic-background.js`，由 Go embed 随应用分发。Canvas 使用单实例、有限绘制频率和分辨率预算，窗口隐藏或系统要求减少动态效果时停止动画循环；详细参数与调研来源见 [动态皮肤说明](../docs/dynamic-themes.md)。外观设置不写入作品或模型配置。

旧版曾将默认作品直接保存在 `output/web-books` 根目录。检测到这类项目时，应用保留原位置；不会自动搬移或覆盖作品。该项目可在“项目中心”创建独立副本，复制只包含项目自身资料，不递归包含其他作品；也可先归档再删除，删除时保留目录内其他独立作品。

“版本与恢复”保存命名快照和差异预览，恢复前自动备份；异常退出后会先回滚未完成的恢复事务。快照、复制和媒体导入均遵守项目租约，避免与其他实例交叉写入。

## 章节工作区

章节整体工作区、目录、正文和指令区使用独立滚动容器。宽窗按目录、阅读/编辑、指令/版本排列；正文再长也不会增加指令区与顶部的距离。阅读工具栏与编辑保存区位于各自滚动内容之外，切换章节重置正文和编辑区的局部位置，保留目录、指令区与整体工作区的位置。

工作区顶部的定位栏保持可达，可切换阅读/编辑并定位到目录、正文和章节指令。布局按工作区可用宽度调整：中等宽度将目录移到下方；窄窗将章节指令放到正文前方。滚动区域带有可见滚动条、键盘焦点和区域标签，不依赖在长正文中滚动到底部操作。

## 并发与退出

默认允许 3 个项目同时创作；项目名额与模型请求总并发、每连接请求并发分别设置。打开另一项目后原项目继续运行，暂停只影响当前项目。共享风格分析使用独立任务，可以与创作同时进行。

后台任务计入项目名额，同一项目不会重复执行任务。队列不保存密钥；重启后排队任务继续，异常中断的运行任务标为 `aborted`，检查项目状态后可重新提交。工作区由单个桌面实例管理，退出应用会停止其中所有创作任务。

## 媒体模块

`internal/mediaworkspace/` 集中放置媒体界面资源与中性章节配图包格式，`internal/chaptermedia/` 负责本机图片文件和元数据。桌面接口仅承担项目访问与读写协调，可在不改动创作引擎的情况下替换媒体界面或接入新的生成实现。

模型设置分为文本、生图、视频、语音四个独立面板，各自管理连接和默认模型。章节媒体可选择指定章节的已保存正式正文或草稿，异步生成图片、4 秒场景短片或整章朗读；只有规划、没有正文时不允许生成。使用 OpenAI 兼容的 `/images/generations`、`/videos`（任务查询及内容下载）和 `/audio/speech`，自定义协议仅保存配置。保存连接不会请求模型，生成按钮才会发起调用。

生成图片接入既有配图管理，可编辑说明与章首/章尾位置；音视频通过本地接口播放或下载，支持 Range 请求。生成记录保存在项目的 `media/chapters/<章号>/generated/`，不保存密钥，记录正文来源和 SHA-256。生成期间修改正文不会把旧结果挂到新正文上。重启保留已完成产物，中断任务明确标为失败，避免自动重试造成重复请求。

真实 Electron 媒体验收使用本地模拟服务及原创图片、静音 MP3、纯色视频素材，检查独立模型配置、指定章节正文请求、图片关联、音视频解码与重启持久化，不消耗付费 API。真实服务商还须支持所选接口、模型与权限。


## 阅读器与手动项目

“整本阅读”支持 TXT、EPUB、PDF、DOCX、Markdown 和 HTML 导入。导入内容
保存为 `mode=manual` 的普通项目；章节、配图、正文版本与人工编辑共用同一份数据。
TXT 支持 UTF-8、UTF-16 BOM 和 GB18030。EPUB 按 spine 顺序读取，HTML/DOCX 仅提取正文，
不会执行外部脚本或宏。PDF 原文件保留在项目阅读器数据目录，PDF.js 负责原页绘制与文字提取。
扫描 PDF 没有 OCR；原页可读，可手动补充章节文字后使用文本相关功能。

PDF.js 6.4.299 与字体、CMap、解码资源离线随包分发。更新时执行
`npm --prefix desktop run vendor:reader`，审查依赖许可证与变更，再执行
`npm --prefix desktop run vendor:check`。构建前会校验资源哈希。

导入限制为每文件 64 MiB、正文 32 MiB、章节 10000 章、PDF 2000 页；
单章人工编辑上限 8 MiB，风格语料每篇上限 8 MiB。ZIP 文档有解压大小、文件数、
路径和加密内容检查。原始文档不覆盖人工编辑后的章节。阅读进度、书签、字号、
字体、行距、段落间距、版心宽度、阅读配色与目录收起状态按书保存，切换界面皮肤不改变正文。

### 桌面阅读

- 书架可按最近阅读、书名或阅读进度排序；“继续阅读”回到最近打开的作品和已保存位置。阅读时间与作品修改时间分别记录。
- 专注阅读收起导入、书架和创作工作区；目录可以单独收起。全屏阅读使用原生全屏，进入或退出时保留章节与阅读位置。
- 排版面板提供宋体、无衬线、系统字体，三档版心宽度与段落间距；仍可选择字号、行距和阅读纸张。PDF 原页保留自身排版，文字选项用于文字阅读。
- 本章进度条可拖动定位，章节选择器可以直接跳章，“返回跳转前”可回到跳转之前的位置。上一屏、下一屏以当前可视正文区域为单位移动，章尾或章首可切换相邻章节；这属于连续文本中的逐屏阅读，不是 EPUB 横向分页。
- 全文搜索可定位到具体文字并高亮匹配内容，中文、生僻字和 emoji 的位置使用 Unicode 码点计算。导入的文本仍以安全文本节点呈现。
- 朗读、配图、视频、编辑与风格素材操作收在创作工具中，继续使用当前书籍的指定章节；打开工具不会自动发起收费生成请求。

在正文阅读区使用以下快捷键；搜索输入、下拉选择、编辑区域及弹窗中的按键优先交给当前控件：

| 快捷键 | 操作 |
| --- | --- |
| PageDown、Space、→ | 下一屏 |
| PageUp、Shift+Space、← | 上一屏 |
| Ctrl+→ / Ctrl+← | 下一章 / 上一章 |
| Ctrl+F | 打开目录并定位全文搜索 |
| Ctrl+B | 为当前位置添加书签 |
| F3 / Shift+F3 | 下一处 / 上一处搜索命中 |
| F | 正文获得焦点且没有选中文字时切换专注阅读 |
| Esc | 退出专注阅读；原生全屏状态由系统退出全屏 |

桌面验收覆盖排版与进度保存、重启恢复、真实键盘输入、输入框/下拉框/弹窗保护、Unicode 搜索定位，以及八款界面皮肤在缩放下的专注阅读和控制可达性。Android 的触控阅读与菜单验收另行保留。

### 开源阅读器调研与实现来源

本轮参考 [Thorium Reader 的阅读与无障碍交互](https://www.edrlab.org/software/thorium-reader/)、[官方快捷键说明](https://github.com/edrlab/thorium-reader/wiki/Keyboard-shortcuts) 与 [Foliate 的专注、排版和进度导航](https://johnfactotum.github.io/foliate/)，在本项目的章节模型和阅读器中独立实现，没有移植这些项目的源码、图标、字体或界面素材。

优先核查的宽松许可项目包括 [Thorium Reader（BSD-3-Clause）](https://github.com/edrlab/thorium-reader/blob/develop/LICENSE) 和 [foliate-js（MIT）](https://github.com/johnfactotum/foliate-js/blob/main/LICENSE)。若后续直接引入其代码，应按实际引入版本保留相应版权与许可。foliate-js 的渲染接口仍在演进，本次保留现有文档解析、章节正文和离线 PDF.js，不替换渲染内核。

[Foliate 使用 GPLv3](https://github.com/johnfactotum/foliate/blob/gtk4/COPYING)；GPL/AGPL 允许商业使用，但附有相应源码与分发条件，并非“非商用协议”。仅改写已有代码不会使原有许可条件失效。参考公开功能和独立实现，与复制或修改上游代码分别记录；后续新增依赖仍需核查它本身及随附资源的许可证。

项目整体采用 AGPL-3.0-only。LICENSE、NOTICE 与第三方许可证随安装包提供，
侧栏“开源许可与源码”可离线查看许可证并打开本项目源码地址。
