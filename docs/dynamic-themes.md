# 动态皮肤设计与实现

桌面界面提供八种皮肤：原始静态、水墨 · 远山、清爽 · 晨间、暧昧 · 微醺、原神风 · 星旅、星空 · 深空、极光 · 夜幕、流光 · 丝绸。水墨、清爽、暧昧和原神风使用随包分发的原创 SVG，后三款通过原生 Canvas 2D 绘制动态场景。原神风是天空、浮岛、星轨和鎏金构成的原创幻想设计，未使用游戏角色、截图或官方素材。

## 使用与保存

首次启动默认使用原始静态界面。右上角外观按钮打开皮肤选择器，选择即时生效，保留当前页面和未保存输入。

- 主动切换到星空、极光或丝绸流光时自动打开“动态效果”；选择已选中的皮肤不会强制重新开启。
- 关闭动态效果后背景停止流动。关闭偏好写入本地 SQLite，重启仍保持关闭；重新切换到另一款动态皮肤会开启该皮肤的动效。
- 水墨、清爽、暧昧和原神风的边缘流光也受同一开关控制。原始皮肤始终静态，可用“恢复原始静态”一次恢复。
- 系统的“减少动态效果”优先于播放偏好，暂停动画但不改写已保存的开关值。窗口隐藏时暂停，重新可见时按当前偏好恢复。

`appearance.js` 使用 localStorage 缓存首次绘制所需设置，再读取 Electron 主进程的持久偏好。异步恢复不会覆盖启动后用户的新选择，保存请求按顺序提交。`desktop/appearance-preferences.js` 限定主题白名单与布尔动效值，主进程校验 IPC 来源。外观偏好保存在 `desktop_settings.appearance`，独立于作品、章节、创作风格语料库和模型配置。

## 参考与调研

调研日期为 **2026-10-24**。以下为当天通过 GitHub 仓库 API 读取的公开关注度，数字会变化。Stars 衡量整个项目的关注度，不能据此认定某一种视觉效果是“最受欢迎”或建立单效果排名。

| 项目 | 当日 Stars | 本次参考内容 | 主来源 |
| --- | ---: | --- | --- |
| React Bits | 50,062 | Galaxy、Aurora、Silk 的场景构成与动效表达 | [仓库](https://github.com/DavidHDev/react-bits)、[Galaxy](https://reactbits.dev/backgrounds/galaxy)、[Aurora](https://reactbits.dev/backgrounds/aurora)、[Silk](https://reactbits.dev/backgrounds/silk) |
| tsParticles | 8,995 | 星群密度、刷新频率与不可见时暂停的配置方式 | [仓库](https://github.com/tsparticles/tsparticles)、[Stars 预设](https://github.com/tsparticles/tsparticles/blob/main/presets/stars/README.md)、[引擎选项](https://particles.js.org/docs/interfaces/tsParticles_Engine.Options_Interfaces_IOptions.IOptions.html) |
| Vanta | 7,265 | 将环境动画隔离于页面内容，控制分辨率与销毁生命周期 | [仓库](https://github.com/tengbao/vanta)、[基础渲染器](https://github.com/tengbao/vanta/blob/master/src/_base.js) |

React Bits 当前的 [Aurora](https://github.com/DavidHDev/react-bits/blob/main/src/content/Backgrounds/Aurora/Aurora.jsx) 与 [Silk](https://github.com/DavidHDev/react-bits/blob/main/src/content/Backgrounds/Silk/Silk.jsx) 源码包含像素比限制、尺寸观察、页面可见性判断与资源释放；tsParticles 暴露 `fpsLimit`、`detectRetina`、`pauseOnBlur` 和 `pauseOnOutsideViewport`；Vanta 提供缩放参数与 `destroy()`。这些项目提供了可核查的成熟实现参考，但不能直接代表本应用的性能。

本应用保留现有原生 JavaScript / Electron 界面，使用自行编写的场景函数，没有为这些背景引入 React、tsParticles、Vanta、Three.js 或其他第三方动画运行时，也未复制上述项目的组件源码。背景无需远程图片、CDN、字体或视频文件。若后续改为直接采用第三方代码，应重新核查对应版本许可；例如 [React Bits 许可](https://github.com/DavidHDev/react-bits/blob/main/LICENSE.md) 含附加条款。

## 场景与性能策略

实现位于 [`dynamic-background.js`](../internal/entry/desktopui/static/dynamic-background.js)，由一个 Canvas 和一套动画循环承载三种场景：

| 场景 | 绘制方式 |
| --- | --- |
| 星空 | 确定种子的星点、缓慢漂移与明暗变化、径向星云光晕；流星每 18 秒出现一次，持续约 1.3 秒 |
| 极光 | 青绿、蓝色和紫色的分层光幕，以曲线起伏和细褶表达流动，辅以稀疏星点 |
| 丝绸流光 | 曲线褶皱与渐变高光随时间移动，形成烟紫、玫瑰与香槟色的绸缎质感 |

当前代码中的开销限制如下。这些是实现约束，不是跨设备帧率或资源占用的实测承诺。

- 使用 `requestAnimationFrame` 调度，通常每秒最多绘制约 30 帧；性能降级后约 20 帧。不会为每个星点、光幕或皮肤创建独立定时器。
- Canvas 像素比上限为 1.5，同时将后备画布控制在约 240 万像素预算内，避免高 DPI 或 4K 窗口成倍增加绘制成本。
- 星点最多 205 个；浏览器报告的逻辑处理器数量不超过 4 时，从 0.75 质量系数开始，减少分辨率、星点和曲线分段。
- 单次绘制耗时超过 24 毫秒时累积慢帧计数，计数达到 8 后降为 0.6 质量系数，并将绘制频率降至约 20 帧。快速帧会逐步减少慢帧计数。
- 切换到非动态皮肤、关闭动效、窗口隐藏、系统减少动态效果或强制颜色模式时取消动画循环。关闭动效或减少动态效果时保留静态画面；强制颜色模式隐藏装饰 Canvas。
- 恢复时重新建立时间基准，每步时间增量最多 100 毫秒，避免从后台返回后场景突然跳跃。调整窗口大小时重新计算分辨率；销毁时取消帧请求并移除监听器。
- Canvas 2D 不可用时隐藏 Canvas，保留 CSS 配色和渐变装饰，界面功能继续可用。

Canvas 固定在背景层，设置 `pointer-events: none` 和 `aria-hidden="true"`，不会接收点击、键盘焦点或屏幕阅读器内容。正文、表单、弹窗和提示使用不透明表面，动效通过留白与装饰区域呈现。颜色、操作层与背景分离，避免动态高光直接穿过正文。

## 长篇章节操作

章节工作台的整体区域、目录列表、正文和指令区独立滚动。宽窗呈现三栏，正文阅读工具栏不随长正文滚出，编辑模式的保存操作位于可滚动字段之外。切换章节让正文与编辑区回到开头，同时保留目录、指令区与整体工作区的位置；在同一章节切换阅读和编辑会保留草稿。

布局按章节工作区自身宽度调整：不超过 1039 像素时目录移到下方，正文与指令并列；不超过 639 像素时指令位于正文前方。顶部定位栏提供目录、正文、编辑和章节指令入口，避免窄窗用户需要穿过整章正文才能提交指令。主要滚动区域带有区域标签、可见滚动条与键盘焦点。

## 验证方式

在仓库根目录运行 `npm test` 检查外观偏好校验、恢复竞态、串行保存、减少动态效果与动态皮肤选择行为。Canvas 单元测试还检查场景绘制几何随时间变化、绘制频率、暂停与恢复、分辨率预算、慢帧降级和销毁清理。

运行 `npm run test:e2e` 进行真实 Electron 验收，包括八种主题在 100%/125%/150% 缩放下的布局、指定文字/表面对比度、Canvas 两帧实际像素变化、手动暂停、系统减少动态效果和隐藏/恢复，以及暂停偏好跨进程保存。章节夹具使用长正文和多章目录，检查滚动独立性、指令与编辑定位、未保存草稿保留。完整测试与环境要求见 [桌面开发文档](../desktop/README.md)。

设置 `AINOVEL_APPEARANCE_SCREENSHOTS` 可保存真实窗口中的各皮肤截图，供人工检查文字清晰度和遮挡。静态截图不验证动画持续变化，也不能单独证明低配置设备的流畅度；动画生命周期和交互仍需结合自动化断言及实际运行检查。
