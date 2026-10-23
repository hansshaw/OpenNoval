# 章节配图与 infinite-canvas 集成

## 结论与本次边界

可采用独立服务与文件交换接入。当前工作台是 Go 内嵌静态前端与 Electron；无限画布使用 Next.js 16 / React 19、Bun、Go / Gin / GORM 和 SQLite。直接把其页面拷入现有静态目录不能运行，合并双方账号、存储与生成队列也不是一次页面嵌入可以完成的工作。

本次保留工作台的章节配图管理和媒体连接配置；将上游应用源码固定在 `third_party/infinite-canvas/`，通过独立画布地址打开，并导出兼容其导入界面的章节画布 ZIP。ZIP 包含当前保存正文、章节规划和配图原文件，不含 API Key。画布生成结果可下载后手动导回章节。正文自动拆镜头、调用图片/视频模型、任务回传和媒体模型密钥桥接尚未实现。

上游来源为 [tigerowo/infinite-canvas](https://github.com/tigerowo/infinite-canvas)，固定提交为 `ccda172efba25cbee990a785bba715b90cc42b95`。`AINOVEL_UPSTREAM.json` 记录了 500 个纳入文件的 SHA256。上游已提交的应用源码均保留原样；排除没有对应原始构建源码的可选 3D 导演台入口/JS/CSS，以及另行采用 Sketchfab Standard 许可的 3D 演示模型二进制，详见 [第三方声明](../THIRD_PARTY_NOTICES.md)。未安装或提交 `node_modules`、`.next`、本地数据库与生成文件。

## 启动独立服务

已提供跨平台 Node 启动脚本，需要预先安装 Docker Engine 与 Docker Compose：

```sh
node scripts/infinite-canvas.mjs verify-source
node scripts/infinite-canvas.mjs start
```

首次启动从纳入源码构建上游镜像，会下载 Go、Bun 依赖。脚本仅将前端发布到 `127.0.0.1:3000`，Go API 留在容器内部；不会自动安装 Docker，不会调用收费模型。可通过环境变量 `AINOVEL_CANVAS_PORT` 使用其他 1024–65535 端口。

管理员随机密码和 JWT 密钥首次写入 `.ainovel/infinite-canvas.env`，不打印在终端；后续启动保留已有配置。SQLite 与媒体数据位于 `.ainovel/infinite-canvas-data/`，均在版本控制之外。启动后在工作台的画布地址填入 `http://127.0.0.1:3000/canvas`。日志与停止命令：

```sh
node scripts/infinite-canvas.mjs logs
node scripts/infinite-canvas.mjs stop
```

停止服务不会删除数据。上游数据库自身提示开发阶段不保证历史结构兼容，更新源码前应备份该目录。

不使用 Docker 时，可按照保留的 [上游本地开发说明](../third_party/infinite-canvas/docs/backend/local-development.md) 启动 Go API 与 Next.js。该方式需要 Go、Bun 及独立的前后端进程；上游 Go 入口默认监听所有网卡的 `8080`，不能将它描述为只监听本机的服务。当前工作台桌面安装包不内置或自动启动 Next.js/Bun。

## 登录、存储与嵌入限制

- 未登录时，画布项目与素材可保存到该浏览器、该源地址的 IndexedDB；登录且开启账号同步后才会同步到对应账号。切换域名或端口会改变本地存储空间。
- 上游自行保存 AI 连接并可能由浏览器直连服务商。工作台保存的模型密钥不会放进 URL、ZIP、剪贴板或跨窗口消息。当前双方连接需要分别配置。
- 上游 Next.js `/api/*` 代理到其独立 Go API，不能直接改成工作台的 `/api/*`。工作台自身的同源和桌面导航约束继续适用。
- 固定版本代码没有设置阻止 iframe 的响应头，但部署代理可以增加此限制；浏览器第三方存储规则也可能影响内嵌登录。当前使用独立打开，不依赖 iframe。上游已有 `postMessage` 仅服务其同源 3D 导演台，不是供本工作台使用的通用桥接协议。
- 导演台使用单独 iframe。上游仓库对它仅提供 `web/public/director/` 内已打包资源，未包含原始构建工程；本次排除这组运行产物和受限许可的演示模型，保留来源记录。因而本快照的可选 3D 导演台不可用，2D 画布和章节 ZIP 导入不依赖这些文件；未修改上游源码来隐藏其原有导演台入口。其他上游功能的完整运行验收仍应在独立服务构建后进行。本轮未构建或发布上游服务。

## 章节画布包契约

工作台导出接口为 `GET /api/chapters/{chapter}/canvas?directory=...`。接口要求当前项目匹配且该项目已经空闲，以一致快照读取当前保存内容；正在运行时返回 409。它不选取旧版本、不读取未保存编辑草稿，也不触发任何模型请求。原始配图总量限制为 256 MiB，以免导出长时间占用项目；超过限制需先减少本章配图。

上游画布库只接受 ZIP，不接受裸 JSON。ZIP 根目录必须包含 `projects.json`：

```json
{
  "app": "infinite-canvas",
  "version": 3,
  "exportedAt": "2026-10-23T00:00:00Z",
  "projects": [{
    "project": {
      "id": "chapter-export-unique-id",
      "title": "第一章 · 雨夜来信",
      "createdAt": "2026-10-23T00:00:00Z",
      "updatedAt": "2026-10-23T00:00:00Z",
      "nodes": [{
        "id": "chapter-text",
        "type": "text",
        "title": "章节正文",
        "position": {"x": 40, "y": 40},
        "width": 520,
        "height": 640,
        "metadata": {"content": "当前保存的章节正文", "fontSize": 18, "status": "success"}
      }, {
        "id": "chapter-image-1",
        "type": "image",
        "title": "邮局外景",
        "position": {"x": 620, "y": 40},
        "width": 480,
        "height": 320,
        "metadata": {
          "content": "", "storageKey": "image:chapter-export-unique-image-id",
          "mimeType": "image/png", "bytes": 1024,
          "naturalWidth": 1200, "naturalHeight": 800,
          "status": "success"
        }
      }],
      "connections": [], "chatSessions": [], "activeChatId": null,
      "backgroundMode": "lines", "viewport": {"x": 0, "y": 0, "k": 1}
    },
    "files": [{
      "storageKey": "image:chapter-export-unique-image-id",
      "path": "projects/chapter-export-unique-id/files/chapter-image-1.png",
      "mimeType": "image/png", "bytes": 1024
    }]
  }]
}
```

`files[].path` 指向 ZIP 内的图片二进制，`storageKey` 与图片节点严格对应。上游导入器将文件写入 IndexedDB，再在打开画布时恢复 `blob:` URL，不依赖工作台端口、临时资源 URL 或 Base64。每次导出生成独立 ID，避免多次导入覆盖原图。当前保存的卷名、章名、事件、悬念、场景另存为规划文本节点；规划与正文的状态也包含在节点标题中。

契约核对来源：上游 `web/src/app/(user)/canvas/export-types.ts`、`page.tsx` 的 `importCanvas`、`utils/canvas-export.ts` 和 `stores/use-canvas-store.ts` 的 `importProject`。这一文件格式由上游版本决定；升级前需重跑导出兼容测试。

## 后续生成接入

1. 将用户确认的章节正文/规划转换为场景与镜头提示词，并记录其正文版本摘要。
2. 通过工作台后端读取用户选择的图片/视频/语音连接，按服务商适配提交、轮询和取消任务；密钥仍留在本地后端。
3. 将输出落入章节媒体存储，保存模型、提示词、任务 ID 和关联正文版本；只有用户明确采用才改变章节配图。
4. 如需自动传入独立画布，应设计带来源校验和一次性令牌的桥接协议，验证请求范围与返回资源，不能复用上游导演台的同源消息。

当前未宣称完成以上自动生成与回传；本轮提供可使用的手动配图与章节内容交换基础。
