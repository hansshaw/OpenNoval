(() => {
  "use strict";

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

  const INPUT_IDS = [
    "prompt",
    "cocreate-reply",
    "cocreate-draft",
    "intervention",
    "plan-feedback",
    "chapter-feedback",
    "batch-count",
    "audit-chapter",
  ];

  const VIEW_META = {
    overview: ["创作总览", "CREATIVE CONTROL ROOM"],
    projects: ["项目中心", "MULTI-PROJECT STUDIO"],
    profile: ["创作资料与 Agent", "BOOK PROFILE"],
    style: ["风格语料", "STYLE DISTILLATION"],
    planning: ["规划审核", "PLANNING REVIEW"],
    chapters: ["章节工作台", "CHAPTER WORKBENCH"],
    audit: ["审计中心", "TRACE & AUDIT"],
    activity: ["活动记录", "LIVE ACTIVITY"],
  };

  const AGENT_ROLES = [
    {
      key: "architect",
      name: "架构师",
      mark: "A",
      hint: "规划故事结构、章节目录、人物弧线与世界规则。",
    },
    {
      key: "writer",
      name: "写作者",
      mark: "W",
      hint: "根据已确认规划和连续性事实撰写章节正文。",
    },
    {
      key: "editor",
      name: "审稿人",
      mark: "E",
      hint: "检查逻辑、节奏、人物一致性和章节质量。",
    },
    {
      key: "import_segment",
      name: "语料切分",
      mark: "S",
      hint: "拆分导入文本并识别自然段落与章节边界。",
    },
    {
      key: "import_analyze",
      name: "语料分析",
      mark: "I",
      hint: "分析导入文本的元素、叙事策略和风格特征。",
    },
    {
      key: "import_synthesize",
      name: "语料综合",
      mark: "Σ",
      hint: "将分析结果整理为可复用的结构化风格画像。",
    },
  ];

  const STAGE = {
    new: {
      title: "先澄清创作意图",
      description: "与共创助手完成多轮问答，明确人物、冲突、世界规则和篇幅后，再确认生成正式规划。",
      step: 0,
    },
    planning: {
      title: "正在生成规划",
      description: "架构师正在生成故事内核、角色、世界规则与章节目录。",
      step: 0,
    },
    "plan-review": {
      title: "等待确认规划",
      description: "阅读完整规划，可多次提交修改意见；确认当前版本后才会开始正文。",
      step: 1,
    },
    ready: {
      title: "规划已确认",
      description: "设置本批次章节数量后开始写作。每章完成都会停下等待反馈或验收。",
      step: 2,
    },
    writing: {
      title: "正文批次进行中",
      description: "当前批次完成后会暂停，等待你审阅最新章节。",
      step: 2,
    },
    "chapter-review": {
      title: "等待章节验收",
      description: "阅读最新章节，提交修改意见让作者调整，或确认本章继续。",
      step: 3,
    },
    complete: {
      title: "作品已完成",
      description: "可以继续阅读、导出作品，或切换到其他创作项目。",
      step: 3,
    },
  };

  const state = {
    data: null,
    config: null,
    workspaces: [],
    planning: null,
    style: null,
    audit: null,
    cocreate: null,
    cocreateDirectory: "",
    cocreatePreview: "",
    cocreateDraftDirty: false,
    events: [],
    stream: "",
    selectedChapter: 0,
    selectedSource: "",
    reading: null,
    view: "overview",
    epoch: 0,
    source: null,
    streamConnected: false,
    loading: {},
    profileDirectory: "",
    toastTimer: null,
    refreshTimer: null,
    styleBusyLocal: false,
  };

  function normalize(value) {
    if (Array.isArray(value)) return value.map(normalize);
    if (!value || typeof value !== "object") return value;
    return Object.fromEntries(
      Object.entries(value).map(([key, child]) => {
        const normalizedKey =
          key === "ID" ? "id" : key ? key[0].toLowerCase() + key.slice(1) : key;
        return [normalizedKey, normalize(child)];
      }),
    );
  }

  async function api(path, options = {}) {
    const request = { ...options, headers: { ...(options.headers || {}) } };
    if (request.body !== undefined) {
      request.headers["Content-Type"] = "application/json";
      request.body = JSON.stringify(request.body);
    }
    const response = await fetch(path, request);
    const contentType = response.headers.get("content-type") || "";
    let payload;
    if (contentType.includes("application/json")) {
      payload = await response.json().catch(() => ({}));
    } else {
      payload = await response.text().catch(() => "");
    }
    payload = normalize(payload);
    if (!response.ok) {
      throw new Error(
        (payload && payload.error) || `请求失败（HTTP ${response.status}）`,
      );
    }
    return payload;
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = String(text ?? "");
    return node;
  }

  function button(label, className, onClick) {
    const node = el("button", className, label);
    node.type = "button";
    if (onClick) node.addEventListener("click", onClick);
    return node;
  }

  function option(value, label, selected = false) {
    const node = el("option", "", label);
    node.value = value;
    node.selected = selected;
    return node;
  }

  function fillSelect(select, values, selectedValue, blankLabel = "") {
    select.replaceChildren();
    if (blankLabel) select.append(option("", blankLabel, !selectedValue));
    const seen = new Set();
    for (const value of values || []) {
      if (!value || seen.has(value)) continue;
      seen.add(value);
      select.append(option(value, value, value === selectedValue));
    }
    if (selectedValue && !seen.has(selectedValue)) {
      select.append(option(selectedValue, selectedValue, true));
    }
    if (!selectedValue && !blankLabel && select.options.length) {
      select.options[0].selected = true;
    }
  }

  function pretty(value, empty = "暂无数据") {
    if (
      value === null ||
      value === undefined ||
      value === "" ||
      (Array.isArray(value) && value.length === 0) ||
      (typeof value === "object" &&
        !Array.isArray(value) &&
        Object.keys(value).length === 0)
    ) {
      return empty;
    }
    if (typeof value === "string") return value;
    try {
      return JSON.stringify(value, null, 2);
    } catch (_) {
      return String(value);
    }
  }

  function renderJson(container, value, empty = "暂无数据") {
    container.replaceChildren();
    const text = pretty(value, empty);
    if (typeof value === "string") {
      container.append(el("p", "prose-block", text));
      return;
    }
    container.append(el("pre", "json-pre", text));
  }

  function number(value) {
    return Number(value || 0).toLocaleString("zh-CN");
  }

  function formatTime(value) {
    if (!value) return "刚刚";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return date.toLocaleString("zh-CN", {
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  }

  function shortPath(path) {
    if (!path) return "尚未选择项目";
    const parts = String(path).split(/[\\/]/).filter(Boolean);
    return parts.length > 2 ? `…/${parts.slice(-2).join("/")}` : path;
  }

  function toast(message, kind = "") {
    const node = $("#toast");
    node.textContent = message;
    node.className = `toast show ${kind}`.trim();
    clearTimeout(state.toastTimer);
    state.toastTimer = setTimeout(() => {
      node.className = "toast";
    }, kind === "error" ? 7000 : 4000);
  }

  function setAlert(message, kind = "") {
    const node = $("#alert");
    node.textContent = message || "";
    node.className = `alert ${kind} ${message ? "" : "hidden"}`.trim();
  }

  function setBusy(key, value) {
    state.loading[key] = value;
    syncDisabledControls();
  }

  function isBusy() {
    const data = state.data || {};
    return Boolean(
      data.busy ||
        data.stopping ||
        data.snapshot?.isRunning ||
        Object.values(state.loading).some(Boolean),
    );
  }

  function isEngineRunning() {
    return Boolean(
      state.data?.busy ||
        state.data?.stopping ||
        state.data?.snapshot?.isRunning,
    );
  }

  function saveInputs(directory = state.data?.directory) {
    if (!directory) return;
    const values = {};
    for (const id of INPUT_IDS) {
      const input = document.getElementById(id);
      if (input) values[id] = input.value;
    }
    try {
      localStorage.setItem(`ainovel-inputs:${directory}`, JSON.stringify(values));
    } catch (_) {
      // Local drafts are a convenience; server state remains authoritative.
    }
  }

  function restoreInputs(directory) {
    let values = {};
    try {
      values = JSON.parse(
        localStorage.getItem(`ainovel-inputs:${directory}`) || "{}",
      );
    } catch (_) {
      values = {};
    }
    for (const id of INPUT_IDS) {
      const input = document.getElementById(id);
      if (!input) continue;
      input.value =
        values[id] ?? (id === "batch-count" ? "1" : "");
    }
  }

  function setView(view, updateHash = true) {
    if (!VIEW_META[view]) view = "overview";
    state.view = view;
    for (const section of $$(".view")) {
      section.classList.toggle("hidden", section.id !== `view-${view}`);
    }
    for (const nav of $$(".nav-item")) {
      nav.classList.toggle("active", nav.dataset.view === view);
      nav.setAttribute(
        "aria-current",
        nav.dataset.view === view ? "page" : "false",
      );
    }
    const [title, eyebrow] = VIEW_META[view];
    $("#page-title").textContent = title;
    $("#page-eyebrow").textContent = eyebrow;
    if (updateHash && location.hash !== `#${view}`) {
      history.replaceState(null, "", `#${view}`);
    }
    if (view === "projects") loadWorkspaces();
    if (view === "overview") loadCoCreate();
    if (view === "profile") renderProfile(true);
    if (view === "style") loadStyle();
    if (view === "planning") loadPlanning();
    if (view === "chapters") {
      renderChapters();
      if (state.selectedChapter) {
        readChapter(state.selectedChapter, state.selectedSource);
      }
    }
    if (view === "audit") loadAudit();
    if (view === "activity") renderActivity();
  }

  function applyState(data) {
    const previousDirectory = state.data?.directory || "";
    const nextDirectory = data.directory || "";
    state.data = data;
    if (previousDirectory !== nextDirectory) {
      state.selectedChapter = Number(data.workflow?.latestChapter || 0);
      state.selectedSource = "";
      state.reading = null;
      state.planning = null;
      state.style = null;
      state.audit = null;
      state.profileDirectory = "";
      state.cocreate = null;
      state.cocreateDirectory = "";
      state.cocreatePreview = "";
      state.cocreateDraftDirty = false;
      restoreInputs(nextDirectory);
      if (nextDirectory) loadCoCreate();
    }
    renderCore();
    renderProfile(previousDirectory !== nextDirectory);
    syncDisabledControls();
  }

  async function refreshState({ quiet = false } = {}) {
    const epoch = state.epoch;
    try {
      const data = await api("/api/state");
      if (epoch !== state.epoch) return null;
      applyState(data);
      return data;
    } catch (error) {
      if (!quiet) setAlert(error.message, "error");
      return null;
    }
  }

  async function loadConfig() {
    try {
      state.config = await api("/api/config");
      renderConfigDependentControls();
      return state.config;
    } catch (error) {
      setAlert(`模型配置读取失败：${error.message}`, "error");
      return null;
    }
  }

  async function loadWorkspaces() {
    try {
      const workspaces = await api("/api/workspaces");
      state.workspaces = Array.isArray(workspaces) ? workspaces : [];
      renderProjects();
    } catch (error) {
      setAlert(`项目列表读取失败：${error.message}`, "error");
    }
  }

  async function refreshAll({ quiet = false } = {}) {
    const epoch = state.epoch;
    const results = await Promise.allSettled([
      api("/api/state"),
      api("/api/config"),
      api("/api/workspaces"),
    ]);
    if (epoch !== state.epoch) return;
    if (results[0].status === "fulfilled") {
      applyState(results[0].value);
    } else if (!quiet) {
      setAlert(results[0].reason.message, "error");
    }
    if (results[1].status === "fulfilled") {
      state.config = results[1].value;
      renderConfigDependentControls();
    }
    if (results[2].status === "fulfilled") {
      state.workspaces = Array.isArray(results[2].value)
        ? results[2].value
        : [];
      renderProjects();
    }
  }

  function renderCore() {
    const data = state.data || {};
    const snapshot = data.snapshot || {};
    const project = data.project || {};
    const workflow = data.workflow || {};
    const stage = STAGE[workflow.stage] || STAGE.new;
    const projectName =
      project.name ||
      snapshot.bookTitle ||
      data.directory?.split(/[\\/]/).filter(Boolean).pop() ||
      "未命名项目";

    $("#current-project-name").textContent = projectName;
    $("#current-project-path").textContent = shortPath(data.directory);
    $("#current-project-path").title = data.directory || "";

    const connection = $("#connection");
    const connected = Boolean(data.ready && state.streamConnected);
    connection.className = `connection ${
      connected ? "ready" : data.error ? "error" : ""
    }`.trim();
    $("span", connection).textContent = connected
      ? "工作台已连接"
      : data.ready
        ? "正在同步"
        : "等待模型配置";

    $("#runtime-pill").textContent = data.stopping
      ? "正在暂停"
      : data.busy
        ? `正在执行 ${data.job || "任务"}`
        : snapshot.isRunning
          ? "创作运行中"
          : data.ready
            ? "引擎就绪"
            : "等待配置";
    $("#workflow-title").textContent = stage.title;
    $("#workflow-description").textContent = data.error || stage.description;
    for (const step of $$(".workflow-steps [data-step]")) {
      step.classList.toggle(
        "active",
        Number(step.dataset.step) === stage.step,
      );
      step.classList.toggle(
        "complete",
        Number(step.dataset.step) < stage.step,
      );
    }

    const currentChapter =
      Number(snapshot.currentChapter || 0) ||
      Number(workflow.latestChapter || 0);
    const completed = Number(snapshot.completedCount || 0);
    const total = Number(snapshot.totalChapters || 0);
    $("#metric-chapter").textContent = currentChapter
      ? `第 ${currentChapter} 章`
      : "—";
    $("#metric-progress").textContent = total
      ? `${completed} / ${total} 章已完成`
      : workflow.latestChapter
        ? `已完成 ${workflow.latestChapter} 章`
        : "等待创作";
    $("#metric-words").textContent = number(snapshot.totalWordCount);
    $("#metric-stage").textContent = snapshot.phase || stage.title;
    $("#metric-batch").textContent = workflow.batchEnd
      ? `批次至第 ${workflow.batchEnd} 章 · 已验收 ${workflow.acceptedChapter || 0} 章`
      : `默认每批 ${workflow.batchSize || 1} 章`;
    $("#metric-cost").textContent = `$${Number(
      snapshot.totalCostUSD || 0,
    ).toFixed(2)}`;
    $("#metric-tokens").textContent =
      `输入 ${number(snapshot.totalInputTokens)} · 输出 ${number(snapshot.totalOutputTokens)}`;
    $("#phase-label").textContent = snapshot.statusLabel || snapshot.flow || "";

    renderAgents(snapshot.agents || []);
    renderOutlinePreview(snapshot.outline || []);
    renderWorkflowControls(workflow);
    $("#live-output").textContent =
      state.stream || "等待创作引擎输出……";
    if (state.view === "activity") renderActivity();
    if (state.view === "chapters") renderChapters();
    renderCoCreate();
    syncDisabledControls();
  }

  function renderWorkflowControls(workflow) {
    $("#go-planning").disabled = !workflow.fingerprint && !workflow.started;
    $("#go-latest").disabled = !workflow.latestChapter;
    $("#batch-count").value =
      document.activeElement === $("#batch-count")
        ? $("#batch-count").value
        : String(workflow.batchSize || 1);
    $("#chapter-feedback-title").textContent = workflow.latestChapter
      ? `批次反馈与验收 · 最新第 ${workflow.latestChapter} 章`
      : "章节反馈与验收";
    $("#batch-status").textContent = workflow.batchEnd
      ? workflow.stage === "chapter-review"
        ? `本批次已全部完成 · 至第 ${workflow.batchEnd} 章，等待统一审核`
        : `本批次至第 ${workflow.batchEnd} 章 · 已验收至 ${workflow.acceptedChapter || 0} 章`
      : "尚未开始正文批次";
    $("#accept-chapter").textContent =
      workflow.stage === "chapter-review"
        ? "确认本批次"
        : "等待批次完成";
  }

  function renderAgents(agents) {
    const container = $("#agent-list");
    container.replaceChildren();
    $("#agent-count").textContent = `${agents.length} 位`;
    if (!agents.length) {
      container.append(
        el("p", "empty-state", "开始创作后显示 Agent 状态。"),
      );
      return;
    }
    for (const agent of agents) {
      const row = el("article", "agent-row");
      const mark = el("span", "agent-mark", (agent.name || "A")[0]);
      const copy = el("div", "agent-copy");
      copy.append(
        el("strong", "", agent.name || "Agent"),
        el(
          "small",
          "",
          agent.summary || agent.taskKind || agent.taskId || "等待任务",
        ),
      );
      const badge = el(
        "span",
        `agent-state ${agent.state || "idle"}`,
        agent.state || "idle",
      );
      row.append(mark, copy, badge);
      container.append(row);
    }
  }

  function renderOutlinePreview(outline) {
    const container = $("#outline-preview");
    container.replaceChildren();
    if (!outline.length) {
      container.append(
        el("p", "empty-state", "规划生成后显示章节地图。"),
      );
      return;
    }
    for (const item of outline.slice(0, 7)) {
      const row = el("div", "outline-row");
      row.append(
        el("span", "outline-number", String(item.chapter || "·").padStart(2, "0")),
      );
      const copy = el("div", "outline-copy");
      copy.append(
        el("strong", "", item.title || "待定标题"),
        el("small", "", item.coreEvent || "详细安排待生成"),
      );
      row.append(copy);
      container.append(row);
    }
  }

  function syncDisabledControls() {
    const data = state.data || {};
    const workflow = data.workflow || {};
    const busy = isBusy();
    const running = isEngineRunning();
    const ready = Boolean(data.ready);
    const cocreate = state.cocreate || {};
    const cocreateStarted = Boolean(cocreate.started);
    const cocreateBusy = Boolean(state.loading.cocreate);
    const cocreateRetry = Boolean(cocreate.confirmed) && !cocreateStarted;
    const cocreateConfined = Boolean(
      cocreateStarted ||
        cocreate.confirmed ||
        (cocreate.transcript || []).length,
    );
    const cocreateDraft =
      document.getElementById("cocreate-draft")?.value || "";

    $("#start-project").disabled =
      busy ||
      !ready ||
      workflow.stage !== "new" ||
      cocreateConfined ||
      cocreateBusy;
    $("#cocreate-reply").disabled =
      busy ||
      !cocreateConfined ||
      cocreateStarted ||
      Boolean(cocreate.confirmed);
    $("#send-cocreate").disabled =
      busy ||
      !(cocreate.transcript || []).length ||
      cocreateStarted ||
      Boolean(cocreate.confirmed);
    $("#confirm-cocreate").disabled =
      busy ||
      (!cocreate.canConfirm && !cocreateRetry) ||
      !cocreateDraft.trim();
    $("#confirm-cocreate").textContent = cocreateRetry
      ? "重试生成规则与目录"
      : "确认创意并生成规划";
    $("#cocreate-draft").disabled =
      busy || cocreateStarted || Boolean(cocreate.confirmed);
    $("#reset-cocreate").disabled =
      busy ||
      !cocreateConfined ||
      cocreateStarted;
    $("#resume-run").disabled = running || data.stopping;
    $("#pause-run").disabled = (!running && !data.busy) || data.stopping;
    $("#export-txt").disabled = !ready || busy;
    $("#export-epub").disabled = !ready || busy;
    $("#create-project").disabled = busy;
    $("#refresh-projects").disabled = busy;
    $("#save-project-profile").disabled = busy || !data.directory;
    $("#run-style-analysis").disabled =
      busy || !ready || state.styleBusyLocal;
    $("#approve-plan").disabled =
      busy || !workflow.canApprove || workflow.approved;
    $("#revise-plan").disabled = busy || !workflow.canApprove;
    $("#start-batch").disabled =
      busy || !workflow.approved || workflow.stage === "chapter-review";
    $("#accept-chapter").disabled =
      busy || workflow.stage !== "chapter-review";
    $("#send-chapter-feedback").disabled =
      busy || workflow.stage !== "chapter-review";
    for (const command of $$("[data-command]")) {
      command.disabled =
        busy || (command.dataset.command !== "sync" && !ready);
    }
    for (const cardButton of $$("[data-project-action]")) {
      cardButton.disabled = busy;
    }
  }

  function renderConfigDependentControls() {
    const config = state.config || {};
    const project = state.data?.project || {};
    fillSelect(
      $("#profile-style"),
      config.styles || [],
      project.stylePreset || "",
      "使用全局风格",
    );
    if (state.view === "profile" || state.profileDirectory) {
      renderProfile(true);
    }
  }

  function projectMatches(project) {
    const query = $("#project-search").value.trim().toLowerCase();
    const tag = $("#project-tag-filter").value;
    if (tag && !(project.tags || []).includes(tag)) return false;
    if (!query) return true;
    return [
      project.title,
      project.genre,
      project.tone,
      project.path,
      ...(project.tags || []),
    ]
      .join(" ")
      .toLowerCase()
      .includes(query);
  }

  function renderProjects() {
    const active = $("#active-project-list");
    const archived = $("#archived-project-list");
    const tagFilter = $("#project-tag-filter");
    const currentTag = tagFilter.value;
    const tags = [
      ...new Set(state.workspaces.flatMap((project) => project.tags || [])),
    ].sort((a, b) => a.localeCompare(b, "zh-CN"));
    tagFilter.replaceChildren(option("", "全部标签"));
    for (const tag of tags) tagFilter.append(option(tag, tag, tag === currentTag));

    const filtered = state.workspaces.filter(projectMatches);
    const activeProjects = filtered.filter((project) => !project.archived);
    const archivedProjects = filtered.filter((project) => project.archived);
    active.replaceChildren();
    archived.replaceChildren();
    for (const project of activeProjects) {
      active.append(projectCard(project, false));
    }
    for (const project of archivedProjects) {
      archived.append(projectCard(project, true));
    }
    if (!activeProjects.length) {
      active.append(el("p", "empty-state panel", "没有符合条件的进行中项目。"));
    }
    if (!archivedProjects.length) {
      archived.append(el("p", "empty-state panel", "没有符合条件的归档项目。"));
    }
    $("#active-project-count").textContent = String(activeProjects.length);
    $("#archived-project-count").textContent = String(archivedProjects.length);
    syncDisabledControls();
  }

  function projectCard(project, archived) {
    const current = project.path === state.data?.directory;
    const card = el(
      "article",
      `project-card panel ${current ? "current" : ""} ${archived ? "archived" : ""}`,
    );
    const heading = el("div", "project-card-heading");
    const titleWrap = el("div", "project-card-title");
    titleWrap.append(
      el("span", "project-mark", archived ? "档" : "书"),
      el("h3", "", project.title || "未命名项目"),
    );
    heading.append(
      titleWrap,
      el(
        "span",
        `status-badge ${project.status || "paused"}`,
        archived
          ? "已归档"
          : current
            ? project.status === "running"
              ? "当前 · 运行中"
              : "当前项目"
            : project.status === "running"
              ? "运行中"
              : "可切换",
      ),
    );
    card.append(heading);

    const tags = el("div", "tag-row");
    for (const tag of project.tags || []) tags.append(el("span", "tag", tag));
    if (!(project.tags || []).length) tags.append(el("span", "tag muted", "无标签"));
    card.append(tags);

    const meta = el("p", "project-meta");
    meta.textContent = [project.genre, project.tone].filter(Boolean).join(" · ") ||
      "题材与语气尚未设置";
    card.append(meta);
    card.append(el("p", "project-path", shortPath(project.path)));

    const stats = el("div", "project-stats");
    stats.append(
      stat("章节", project.completed || 0),
      stat("字数", number(project.words || 0)),
      stat("阶段", project.stage || "new"),
    );
    card.append(stats);

    const actions = el("div", "project-card-actions");
    if (archived) {
      const restore = button("恢复项目", "secondary-button", () =>
        archiveProject(project, false),
      );
      restore.dataset.projectAction = "restore";
      actions.append(restore);
    } else {
      const open = button(
        current ? "当前项目" : "打开项目",
        current ? "text-button" : "primary-button",
        () => switchProject(project),
      );
      open.disabled = current;
      open.dataset.projectAction = "open";
      const archive = button("归档", "text-button danger-text", () =>
        archiveProject(project, true),
      );
      archive.dataset.projectAction = "archive";
      actions.append(open, archive);
    }
    card.append(actions);
    return card;
  }

  function stat(label, value) {
    const node = el("div", "project-stat");
    node.append(el("strong", "", value), el("span", "", label));
    return node;
  }

  async function switchProject(project) {
    if (!project || project.archived || project.path === state.data?.directory) {
      return;
    }
    if (isEngineRunning()) {
      toast("请先暂停当前创作，再切换项目。", "error");
      return;
    }
    state.epoch += 1;
    setBusy("switch", true);
    try {
      const result = await api("/api/workspaces", {
        method: "POST",
        body: { path: project.path, name: "", create: false },
      });
      await refreshAll();
      setView("overview");
      toast(result.error || `已切换到《${project.title}》`, result.error ? "error" : "");
    } catch (error) {
      toast(error.message, "error");
    } finally {
      setBusy("switch", false);
    }
  }

  async function archiveProject(project, archived) {
    if (!project) return;
    setBusy("archive", true);
    try {
      await api("/api/workspaces/archive", {
        method: "POST",
        body: { directory: project.path, archived },
      });
      await refreshAll();
      toast(archived ? "项目已归档" : "项目已恢复");
    } catch (error) {
      toast(error.message, "error");
    } finally {
      setBusy("archive", false);
    }
  }

  function openProjectDialog() {
    $("#create-project-form").reset();
    $("#create-project-status").textContent = "";
    $("#confirm-create-project").disabled = false;
    const dialog = $("#project-dialog");
    if (!dialog.open) dialog.showModal();
    $("#new-project-name").focus();
  }

  async function createProject(event) {
    event.preventDefault();
    const name = $("#new-project-name").value.trim();
    const genre = $("#new-project-genre").value.trim();
    const tone = $("#new-project-tone").value.trim();
    const status = $("#create-project-status");
    if (!name) {
      status.textContent = "请填写项目名称。";
      return;
    }
    const submit = $("#confirm-create-project");
    submit.disabled = true;
    status.textContent = "正在创建项目……";
    state.epoch += 1;
    try {
      const created = await api("/api/workspaces", {
        method: "POST",
        body: { create: true, name, path: "" },
      });
      if (created.directory && (genre || tone)) {
        await api("/api/workspaces", {
          method: "PATCH",
          body: {
            directory: created.directory,
            genre,
            tone,
          },
        });
      }
      $("#project-dialog").close();
      await refreshAll();
      setView("profile");
      toast(`已创建《${name}》，可以继续完善创作资料。`);
    } catch (error) {
      status.textContent = error.message;
    } finally {
      submit.disabled = false;
    }
  }

  function renderProfile(force = false) {
    const data = state.data;
    const project = data?.project;
    const directory = data?.directory || "";
    if (!project || !directory) return;
    if (!force && state.profileDirectory === directory) return;
    state.profileDirectory = directory;

    $("#profile-name").value = project.name || "";
    $("#profile-genre").value = project.genre || "";
    $("#profile-audience").value = project.targetAudience || "";
    $("#profile-pov").value = project.pov || "";
    $("#profile-tone").value = project.tone || "";
    $("#profile-target-words").value = project.targetWords || "";
    $("#profile-target-chapters").value = project.targetChapters || "";
    $("#profile-agent-preset").value = project.agentPreset || "";
    $("#profile-tags").value = (project.tags || []).join(", ");
    $("#profile-writing-rules").value = project.writingRules || "";
    fillSelect(
      $("#profile-style"),
      state.config?.styles || [],
      project.stylePreset || "",
      "使用全局风格",
    );
    renderAgentConfigs(project);
    $("#profile-status").textContent = "";
  }

  function renderAgentConfigs(project) {
    const container = $("#agent-config-list");
    container.replaceChildren();
    const providers = state.config?.providers || [];
    const roleModels = project.roleModels || {};
    const directives = project.roleDirectives || {};
    for (const role of AGENT_ROLES) {
      const card = el("article", "agent-config");
      card.dataset.role = role.key;
      const heading = el("div", "agent-config-heading");
      heading.append(
        el("span", "agent-mark", role.mark),
        el("div", "", ""),
      );
      heading.lastChild.append(
        el("strong", "", role.name),
        el("small", "", role.key),
      );
      card.append(heading, el("p", "agent-hint", role.hint));

      const grid = el("div", "agent-config-grid");
      const providerLabel = el("label", "", "Provider");
      const providerSelect = el("select");
      providerLabel.append(providerSelect);
      const modelLabel = el("label", "", "模型");
      const modelSelect = el("select");
      modelLabel.append(modelSelect);
      const selected = roleModels[role.key] || {};
      providerSelect.append(option("", "继承全局默认", !selected.provider));
      for (const provider of providers) {
        providerSelect.append(
          option(
            provider.name,
            `${provider.name}${provider.has_api_key ? "" : " · 未配置 Key"}`,
            provider.name === selected.provider,
          ),
        );
      }
      const populateModels = (preferred = "") => {
        modelSelect.replaceChildren();
        const provider = providers.find(
          (item) => item.name === providerSelect.value,
        );
        if (!provider) {
          modelSelect.append(option("", "继承全局默认", true));
          modelSelect.disabled = true;
          return;
        }
        modelSelect.disabled = false;
        modelSelect.append(option("", "继承全局默认模型", !preferred));
        for (const model of provider.models || []) {
          modelSelect.append(
            option(model, model, model === preferred),
          );
        }
      };
      populateModels(selected.model || "");
      providerSelect.addEventListener("change", () => populateModels());
      grid.append(providerLabel, modelLabel);

      const directiveLabel = el("label", "wide", "持久角色指令");
      const directive = el("textarea");
      directive.rows = 3;
      directive.maxLength = 4000;
      directive.placeholder = `例如：${role.hint}`;
      directive.value = directives[role.key] || "";
      directive.dataset.directiveRole = role.key;
      directiveLabel.append(directive);
      grid.append(directiveLabel);
      card.append(grid);
      container.append(card);
    }
  }

  async function saveProfile() {
    const data = state.data;
    if (!data?.directory) return;
    const name = $("#profile-name").value.trim();
    if (!name) {
      $("#profile-status").textContent = "项目名称不能为空。";
      return;
    }
    const roleModels = {};
    const roleDirectives = {};
    for (const card of $$(".agent-config")) {
      const role = card.dataset.role;
      const selects = $$("select", card);
      const provider = selects[0]?.value || "";
      const model = selects[1]?.value || "";
      if (provider && model) roleModels[role] = { provider, model };
      const directive = $("[data-directive-role]", card)?.value.trim() || "";
      if (directive) roleDirectives[role] = directive;
    }
    const body = {
      directory: data.directory,
      name,
      genre: $("#profile-genre").value.trim(),
      targetAudience: $("#profile-audience").value.trim(),
      pov: $("#profile-pov").value.trim(),
      tone: $("#profile-tone").value.trim(),
      targetWords: Number($("#profile-target-words").value || 0),
      targetChapters: Number($("#profile-target-chapters").value || 0),
      writingRules: $("#profile-writing-rules").value.trim(),
      stylePreset: $("#profile-style").value,
      agentPreset: $("#profile-agent-preset").value.trim(),
      tags: $("#profile-tags")
        .value.split(/[,，]/)
        .map((tag) => tag.trim())
        .filter(Boolean),
      roleModels,
      roleDirectives,
    };
    setBusy("profile", true);
    $("#profile-status").textContent = "正在保存……";
    try {
      await api("/api/workspaces", { method: "PATCH", body });
      await refreshAll();
      renderProfile(true);
      $("#profile-status").textContent = "已保存。项目隔离与后续规划门禁已同步更新。";
      toast("项目资料已保存");
    } catch (error) {
      $("#profile-status").textContent = error.message;
    } finally {
      setBusy("profile", false);
    }
  }

  async function loadStyle() {
    const directory = state.data?.directory;
    if (!directory || state.loading.style) return;
    setBusy("style", true);
    try {
      const result = await api(
        `/api/style?directory=${encodeURIComponent(directory)}`,
      );
      if (result.directory !== state.data?.directory) return;
      state.style = result;
      state.styleBusyLocal = Boolean(result.busy);
      renderStyle();
    } catch (error) {
      $("#style-status").textContent = error.message;
    } finally {
      setBusy("style", false);
    }
  }

  function renderStyle() {
    const result = state.style || { sources: [], profile: null };
    const list = $("#style-source-list");
    list.replaceChildren();
    const sources = result.sources || [];
    if (!sources.length) {
      list.append(
        el("p", "empty-state", "尚未添加语料。支持 TXT、MD 和 Markdown。"),
      );
    }
    for (const source of sources) {
      const row = el("article", "source-row");
      const copy = el("div", "source-copy");
      copy.append(
        el("strong", "", source.name),
        el(
          "small",
          "",
          `${number(Math.ceil((source.sizeBytes || 0) / 1024))} KiB · ${formatTime(source.modifiedAt)}`,
        ),
      );
      const remove = button("删除", "text-button danger-text", () =>
        deleteStyleSource(source.name),
      );
      remove.disabled = isBusy();
      row.append(copy, remove);
      list.append(row);
    }
    $("#style-profile-state").textContent =
      result.busy
        ? "正在分析"
        : pretty(result.profile, "") !== ""
          ? "画像已生成"
          : "尚未分析";
    renderJson(
      $("#style-profile"),
      result.profile,
      "添加语料并完成分析后，这里会显示可复用的结构化风格画像。",
    );
    $("#style-progress-wrap").classList.toggle(
      "hidden",
      !state.styleBusyLocal,
    );
    $("#run-style-analysis").disabled =
      isBusy() || !state.data?.ready || state.styleBusyLocal;
  }

  async function uploadStyleSource(event) {
    event.preventDefault();
    const name = $("#style-source-name").value.trim();
    const content = $("#style-source-content").value;
    const status = $("#style-status");
    if (!name || !content.trim()) {
      status.textContent = "请填写文件名和语料内容。";
      return;
    }
    if (new Blob([content]).size > 8 * 1024 * 1024) {
      status.textContent = "单篇语料不能超过 8 MiB。";
      return;
    }
    setBusy("styleUpload", true);
    status.textContent = "正在保存语料……";
    try {
      await api("/api/style/upload", {
        method: "POST",
        body: { name, content },
      });
      $("#style-upload-form").reset();
      status.textContent = "语料已保存，旧风格画像已失效。";
      await loadStyle();
    } catch (error) {
      status.textContent = error.message;
    } finally {
      setBusy("styleUpload", false);
    }
  }

  async function deleteStyleSource(name) {
    if (!confirm(`确定删除语料“${name}”？`)) return;
    setBusy("styleDelete", true);
    try {
      await api("/api/style/delete", {
        method: "POST",
        body: { name },
      });
      await loadStyle();
      toast("语料已删除");
    } catch (error) {
      $("#style-status").textContent = error.message;
    } finally {
      setBusy("styleDelete", false);
    }
  }

  async function runStyleAnalysis() {
    const directory = state.data?.directory;
    setBusy("styleAnalysis", true);
    state.styleBusyLocal = true;
    $("#style-status").textContent = "正在启动风格蒸馏……";
    $("#style-progress-wrap").classList.remove("hidden");
    $("#style-progress-bar").style.width = "0%";
    try {
      await api("/api/style/run", { method: "POST", body: {} });
      if (directory === state.data?.directory) {
        $("#style-status").textContent = "分析已启动，完成前请勿切换或开始创作。";
      }
    } catch (error) {
      state.styleBusyLocal = false;
      $("#style-progress-wrap").classList.add("hidden");
      $("#style-status").textContent = error.message;
    } finally {
      setBusy("styleAnalysis", false);
      renderStyle();
    }
  }

  async function selectStyleFile() {
    const file = $("#style-file").files?.[0];
    if (!file) return;
    if (file.size > 8 * 1024 * 1024) {
      $("#style-status").textContent = "文件超过 8 MiB。";
      return;
    }
    if (!/\.(txt|md|markdown)$/i.test(file.name)) {
      $("#style-status").textContent = "仅支持 TXT、MD 或 Markdown 文件。";
      return;
    }
    $("#style-source-name").value = file.name;
    $("#style-source-content").value = await file.text();
    $("#style-status").textContent = `已读取 ${file.name}`;
  }

  async function loadPlanning() {
    const directory = state.data?.directory;
    if (!directory || state.loading.planning) return;
    setBusy("planning", true);
    try {
      const result = await api(
        `/api/planning?directory=${encodeURIComponent(directory)}`,
      );
      if (result.directory !== state.data?.directory) return;
      state.planning = result;
      renderPlanning();
    } catch (error) {
      $("#planning-warnings").classList.remove("hidden");
      $("#planning-warnings").replaceChildren(
        el("p", "", error.message),
      );
    } finally {
      setBusy("planning", false);
    }
  }

  function renderPlanning() {
    const planning = state.planning || {};
    const workflow = planning.workflow || state.data?.workflow || {};
    const outline = planning.outline || [];
    $("#plan-fingerprint").textContent = workflow.fingerprint || "尚未生成";
    $("#plan-approval-state").textContent = workflow.approved
      ? "已确认当前版本"
      : workflow.canApprove
        ? "等待用户确认"
        : "规划尚未就绪";
    $("#plan-approval-state").className =
      `soft-badge ${workflow.approved ? "approved" : workflow.canApprove ? "pending" : ""}`.trim();

    renderWarnings($("#planning-warnings"), planning.warnings || []);
    const premise = $("#plan-premise");
    premise.replaceChildren();
    premise.classList.toggle("empty-state", !planning.premise);
    premise.append(
      el(
        "div",
        "prose-block",
        pretty(planning.premise, "尚未生成故事内核。"),
      ),
    );
    renderJson($("#plan-compass"), planning.compass, "尚未生成故事指南针。");
    renderJson(
      $("#plan-foundation-audit"),
      planning.foundationAudit,
      "尚未完成基础设定审查。",
    );
    renderJson($("#plan-characters"), planning.characters, "尚未生成角色档案。");
    renderJson($("#plan-world-rules"), planning.worldRules, "尚未生成世界规则。");
    $("#plan-layered").textContent = pretty(
      planning.layered,
      "当前没有分卷或分层规划数据。",
    );
    $("#outline-count").textContent = `${outline.length} 章`;
    renderPlanOutline(outline);
    syncDisabledControls();
  }

  function renderWarnings(container, warnings) {
    container.replaceChildren();
    container.classList.toggle("hidden", !warnings.length);
    if (!warnings.length) return;
    container.append(el("strong", "", "读取警告"));
    const list = el("ul");
    for (const warning of warnings) list.append(el("li", "", warning));
    container.append(list);
  }

  function renderPlanOutline(outline) {
    const container = $("#plan-outline");
    container.replaceChildren();
    if (!outline.length) {
      container.append(el("p", "empty-state", "章节目录尚未生成。"));
      return;
    }
    for (const item of outline) {
      const details = el("details", "outline-detail");
      const summary = el(
        "summary",
        "",
        `第 ${item.chapter || "?"} 章 · ${item.title || "待定标题"}`,
      );
      details.append(summary);
      if (item.coreEvent) {
        details.append(el("p", "outline-event", item.coreEvent));
      }
      const raw = { ...item };
      delete raw.chapter;
      delete raw.title;
      delete raw.coreEvent;
      if (Object.keys(raw).length) {
        const more = el("details", "raw-details");
        more.append(
          el("summary", "", "查看场景、冲突与连续性安排"),
          el("pre", "json-pre", pretty(raw)),
        );
        details.append(more);
      }
      container.append(details);
    }
  }

  async function revisePlan() {
    const text = $("#plan-feedback").value.trim();
    if (!text) {
      toast("请先填写规划修改意见。", "error");
      $("#plan-feedback").focus();
      return;
    }
    await runAction({ action: "revise-plan", text });
  }

  async function approvePlan() {
    const fingerprint = state.planning?.workflow?.fingerprint ||
      state.data?.workflow?.fingerprint;
    if (!fingerprint) {
      toast("当前没有可确认的规划版本。", "error");
      return;
    }
    await runAction({ action: "approve-plan", fingerprint });
  }

  function renderChapters() {
    const chapters = state.data?.chapters || [];
    const container = $("#chapter-list");
    container.replaceChildren();
    if (!chapters.length) {
      container.append(el("p", "empty-state", "尚无正文。"));
    }
    for (const chapter of chapters) {
      const active =
        Number(chapter.number) === Number(state.selectedChapter) &&
        (chapter.source || "") === (state.selectedSource || "");
      const item = button("", `chapter-item ${active ? "active" : ""}`, () => {
        state.selectedChapter = Number(chapter.number);
        state.selectedSource = chapter.source || "";
        renderChapters();
        readChapter(state.selectedChapter, state.selectedSource);
      });
      item.append(
        el("strong", "", chapter.title || `第 ${chapter.number} 章`),
        el(
          "small",
          "",
          `${number(chapter.words)} 字${chapter.source === "draft" ? " · 草稿" : " · 终稿"}`,
        ),
      );
      container.append(item);
    }
    if (!state.reading && chapters.length) {
      const latest = chapters[chapters.length - 1];
      state.selectedChapter = Number(latest.number);
      state.selectedSource = latest.source || "";
      readChapter(state.selectedChapter, state.selectedSource);
    }
    syncDisabledControls();
  }

  async function readChapter(chapter, source = "") {
    const directory = state.data?.directory;
    if (!directory || !chapter) return;
    setBusy("chapter", true);
    $("#reader-title").textContent = "正在读取章节……";
    try {
      const query = new URLSearchParams({ directory });
      if (source === "draft") query.set("source", "draft");
      const result = await api(
        `/api/chapters/${encodeURIComponent(chapter)}?${query}`,
      );
      if (directory !== state.data?.directory) return;
      state.reading = result;
      state.selectedChapter = Number(result.number || chapter);
      state.selectedSource = source;
      renderReader();
    } catch (error) {
      $("#reader-meta").textContent = "读取失败";
      $("#reader-title").textContent = "章节不可用";
      $("#reader-content").textContent = error.message;
      $("#reader-content").classList.add("empty-state");
    } finally {
      setBusy("chapter", false);
    }
  }

  function renderReader() {
    const reading = state.reading;
    if (!reading) return;
    const draft = state.selectedSource === "draft";
    $("#reader-meta").textContent =
      `第 ${reading.number} 章 · ${draft ? "草稿" : "终稿"} · ${number(
        (reading.content || "").replace(/\s/g, "").length,
      )} 字`;
    $("#reader-title").textContent =
      reading.summary?.title || `第 ${reading.number} 章`;
    $("#reader-content").textContent = reading.content || "正文为空。";
    $("#reader-content").classList.toggle("empty-state", !reading.content);
    $("#reader-notes-wrap").open = false;
    renderJson(
      $("#reader-notes"),
      {
        summary: reading.summary,
        review: reading.review,
      },
      "本章暂无摘要或评审记录。",
    );
  }

  async function startBatch() {
    const count = Number($("#batch-count").value || 1);
    if (!Number.isInteger(count) || count < 1 || count > 100) {
      toast("本批次章节数必须是 1–100 的整数。", "error");
      return;
    }
    saveInputs();
    await runAction({ action: "batch", count });
  }

  async function sendChapterFeedback() {
    const text = $("#chapter-feedback").value.trim();
    const chapter = Number(
      state.selectedChapter ||
        state.reading?.number ||
        state.data?.workflow?.latestChapter ||
        0,
    );
    if (!chapter) {
      toast("当前没有可反馈的章节。", "error");
      return;
    }
    if (!text) {
      toast("请填写章节修改意见。", "error");
      $("#chapter-feedback").focus();
      return;
    }
    await runAction({ action: "chapter-feedback", chapter, text });
  }

  async function acceptChapter() {
    const chapter = Number(state.data?.workflow?.latestChapter || 0);
    if (!chapter) {
      toast("当前没有可验收的章节。", "error");
      return;
    }
    await runAction({ action: "accept-chapter", chapter });
  }

  async function loadAudit() {
    const directory = state.data?.directory;
    if (!directory || state.loading.audit) return;
    const chapter = $("#audit-chapter").value.trim();
    setBusy("audit", true);
    try {
      const query = new URLSearchParams({ directory });
      if (chapter) query.set("chapter", chapter);
      const result = await api(`/api/audit?${query}`);
      if (result.directory !== state.data?.directory) return;
      state.audit = result;
      renderAudit();
    } catch (error) {
      renderWarnings($("#audit-warnings"), [error.message]);
    } finally {
      setBusy("audit", false);
    }
  }

  function renderAudit() {
    const audit = state.audit || {};
    const review = audit.review || {};
    $("#audit-chapter-value").textContent = audit.chapter
      ? `第 ${audit.chapter} 章`
      : "—";
    $("#audit-decision-count").textContent = number(
      (audit.decisions || []).length,
    );
    $("#audit-checkpoint-count").textContent = number(
      (audit.checkpoints || []).length,
    );
    $("#audit-ready-state").textContent =
      audit.progress?.phase || state.data?.workflow?.stage || "—";
    $("#audit-review-verdict").textContent =
      review.verdict || review.status || "尚无评审";
    renderWarnings($("#audit-warnings"), audit.warnings || []);
    renderJson(
      $("#audit-record"),
      audit.chapterRecord,
      "该章节尚无接纳记录。",
    );
    renderJson($("#audit-review"), audit.review, "该章节尚无评审。");
    renderJson(
      $("#audit-facts"),
      {
        compass: audit.compass,
        foundationAudit: audit.foundationAudit,
        characters: audit.characters,
        characterSnapshots: audit.characterSnapshots,
        worldRules: audit.worldRules,
        timeline: audit.timeline,
        foreshadows: audit.foreshadows,
        relationships: audit.relationships,
        stateChanges: audit.stateChanges,
      },
      "尚无事实状态。",
    );
    renderJson(
      $("#audit-decisions"),
      audit.decisions,
      "尚无决策审计记录。",
    );
    renderJson(
      $("#audit-checkpoints"),
      audit.checkpoints,
      "尚无检查点。",
    );
    renderJson($("#audit-agents"), audit.agents, "尚无项目 Agent 配置。");
  }

  async function loadCoCreate(force = false) {
    const directory = state.data?.directory;
    if (!directory || (state.loading.cocreate && !force)) return;
    if (!force && state.cocreate && state.cocreateDirectory === directory) {
      renderCoCreate();
      return;
    }
    const ownsBusy = !state.loading.cocreate;
    if (ownsBusy) setBusy("cocreate", true);
    try {
      const query = new URLSearchParams({ directory });
      const result = await api(`/api/cocreate?${query}`);
      if (directory !== state.data?.directory) return;
      state.cocreate = result;
      state.cocreateDirectory = directory;
      state.cocreatePreview = "";
      state.cocreateDraftDirty = false;
      renderCoCreate();
    } catch (error) {
      if (directory === state.data?.directory) {
        $("#cocreate-status").textContent = "共创状态读取失败";
        $("#cocreate-message").textContent = error.message;
      }
    } finally {
      if (ownsBusy) setBusy("cocreate", false);
      else syncDisabledControls();
    }
  }

  function renderCoCreateTurn(turn, streaming = false) {
    const role = turn.role === "user" ? "user" : "assistant";
    const article = el(
      "article",
      `co-create-turn ${role}${streaming ? " streaming" : ""}`,
    );
    const heading = el("header");
    heading.append(
      el("span", "", role === "user" ? "你的回答" : "共创助手"),
      el("span", "", streaming ? "正在生成" : ""),
    );
    article.append(heading, el("p", "", turn.content || ""));
    return article;
  }

  function renderCoCreate() {
    const session = state.cocreate || {};
    const transcript = Array.isArray(session.transcript)
      ? session.transcript
      : [];
    const hasSession = Boolean(
      transcript.length ||
        session.draftPrompt ||
        session.ready ||
        session.confirmed ||
        session.started,
    );
    const started = Boolean(session.started);
    const confirmed = Boolean(session.confirmed);
    const busy = Boolean(state.loading.cocreate);
    const start = $("#cocreate-start");
    const workspace = $("#cocreate-workspace");

    start.classList.toggle("hidden", hasSession || started);
    workspace.classList.toggle("hidden", !hasSession || started);

    const thread = $("#cocreate-thread");
    thread.replaceChildren();
    for (const turn of transcript) {
      thread.append(renderCoCreateTurn(turn));
    }
    if (!thread.childElementCount && hasSession && !started) {
      thread.append(
        el("p", "empty-state", "共创会话已建立，等待第一条回复。"),
      );
    }
    thread.scrollTop = thread.scrollHeight;

    const stream = $("#cocreate-stream");
    stream.textContent = state.cocreatePreview || "";
    stream.classList.toggle("hidden", !state.cocreatePreview);

    const suggestions = $("#cocreate-suggestions");
    suggestions.replaceChildren();
    if (!confirmed && !started) {
      for (const suggestion of session.suggestions || []) {
        const suggestionButton = button(suggestion, "", () => {
          $("#cocreate-reply").value = suggestion;
          $("#cocreate-reply").focus();
        });
        suggestions.append(suggestionButton);
      }
    }

    const draft = $("#cocreate-draft");
    if (!state.cocreateDraftDirty) {
      draft.value = session.draftPrompt || "";
    }

    if (busy) {
      $("#cocreate-status").textContent = "共创助手思考中";
    } else if (started) {
      $("#cocreate-status").textContent = "已进入正式创作";
    } else if (confirmed) {
      $("#cocreate-status").textContent = "创意已确认";
    } else if (session.canConfirm) {
      $("#cocreate-status").textContent = "关键信息已齐";
    } else if (transcript.length) {
      $("#cocreate-status").textContent = "继续澄清中";
    } else {
      $("#cocreate-status").textContent = "等待开始";
    }

    const message = $("#cocreate-message");
    if (session.lastError) {
      message.textContent = session.lastError;
      message.className = "form-status error";
    } else if (started) {
      message.textContent = "规划或正文流程已经启动，可前往“规划审核”查看进度。";
      message.className = "form-status";
    } else if (confirmed) {
      message.textContent =
        "完整创作指令已确认。若规划启动失败，可点击“重试生成规则与目录”。";
      message.className = "form-status";
    } else if (session.ready) {
      message.textContent =
        "关键信息已经齐全。请检查并编辑完整创作指令，确认无误后生成正式规划。";
      message.className = "form-status";
    } else if (transcript.length) {
      message.textContent =
        "助手仍有需要澄清的问题。继续回答后，确认按钮才会启用。";
      message.className = "form-status";
    } else {
      message.textContent = "";
      message.className = "form-status";
    }
    syncDisabledControls();
  }

  function applyCoCreateResult(result) {
    state.cocreate = result || {};
    state.cocreateDirectory = state.data?.directory || "";
    state.cocreatePreview = "";
    renderCoCreate();
  }

  async function startCoCreate() {
    const text = $("#prompt").value.trim();
    if (!text) {
      toast("请先描述你要创作的故事。", "error");
      $("#prompt").focus();
      return;
    }
    const directory = state.data?.directory;
    if (!directory) {
      toast("请先选择创作项目。", "error");
      return;
    }
    saveInputs();
    setBusy("cocreate", true);
    $("#cocreate-message").textContent = "共创助手正在理解你的想法并准备问题……";
    try {
      const result = await api("/api/cocreate", {
        method: "POST",
        body: { directory, text },
      });
      if (directory !== state.data?.directory) return;
      applyCoCreateResult(result);
      $("#cocreate-reply").focus();
    } catch (error) {
      toast(error.message, "error");
      await loadCoCreate(true);
    } finally {
      setBusy("cocreate", false);
    }
  }

  async function sendCoCreateReply() {
    const text = $("#cocreate-reply").value.trim();
    if (!text) {
      toast("请填写对共创助手问题的回答。", "error");
      $("#cocreate-reply").focus();
      return;
    }
    const directory = state.data?.directory;
    if (!directory) {
      toast("请先选择创作项目。", "error");
      return;
    }
    saveInputs();
    setBusy("cocreate", true);
    state.cocreatePreview = "";
    renderCoCreate();
    try {
      const result = await api("/api/cocreate", {
        method: "POST",
        body: { directory, text },
      });
      if (directory !== state.data?.directory) return;
      $("#cocreate-reply").value = "";
      saveInputs(directory);
      applyCoCreateResult(result);
      $("#cocreate-reply").focus();
    } catch (error) {
      toast(error.message, "error");
      await loadCoCreate(true);
    } finally {
      setBusy("cocreate", false);
    }
  }

  async function confirmCoCreate() {
    const directory = state.data?.directory;
    const draftPrompt = $("#cocreate-draft").value.trim();
    if (!directory) {
      toast("请先选择创作项目。", "error");
      return;
    }
    if (!draftPrompt) {
      toast("完整创作指令不能为空。", "error");
      $("#cocreate-draft").focus();
      return;
    }
    setBusy("cocreate", true);
    try {
      await api("/api/cocreate/confirm", {
        method: "POST",
        body: { directory, draftPrompt },
      });
      if (state.cocreate) {
        state.cocreate = {
          ...state.cocreate,
          draftPrompt,
          confirmed: true,
          canConfirm: false,
        };
      }
      state.cocreateDraftDirty = false;
      renderCoCreate();
      toast("创意已确认，正在生成规则与章节目录");
      await refreshAll();
    } catch (error) {
      toast(error.message, "error");
      await loadCoCreate(true);
    } finally {
      setBusy("cocreate", false);
    }
  }

  async function resetCoCreate() {
    const directory = state.data?.directory;
    if (!directory) return;
    if (!window.confirm("清空当前项目的共创对话与指令草稿？已开始正式创作的项目不能清空。")) {
      return;
    }
    setBusy("cocreate", true);
    try {
      const result = await api("/api/cocreate/reset", {
        method: "POST",
        body: { directory },
      });
      state.cocreate = result || {};
      state.cocreateDirectory = directory;
      state.cocreatePreview = "";
      state.cocreateDraftDirty = false;
      $("#cocreate-reply").value = "";
      $("#cocreate-draft").value = "";
      renderCoCreate();
      toast("共创会话已重新开始");
    } catch (error) {
      toast(error.message, "error");
    } finally {
      setBusy("cocreate", false);
    }
  }

  async function runAction(payload) {
    const directory = state.data?.directory;
    if (!directory) {
      toast("请先选择创作项目。", "error");
      return;
    }
    const actionName = payload.action;
    setBusy(`action:${actionName}`, true);
    try {
      await api("/api/action", {
        method: "POST",
        body: { ...payload, directory },
      });
      const labels = {
        start: "规划任务已启动",
        resume: "已请求恢复创作",
        pause: "已请求暂停",
        steer: "调整方向已提交",
        continue: "续写要求已提交",
        reopen: "重新规划任务已启动",
        sync: "同步任务已启动",
        "approve-plan": "规划确认已提交",
        "revise-plan": "规划修改任务已启动",
        batch: "正文批次已启动",
        "accept-chapter": "章节验收已提交",
        "chapter-feedback": "章节反馈已提交",
      };
      toast(labels[actionName] || "操作已提交");
      await refreshState();
    } catch (error) {
      toast(error.message, "error");
    } finally {
      setBusy(`action:${actionName}`, false);
    }
  }

  async function startProject() {
    await startCoCreate();
  }

  async function submitIntervention(command) {
    const text = $("#intervention").value.trim();
    if (!text && command !== "sync") {
      toast("请填写调整或续写要求。", "error");
      $("#intervention").focus();
      return;
    }
    saveInputs();
    await runAction({ action: command, text });
  }

  async function download(format) {
    setBusy("export", true);
    try {
      const response = await fetch("/api/export", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ format }),
      });
      if (!response.ok) {
        const payload = normalize(await response.json().catch(() => ({})));
        throw new Error(payload.error || "导出失败");
      }
      const blob = await response.blob();
      const disposition = response.headers.get("content-disposition") || "";
      const match = disposition.match(/filename="([^"]+)"/);
      const anchor = document.createElement("a");
      const href = URL.createObjectURL(blob);
      anchor.href = href;
      anchor.download = match?.[1] || `novel.${format}`;
      document.body.append(anchor);
      anchor.click();
      anchor.remove();
      setTimeout(() => URL.revokeObjectURL(href), 1000);
      toast(`已导出 ${format.toUpperCase()} 文件`);
    } catch (error) {
      toast(error.message, "error");
    } finally {
      setBusy("export", false);
    }
  }

  function appendEvent(event) {
    if (!event) return;
    const index = event.id
      ? state.events.findIndex((item) => item.id === event.id)
      : -1;
    if (index >= 0) state.events[index] = event;
    else state.events.push(event);
    if (state.events.length > 200) state.events = state.events.slice(-200);
    if (state.view === "activity") renderActivity();
  }

  function renderActivity() {
    const container = $("#activity-list");
    container.replaceChildren();
    if (!state.events.length) {
      container.append(el("p", "empty-state", "暂无活动。"));
      return;
    }
    for (const event of [...state.events].reverse()) {
      const row = el(
        "article",
        `activity-row ${event.failed ? "failed" : ""}`,
      );
      const head = el("div", "activity-heading");
      head.append(
        el("time", "", formatTime(event.time)),
        el("span", "activity-category", event.category || "EVENT"),
        el("span", "activity-agent", event.agent || "system"),
      );
      row.append(
        head,
        el("p", "", event.summary || event.detail || "状态更新"),
      );
      if (event.duration) {
        const seconds = Number(event.duration) / 1_000_000_000;
        row.append(
          el("small", "", `耗时 ${seconds.toFixed(seconds < 1 ? 2 : 1)} 秒`),
        );
      }
      container.append(row);
    }
  }

  function updateStyleProgress(data) {
    const current = Number(data.current || 0);
    const total = Number(data.total || 0);
    const percent = total > 0 ? Math.min(100, (current / total) * 100) : 0;
    state.styleBusyLocal = true;
    $("#style-progress-wrap").classList.remove("hidden");
    $("#style-progress-bar").style.width = `${percent}%`;
    $("#style-profile-state").textContent =
      total > 0 ? `分析中 ${current}/${total}` : "分析中";
    $("#style-status").textContent =
      data.error || data.message || "正在分析风格语料……";
  }

  function connectEvents() {
    if (state.source) state.source.close();
    state.streamConnected = false;
    renderCore();
    const source = new EventSource("/api/events");
    state.source = source;
    source.addEventListener("open", () => {
      state.streamConnected = true;
      renderCore();
    });
    source.addEventListener("error", () => {
      state.streamConnected = false;
      renderCore();
    });
    source.addEventListener("message", (message) => {
      let envelope;
      try {
        envelope = normalize(JSON.parse(message.data));
      } catch (_) {
        return;
      }
      handleStreamMessage(envelope.type, envelope.data);
    });
  }

  function handleStreamMessage(type, data) {
    if (type === "history") {
      state.events = data?.events || [];
      state.stream = data?.stream || "";
      $("#live-output").textContent =
        state.stream || "等待创作引擎输出……";
      if (state.view === "activity") renderActivity();
      return;
    }
    if (type === "event") {
      appendEvent(data);
      return;
    }
    if (type === "delta") {
      state.stream += String(data || "");
      if (state.stream.length > 200000) {
        state.stream = state.stream.slice(-180000);
      }
      $("#live-output").textContent = state.stream;
      return;
    }
    if (type === "clear") {
      state.stream = "";
      $("#live-output").textContent = "等待创作引擎输出……";
      return;
    }
    if (type === "refresh") {
      refreshState();
      loadCoCreate();
      if (state.view === "planning") loadPlanning();
      if (state.view === "chapters") renderChapters();
      return;
    }
    if (type === "workspace") {
      refreshAll();
      return;
    }
    if (type === "cocreate-delta") {
      if (data?.kind === "reply") {
        state.cocreatePreview = String(data.text || "");
      } else {
        state.cocreatePreview = "共创助手正在整理你的创作意图……";
      }
      $("#cocreate-status").textContent = "共创助手思考中";
      $("#cocreate-stream").textContent = state.cocreatePreview;
      $("#cocreate-stream").classList.remove("hidden");
      return;
    }
    if (type === "cocreate-updated") {
      state.cocreate = data || {};
      state.cocreateDirectory = state.data?.directory || "";
      state.cocreatePreview = "";
      renderCoCreate();
      return;
    }
    if (type === "result") {
      if (data?.error) toast(data.error, "error");
      else if (data?.action) toast("后台任务已完成");
      refreshAll();
      loadCoCreate(true);
      if (state.view === "planning") loadPlanning();
      if (state.view === "style") loadStyle();
      return;
    }
    if (type === "style-progress") {
      updateStyleProgress(data || {});
      return;
    }
    if (type === "style-done") {
      state.styleBusyLocal = false;
      $("#style-progress-bar").style.width = "100%";
      $("#style-status").textContent = data?.warning
        ? `分析完成，但读取画像失败：${data.warning}`
        : "风格画像已生成。";
      toast("风格蒸馏完成");
      loadStyle();
      return;
    }
    if (type === "style-error") {
      state.styleBusyLocal = false;
      $("#style-progress-wrap").classList.add("hidden");
      $("#style-status").textContent =
        data?.error || data?.message || "风格分析失败";
      toast($("#style-status").textContent, "error");
      return;
    }
    if (type === "style-updated") {
      if (state.view === "style") loadStyle();
    }
  }

  function configPayload() {
    const form = $("#config-form");
    const data = Object.fromEntries(new FormData(form));
    if (data.type !== "openai") data.api = "";
    return {
      provider: state.config?.provider || "",
      name: data.name.trim(),
      model: data.model.trim(),
      type: data.type,
      api: data.api,
      base_url: data.base_url.trim(),
      api_key: data.api_key,
    };
  }

  function openSettings() {
    const config = state.config || {};
    const provider =
      (config.providers || []).find((item) => item.name === config.provider) ||
      (config.providers || [])[0] ||
      {};
    const form = $("#config-form");
    form.elements.name.value = provider.name || config.provider || "";
    form.elements.type.value = provider.type || "openai";
    form.elements.base_url.value = provider.base_url || "";
    form.elements.model.value = config.model || provider.models?.[0] || "";
    form.elements.api.value = provider.api || "chat";
    form.elements.api_key.value = "";
    $("#config-status").textContent = provider.has_api_key
      ? `已保存 Key：${provider.api_key_hint || "已隐藏"}`
      : "尚未保存 API Key。";
    const dialog = $("#settings-dialog");
    if (!dialog.open) dialog.showModal();
  }

  async function testConfig() {
    const status = $("#config-status");
    const target = $("#test-config");
    target.disabled = true;
    status.textContent = "正在测试连接，最多等待 30 秒……";
    try {
      await api("/api/config/test", {
        method: "POST",
        body: configPayload(),
      });
      status.textContent = "连接成功，可以保存。";
      toast("模型连接测试成功");
    } catch (error) {
      status.textContent = error.message;
    } finally {
      target.disabled = false;
    }
  }

  async function saveConfig(event) {
    event.preventDefault();
    const status = $("#config-status");
    const submit = $("#config-form button[type=submit]");
    submit.disabled = true;
    status.textContent = "正在保存并连接……";
    try {
      const result = await api("/api/config", {
        method: "POST",
        body: configPayload(),
      });
      await refreshAll();
      if (result.error) {
        status.textContent = `配置已保存，但项目连接未就绪：${result.error}`;
        toast(status.textContent, "error");
      } else {
        $("#settings-dialog").close();
        toast("模型配置已保存，工作台已连接");
      }
    } catch (error) {
      status.textContent = error.message;
    } finally {
      submit.disabled = false;
    }
  }

  function bindEvents() {
    for (const nav of $$(".nav-item")) {
      nav.addEventListener("click", () => setView(nav.dataset.view));
    }
    for (const go of $$("[data-go-view]")) {
      go.addEventListener("click", () => setView(go.dataset.goView));
    }
    $("#current-project-button").addEventListener("click", () =>
      setView("projects"),
    );
    $("#go-planning").addEventListener("click", () => setView("planning"));
    $("#go-latest").addEventListener("click", () => {
      setView("chapters");
      const chapter = state.data?.workflow?.latestChapter;
      if (chapter) readChapter(chapter, "");
    });
    $("#refresh-all").addEventListener("click", () => refreshAll());
    $("#create-project").addEventListener("click", openProjectDialog);
    $("#cancel-create-project").addEventListener("click", () =>
      $("#project-dialog").close(),
    );
    $("#create-project-form").addEventListener("submit", createProject);
    $("#project-search").addEventListener("input", renderProjects);
    $("#project-tag-filter").addEventListener("change", renderProjects);
    $("#refresh-projects").addEventListener("click", loadWorkspaces);
    $("#save-project-profile").addEventListener("click", saveProfile);
    $("#project-profile-form").addEventListener("submit", (event) => {
      event.preventDefault();
      saveProfile();
    });
    $("#style-upload-form").addEventListener("submit", uploadStyleSource);
    $("#style-file").addEventListener("change", selectStyleFile);
    $("#refresh-style").addEventListener("click", loadStyle);
    $("#run-style-analysis").addEventListener("click", runStyleAnalysis);
    $("#reload-planning").addEventListener("click", loadPlanning);
    $("#revise-plan").addEventListener("click", revisePlan);
    $("#approve-plan").addEventListener("click", approvePlan);
    $("#start-batch").addEventListener("click", startBatch);
    $("#send-chapter-feedback").addEventListener("click", sendChapterFeedback);
    $("#accept-chapter").addEventListener("click", acceptChapter);
    $("#refresh-audit").addEventListener("click", loadAudit);
    $("#audit-chapter").addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        loadAudit();
      }
    });
    $("#clear-activity").addEventListener("click", () => {
      state.events = [];
      state.stream = "";
      renderActivity();
      $("#live-output").textContent = "等待创作引擎输出……";
    });
    $("#start-project").addEventListener("click", startProject);
    $("#send-cocreate").addEventListener("click", sendCoCreateReply);
    $("#confirm-cocreate").addEventListener("click", confirmCoCreate);
    $("#reset-cocreate").addEventListener("click", resetCoCreate);
    $("#cocreate-reply").addEventListener("keydown", (event) => {
      if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        sendCoCreateReply();
      }
    });
    $("#cocreate-draft").addEventListener("input", () => {
      state.cocreateDraftDirty = true;
      syncDisabledControls();
    });
    $("#prompt").addEventListener("keydown", (event) => {
      if (event.key === "Enter" && !event.shiftKey) {
        event.preventDefault();
        startProject();
      }
    });
    for (const template of $$("[data-template]")) {
      template.addEventListener("click", () => {
        $("#prompt").value = template.dataset.template;
        saveInputs();
        $("#prompt").focus();
      });
    }
    $("#resume-run").addEventListener("click", () =>
      runAction({ action: "resume", text: "" }),
    );
    $("#pause-run").addEventListener("click", () =>
      runAction({ action: "pause", text: "" }),
    );
    $("#retry-connection").addEventListener("click", async () => {
      connectEvents();
      await refreshAll();
    });
    for (const command of $$("[data-command]")) {
      command.addEventListener("click", () =>
        submitIntervention(command.dataset.command),
      );
    }
    $("#export-txt").addEventListener("click", () => download("txt"));
    $("#export-epub").addEventListener("click", () => download("epub"));
    $("#open-settings").addEventListener("click", openSettings);
    $("#test-config").addEventListener("click", testConfig);
    $("#config-form").addEventListener("submit", saveConfig);
    for (const input of INPUT_IDS.map((id) => document.getElementById(id))) {
      input?.addEventListener("input", () => saveInputs());
    }
    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape") return;
      if ($("#project-dialog").open) $("#project-dialog").close();
      if ($("#settings-dialog").open) $("#settings-dialog").close();
    });
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden) refreshState({ quiet: true });
    });
    window.addEventListener("hashchange", () => {
      const view = location.hash.replace(/^#/, "");
      if (VIEW_META[view]) setView(view, false);
    });
    window.addEventListener("beforeunload", () => {
      saveInputs();
      state.source?.close();
    });
  }

  async function init() {
    bindEvents();
    await refreshAll({ quiet: true });
    const initialView = location.hash.replace(/^#/, "");
    setView(VIEW_META[initialView] ? initialView : "overview", false);
    connectEvents();
    clearInterval(state.refreshTimer);
    state.refreshTimer = setInterval(() => {
      if (!document.hidden) refreshState({ quiet: true });
    }, 3500);
  }

  init();
})();
