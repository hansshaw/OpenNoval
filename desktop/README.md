# AI Novel Studio 桌面版

桌面版使用 Electron 承载与 Web 工作台完全相同的 Go 引擎和界面。Electron
主进程启动随机回环端口的 Go sidecar，窗口加载 sidecar 页面；退出应用时关闭
sidecar。

## 开发启动

需要 Go 1.25+、Node.js 24+ 和 npm。

```powershell
cd desktop
npm install
npm start
```

`npm start` 会先构建 `desktop/bin/ainovel-sidecar.exe`，再启动 Electron。
界面、创作流程、项目管理、模型设置和 TXT/EPUB 导出与 Web 工作台一致；
桌面版导出会打开系统“另存为”对话框。

## 测试与打包

```powershell
npm test
npm run smoke
npm run dist
```

`npm run smoke` 会以隐藏窗口启动完整 sidecar，检查 Web API、SQLite 和退出清理。
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
└── output/web-books/
```

SQLite 用于桌面项目索引、共创会话快照、界面设置和最近运行事件。小说正文、
规划、评审和连续性事实继续保存在 `output/web-books/<项目>` 中，保证 Web、
桌面和命令行可以读取同一套版本化数据。

已归档项目可从界面永久删除。后端删除整个项目目录后，桌面同步会清理 SQLite
中对应的项目索引、共创会话和运行快照，并保留不属于具体项目的全局运行快照。

模型 API Key 由 Go 引擎保存到其既有配置位置，不写入桌面 SQLite，也不会通过
Electron preload 暴露给渲染进程。
