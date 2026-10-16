# ainovel-cli 项目架构与业务逻辑分析报告

分析日期：2026-10-15。基准提交：638ca2445969b055a1013fedf8bf41c9d0b65c2a。

本报告基于源码、测试、CI 和内置 case 的静态分析。当前环境没有 Go 可执行文件，未运行 go test/go vet。报告中的运行行为是源码结论，不代表本机测试通过。

## 1. 核心结论

项目是一个 Go 单机、本地文件驱动的长篇小说创作引擎，支持 TUI 和 headless。确定性代码决定工作流，LLM 负责规划、写作、评审和语义裁定。它采用多个独立 Worker 身份，但一部书的 Engine 按安全边界顺序运行；不是分布式 Agent 集群。

核心设计可以概括为：**事实层确定，语义层自主**。flow.LoadState 从 Store 重建事实，纯函数 flow.Route 生成 Instruction，Host 调用 Worker，Worker 只能通过受限工具写入工件，工具追加 checkpoint 并更新进度。

项目最有价值的部分是可恢复工作流、章节接受基线、滚动卷弧规划与人机干预。主要维护成本集中于 Host 生命周期、跨文件协调、上下文构建和 TUI 状态；内容语义正确性仍依赖模型判断。

## 2. 技术与目录职责

| 模块 | 责任 |
|---|---|
| cmd/ainovel-cli | 参数解析、版本、自更新、配置、模式分支 |
| internal/entry | TUI、headless、首次启动与共创入口 |
| internal/host | 组合根、Engine、生命周期、预算、usage、锁、事件 |
| internal/flow | 事实状态加载与纯函数路由 |
| internal/arbiter | 启动规划、失败、用户干预的结构化语义裁定 |
| internal/agents | Worker 构建、工具白名单、上下文管理、StopGuard |
| internal/tools | 规划、正文、检查、提交、审阅、摘要工具 |
| internal/domain、chapterfacts | 状态、章节事实、卷弧、审阅和运行元数据 |
| internal/store | 文件 IO、子存储、投影、格式迁移、恢复记录 |
| internal/host/imp、revision | 外部作品导入与人工修改同步 |
| internal/host/sim、host/exp | 文风分析模拟与 TXT/EPUB 导出 |
| internal/diag、stylestat、eval | 诊断、跨章文风统计、离线评测 |
| assets | 内嵌角色提示词、协议与风格资源 |

Go module 声明 Go 1.25.5。主要依赖：agentcore 1.8.3、litellm 1.8.10、Bubble Tea 1.3.10、Bubbles 1.0.0、Lipgloss 1.1.0、flock 0.13.0。许可证为 Apache 2.0。

静态统计：325 个 Go 文件，其中 205 个生产文件、120 个测试文件；约 47,472 行生产代码、25,339 行测试代码，包含注释、空行与生成代码，不能视为覆盖率。internal 下有 24 个直接子目录。

## 3. 架构关系

```mermaid
flowchart TB
  Entry[CLI / TUI / Headless] --> Host[Host 组合根与生命周期]
  Host --> Engine[Engine 顺序循环]
  Engine --> Flow[LoadState + Router]
  Flow --> Store[文件事实层 Store]
  Engine --> Worker[Architect / Writer / Editor]
  Engine --> Arbiter[结构化语义裁定]
  Worker --> Model[ModelSet / Provider]
  Arbiter --> Model
  Worker --> Tools[受限工具与 StopGuard]
  Tools --> Store
  Host --> Other[Import / Sync / Simulation / Export]
  Other --> Store
  Store --> Observe[Diag / StyleStat / Eval]
```

这是主要协作关系，不是严格依赖 DAG。例如 revision 读取并重建投影，assets 装配也引用工具协议类型。不存在数据库或常驻 Web 服务；持久状态位于作品目录。单书 lease 阻止两个 Host 同时写同一目录；UI、metadata、usage 和干预可使用后台 goroutine，但主写作链按书串行。

## 4. 启动与运行生命周期

### 4.1 配置与入口

main 首先拦截 `eval` 子命令，再进入普通参数解析。一般运行依次处理版本、自更新、首次配置、配置加载和资源装配，最后选择 TUI 或 headless。配置分全局 `~/.ainovel/config.json` 与项目 `./.ainovel/config.json`；项目配置覆盖命名 provider/role 等字段，空字符串不能简单当成“清除继承值”。`provider` 是凭据配置的 key，模型协议由对应配置决定。

默认作品目录是当前 cwd 下的 `output/novel`。`--prompt` 和 `--prompt-file` 只用于 headless，二者互斥；prompt-file 为 `-` 可读取 stdin。不提供需求时尝试 Resume。TUI 提供快捷需求、共创、导入、修订、仿写、导出等命令；两种入口共用引擎，但并非所有操作都暴露为 headless 参数。`/start file` 读取的是创作需求，已有正文应走 `/import`。

### 4.2 Host 装配

`Host.New` 是组合根：

1. FillDefaults、验证配置，先取得书籍 lease。
2. 初始化日志和 Store，升级作品文件格式；当前为格式 3，未来版本拒绝打开。
3. 初始化 RunMeta、模型与定价元数据。
4. 加载 usage、必要时从 session 重放，启用自动保存。
5. 建立 Worker、style index、observer、预算和 advance gate。
6. 创建 Engine，向 TUI/headless 暴露事件与控制入口。

`StartPrepared` 在运行冲突、空需求、已有完成章节、预算等校验后启动新书；先保存原始 StartPrompt，再执行 arbiter 启动选型。decision 审计失败可 warning，但 PlanStartRecord 必须保存成功。这样首次规划没有落盘工件时仍可从需求恢复。

`Resume` 要求人工修改已同步，恢复持久干预与门控，刷新上下文并启动 Engine。运行态（idle/running/paused 等）与作品的 Phase/Flow 不是同一维度；终止进程也不等于删除作品事实。

### 4.3 Engine 一轮的动作

```mermaid
sequenceDiagram
  participant E as Engine
  participant F as LoadState / Router
  participant S as Store
  participant W as Worker
  participant T as Tool
  E->>E: 安全边界应用控制、预算与推进门控
  E->>F: 重建当前事实并求下一指令
  F->>S: 读取 Progress、工件、边界和反馈
  F-->>E: Instruction
  E->>W: Runner.Run(agent, task)
  W->>T: 多轮模型工具调用
  T->>S: 校验、写工件、更新状态/checkpoint
  T-->>W: 工具结果或错误
  W-->>E: 本次 Worker 结束
  E->>E: 检查失败/无进展，继续或暂停
```

普通 worker 同一指令首次失败后先重试一次，再由 failure arbiter 选择 retry/reroute/abort。死锁按 Agent+Task 跟踪，连续到 3 次开始咨询，达到 5 次熔断；不同非语义故障有单独处理。这里“失败重试”和“连续无进展”是两个机制，不应混为无限自动重试。

## 5. 状态机与路由决策

### 5.1 三个状态维度

| 维度 | 主要字段/取值 | 意义 |
|---|---|---|
| 作品阶段 Phase | init、premise、outline、writing、complete | 基础建设/写作/完本 |
| 当前 Flow | writing、reviewing、rewriting、polishing、steering | 当前处理的是新章、返工或控制 |
| Runtime/RunMeta | running/paused、planning tier、PendingSteer、advance/hold/permit | 进程生命周期与用户意图 |

Phase 的普通更新单向前进；`/reopen` 是受控回退，使用 ReopenedFromComplete/ReopenCount 标识重新开放的完本。FlowReviewing 是保留分支：当前生产路径没有主动写成 reviewing，Editor 实际评审时 Flow 仍可为 writing。

Progress 保存 CompletedChapters、WordCounts、CurrentVolume/Arc、PendingRewrites、章节计划/场景进度、HookHistory/StrandHistory。NextChapter 根据最大已完成章节加一计算，不是运行时自由指定的游标。分层模式的 TotalChapters 含骨架估计，不是锁定最终长度。

### 5.2 Router 优先级

| 优先级 | 当前事实 | 下一动作 |
|---:|---|---|
| 1 | Phase complete | 返回空指令，宿主生成结束信息 |
| 2 | 未进入 writing 且基础缺失、已有 tier | 同一规划师补齐或审计 |
| 3 | 返工队列非空 | writer 处理队首 rewrite/polish |
| 4 | Flow reviewing | 返回空指令，当前保留态 |
| 5 | Flow steering | 控制处理期间让位 |
| 6 | 失效/缺失聚合工件 | editor 重建 review/arc summary/volume summary |
| 7 | 人工修订影响未传播到计划 | architect 消费外部反馈 |
| 8 | 分层弧已完成 | 弧审阅 → 弧摘要 → 卷摘要 → 扩弧/追加卷/完本 |
| 9 | 非分层审阅周期到达 | editor global review（每 5 个完成章节） |
| 10 | 非分层大纲耗尽 | architect 决定完成或续接 |
| 11 | 其他可写状态 | writer 写下一章 |

LoadState 负责 IO，Route 不直接读写文件，也不调用模型。首次基础空白且无 tier 时，Engine 用持久 StartPrompt 走 planStartFallback，而不是 Router 硬猜规模。

## 6. 核心业务：规划、写作、审阅与完本

### 6.1 规划如何转化为可执行事实

Architect 有 `architect_short` 和 `architect_long` 两个 Worker 身份，共享 architect 模型角色。它们通过 save_book、save_foundation、audit_foundation、expand_next_arc、revise_outline 等工具落盘书籍元数据、premise、角色、世界规则、大纲和 compass。

短模式使用紧凑平面大纲；中长模式使用卷—弧—章节层次，初始细化当前/首个弧，其余保留骨架。扩展基于上一弧实际摘要、角色快照、伏笔、世界状态和反馈。Compass 保存结局方向、活跃长线和规模估计。规划层不应覆盖已经接受的正文事实。

基础审计是模型对跨文件设定的语义检查，通过 fingerprint 绑定当前工件；代码保证审计对应同一组输入，模型决定是否协调。StopGuard 只保证规划产物落盘，Router 再通过 FoundationMissing 检查基础是否真正齐全。

### 6.2 Writer 的工具协议及其保障边界

推荐顺序为：`novel_context → read_chapter → plan_chapter → draft_chapter → 回读草稿 → check_consistency → commit_chapter`；返工可通过 edit_chapter 修改草稿。

| 动作 | 产物/作用 | 代码能证明什么 |
|---|---|---|
| novel_context | 按角色装配当前事实 | 提供事实包，不能证明模型已理解 |
| plan_chapter | drafts/NN.plan.json、Progress/checkpoint | 计划已落盘，章节目标合法 |
| draft_chapter/edit_chapter | 草稿与 draft/edit checkpoint | 正文已保存，尚未成为完成章 |
| check_consistency | 对照事实与草稿 digest checkpoint | 对照材料已返回，不能证明语义通过 |
| commit_chapter | 正文、accepted record、摘要、状态、Progress | 顺序/边界/事实结构与恢复协议通过 |

**关键区别：上述工具顺序是推荐协议，并非全部被提交工具强制。** 当前 commit_chapter 不核对 plan/consistency_check 前置 checkpoint；Writer StopGuard 只要求本轮出现成功 commit，在未完成时根据 draft/check 状态催促。因而“有检查点”或“有 commit”不能推导出模型真正审查过正文。这是可强化的协议边界，而不是本报告发现了某部书的实际质量故障。

commit 校验下一章、返工队列、写作阶段、分层边界、事实字段，以及伏笔推进是否引用本章可见的种植事实。重写必须正文或标题发生变化。字数由 utf8.RuneCountInString 统计，包含标点/空白，不是英语 word count 或严格汉字数。

### 6.3 评审如何触发返工

ReviewEntry 由 verdict（accept/polish/rewrite）、issues、维度分数、affected chapters 和总结组成。提示词定义七维审阅，但实际维度名可扩展；分数不替代 verdict 的业务裁定。

save_review 先原子更新 Progress 的控制状态，再写 review 工件和 checkpoint。第二步失败时返工意图仍在；Writer 排空队列后，Router 会因为 review 工件缺失重新派 Editor。聚合审阅拒绝非目标范围，同内容重试可补齐，不同已存内容拒绝盲覆盖。

弧尾 review、arc summary、volume summary 分别是不同任务，StopGuard 通过 Task 字符串识别需要落盘的产物。当前实现能阻止“被派生成卷摘要却只做 review 就结束”，但字符串识别对重命名敏感。

### 6.4 完本与 reopen

分层收官卷 final=true 提供明确终点；收官弧/卷的审阅和摘要补齐后可自动完成。手工 complete_book 至少要求 writing、有完成章节、无返工队列、无未写详细大纲、compass 活跃长线清空。不同路径的守卫不能泛化成“所有未收伏笔都由代码检查”。

/reopen 为完本开放受控修订，不应因修改旧章而额外续写新章；重返工完成后重新标记 complete，ReopenCount 使多次完成审计可区分。

## 7. 文件事实、提交恢复与人工同步

### 7.1 哪些数据是源，哪些是投影

| 对象 | 保存内容 | 地位 |
|---|---|---|
| ChapterFacts/ChapterRecord | 标题、摘要、人物、事件、伏笔、关系、状态；接受正文/hash/revision/origin/style | 章节接受基线与重建依据 |
| chapters/*.md | 当前可编辑正文 | 人工工作副本，可能与接受基线不同 |
| Progress | 已完成章、字数、队列、阶段、卷弧 | 调度事实 |
| RunMeta | 启动需求、规划选型、控制意图、推进策略 | 运行/恢复事实 |
| summary/review/world 状态 | 摘要、评价、世界投影 | 下游上下文与路由依据 |
| checkpoint | seq/scope/step/artifact/digest/time | 已落盘动作的证据 |
| session/usage/log | 模型调用、成本、运行事件 | 观测与成本恢复 |

不能把所有 Markdown 当缓存：premise.md 是设定源，chapters/*.md 是可编辑正文；outline.md、characters.md 等是结构化数据的人类可读投影。checkpoint 日志不包含全部正文与状态，系统不是纯 Event Sourcing。

### 7.2 实际文件布局

```text
output/novel/
  book.md                     # meta/book.json 的可读投影
  premise.md
  outline.json / outline.md
  layered_outline.json / layered_outline.md
  characters.json / characters.md
  world_rules.json / world_rules.md
  timeline.jsonl / timeline.md
  foreshadow_ledger.json / foreshadow_ledger.md
  relationship_state.json / relationship_state.md
  chapters/01.md
  drafts/01.plan.json / 01.draft.md
  summaries/01.json / arc-v01a01.json / vol-v01.json
  reviews/01.json / 05-global.json
  meta/
    book.json / progress.json / run.json / format.json
    compass.json / foundation_audit.json
    checkpoints.jsonl / decisions.jsonl / state_changes.jsonl
    chapter_records/000001.json
    pending_commit.json / pending_revision.json
    outline_feedback.jsonl / user_rules.json / usage.json
    snapshots/v01a01.json
    import/*
    runtime/queue.jsonl
    sessions/agents/*.jsonl
  logs/*
```

路径按 Store 源码整理，文件按所用功能生成，并非每部书启动时全部存在。章节文件名的两位补零是最小宽度，不限制到 99 章。

### 7.3 commit Saga

首次调用先冻结完整参数和 DraftContent，再进入以下持久阶段：

| 阶段 | 工作 | 中断后处理 |
|---|---|---|
| started | 保存正文、记录、摘要、事实增量 | 使用冻结载荷幂等重放 |
| state_applied | 标记 Progress、生成边界信息与输出 | 继续后续阶段 |
| progress_marked | 固化 result/output | 补 checkpoint |
| signal_saved | checkpoint 已存在 | 清理进行中状态与 pending |

恢复时不用模型新生成参数或被覆盖的草稿，避免“旧事实 + 新正文”。重写也冻结正文，但先构造候选记录集、验证事实链，再接纳并重建投影；不会仅覆盖正文而放任世界状态陈旧。

这保证常见进程崩溃后的可重放一致性，不代表事务回滚。Store.crossMu 是协调锁，多文件写入仍可中途失败；append/expand 的同输入幂等与投影修复，也不等于都拥有同样的 durable intent。IO 使用同目录临时文件、文件 sync、rename，但未显式 sync 父目录；突然断电耐久性需要文件系统和平台层验证。

### 7.4 人工修改与 /sync

Sync 先恢复 pending revision，扫描正文 hash 与 accepted record 差异。为理解后文影响，它倒序分析修改章，获得新的 ChapterFacts，再按章节正序验证并保存 pending：prepared → records_applied → projections_applied。投影重建会使受影响的 review、弧/卷摘要、snapshot 失效，并将后续计划影响写入 feedback。

写作前 requireCleanChapters 会阻止未同步修改。这是“人工编辑与模型事实分离”的必要协议；文件可编辑不代表进度和世界状态会实时自动更新。

## 8. 上下文、提示词和模型接入

### 8.1 分层记忆

Writer 上下文区分工作记忆、情节记忆与参考资源：当前计划/大纲/章节状态放在工作区，历史章节通过摘要、相关章建议、卷弧总结、人物快照和世界状态压缩召回。Architect 上下文聚焦基础设定、当前卷弧、compass 和计划偏差，不把所有正文塞入提示词。

ContextProfile 随规模缩小近期窗口：15 章以内 summary/timeline 窗口为 10/10，50 章以内为 5/8，超过 50 为 3/5。这个规模策略中的 Layered 标记与 Progress 的实际分层大纲状态是不同概念，不能据此断言超过 50 章自动完成模式迁移。较长作品还按伏笔、角色、状态、关系推荐相关历史；当前是结构化事实召回，未见向量数据库。

上下文管理使用 ToolResultMicrocompact、角色裁剪、Writer 的 StoreSummaryCompact，以及必要时 FullSummary。模型切换后按新窗口重建管理器；压缩后会补回计划/大纲/状态包。事实压缩能减少成本，但摘要遗漏仍会积累，最终质量需要跨弧评测证明。

### 8.2 资源覆盖与用户规则

角色协议、voice、style 和 references 由 go:embed 携带，可使用全局与作品级 style 资源覆盖；voice/anti-ai 追加、同名 style 替换。生产、测试、eval 共用 BuildWriterPrompt，当前签名是 writerPrompt、voice、style 三个参数。

用户规则从全局、项目和启动自然语言合成为本书快照；语义正规化失败可以保留原文降级，持久化失败会阻断。内置 lint 和结构化规则输出 violations，forbidden chars/phrases 标为 error，fatigue words 标为 warning；**规则 severity 不等于 commit 阻断**，当前 rules.Check 只返回事实，commit 将结果附在输出中。

### 8.3 模型契约

ModelSet 管理角色模型、Provider 与 failover，Worker 具备独立上下文和工具白名单。llmcontract.Execute 选择原生 JSON Schema 或 prompt JSON 契约：先 schema 校验再 DTO 解码和业务校验；prompt 模式结构错误及两种模式的业务错误会反馈给模型修正，直到成功或 context 结束。原生 schema 违约立即失败，schema 已通过但 DTO 解码失败视为代码契约问题。

因此应区分网络重试、格式自愈、业务纠错、Worker 重派四层循环。结构化函数没有统一固定纠错次数，超时和取消边界需要由调用方控制。

## 9. 用户控制、预算与观测

### 9.1 自然语言干预

用户干预不是直接拼进 Writer 聊天。Arbiter 根据 phase、flow、next、queue、checkpoint、近期决策和推进策略，生成独立可选的 Answer/Rules/Hold/Reopen/Dispatch。Host 按 answer → rules → hold → reopen → dispatch 执行；涉及运行任务的修改排到安全边界。事实已变化的 dispatch 会重新裁定，PendingSteer 持久化支持中断重放。

ChapterAdvanceGate 支持 auto/review，/next 只授权确切下一章；返工与恢复不会随意消耗新章 permit。hold 支持边界、返工排空、指定章后停止，控制意图跨重启保留。

### 9.2 成本

usage.json 是总量缓存，agent sessions 可按消息中的 provider/model 重放。成本依赖上游 usage 和定价；missing usage、零成本连续出现等情况有 warning。默认预算在 worker 边界停止，另有 hardstop 选项，因此预算是运行控制而非精确预付硬上限。Prompt cache 使用稳定书籍/角色/session 信息，但实际收益取决于 Provider。

### 9.3 观测不是控制源

observer 给 TUI/headless 转发事件、stream、工具详情和错误；显示层可截断，日志保留更多信息。事件 channel 与 stream channel 有界（100/256），满时丢弃旧项，不能把屏幕流当完整正文或审计记录。

diag 读取持久工件、sessions、logs，生成诊断与脱敏导出，不自动修复或改变调度。stylestat 统计跨章重复句式、词癖、结尾和标题等，达到样本阈值后反馈 Writer；它衡量风格信号，不等于文学质量评分。

## 10. 外围业务拆解

### 10.1 导入已有小说

导入是独立的语义编译流水线，不是放任一个 Import Agent 自由操作：

1. 读取源文本，按 UTF-8/BOM/GB18030 解码，只规范化换行并保存源快照。
2. 建立稳定行/分片坐标，由模型识别章节与附属文本边界，代码校验覆盖、顺序和 anchor。
3. 生成预览与确认工件；--yes 可自动接受，但有切分容错说明时必须人工核对。
4. 按模型窗口分批分析，再做 range digest 与整书综合。
5. 验证 foundation 和故事 open/closed/uncertain 状态，必要时由用户裁定。
6. 发布基础设定及原文章节，复用 ChapterCommit Saga；完成后默认 hold，显式 continue 才续写。

meta/import 保存 manifest、intent、source、guidance、segmentation、confirmation、analysis、synthesis、failure 等。NextAction 从工件/InputDigest 推断，UI stage 不是恢复依据。raw/normalized hash、prompt/semantic version 和模型预算身份使缓存可正确失效。

发布前的分析与验证在隔离 workspace 中完成，但 publish 本身是多步可恢复写入，可能已部分落盘，不能称为一次性全书原子导入。Engine 会拒绝在未完成导入上普通续写。输入处理针对文本，未见 EPUB/PDF 内容解析器。

### 10.2 文风模拟

Simulation 扫描 txt/md/markdown，按路径与 SHA256 跳过已分析资料，生成 source report 并合并为 SimulationProfile。单源超 60,000 rune 时保留头部约四分之三与尾部四分之一，中间截断。因此 profile 是采样后的风格分析，不是全本训练或模型微调。

Profile 以结构化风格指导注入 Worker。原文是分析输入，生成侧主要使用提炼结果；这与“导入正文后续写”是不同业务。

### 10.3 导出

Exporter 依据 Progress 选择已完成章，可指定范围；正文从 chapters/NN.md 读取，标题优先取已提交摘要，卷分隔来自分层大纲。支持 TXT/EPUB，目标已存在默认拒绝，显式 overwrite 才覆盖。读取的是当前章节文件，不能把它描述为强制从 ChapterRecord.Content 导出。

### 10.4 离线评测

正确入口是 `ainovel-cli eval --cases ...`，不带额外 run 子命令。支持 single、baseline/variant A/B、repeat、超时、章数上限、CI 静默模式、独立配置与 Markdown/JSON 报告。Baseline 使用内置资源避免本机覆盖污染；Variant 通过同一提示词装配入口替换。

指标来自 diag、case contract、stylestat、cost、tool count。退出码 0 为 PASS/WARN、1 为 case FAIL、2 为用法/配置错误。当前仓库只带 architect_short、architect_long、writer_first_chapter 三个 smoke case。虽然 schema 有 rubric 字段，LLM Judge 仍是后续层；不能宣称已经自动完成文学质量评测。

## 11. 工程质量与交付

| 项目 | 源码/配置可见情况 | 本次验证边界 |
|---|---|---|
| 单元与契约测试 | Router、StopGuard、agentcore、提交恢复、锁、导入、修订、导出、eval | 已阅读关键测试，未执行 |
| CI | Ubuntu/Windows gofmt、vet、全量 test；Linux installer syntax/race | 未查询远端运行结果 |
| 发布 | GoReleaser 覆盖多系统 amd64/arm64 与校验和 | 未构建二进制 |
| 容器 | 多阶段 Go 构建、Alpine runtime，挂载配置和工作区 | 未构建/启动 |
| 真实模型闭环 | 需要配置 Provider 和预算 | 本次未调用模型生成小说 |

静态代码结构显示项目重视重启与边界测试，但测试文件数量不能证明长篇质量或无并发缺陷。当前环境缺少 Go，本报告没有用“通过测试”代替源码推断。

## 12. 架构评价与改进路线

### 12.1 已有优势

- 纯函数 Router 使调度结果可复现；模型语义职责与可枚举规则分开。
- 章节接受基线、正文工作副本、世界投影分离，支持人工修订和重建。
- Saga/pending、工件 digest、lease 和 stale decision 提供明确恢复边界。
- 卷弧滚动规划和角色化上下文适合长篇持续推进。
- 单机文件产物可检查、可备份，TUI 与 headless 复用业务核心。

### 12.2 风险与优先级

下表是源码支持的维护风险或保障边界，不是已经复现的生产故障清单。

| 优先级 | 发现/证据 | 影响 | 建议与验证目标 |
|---|---|---|---|
| P1 | 推荐 plan/check 顺序并非全部提交前置守卫；check 只返回对照数据 | 完成态可能被误解成已审查 | 明确“检查执行”契约；若业务要求强制，校验当前草稿 digest 与检查证据，同时为导入保留显式通道 |
| P1 | Editor 以 Task 字符串判断摘要，Engine 以 Agent+Task 识别重复 | 文案改动影响控制行为 | 增加 TaskKind/Target，测试改动提示文本不影响调度 |
| P1 | bundled eval 只有三个 smoke case | 多弧、干预和恢复缺少真实模型回归数据 | 补 recovery/steering/longform case，故障注入验证事实一致性 |
| P2 | host.go 约 1,996 行、commit 约 1,010 行，上下文/TUI 也集中 | 生命周期和跨域修改耦合 | 沿 control、lifecycle、commit replay、projection 拆分，保留外部接口 |
| P2 | 结构操作无统一 durable intent；IO 没有目录 fsync | 保障范围不一致 | 明确操作级恢复契约；对关键结构扩展补 intent/replay，按平台验证掉电耐久性 |
| P2 | JSON/schema 自愈可能持续到 context 结束 | 时间/成本难预测 | 给业务阶段设置可观测的次数、墙钟或预算边界 |
| P2 | usage/pricing 依赖外部，worker 边界结算 | 可能超出预算 | 展示成本可信度和最大调用窗口，保留缺失 warning |
| P2 | 文档路径/接口漂移 | 使用/排障误导 | 从常量与签名生成 manifest/示例并检查链接 |
| P3 | channel 丢旧 stream、长期事实/日志文件增长 | 展示缺片、读放大 | 保持工件权威；真实规模测量后增加索引、日志归档 |

文档漂移的具体例子：docs/observability.md 把 layered_outline/characters 写在 meta 下，而 Store 实际存于作品根；docs/voice-layer.md 展示四参 BuildWriterPrompt，而当前源码为三参。FlowReviewing 的保留语义也应在业务文档明确。

### 12.3 分阶段落地

第一阶段先补契约与证据：TaskKind、工具链保障说明、artifact 路径文档、典型 Router/commit/reopen/import 回归。验收应检查最终正文 hash、accepted facts、Progress、pending 清空、checkpoint 与投影一致。

第二阶段补集成恢复：在 Saga 各阶段注入失败，覆盖导入部分发布、修改早期伏笔、返工队列、stale intervention、hold/permit、跨进程 lease 和 Provider failover。

第三阶段做长篇质量评测：以 5–20 章及多卷/多弧样本运行 repeat，分别观察确定性门禁、摘要事实漂移、角色/伏笔召回、风格变化、成本和工具调用。Judge 可作为附加证据，不能替代事实门禁。

最后才依据性能数据决定数据库、向量检索或并行化。当前单书串行是保持因果一致性的设计选择，没有证据证明分布式化是首要需求。

## 13. 源码导航与核对入口

以下路径相对本报告所在 docs 目录；行号为本次基准提交位置。

| 问题 | 源码入口 |
|---|---|
| 程序如何启动 | [main.go](../cmd/ainovel-cli/main.go) |
| Host 如何装配/开书/恢复 | [host.go](../internal/host/host.go)（102/376/551 行） |
| 每一轮如何运行 | [engine.go](../internal/host/engine.go)（150 行） |
| 事实如何转换为下一任务 | [state.go](../internal/flow/state.go)（14 行）、[router.go](../internal/flow/router.go)（105 行） |
| 状态对象是什么 | [runtime.go](../internal/domain/runtime.go)、[revision.go](../internal/domain/revision.go) |
| Worker 如何获得权限 | [build.go](../internal/agents/build.go)（109 行） |
| 什么情况下允许退出 | [subagent_guards.go](../internal/agents/guard/subagent_guards.go)（127/148/167 行） |
| 上下文与召回 | [novel_context.go](../internal/tools/novel_context.go)、[context_manager.go](../internal/agents/context_manager.go) |
| 章节如何提交与恢复 | [commit_chapter.go](../internal/tools/commit_chapter.go)（77/442 行） |
| 一致性检查究竟做什么 | [check_consistency.go](../internal/tools/check_consistency.go) |
| Review 如何形成返工 | [save_review.go](../internal/tools/save_review.go)（69 行） |
| 文件布局与 IO | [store.go](../internal/store/store.go)、[world.go](../internal/store/world.go)、[io.go](../internal/store/io.go)（36 行） |
| 接受基线和检查点 | [chapter_records.go](../internal/store/chapter_records.go)、[checkpoints.go](../internal/store/checkpoints.go) |
| 人工修改如何传播 | [service.go](../internal/revision/service.go)（39 行） |
| 用户干预如何裁定 | [intervention.go](../internal/arbiter/intervention.go) |
| 导入如何切分/恢复/发布 | [imp/runner.go](../internal/host/imp/runner.go)（154 行） |
| 仿写如何分析 | [sim/runner.go](../internal/host/sim/runner.go)（19 行） |
| 导出取什么正文 | [exporter.go](../internal/host/exp/exporter.go)（22 行） |
| 模型契约如何自愈 | [execute.go](../internal/llmcontract/execute.go)（82 行） |
| 机械规则是否阻断 | [checker.go](../internal/rules/checker.go)（17 行） |
| 评测与测试保障 | [eval.go](../internal/eval/eval.go)（20 行）、[CI](../.github/workflows/ci.yml)、[smoke cases](../evals/cases/smoke) |
