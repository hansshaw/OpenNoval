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
    versions: ["版本与恢复", "PROJECT HISTORY"],
    jobs: ["后台任务", "PROJECT JOBS"],
    profile: ["创作资料与 Agent", "BOOK PROFILE"],
    style: ["共享风格库", "SHARED STYLE LIBRARY"],
    planning: ["规划审核", "PLANNING REVIEW"],
    chapters: ["章节工作台", "CHAPTER WORKBENCH"],
    illustrations: ["章节配图", "CHAPTER VISUAL STUDIO"],
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
    "foundation-audit": {
      title: "等待基础设定审查",
      description: "规划内容已保留。请提交修改意见，或恢复旧项目并重新审查基础设定。",
      step: 1,
    },
    "plan-review": {
      title: "等待确认规划",
      description: "目录与设定仍可反复修改覆盖，第一阶段共创记录和原则持续保留；审查通过并确认后开始正文。",
      step: 1,
    },
    ready: {
      title: "规划已确认",
      description: "确认后的目录已同步到章节工作台。设置批次开始写作，整批完成后统一验收。",
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
    styleEntryId: "",
    styleEntries: [],
    styleLoadRequest: 0,
    styleAnalysisEntryId: "",
    styleAnalysisRunning: false,
    audit: null,
    cocreate: null,
    cocreateDirectory: "",
    cocreatePreview: "",
    cocreateDraftDirty: false,
    events: [],
    stream: "",
    selectedChapter: 0,
    selectedSource: "",
    readingRequest: 0,
    stateRequest: 0,
    chapterFilter: "all",
    reading: null,
    view: "overview",
    epoch: 0,
    source: null,
    streamConnected: false,
    loading: {},
    profileDirectory: "",
    modelCatalog: { provider: "", details: [] },
    modelDiscovery: { provider: "", details: {} },
    toastTimer: null,
    refreshTimer: null,
    styleBusyLocal: false,
    copySource: null,
    settingsRevision: 0,
    settingsBusy: false,
    planOutlineInitialized: false,
    activityTab: "events",
  };

  let versionPanel = null;
  let modelPanel = null;
  let jobsPanel = null;
  let modelRequestsPanel = null;
  let styleReferencesPanel = null;
  let chapterWorkbench = null;
  let mediaPanel = null;

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
    const { normalizeResponse = true, ...fetchOptions } = options;
    const request = { ...fetchOptions, headers: { ...(fetchOptions.headers || {}) } };
    if (request.body !== undefined && !(typeof FormData !== "undefined" && request.body instanceof FormData)) {
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
    if (normalizeResponse) payload = normalize(payload);
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
        Object.entries(state.loading).some(([key, value]) => value && !key.startsWith("style")),
    );
  }

  function isProjectNavigationBusy() {
    return Boolean(state.data?.busy || state.data?.stopping ||
      Object.entries(state.loading).some(([key, value]) => value && !key.startsWith("style") && !["chapter", "planning", "audit", "export"].includes(key)));
  }

  function isStyleEditingBusy() {
    return Boolean(state.styleBusyLocal || state.loading.styleUpload || state.loading.styleDelete || state.loading.styleAnalysis);
  }

  function syncStyleControls() {
    const busy = isStyleEditingBusy();
    const configured = Boolean(state.config?.provider && state.config?.model);
    $("#style-model-hint").textContent = configured
      ? `蒸馏使用默认模型 ${state.config.provider} / ${state.config.model}，其他项目可继续创作。`
      : "可以先管理条目和语料；启动蒸馏前，请在“模型与连接设置”保存并选择默认模型。";
    $("#run-style-analysis").disabled = !state.styleEntryId || !configured || busy || state.styleAnalysisRunning || !(state.style?.sources || []).length;
    $("#style-entry-select").disabled = Boolean(state.loading.styleCreate);
    $("#create-style-entry").disabled = Boolean(state.loading.styleCreate);
    for (const control of $$("input, textarea, button", $("#style-upload-form"))) control.disabled = !state.styleEntryId || busy;
    for (const control of $$("[data-style-delete]")) control.disabled = busy;
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
    $("#style-reference-panel").classList.toggle("hidden", !["overview", "chapters"].includes(view));
    if (["overview", "chapters"].includes(view)) styleReferencesPanel?.load({ quiet: true });
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
    if (view === "versions") versionPanel?.load();
    if (view === "jobs") jobsPanel?.load();
    if (view === "overview") loadCoCreate();
    if (view === "profile") renderProfile(true);
    if (view === "style") loadStyle();
    if (view === "planning") loadPlanning();
    if (view === "chapters") {
      renderChapters();
      if (state.selectedChapter && !state.loading.chapter && state.reading && !state.reading.planned) {
        readChapter(state.selectedChapter, state.selectedSource);
      }
    }
    if (view === "audit") loadAudit();
    if (view === "illustrations") {
      mediaPanel?.load();
      window.scrollTo?.({ top: 0, left: 0, behavior: "instant" });
    }
    if (view === "activity") setActivityTab(state.activityTab);
  }

  function applyState(data) {
    const previousDirectory = state.data?.directory || "";
    const wasRunning = isEngineRunning();
    const nextDirectory = data.directory || "";
    state.data = data;
    if (previousDirectory !== nextDirectory) {
      state.selectedChapter = Number(data.workflow?.latestChapter || 0);
      state.selectedSource = "";
      state.reading = null;
      state.readingRequest++;
      state.loading.chapter = false;
      state.chapterFilter = "all";
      $("#chapter-search").value = "";
      $("#chapter-filter").value = "all";
      state.planning = null;
      state.planOutlineInitialized = false;
      state.audit = null;
      state.profileDirectory = "";
      state.cocreate = null;
      state.cocreateDirectory = "";
      state.cocreatePreview = "";
      state.cocreateDraftDirty = false;
      versionPanel?.reset();
      modelRequestsPanel?.reset();
      styleReferencesPanel?.reset();
      mediaPanel?.reset();
      styleReferencesPanel?.load();
      restoreInputs(nextDirectory);
      if (nextDirectory) loadCoCreate();
    }
    renderCore();
    mediaPanel?.sync();
    if (state.view === "illustrations" && previousDirectory !== nextDirectory) mediaPanel?.load();
    renderProfile(previousDirectory !== nextDirectory);
    if (state.view === "chapters" && wasRunning && !isEngineRunning() && state.selectedChapter) {
      readChapter(state.selectedChapter, state.selectedSource);
    }
    syncDisabledControls();
  }

  async function refreshState({ quiet = false } = {}) {
    const epoch = state.epoch;
    const request = ++state.stateRequest;
    try {
      const data = await api("/api/state");
      if (epoch !== state.epoch || request !== state.stateRequest) return null;
      applyState(data);
      return data;
    } catch (error) {
      if (!quiet && epoch === state.epoch && request === state.stateRequest) setAlert(error.message, "error");
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
    const request = ++state.stateRequest;
    const results = await Promise.allSettled([
      api("/api/state"),
      api("/api/config"),
      api("/api/workspaces"),
    ]);
    if (epoch !== state.epoch) return;
    if (request === state.stateRequest && results[0].status === "fulfilled") {
      applyState(results[0].value);
    } else if (request === state.stateRequest && !quiet) {
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
    $("#go-latest").disabled = !(state.data?.chapters || []).length;
    $("#go-latest").textContent = workflow.latestChapter ? "查看最新章节 →" : "查看章节规划 →";
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
    for (const item of outline) {
      const row = el("div", "outline-row");
      row.append(
        el("span", "outline-number", String(item.chapter || "·").padStart(2, "0")),
      );
      const copy = el("div", "outline-copy");
      copy.append(
        el("strong", "", item.title || "待定标题"),
        el("small", "", item.coreEvent || item.core_event || "详细安排待生成"),
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
    const projectNavigationBusy = isProjectNavigationBusy();
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
    $("#resume-run").disabled = running || Boolean(data.stopping);
    $("#pause-run").disabled = (!running && !data.busy) || data.stopping;
    const hasCurrentExport = hasExportableChapters(data.directory);
    const currentExportBusy = projectExportBusy(data.directory);
    for (const format of ["txt", "epub"]) {
      const control = $(`#export-${format}`);
      control.disabled = busy || currentExportBusy || !hasCurrentExport;
      control.title = currentExportBusy ? "请先暂停并等待此项目操作结束后导出" : hasCurrentExport ? `导出已完成章节的 ${format.toUpperCase()}` : "尚无已完成章节";
    }
    $("#create-project").disabled = projectNavigationBusy;
    $("#refresh-projects").disabled = projectNavigationBusy;
    $("#runtime-model").disabled = busy || Boolean(data.modelConfigurationBusy);
    $("#select-runtime-model").disabled =
      busy || Boolean(data.modelConfigurationBusy) || !$("#runtime-model").value ||
      $("#runtime-model").value === modelPanel?.modelSelection(state.config?.provider, state.config?.model);
    $("#save-project-profile").disabled = busy || !data.directory;
    syncStyleControls();
    $("#approve-plan").disabled =
      busy || !workflow.canApprove || workflow.approved;
    $("#revise-plan").disabled = busy || !workflow.canRevise;
    $("#repair-plan").disabled =
      busy || !workflow.canRepair;
    $("#start-batch").disabled =
      busy || !workflow.approved || workflow.stage === "chapter-review";
    $("#accept-chapter").disabled =
      busy || workflow.stage !== "chapter-review";
    const selected = selectedChapterEntry();
    $("#send-chapter-feedback").disabled =
      busy || !workflow.approved || !selected || !chapterCanRewrite(selected);
    $("#chapter-feedback").disabled = !selected || !chapterCanRewrite(selected);
    for (const command of $$("[data-command]")) {
      command.disabled =
        busy || (command.dataset.command !== "sync" && !ready);
    }
    for (const cardButton of $$("[data-project-action]")) {
      cardButton.disabled =
        projectNavigationBusy || cardButton.dataset.persistentDisabled === "true";
    }
    versionPanel?.syncDisabled();
    chapterWorkbench?.syncDisabled();
  }

  function renderConfigDependentControls() {
    const config = state.config || {};
    const project = state.data?.project || {};
    modelPanel?.renderRuntimeModels();
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
    const projectBusy = Boolean(project.backgroundBusy);
    const projectRunning = project.status === "running" || (current && Boolean(state.data?.snapshot?.isRunning));
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
              : project.status === "background" ? "后台任务中" : "可切换",
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
    const exportable = Number(project.completed || 0) > 0;
    const exportBusy = projectExportBusy(project.path, project);
    const exportTxt = button("TXT", "text-button", () =>
      download("txt", project),
    );
    const exportEpub = button("EPUB", "text-button", () =>
      download("epub", project),
    );
    exportTxt.disabled = !exportable || exportBusy;
    exportEpub.disabled = !exportable || exportBusy;
    exportTxt.dataset.persistentDisabled = String(!exportable || exportBusy);
    exportEpub.dataset.persistentDisabled = String(!exportable || exportBusy);
    exportTxt.dataset.projectAction = "export-txt";
    exportEpub.dataset.projectAction = "export-epub";
    exportTxt.title = exportBusy ? "请先暂停并等待此项目操作结束后导出" : exportable ? "导出已完成章节的 TXT" : "尚无已完成章节";
    exportEpub.title = exportBusy ? "请先暂停并等待此项目操作结束后导出" : exportable ? "导出已完成章节的 EPUB" : "尚无已完成章节";
    actions.append(exportTxt, exportEpub);
    const copy = button("创建副本", "secondary-button", () => openCopyDialog(project));
    copy.dataset.projectAction = "copy";
    copy.dataset.persistentDisabled = String(projectBusy);
    copy.disabled = projectBusy;
    actions.append(copy);
    if (!archived) {
      const background = button("后台创作", "text-button", () => jobsPanel?.open(project));
      background.dataset.projectAction = "background";
      background.dataset.persistentDisabled = String(current || projectBusy || projectRunning);
      background.disabled = current || projectBusy || projectRunning;
      background.title = projectRunning ? "此项目正在创作，请先暂停后再安排后台任务" : current ? "请先切换到其他项目，再将本项目安排为后台任务" : "在后台开始正文批次或恢复";
      actions.append(background);
    }
    if (archived) {
      const restore = button("恢复项目", "secondary-button", () =>
        archiveProject(project, false),
      );
      restore.dataset.projectAction = "restore";
      restore.dataset.persistentDisabled = String(projectBusy || projectRunning);
      const remove = button("永久删除", "text-button danger-text", () =>
        deleteProject(project),
      );
      remove.dataset.projectAction = "delete";
      remove.dataset.persistentDisabled = String(projectBusy || projectRunning);
      actions.append(restore, remove);
    } else {
      const open = button(
        current ? "当前项目" : "打开项目",
        current ? "text-button" : "primary-button",
        () => switchProject(project),
      );
      open.disabled = current || projectBusy;
      open.dataset.persistentDisabled = String(current || projectBusy);
      open.dataset.projectAction = "open";
      const archive = button("归档", "text-button danger-text", () =>
        archiveProject(project, true),
      );
      archive.dataset.projectAction = "archive";
      archive.dataset.persistentDisabled = String(projectBusy || projectRunning);
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
    if (isProjectNavigationBusy()) {
      toast("正在处理项目操作，请稍后切换。", "error");
      return;
    }
    if (project.backgroundBusy) {
      toast("此项目的后台任务仍在运行，请等待结束后打开。", "error");
      return;
    }
    state.epoch += 1;
    modelRequestsPanel?.reset();
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

  function openCopyDialog(project) {
    state.copySource = project;
    $("#copy-project-source").textContent = `来源：${project.title || "未命名项目"}`;
    $("#copy-project-name").value = `${Array.from(project.title || "未命名项目").slice(0, 76).join("")}（副本）`;
    $("#copy-project-status").textContent = "";
    $("#copy-project-dialog").showModal();
  }

  async function copyProject(event) {
    event.preventDefault();
    if (!state.copySource || state.loading.copy) return;
    const source = state.copySource;
    const name = $("#copy-project-name").value.trim();
    const status = $("#copy-project-status");
    if (!name) {
      status.textContent = "请输入副本名称。";
      return;
    }
    setBusy("copy", true);
    $("#confirm-copy-project").disabled = true;
    status.textContent = "正在复制项目文件……";
    try {
      await api("/api/workspaces/copy", {
        method: "POST", body: { directory: source.path, name },
      });
      await loadWorkspaces();
      $("#copy-project-dialog").close();
      toast(`已创建独立副本《${name}》，可从项目中心打开`);
    } catch (error) {
      status.textContent = error.message;
    } finally {
      setBusy("copy", false);
      $("#confirm-copy-project").disabled = false;
    }
  }

  async function deleteProject(project) {
    if (!project?.archived) return;
    const title = project.title || "未命名项目";
    if (
      !confirm(
        `确定永久删除已归档项目《${title}》及其全部本地文件？`,
      )
    ) {
      return;
    }
    if (
      !confirm(
        `再次确认：永久删除后无法恢复，也不会保留章节、规划或项目记录。\n\n项目：${title}`,
      )
    ) {
      return;
    }
    setBusy("delete", true);
    try {
      await api("/api/workspaces/delete", {
        method: "POST",
        body: { directory: project.path, confirmed: true },
      });
      await refreshAll();
      toast(`已永久删除《${title}》`);
    } catch (error) {
      toast(error.message, "error");
    } finally {
      setBusy("delete", false);
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
    if (isProjectNavigationBusy()) return;
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
    setBusy("create", true);
    status.textContent = "正在创建项目……";
    state.epoch += 1;
    modelRequestsPanel?.reset();
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
      setBusy("create", false);
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
            `${provider.name}${provider.has_api_key ? "" : provider.requires_api_key === false ? " · 无需 Key" : " · 未配置 Key"}`,
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

  function createStyleReferencesPanel({ getContext, request }) {
    let directory = "", epoch = -1, generation = 0, readSequence = 0, revision = 0;
    let entries = [], selected = new Set(), dirty = false, saving = false, loaded = false, pending = false;
    let renderedKey = "";
    const current = (context, token) => {
      const now = getContext();
      return generation === token && context.directory === now.directory && context.epoch === now.epoch;
    };
    const message = (value) => { $("#style-reference-status").textContent = value; };

    function render() {
      $("#save-style-references").disabled = !directory || !loaded || !dirty || saving;
      const key = JSON.stringify([directory, loaded, saving, [...selected].sort(), entries.map((entry) => [entry.id, entry.name, entry.ready, entry.profileError])]);
      if (key === renderedKey) return;
      renderedKey = key;
      const list = $("#style-reference-list");
      const top = list.scrollTop;
      list.replaceChildren();
      if (!directory) list.append(el("p", "empty-state", "选择创作项目后，可勾选本项目要引用的共享风格。"));
      else if (!loaded) list.append(el("p", "empty-state", "正在读取风格库与本项目引用……"));
      else if (!entries.length) list.append(el("p", "empty-state", "共享风格库暂无条目。请先新建条目并完成蒸馏，再回来勾选引用。"));
      for (const entry of entries) {
        const label = el("label", `style-reference-choice${selected.has(entry.id) ? " selected" : ""}`);
        const checkbox = el("input");
        checkbox.type = "checkbox";
        checkbox.dataset.styleEntry = entry.id;
        checkbox.checked = selected.has(entry.id);
        checkbox.disabled = saving || (!entry.ready && !checkbox.checked);
        const copy = el("span", "style-reference-copy");
        copy.append(el("strong", "", entry.name || entry.id), el("small", "", entry.profileError ? `画像需修复：${entry.profileError}` : entry.ready ? "已蒸馏，可引用" : selected.has(entry.id) ? "已选条目需重新蒸馏，可取消引用" : "完成蒸馏后可引用"));
        checkbox.addEventListener("change", () => {
          if (saving || (checkbox.checked && !entry.ready)) return;
          if (checkbox.checked && selected.size >= 20) {
            checkbox.checked = false;
            message("最多引用 20 个风格条目，请先取消其他勾选。");
            return;
          }
          if (checkbox.checked) selected.add(entry.id);
          else selected.delete(entry.id);
          dirty = true;
          revision++;
          message("有未保存的风格引用更改。保存后用于本项目后续请求。");
          render();
        });
        label.append(checkbox, copy);
        list.append(label);
      }
      list.scrollTop = top;
    }

    function reset() {
      const context = getContext();
      directory = context.directory;
      epoch = context.epoch;
      generation++;
      readSequence++;
      revision++;
      entries = [];
      selected = new Set();
      dirty = saving = loaded = pending = false;
      renderedKey = "";
      message(directory ? "正在读取本项目的风格引用……" : "请先选择创作项目。");
      render();
    }

    async function load({ quiet = false } = {}) {
      const context = { ...getContext() };
      if (context.directory !== directory || context.epoch !== epoch) reset();
      if (!context.directory || (quiet && pending)) return;
      const token = generation, sequence = ++readSequence, editRevision = revision;
      pending = true;
      if (!quiet && !dirty && !saving) message("正在读取风格引用……");
      try {
        const result = await request(`/api/style/references?directory=${encodeURIComponent(context.directory)}`);
        if (!current(context, token) || sequence !== readSequence) return;
        if (result.directory !== context.directory) throw new Error("项目已切换，请刷新后重试。");
        entries = Array.isArray(result.entries) ? result.entries : [];
        loaded = true;
        if (!dirty && !saving && revision === editRevision) {
          selected = new Set(result.selectedIds || []);
          if (result.selectionNeedsConfirmation) {
            dirty = true;
            revision++;
            message(`${(result.warnings || []).join("；")} 可重新勾选，或直接保存空引用。`.trim());
          }
        }
        if (!dirty && !saving) {
          const status = selected.size ? `已保存 ${selected.size} 个风格引用；调整不会改写已完成正文。` : "尚未引用共享风格，可勾选已完成蒸馏的条目。";
          message([status, ...(result.warnings || [])].join(" "));
        }
        render();
      } catch (error) {
        if (current(context, token) && sequence === readSequence) message(`风格引用读取失败：${error.message}${dirty ? "；未保存的勾选已保留。" : ""}`);
      } finally {
        if (current(context, token) && sequence === readSequence) pending = false;
      }
    }

    async function save() {
      const context = { ...getContext() };
      if (context.directory !== directory || context.epoch !== epoch) {
        reset();
        return;
      }
      if (!context.directory || !loaded || !dirty || saving) return;
      const token = generation, selectedIds = [...selected], editRevision = ++revision;
      saving = true;
      message("正在保存本项目的风格引用……");
      render();
      try {
        const result = await request("/api/style/references", { method: "POST", body: { directory: context.directory, selectedIds } });
        if (!current(context, token)) return;
        if (result.directory !== context.directory) throw new Error("保存返回的项目不匹配，请刷新后重试。");
        if (revision === editRevision) {
          selected = new Set(result.selectedIds || selectedIds);
          dirty = false;
          revision++;
        }
        if (Array.isArray(result.entries)) entries = result.entries;
        message("风格引用已保存，下次模型请求生效；已完成正文保持原样。");
      } catch (error) {
        if (current(context, token)) message(`风格引用保存失败：${error.message}；当前勾选已保留。`);
      } finally {
        if (current(context, token)) {
          saving = false;
          render();
        }
      }
    }

    return { load, save, reset };
  }

  async function loadStyle({ entryId = state.styleEntryId, quiet = false } = {}) {
    const sequence = ++state.styleLoadRequest;
    setBusy("style", true);
    if (!quiet) $("#style-status").textContent = "正在读取共享风格库……";
    try {
      const result = await api(`/api/style${entryId ? `?entryId=${encodeURIComponent(entryId)}` : ""}`);
      if (sequence !== state.styleLoadRequest) return;
      if (entryId && result.entryId !== entryId) throw new Error("返回的风格条目不匹配，请刷新后重试。");
      state.style = result;
      state.styleEntryId = result.entryId || "";
      state.styleEntries = Array.isArray(result.entries) ? result.entries : [];
      state.styleAnalysisRunning = Boolean(result.analysisRunning);
      state.styleAnalysisEntryId = result.analysisEntryId || "";
      state.styleBusyLocal = Boolean(result.busy);
      if (!quiet) $("#style-status").textContent = state.styleEntryId ? "" : "请先新建并命名一个共享风格条目，再添加语料。";
      renderStyle();
      styleReferencesPanel?.load({ quiet: true });
    } catch (error) {
      if (sequence === state.styleLoadRequest) $("#style-status").textContent = `共享风格库读取失败：${error.message}`;
    } finally {
      if (sequence === state.styleLoadRequest) setBusy("style", false);
    }
  }

  async function selectStyleEntry(entryId) {
    if (entryId === state.styleEntryId) return;
    state.styleEntryId = entryId;
    state.style = null;
    state.styleBusyLocal = state.styleAnalysisRunning && state.styleAnalysisEntryId === entryId;
    $("#style-upload-form").reset();
    renderStyle();
    await loadStyle({ entryId });
  }

  async function createStyleEntry(event) {
    event.preventDefault();
    if (state.loading.styleCreate) return;
    const input = $("#style-entry-name");
    const name = input.value.trim();
    if (!name) {
      $("#style-status").textContent = "请为共享风格条目填写名称。";
      return;
    }
    setBusy("styleCreate", true);
    $("#style-status").textContent = "正在新建共享风格条目……";
    try {
      const entry = await api("/api/style/entries", { method: "POST", body: { name } });
      if (!entry.id) throw new Error("服务端未返回新条目，请刷新风格库。");
      if (input.value.trim() === name) input.value = "";
      await selectStyleEntry(entry.id);
      $("#style-status").textContent = `已新建“${entry.name || name}”，可以添加语料。`;
    } catch (error) {
      $("#style-status").textContent = `新建失败：${error.message}`;
    } finally {
      setBusy("styleCreate", false);
    }
  }

  function renderStyle() {
    const result = state.style || { sources: [], profile: null };
    const select = $("#style-entry-select");
    select.replaceChildren();
    if (!state.styleEntries.length) select.append(option("", "请先新建风格条目"));
    for (const entry of state.styleEntries) select.append(option(entry.id, `${entry.name} · ${entry.profileError ? "画像需修复" : entry.ready ? "可引用" : "待蒸馏"} · ${number(entry.sourceCount)} 份语料`, entry.id === state.styleEntryId));
    select.value = state.styleEntryId;
    $("#style-library-root").textContent = result.libraryRoot ? `本机共享库：${result.libraryRoot}` : "共享库保存在本机，所有项目均可引用。";
    $("#style-library-warnings").textContent = (result.warnings || []).join("\n");
    $("#style-library-warnings").classList.toggle("hidden", !(result.warnings || []).length);
    const selected = result.entry || state.styleEntries.find((entry) => entry.id === state.styleEntryId);
    $("#style-sources-title").textContent = selected ? `“${selected.name}”的语料` : "所选风格条目的语料";
    const list = $("#style-source-list");
    list.replaceChildren();
    const sources = result.sources || [];
    if (!sources.length) {
      list.append(
        el("p", "empty-state", state.styleEntryId ? "此条目尚未添加语料。支持 TXT、MD 和 Markdown。" : "请先新建一个共享风格条目。"),
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
      remove.dataset.styleDelete = "true";
      remove.disabled = isStyleEditingBusy();
      row.append(copy, remove);
      list.append(row);
    }
    if (state.styleAnalysisRunning && !state.styleBusyLocal) {
      const analyzing = state.styleEntries.find((entry) => entry.id === state.styleAnalysisEntryId);
      $("#style-status").textContent = `共享条目“${analyzing?.name || "其他条目"}”正在蒸馏；可以继续编辑此条目，完成后可启动新的分析。`;
    }
    $("#style-profile-state").textContent =
      state.styleBusyLocal
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
    syncStyleControls();
  }

  async function uploadStyleSource(event) {
    event.preventDefault();
    const entryId = state.styleEntryId;
    if (!entryId || isStyleEditingBusy()) return;
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
        body: { entryId, name, content },
      });
      if (entryId === state.styleEntryId) {
        $("#style-upload-form").reset();
        status.textContent = "语料已保存到共享条目，请重新蒸馏以更新画像。";
        await loadStyle({ entryId, quiet: true });
      }
      styleReferencesPanel?.load({ quiet: true });
    } catch (error) {
      if (entryId === state.styleEntryId) status.textContent = error.message;
    } finally {
      setBusy("styleUpload", false);
    }
  }

  async function deleteStyleSource(name) {
    const entryId = state.styleEntryId;
    if (!entryId || isStyleEditingBusy()) return;
    if (!confirm(`确定删除语料“${name}”？`)) return;
    setBusy("styleDelete", true);
    try {
      await api("/api/style/delete", {
        method: "POST",
        body: { entryId, name },
      });
      if (entryId === state.styleEntryId) await loadStyle({ entryId, quiet: true });
      styleReferencesPanel?.load({ quiet: true });
      toast("语料已删除");
    } catch (error) {
      if (entryId === state.styleEntryId) $("#style-status").textContent = error.message;
    } finally {
      setBusy("styleDelete", false);
    }
  }

  async function runStyleAnalysis() {
    const entryId = state.styleEntryId;
    if (!entryId || !(state.config?.provider && state.config?.model) || isStyleEditingBusy() || state.styleAnalysisRunning || !(state.style?.sources || []).length) return;
    setBusy("styleAnalysis", true);
    state.styleBusyLocal = true;
    state.styleAnalysisRunning = true;
    state.styleAnalysisEntryId = entryId;
    $("#style-status").textContent = "正在启动风格蒸馏……";
    $("#style-progress-wrap").classList.remove("hidden");
    $("#style-progress-bar").style.width = "0%";
    try {
      await api("/api/style/run", { method: "POST", body: { entryId } });
      if (entryId === state.styleEntryId) {
        $("#style-status").textContent = "共享风格条目已开始蒸馏。完成后，可在创作页面勾选引用；创作可继续进行。";
      }
    } catch (error) {
      if (state.styleAnalysisEntryId === entryId) {
        state.styleAnalysisRunning = false;
        state.styleAnalysisEntryId = "";
      }
      if (entryId === state.styleEntryId) {
        state.styleBusyLocal = false;
        $("#style-progress-wrap").classList.add("hidden");
        $("#style-status").textContent = error.message;
      }
    } finally {
      setBusy("styleAnalysis", false);
      renderStyle();
    }
  }

  async function selectStyleFile() {
    const entryId = state.styleEntryId;
    if (!entryId || isStyleEditingBusy()) return;
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
    const content = await file.text();
    if (entryId !== state.styleEntryId) return;
    $("#style-source-name").value = file.name;
    $("#style-source-content").value = content;
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
    const outline = (planning.outline || []).map(chapterEntry);
    const layered = layeredPlanVolumes(planning.layered);
    const layeredStats = layeredPlanStats(layered);
    $("#plan-fingerprint").textContent = workflow.fingerprint || "尚未生成";
    const auditState = workflow.auditState || "missing";
    $("#plan-approval-state").textContent = workflow.approved
      ? "已确认当前版本"
      : auditState === "failed"
        ? "审查未通过，可修改后重新审查"
        : auditState === "stale"
          ? "审查版本已过期，可修改后重新审查"
          : auditState === "ready"
            ? "审查通过，等待用户确认"
            : workflow.canRepair
              ? "等待恢复并重新审查"
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
    $("#outline-count").textContent = layered.length
      ? [
          `${layered.length} 卷`,
          `${layeredStats.arcs} 弧`,
          `${layeredStats.chapters} 章`,
          layeredStats.incompleteArcs
            ? `${layeredStats.incompleteArcs} 弧待补齐`
            : "",
        ].filter(Boolean).join(" · ")
      : `${groupChapters(outline).length} 卷 · ${outline.length} 章`;
    const hasWritten = workbenchChapters().some(chapterHasContent);
    const completedBook = workflow.stage === "complete" || state.data?.snapshot?.phase === "complete";
    $("#plan-edit-policy").textContent = completedBook
      ? "作品已完结 · 当前目录和设定已归档。已完成正文如需调整，请在章节工作台选中一章，提交具体指令单独重写。"
      : hasWritten
      ? "第三阶段 · 调整目录、分卷和后续规划后，需重新审查并确认。已完成正文保留原样；如需改写，请在章节工作台选中一章并提供具体指令。"
      : "第二阶段 · 目录、分卷和设定均可按意见修改并覆盖。第一阶段共创记录、创作原则和用户约束会保留，修改后重新审查并由你确认。";
    renderPlanOutline(outline, layered);
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

  function renderPlanOutline(outline, layered = []) {
    if (layered.length) {
      renderLayeredPlanOutline(layered);
      return;
    }
    const container = $("#plan-outline");
    const expanded = new Set($$(".outline-detail[open]", container).map((node) => node.dataset.chapter));
    container.replaceChildren();
    if (!outline.length) {
      container.append(el("p", "empty-state", "章节目录尚未生成。"));
      return;
    }
    for (const group of groupChapters(outline)) {
      const section = el("section", "plan-volume");
      const heading = el("div", "volume-heading");
      heading.append(el("h4", "", group.label), el("span", "soft-badge", `${group.chapters.length} 章`));
      section.append(heading);
      for (const item of group.chapters) {
        const details = el("details", "outline-detail");
        details.dataset.chapter = String(item.number);
        details.open = expanded.has(String(item.number));
        details.append(el("summary", "", `第 ${item.number || "?"} 章 · ${item.title || "待定标题"}`));
        if (item.arcTitle) details.append(el("span", "chapter-arc", item.arcTitle));
        if (item.coreEvent) details.append(el("p", "outline-event", item.coreEvent));
        if (item.hook) details.append(el("p", "outline-event", `章节悬念：${item.hook}`));
        if (item.scenes?.length) {
          const scenes = el("ul", "outline-scenes");
          for (const scene of item.scenes) scenes.append(el("li", "", typeof scene === "string" ? scene : pretty(scene)));
          details.append(scenes);
        }
        section.append(details);
      }
      container.append(section);
    }
  }

  function layeredPlanVolumes(value) {
    if (!Array.isArray(value)) return [];
    let chapterNumber = 0;
    return value.map((rawVolume, volumePosition) => {
      const volumeIndex = Number(rawVolume?.index || volumePosition + 1);
      const volumeTitle = rawVolume?.title || `第 ${volumeIndex} 卷`;
      const rawArcs = Array.isArray(rawVolume?.arcs) ? rawVolume.arcs : [];
      return {
        index: volumeIndex,
        title: volumeTitle,
        theme: rawVolume?.theme || "",
        final: Boolean(rawVolume?.final),
        arcs: rawArcs.map((rawArc, arcPosition) => {
          const arcIndex = Number(rawArc?.index || arcPosition + 1);
          const arcTitle = rawArc?.title || `第 ${arcIndex} 弧`;
          const rawChapters = Array.isArray(rawArc?.chapters)
            ? rawArc.chapters
            : [];
          const chapters = rawChapters.map((item) => {
            const entry = chapterEntry(item);
            chapterNumber += 1;
            return {
              ...entry,
              number: chapterNumber,
              volumeId: volumeIndex,
              volumeTitle,
              arcId: arcIndex,
              arcTitle,
            };
          });
          return {
            index: arcIndex,
            title: arcTitle,
            goal: rawArc?.goal || "",
            estimatedChapters: Number(
              rawArc?.estimatedChapters ?? rawArc?.estimated_chapters ?? 0,
            ) || 0,
            chapters,
          };
        }),
      };
    });
  }

  function layeredPlanStats(volumes) {
    let arcs = 0;
    let chapters = 0;
    let incompleteArcs = 0;
    for (const volume of volumes) {
      for (const arc of volume.arcs) {
        arcs += 1;
        chapters += arc.chapters.length;
        if (!arc.chapters.length) incompleteArcs += 1;
      }
    }
    return { arcs, chapters, incompleteArcs };
  }

  function renderLayeredPlanOutline(volumes) {
    const container = $("#plan-outline");
    const openKeys = new Set(
      $$("details[data-plan-key][open]", container).map(
        (node) => node.dataset.planKey,
      ),
    );
    const firstRender = !state.planOutlineInitialized;
    const isOpen = (key, defaultOpen) =>
      firstRender ? defaultOpen : openKeys.has(key);
    container.replaceChildren();
    for (const volume of volumes) {
      const volumeChapters = volume.arcs.reduce(
        (total, arc) => total + arc.chapters.length,
        0,
      );
      const incompleteArcs = volume.arcs.filter(
        (arc) => !arc.chapters.length,
      ).length;
      const volumeKey = `volume:${volume.index}`;
      const volumeDetails = el("details", "plan-volume");
      volumeDetails.dataset.planKey = volumeKey;
      volumeDetails.open = isOpen(volumeKey, true);
      const summary = el("summary", "plan-volume-summary");
      const title = el("div", "plan-volume-title");
      title.append(
        el("span", "plan-level-label", `卷 ${volume.index}`),
        el("h4", "", volume.title),
      );
      const meta = el("div", "plan-volume-meta");
      meta.append(
        el("span", "soft-badge", `${volume.arcs.length} 弧`),
        el("span", "soft-badge", `${volumeChapters} 章`),
      );
      if (incompleteArcs) {
        meta.append(
          el("span", "soft-badge pending", `${incompleteArcs} 弧待补齐`),
        );
      }
      if (volume.final) {
        meta.append(el("span", "soft-badge approved", "收官卷"));
      }
      summary.append(title, meta);
      volumeDetails.append(summary);
      if (volume.theme) {
        volumeDetails.append(el("p", "plan-volume-theme", volume.theme));
      }
      const arcList = el("div", "plan-arcs");
      for (const arc of volume.arcs) {
        arcList.append(renderPlanArc(volume, arc, isOpen));
      }
      if (!volume.arcs.length) {
        arcList.append(
          el(
            "p",
            "plan-skeleton-note",
            "本卷尚未生成弧与章节规划，需要补齐。",
          ),
        );
      }
      volumeDetails.append(arcList);
      container.append(volumeDetails);
    }
    state.planOutlineInitialized = true;
  }

  function renderPlanArc(volume, arc, isOpen) {
    const arcKey = `arc:${volume.index}:${arc.index}`;
    const details = el(
      "details",
      arc.chapters.length ? "plan-arc" : "plan-arc skeleton",
    );
    details.dataset.planKey = arcKey;
    details.open = isOpen(arcKey, true);
    const summary = el("summary", "plan-arc-summary");
    const copy = el("div", "plan-arc-title");
    copy.append(
      el("span", "plan-level-label", `弧 ${arc.index}`),
      el("strong", "", arc.title),
    );
    const meta = el("div", "plan-arc-meta");
    meta.append(
      arc.chapters.length
        ? el("span", "soft-badge", `${arc.chapters.length} 章`)
        : el(
            "span",
            "soft-badge pending",
            arc.estimatedChapters
              ? `预估 ${arc.estimatedChapters} 章`
              : "尚无逐章目录",
          ),
    );
    if (!arc.chapters.length) {
      meta.append(el("span", "soft-badge pending", "历史骨架待补齐"));
    }
    summary.append(copy, meta);
    details.append(summary);
    if (arc.goal) details.append(el("p", "plan-arc-goal", arc.goal));
    if (!arc.chapters.length) {
      details.append(
        el(
          "p",
          "plan-skeleton-note",
          "该弧只有历史容量估算，没有真实章节。全量重规划后会在这里列出每一章。",
        ),
      );
      return details;
    }
    const chapterList = el("div", "plan-chapters");
    for (const item of arc.chapters) {
      const chapterKey = `chapter:${volume.index}:${arc.index}:${item.number}`;
      const chapterDetails = el("details", "outline-detail");
      chapterDetails.dataset.planKey = chapterKey;
      chapterDetails.open = isOpen(chapterKey, false);
      chapterDetails.append(
        el(
          "summary",
          "",
          `第 ${item.number || "?"} 章 · ${item.title || "待定标题"}`,
        ),
      );
      if (item.coreEvent) {
        chapterDetails.append(el("p", "outline-event", item.coreEvent));
      }
      if (item.hook) {
        chapterDetails.append(
          el("p", "outline-event", `章节悬念：${item.hook}`),
        );
      }
      if (item.scenes?.length) {
        const scenes = el("ul", "outline-scenes");
        for (const scene of item.scenes) {
          scenes.append(
            el("li", "", typeof scene === "string" ? scene : pretty(scene)),
          );
        }
        chapterDetails.append(scenes);
      }
      chapterList.append(chapterDetails);
    }
    details.append(chapterList);
    return details;
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

  async function repairPlan() {
    await runAction({ action: "repair-plan" });
  }

  function chapterEntry(item) {
    return {
      ...item,
      number: Number(item.number || item.chapter || 0),
      coreEvent: item.coreEvent || item.core_event || "",
      volumeId: item.volumeId || item.volumeID || item.volume_id || 1,
      volumeTitle: item.volumeTitle || item.volume_title || "正文",
      arcId: item.arcId || item.arcID || item.arc_id || 0,
      arcTitle: item.arcTitle || item.arc_title || "",
      hasContent: item.hasContent ?? item.has_content,
      hasDraft: item.hasDraft ?? item.has_draft,
    };
  }

  function workbenchChapters() {
    return (state.data?.chapters || []).map(chapterEntry);
  }

  function chapterHasContent(chapter) {
    return Boolean(chapter && (chapter.hasContent ?? (chapter.source !== "planned" && chapter.status !== "planned")));
  }

  function chapterCanRewrite(chapter) {
    return chapterHasContent(chapter) && chapter.source !== "draft" && chapter.status !== "draft";
  }

  function selectedChapterEntry() {
    return workbenchChapters().find((chapter) => chapter.number === Number(state.selectedChapter) &&
      (chapter.source || "") === (state.selectedSource || "")) ||
      workbenchChapters().find((chapter) => chapter.number === Number(state.selectedChapter));
  }

  function groupChapters(chapters) {
    const groups = new Map();
    for (const chapter of chapters) {
      const key = String(chapter.volumeId || 1);
      if (!groups.has(key)) groups.set(key, {
        key, label: `第 ${chapter.volumeId || 1} 卷 · ${chapter.volumeTitle || "正文"}`, chapters: [],
      });
      groups.get(key).chapters.push(chapter);
    }
    return [...groups.values()];
  }

  function chapterStatus(chapter) {
    if (!chapterHasContent(chapter)) return "待写";
    return chapter.source === "draft" ? "草稿" : chapter.hasDraft ? "已有终稿 · 重写中" : "已完成";
  }

  function renderChapters() {
    const chapters = workbenchChapters();
    const container = $("#chapter-list");
    const closed = new Set($$(".chapter-volume", container).filter((node) => !node.open).map((node) => node.dataset.volume));
    let selected = selectedChapterEntry();
    if (!selected && chapters.length) {
      selected = [...chapters].reverse().find(chapterHasContent) || chapters[0];
      state.selectedChapter = selected.number;
      state.selectedSource = selected.source || "";
    }
    const sourceChanged = selected && state.selectedSource !== (selected.source || "");
    if (sourceChanged) state.selectedSource = selected.source || "";
    const query = $("#chapter-search").value.trim().toLowerCase();
    const filtered = chapters.filter((chapter) => {
      const statusMatch = state.chapterFilter === "all" ||
        (state.chapterFilter === "planned" ? !chapterHasContent(chapter) : chapterHasContent(chapter));
      return statusMatch && `${chapter.number} ${chapter.title} ${chapter.volumeTitle} ${chapter.arcTitle}`.toLowerCase().includes(query);
    });
    container.replaceChildren();
    const completed = chapters.filter(chapterCanRewrite).length;
    $("#chapter-count").textContent = `${groupChapters(chapters).length} 卷 · ${chapters.length} 章`;
    $("#chapter-progress").textContent = `已完成 ${completed} / ${chapters.length} 章`;
    $("#chapter-progress-bar").style.width = `${chapters.length ? Math.round(completed / chapters.length * 100) : 0}%`;
    $("#workbench-plan-state").textContent = state.data?.workflow?.approved ? "当前规划已确认" : "规划待审查 / 确认";
    $("#workbench-plan-state").className = `soft-badge ${state.data?.workflow?.approved ? "approved" : "pending"}`;
    if (!filtered.length) container.append(el("p", "empty-state", chapters.length ? "没有符合筛选条件的章节。" : "规划生成后，分卷与全部章节会显示在这里。"));
    for (const group of groupChapters(filtered)) {
      const section = el("details", "chapter-volume");
      section.dataset.volume = group.key;
      section.open = !closed.has(group.key);
      const heading = el("summary", "volume-heading");
      heading.append(el("strong", "", group.label), el("small", "", `${group.chapters.length} 章`));
      section.append(heading);
      for (const chapter of group.chapters) {
        const active = chapter.number === Number(state.selectedChapter) && (chapter.source || "") === (state.selectedSource || "");
        const item = button("", `chapter-item ${active ? "active" : ""}`, () => {
          const changed = chapter.number !== Number(state.selectedChapter) || (chapter.source || "") !== (state.selectedSource || "");
          const reading = readChapter(chapter.number, chapter.source || "", { resetScroll: changed });
          renderChapters();
          return reading;
        });
        item.setAttribute("aria-current", active ? "true" : "false");
        item.title = `第 ${chapter.number} 章 · ${chapter.title || "待定标题"}`;
        item.append(el("strong", "", item.title));
        const meta = el("span", "chapter-item-meta");
        meta.append(el("span", `chapter-status ${chapterHasContent(chapter) ? "written" : "planned"}`, chapterStatus(chapter)),
          el("small", "", chapterHasContent(chapter) ? `${number(chapter.words)} 字` : chapter.arcTitle || "查看章节规划"));
        item.append(meta);
        section.append(chapterWorkbench ? chapterWorkbench.chapterRow(chapter, item) : item);
      }
      container.append(section);
    }
    if (selected) {
      $("#chapter-feedback-title").textContent = "按指令重写与批次验收";
      $("#send-chapter-feedback").textContent = `按指令重写第 ${selected.number} 章`;
      $("#chapter-feedback-hint").textContent = !chapterHasContent(selected)
        ? "本章尚未生成正文，可以展开下方编辑区保存章节规划；批量重写仅用于已有正文的章节。"
        : !chapterCanRewrite(selected)
          ? "本章草稿尚未完成，请等待章节完成后再提交单章重写指令。"
        : !state.data?.workflow?.approved
          ? "请先完成当前规划的重新审查和确认，再提交本章的重写指令。"
          : "在分卷目录勾选需要重写的章节。新稿默认作为候选版本保留，也可勾选自动采用；未勾选章节时重写当前章。";
      if (!chapterHasContent(selected)) renderPlannedChapter(selected);
      else if (sourceChanged || !state.reading || Number(state.reading.number) !== selected.number) {
        if (!state.loading.chapter) readChapter(selected.number, selected.source || "");
      }
    } else {
      state.reading = null;
      $("#reader-meta").textContent = "等待章节规划";
      $("#reader-title").textContent = "章节工作台";
      $("#reader-content").textContent = "确认规划后，即可在这里按卷查看目录和章节安排。";
      $("#reader-content").classList.add("empty-state");
      $("#reader-notes-wrap").classList.add("hidden");
    }
    chapterWorkbench?.sync();
    mediaPanel?.syncReader();
    syncDisabledControls();
  }

  function renderPlannedChapter(chapter) {
    state.reading = { number: chapter.number, planned: true };
    $("#reader-meta").textContent = `第 ${chapter.volumeId} 卷 · ${chapter.volumeTitle} / 第 ${chapter.number} 章 · 待写${chapter.arcTitle ? ` · ${chapter.arcTitle}` : ""}`;
    $("#reader-title").textContent = chapter.title || `第 ${chapter.number} 章`;
    const content = $("#reader-content");
    content.classList.remove("empty-state");
    content.replaceChildren();
    const label = el("div", "planned-chapter-label", "章节规划 · 正文尚未生成");
    content.append(label, el("p", "", chapter.coreEvent || "本章详细事件将在规划中完善。"));
    if (chapter.hook) content.append(el("h3", "", "章节悬念"), el("p", "", chapter.hook));
    if (chapter.scenes?.length) {
      content.append(el("h3", "", "场景安排"));
      const scenes = el("ol", "planned-scenes");
      for (const scene of chapter.scenes) scenes.append(el("li", "", typeof scene === "string" ? scene : pretty(scene)));
      content.append(scenes);
    }
    $("#reader-notes-wrap").classList.add("hidden");
  }

  function resetChapterScroll() {
    for (const node of [$(".reader"), $("#reader-content"), document.scrollingElement]) {
      if (node) {
        node.scrollTop = 0;
        node.scrollLeft = 0;
      }
    }
    window.scrollTo?.({ top: 0, left: 0, behavior: "instant" });
  }

  async function readChapter(chapter, source = "", { resetScroll = false } = {}) {
    const directory = state.data?.directory;
    if (!directory || !chapter) return;
    state.selectedChapter = Number(chapter);
    state.selectedSource = source;
    const entry = selectedChapterEntry();
    const request = ++state.readingRequest;
    if (entry && !chapterHasContent(entry)) {
      state.loading.chapter = false;
      renderPlannedChapter(entry);
      if (resetScroll) resetChapterScroll();
      syncDisabledControls();
      return;
    }
    setBusy("chapter", true);
    $("#reader-title").textContent = "正在读取章节……";
    try {
      const query = new URLSearchParams({ directory });
      if (source === "draft") query.set("source", "draft");
      const result = await api(`/api/chapters/${encodeURIComponent(chapter)}?${query}`);
      if (request !== state.readingRequest || directory !== state.data?.directory) return;
      state.reading = result;
      renderReader();
      if (resetScroll) resetChapterScroll();
    } catch (error) {
      if (request !== state.readingRequest || directory !== state.data?.directory) return;
      $("#reader-meta").textContent = "读取失败";
      $("#reader-title").textContent = "章节不可用";
      $("#reader-content").textContent = error.message;
      $("#reader-content").classList.add("empty-state");
      if (resetScroll) resetChapterScroll();
    } finally {
      if (request === state.readingRequest) setBusy("chapter", false);
    }
  }

  function renderReader() {
    const reading = state.reading;
    if (!reading || reading.planned) return;
    if (chapterWorkbench?.renderReading()) return;
    const chapter = selectedChapterEntry();
    const volume = chapter ? `第 ${chapter.volumeId} 卷 · ${chapter.volumeTitle} / ` : "";
    const draft = state.selectedSource === "draft";
    $("#reader-meta").textContent = `${volume}第 ${reading.number} 章 · ${draft ? "草稿" : "已完成"} · ${number((reading.content || "").replace(/\s/g, "").length)} 字`;
    $("#reader-title").textContent = reading.summary?.title || chapter?.title || `第 ${reading.number} 章`;
    $("#reader-content").textContent = reading.content || "正文为空。";
    $("#reader-content").classList.toggle("empty-state", !reading.content);
    $("#reader-notes-wrap").classList.remove("hidden");
    $("#reader-notes-wrap").open = false;
    renderJson($("#reader-notes"), { summary: reading.summary, review: reading.review }, "本章暂无摘要或评审记录。");
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
    if (chapterWorkbench) return chapterWorkbench.rewrite();
    const text = $("#chapter-feedback").value.trim();
    const chapter = Number(
      state.selectedChapter ||
        state.reading?.number ||
        state.data?.workflow?.latestChapter ||
        0,
    );
    if (!chapter || !chapterCanRewrite(selectedChapterEntry())) {
      toast("请选择已完成的单个章节再提交重写指令。", "error");
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
      // Refreshes also follow background task events and failed submissions.
      // Keep a user's draft until they explicitly confirm/reset it or switch
      // projects; renderCoCreate already respects this dirty flag.
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
    workspace.classList.toggle("hidden", !hasSession);
    workspace.classList.toggle("archived", started);
    const history = $("#cocreate-history");
    if (history.dataset.started !== String(started)) {
      history.open = !started;
      history.dataset.started = String(started);
    }
    $("#cocreate-history-title").textContent = started
      ? `第一阶段共创记录 · ${transcript.length} 条（已保留）` : "共创问答记录";
    $("#cocreate-draft-title").textContent = started
      ? "查看第一阶段创作原则与完整指令（已保留）" : "查看并编辑当前完整创作指令";
    for (const controls of $$(".co-create-reply-label, .co-create-actions", workspace)) {
      controls.classList.toggle("hidden", started);
    }

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
      message.textContent = "第一阶段的问答记录与创作原则已保留。后续修改请前往“规划审核”，将以这些原则作为持续约束。";
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
        "repair-plan": "恢复旧项目并重新审查规划",
        batch: "正文批次已启动",
        "accept-chapter": "章节验收已提交",
        "chapter-feedback": "章节反馈已提交",
      };
      toast(labels[actionName] || "操作已提交");
      await refreshState();
      if (actionName === "approve-plan") {
        state.planning = null;
        setView("chapters");
      } else if (["revise-plan", "repair-plan"].includes(actionName)) {
        await loadPlanning();
      }
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

  function projectExportBusy(directory, project = null) {
    const workspace = state.workspaces.find((item) => item.path === directory) || project;
    return Boolean(workspace?.backgroundBusy || ["running", "background"].includes(workspace?.status) ||
      directory === state.data?.directory && (state.data.busy || state.data.stopping || state.data.snapshot?.isRunning));
  }

  function hasExportableChapters(projectPath) {
    if (!projectPath) return false;
    if (projectPath === state.data?.directory) {
      if (
        workbenchChapters().some(
          (chapter) => chapterHasContent(chapter) && (chapter.source || "") !== "draft",
        )
      ) {
        return true;
      }
    }
    const project = state.workspaces.find(
      (item) => item.path === projectPath,
    );
    return Number(project?.completed || 0) > 0;
  }

  function exportFilename(response, format) {
    const disposition = response.headers.get("content-disposition") || "";
    const encoded = disposition.match(
      /filename\*\s*=\s*UTF-8''([^;]+)/i,
    );
    if (encoded?.[1]) {
      try {
        return decodeURIComponent(encoded[1].trim().replace(/^"|"$/g, ""));
      } catch (_) {
        // Fall back to the legacy filename parameter below.
      }
    }
    const plain = disposition.match(
      /filename\s*=\s*"([^"]+)"|filename\s*=\s*([^;]+)/i,
    );
    return (plain?.[1] || plain?.[2] || `novel.${format}`).trim();
  }

  async function download(format, project = null) {
    const directory = project?.path || state.data?.directory || "";
    if (!directory) {
      toast("请先选择创作项目。", "error");
      return;
    }
    if (state.loading.export) return;
    if (projectExportBusy(directory, project)) {
      toast("请先暂停并等待此项目操作结束后导出。", "error");
      return;
    }
    if (!hasExportableChapters(directory)) {
      toast("项目尚无已完成章节，草稿不会进入导出。", "error");
      return;
    }
    setBusy("export", true);
    try {
      const response = await fetch("/api/export", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ format, directory }),
      });
      if (!response.ok) {
        const payload = normalize(await response.json().catch(() => ({})));
        throw new Error(payload.error || "导出失败");
      }
      const filename = exportFilename(response, format);
      const data = new Uint8Array(await response.arrayBuffer());
      const result = await window.ainovelDesktop.saveExport({ format, filename, data });
      if (result?.saved) toast(`已保存 ${format.toUpperCase()}：${result.path || filename}`);
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

  function setActivityTab(tab, focus = false) {
    state.activityTab = tab === "requests" ? "requests" : "events";
    for (const name of ["events", "requests"]) {
      const active = state.activityTab === name;
      const control = $(`#activity-${name}-tab`);
      control.classList.toggle("active", active);
      control.setAttribute("aria-selected", String(active));
      control.tabIndex = active ? 0 : -1;
      $(`#activity-${name}-panel`).classList.toggle("hidden", !active);
      if (active && focus) control.focus();
    }
    if (state.activityTab === "requests") modelRequestsPanel?.load();
    else renderActivity();
  }

  // Keep the selected detail nodes connected while polling. Native details state,
  // keyboard focus and long JSON scroll positions then survive each update.
  function createModelRequestsPanel({ getContext, request }) {
    const statusNames = {
      running: "请求中", success: "传输完成", error: "失败", canceled: "已取消", interrupted: "已中断",
    };
    let generation = 0;
    let listRequest = 0;
    let detailRequest = 0;
    let directory = "";
    let epoch = -1;
    let records = [];
    let selectedId = "";
    let detailNodes = null;
    let listSignature = "";
    let listInFlight = false;
    let detailInFlight = false;

    const setText = (node, value) => {
      const text = String(value ?? "—");
      if (node.textContent === text) return;
      const top = node.scrollTop;
      const left = node.scrollLeft;
      node.textContent = text;
      node.scrollTop = top;
      node.scrollLeft = left;
    };
    const milliseconds = (value) => {
      if (value === undefined || value === null || Number(value) < 0) return "—";
      const ms = Number(value);
      if (!Number.isFinite(ms)) return "—";
      return ms < 1000 ? `${Math.round(ms)} 毫秒` : `${(ms / 1000).toFixed(1)} 秒`;
    };
    const timeValue = (value) => {
      const time = value ? new Date(value).getTime() : 0;
      return Number.isFinite(time) && time > 0 ? time : 0;
    };
    const timestamp = (value) => timeValue(value) ? formatTime(value) : "—";
    const requestDuration = (record, now = Date.now()) => {
      if (record.status !== "running") return record.durationMs;
      const saved = Number(record.durationMs);
      const started = timeValue(record.startedAt);
      return Math.max(Number.isFinite(saved) ? saved : 0, started ? now - started : 0, 0);
    };
    const knownStatus = (value) => Object.hasOwn(statusNames, value) ? value : "unknown";
    const statusLabel = (value) => statusNames[knownStatus(value)] || "未知状态";
    const sensitiveKey = (key) => /^(authorization|proxyauthorization|apikey|accesskey|secretkey|clientsecret|accesstoken|refreshtoken|password|cookie|setcookie|xapikey|token)$/i.test(String(key).replace(/[-_\s]/g, ""));
    const safeJson = (value, depth = 0) => {
      if (depth > 40) return "[内容层级过深]";
      if (Array.isArray(value)) return value.map((item) => safeJson(item, depth + 1));
      if (!value || typeof value !== "object") return value;
      return Object.fromEntries(Object.entries(value).map(([key, child]) => [
        key, sensitiveKey(key) ? "[已隐藏]" : safeJson(child, depth + 1),
      ]));
    };
    const endpointLabel = (value) => {
      if (!value) return "—";
      try {
        const url = new URL(value);
        url.username = "";
        url.password = "";
        for (const key of [...url.searchParams.keys()]) {
          if (sensitiveKey(key)) url.searchParams.set(key, "[已隐藏]");
        }
        return url.toString();
      } catch (_) {
        return "未提供有效请求地址";
      }
    };
    const current = (context, token) => {
      const now = getContext();
      return token === generation && context.directory === now.directory && context.epoch === now.epoch;
    };

    function reset() {
      generation++;
      listRequest++;
      detailRequest++;
      listInFlight = false;
      detailInFlight = false;
      directory = getContext().directory;
      epoch = getContext().epoch;
      records = [];
      selectedId = "";
      detailNodes = null;
      listSignature = "";
      $("#model-request-list").replaceChildren(el("p", "empty-state", "尚未加载当前项目的模型请求。"));
      $("#model-request-detail").replaceChildren(el("p", "empty-state", "选择一条请求查看连接、耗时与输入输出。"));
      $("#model-requests-count").textContent = "0 条";
      $("#model-requests-message").textContent = "";
      $("#model-request-detail-message").textContent = "";
    }

    function renderList() {
      const now = Date.now();
      const signature = JSON.stringify([selectedId, records.map((record) => [
        record.id, record.model, record.agent, record.provider, record.status,
        record.stream, record.startedAt, requestDuration(record, now), record.chunks,
      ])]);
      if (signature === listSignature) return;
      listSignature = signature;
      const list = $("#model-request-list");
      const top = list.scrollTop;
      list.replaceChildren();
      $("#model-requests-count").textContent = `${records.length} 条`;
      if (!records.length) {
        list.append(el("p", "empty-state", $("#model-request-status").value ? "当前筛选下暂无请求记录。" : "暂无模型请求。开始共创或创作后，请求会记录在这里。"));
      }
      for (const record of records) {
        const row = button("", `model-request-row${record.id === selectedId ? " selected" : ""}`, () => select(record.id));
        row.setAttribute("aria-pressed", String(record.id === selectedId));
        const head = el("span", "model-request-row-heading");
        head.append(el("strong", "", record.model || "未指定模型"), el("span", `model-request-badge ${knownStatus(record.status)}`, statusLabel(record.status)));
        row.append(head,
          el("span", "model-request-row-meta", `${record.agent || "默认 Agent"} · ${record.provider || "默认连接"} · ${record.stream ? "流式" : "非流式"}`),
          el("span", "model-request-row-meta", `${timestamp(record.startedAt)} · ${milliseconds(requestDuration(record, now))} · ${number(record.chunks)} 个流式事件`));
        list.append(row);
      }
      list.scrollTop = top;
    }

    function buildDetail() {
      const container = $("#model-request-detail");
      container.replaceChildren();
      const heading = el("div", "model-request-detail-heading");
      const title = el("h4", "", "正在读取请求");
      const badge = el("span", "model-request-badge");
      const id = el("p", "model-request-id");
      heading.append(title, badge);
      const metrics = el("dl", "model-request-fields");
      const fields = new Map();
      const addField = (key, label, wide = false) => {
        const field = el("div", wide ? "wide" : "");
        const value = el("dd", "", "—");
        field.append(el("dt", "", label), value);
        fields.set(key, value);
        metrics.append(field);
      };
      for (const [key, label] of [["provider", "连接"], ["agent", "Agent"], ["stream", "输出方式"], ["status", "请求状态"], ["startedAt", "开始时间"], ["finishedAt", "结束时间"], ["firstChunkMs", "首个事件耗时"], ["durationMs", "总耗时"], ["idleMs", "最后接收后等待"], ["idleTimeoutMs", "读取等待上限"], ["maxOutputTokens", "最大输出 Token"], ["tokens", "Token 用量（输入 / 输出）"], ["messageCount", "输入消息 / 工具数"], ["chunks", "流式事件 / 输出大小"], ["stopReason", "停止原因"]]) addField(key, label);
      addField("endpoint", "请求地址", true);
      const errorBox = el("div", "model-request-error hidden");
      const error = el("pre", "model-request-error-text");
      errorBox.append(el("strong", "", "错误信息"), error);
      const outputLimit = el("p", "model-request-limit hidden", "已达到模型输出上限（length）。本次响应可能不完整，请检查响应内容与后续执行结果。");
      const truncated = el("p", "hint hidden", "记录较长，输入输出预览已截断；已接收的统计与请求状态保留。 ");
      const completionHint = el("p", "hint", "“传输完成”表示请求正常结束；请同时查看停止原因，length 表示已达到模型输出上限。");
      const json = {};
      for (const [key, label] of [["request", "请求 JSON"], ["response", "响应 JSON"]]) {
        const details = el("details", "model-request-json");
        const pre = el("pre", "model-request-json-pre", "暂无数据");
        pre.tabIndex = 0;
        pre.setAttribute("aria-label", `${label} 内容，可滚动阅读`);
        details.append(el("summary", "", label));
        if (key === "request") details.append(el("p", "hint model-request-json-hint", "请求为模型适配器输入快照，发送前可能经过协议转换；凭据已隐藏，超长内容会截断。"));
        details.append(pre);
        json[key] = pre;
        container.append(details);
      }
      // Append in reading order without rebuilding JSON disclosure nodes on updates.
      const disclosures = [...container.children];
      container.replaceChildren(heading, id, metrics, errorBox, outputLimit, completionHint, truncated, ...disclosures);
      detailNodes = { title, badge, id, fields, errorBox, error, outputLimit, truncated, json };
    }

    function renderDetail(record) {
      if (!detailNodes) buildDetail();
      const nodes = detailNodes;
      setText(nodes.title, record.model || "未指定模型");
      setText(nodes.id, `请求 ID：${record.id || "—"}`);
      nodes.badge.className = `model-request-badge ${knownStatus(record.status)}`;
      setText(nodes.badge, statusLabel(record.status));
      const last = timeValue(record.lastChunkAt) || timeValue(record.startedAt);
      const end = timeValue(record.finishedAt) || Date.now();
      const values = {
        provider: record.provider || "默认连接", agent: record.agent || "默认 Agent",
        stream: record.stream ? "流式输出" : "非流式输出", status: statusLabel(record.status),
        startedAt: timestamp(record.startedAt), finishedAt: record.status === "running" ? "等待完成" : timestamp(record.finishedAt),
        firstChunkMs: timeValue(record.firstChunkAt) ? milliseconds(record.firstChunkMs) : "尚未收到首个事件",
        durationMs: milliseconds(requestDuration(record)), idleMs: last ? milliseconds(Math.max(0, end - last)) : "—",
        idleTimeoutMs: milliseconds(record.idleTimeoutMs), maxOutputTokens: record.maxOutputTokens ? number(record.maxOutputTokens) : "模型默认",
        tokens: `${number(record.inputTokens)} / ${number(record.outputTokens)}`,
        messageCount: `${number(record.messageCount)} 条消息 / ${number(record.toolCount)} 个工具`,
        chunks: `${number(record.chunks)} 个事件 / ${number(record.outputBytes)} 字节`,
        stopReason: record.stopReason || (record.status === "running" ? "等待完成" : "未提供"),
        endpoint: endpointLabel(record.endpoint),
      };
      for (const [key, value] of Object.entries(values)) setText(nodes.fields.get(key), value);
      nodes.errorBox.classList.toggle("hidden", !record.error);
      nodes.outputLimit.classList.toggle("hidden", record.stopReason !== "length");
      setText(nodes.error, record.error || "");
      nodes.truncated.classList.toggle("hidden", !record.truncated);
      setText(nodes.json.request, pretty(safeJson(record.request), "未提供请求内容"));
      setText(nodes.json.response, pretty(safeJson(record.response), record.status === "running" ? "正在接收模型响应……" : "未提供响应内容"));
    }

    async function loadDetail({ quiet = false } = {}) {
      if (!selectedId || (quiet && detailInFlight)) return;
      const context = { ...getContext() };
      const token = generation;
      const sequence = ++detailRequest;
      const id = selectedId;
      detailInFlight = true;
      if (!quiet) $("#model-request-detail-message").textContent = "正在读取请求详情……";
      try {
        const record = await request(`/api/model-requests/${encodeURIComponent(id)}`);
        if (!current(context, token) || sequence !== detailRequest || id !== selectedId) return;
        if (record.id !== id) throw new Error("返回的请求记录不匹配，请刷新后重试。");
        renderDetail(record);
        $("#model-request-detail-message").textContent = records.some((item) => item.id === id) ? "" : "所选记录不在当前筛选结果中。";
      } catch (error) {
        if (current(context, token) && sequence === detailRequest && id === selectedId) {
          $("#model-request-detail-message").textContent = `请求详情读取失败：${error.message}`;
        }
      } finally {
        if (current(context, token) && sequence === detailRequest) detailInFlight = false;
      }
    }

    async function select(id) {
      if (id !== selectedId) {
        selectedId = id;
        detailNodes = null;
        detailRequest++;
        detailInFlight = false;
        $("#model-request-detail").replaceChildren(el("p", "empty-state", "正在读取所选请求……"));
      }
      renderList();
      await loadDetail();
    }

    async function load({ quiet = false } = {}) {
      const context = { ...getContext() };
      if (directory !== context.directory || epoch !== context.epoch) reset();
      if (!context.directory) {
        $("#model-requests-message").textContent = "请先选择一个创作项目。";
        return;
      }
      if (quiet && listInFlight) return;
      const token = generation;
      const sequence = ++listRequest;
      listInFlight = true;
      if (!quiet) $("#model-requests-message").textContent = "正在读取模型请求记录……";
      try {
        const params = new URLSearchParams({ limit: "100" });
        const filter = $("#model-request-status").value;
        if (filter) params.set("status", filter);
        const result = await request(`/api/model-requests?${params}`);
        if (!current(context, token) || sequence !== listRequest) return;
        if (result.directory !== context.directory) throw new Error("项目正在切换，请稍后刷新。");
        records = Array.isArray(result.requests) ? result.requests : [];
        renderList();
        $("#model-requests-message").textContent = `已更新 ${timestamp(new Date().toISOString())} · 自动刷新保留所选请求与展开内容`;
        if (!selectedId && records.length) await select(records[0].id);
        else if (selectedId) await loadDetail({ quiet: true });
      } catch (error) {
        if (current(context, token) && sequence === listRequest) $("#model-requests-message").textContent = `模型请求记录读取失败：${error.message}`;
      } finally {
        if (current(context, token) && sequence === listRequest) listInFlight = false;
      }
    }

    return { load, reset, select };
  }

  function updateStyleProgress(data) {
    if (!data.entryId) return;
    state.styleAnalysisRunning = true;
    state.styleAnalysisEntryId = data.entryId;
    if (data.entryId !== state.styleEntryId) {
      syncStyleControls();
      return;
    }
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
    syncStyleControls();
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
      if (state.view === "projects") loadWorkspaces();
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
      chapterWorkbench?.onResult(data);
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
      if (data?.entryId === state.styleAnalysisEntryId) {
        state.styleAnalysisRunning = false;
        state.styleAnalysisEntryId = "";
      }
      if (data?.entryId === state.styleEntryId) {
        state.styleBusyLocal = false;
        $("#style-progress-bar").style.width = "100%";
        $("#style-status").textContent = data?.warning ? `分析完成，但读取画像失败：${data.warning}` : "共享风格画像已生成，可在创作页面勾选引用。";
      }
      toast("共享风格库蒸馏完成");
      loadStyle({ quiet: true });
      styleReferencesPanel?.load({ quiet: true });
      return;
    }
    if (type === "style-error") {
      if (data?.entryId === state.styleAnalysisEntryId) {
        state.styleAnalysisRunning = false;
        state.styleAnalysisEntryId = "";
      }
      const message = data?.error || data?.message || "共享风格分析失败";
      if (data?.entryId === state.styleEntryId) {
        state.styleBusyLocal = false;
        $("#style-progress-wrap").classList.add("hidden");
        $("#style-status").textContent = message;
      }
      toast(message, "error");
      loadStyle({ quiet: true });
      styleReferencesPanel?.load({ quiet: true });
      return;
    }
    if (type === "style-updated") {
      loadStyle({ quiet: true });
      styleReferencesPanel?.load({ quiet: true });
    }
    if (type === "style-references-updated" && data?.directory === state.data?.directory) styleReferencesPanel?.load({ quiet: true });
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
    $("#refresh-all").addEventListener("click", () => {
      refreshAll();
      if (state.view === "activity" && state.activityTab === "requests") modelRequestsPanel?.load();
    });
    $("#create-project").addEventListener("click", openProjectDialog);
    $("#cancel-create-project").addEventListener("click", () =>
      $("#project-dialog").close(),
    );
    $("#close-create-project").addEventListener("click", () => $("#project-dialog").close());
    $("#close-settings").addEventListener("click", () => $("#settings-dialog").close());
    $("#create-project-form").addEventListener("submit", createProject);
    $("#copy-project-form").addEventListener("submit", copyProject);
    for (const id of ["close-copy-project", "cancel-copy-project"]) {
      $(`#${id}`).addEventListener("click", () => $("#copy-project-dialog").close());
    }
    $("#project-search").addEventListener("input", renderProjects);
    $("#project-tag-filter").addEventListener("change", renderProjects);
    $("#refresh-projects").addEventListener("click", loadWorkspaces);
    $("#save-project-profile").addEventListener("click", saveProfile);
    $("#project-profile-form").addEventListener("submit", (event) => {
      event.preventDefault();
      saveProfile();
    });
    $("#style-upload-form").addEventListener("submit", uploadStyleSource);
    $("#style-entry-form").addEventListener("submit", createStyleEntry);
    $("#style-entry-select").addEventListener("change", (event) => selectStyleEntry(event.target.value));
    $("#save-style-references").addEventListener("click", () => styleReferencesPanel?.save());
    $("#style-file").addEventListener("change", selectStyleFile);
    $("#refresh-style").addEventListener("click", loadStyle);
    $("#run-style-analysis").addEventListener("click", runStyleAnalysis);
    $("#reload-planning").addEventListener("click", loadPlanning);
    $("#revise-plan").addEventListener("click", revisePlan);
    $("#approve-plan").addEventListener("click", approvePlan);
    $("#repair-plan").addEventListener("click", repairPlan);
    $("#start-batch").addEventListener("click", startBatch);
    $("#chapter-search").addEventListener("input", renderChapters);
    $("#chapter-filter").addEventListener("change", () => {
      state.chapterFilter = $("#chapter-filter").value;
      renderChapters();
    });
    $("#reader-font-size").addEventListener("change", () => {
      const size = Number($("#reader-font-size").value);
      if ([16, 18, 20, 22].includes(size)) {
        document.documentElement.style.setProperty("--reader-font-size", `${size}px`);
        try { localStorage.setItem("ainovel:reader-font-size", String(size)); } catch (_) {}
      }
    });
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
    for (const tab of ["events", "requests"]) {
      $(`#activity-${tab}-tab`).addEventListener("click", () => setActivityTab(tab));
      $(`#activity-${tab}-tab`).addEventListener("keydown", (event) => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        setActivityTab(event.key === "Home" ? "events" : event.key === "End" ? "requests" : tab === "events" ? "requests" : "events", true);
      });
    }
    $("#refresh-model-requests").addEventListener("click", () => modelRequestsPanel?.load());
    $("#model-request-status").addEventListener("change", () => modelRequestsPanel?.load());
    $("#start-project").addEventListener("click", startProject);
    $("#send-cocreate").addEventListener("click", sendCoCreateReply);
    $("#confirm-cocreate").addEventListener("click", confirmCoCreate);
    $("#reset-cocreate").addEventListener("click", resetCoCreate);
    $("#cocreate-reply").addEventListener("keydown", (event) => {
      if (event.key === "Enter" && !event.isComposing && event.keyCode !== 229 && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        sendCoCreateReply();
      }
    });
    $("#cocreate-draft").addEventListener("input", () => {
      state.cocreateDraftDirty = true;
      syncDisabledControls();
    });
    $("#prompt").addEventListener("keydown", (event) => {
      if (event.key === "Enter" && !event.isComposing && event.keyCode !== 229 && !event.shiftKey && !$("#start-project").disabled) {
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
    for (const input of INPUT_IDS.map((id) => document.getElementById(id))) {
      input?.addEventListener("input", () => saveInputs());
    }
    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape") return;
      if ($("#project-dialog").open) $("#project-dialog").close();
      if ($("#copy-project-dialog").open) $("#copy-project-dialog").close();
      if ($("#settings-dialog").open) $("#settings-dialog").close();
    });
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden) {
        refreshState({ quiet: true });
        if (state.view === "activity" && state.activityTab === "requests") modelRequestsPanel?.load({ quiet: true });
      }
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
    mediaPanel = window.NovelMedia.create({
      api, el, button, getContext: () => ({ directory: state.data?.directory || "", chapter: Number(state.selectedChapter || 0), epoch: state.epoch }),
      getChapters: workbenchChapters, setView, toast,
      download: async (url, filename) => {
        const response = await fetch(url);
        if (!response.ok) { const error = await response.json().catch(() => ({})); throw new Error(error.error || "素材包导出失败"); }
        const result = await window.ainovelDesktop.saveExport({ format: "zip", filename, data: new Uint8Array(await response.arrayBuffer()) });
        if (!result?.saved) throw new Error("已取消保存章节素材包。");
      },
    });
    mediaPanel.bindEvents();
    chapterWorkbench = window.NovelChapterWorkbench.create({
      api, el, button,
      getContext: () => ({ directory: state.data?.directory || "", chapter: Number(state.selectedChapter || 0), epoch: state.epoch, approved: Boolean(state.data?.workflow?.approved), fingerprint: state.data?.workflow?.fingerprint || "", busy: Boolean(state.data?.busy || state.data?.stopping || state.data?.snapshot?.isRunning) }),
      getChapters: workbenchChapters, canRewrite: chapterCanRewrite, isBusy, setBusy,
      refresh: refreshAll, repaint: renderChapters, syncRevisions: () => runAction({ action: "sync" }), toast,
    });
    chapterWorkbench.bindEvents();
    styleReferencesPanel = createStyleReferencesPanel({
      getContext: () => ({ directory: state.data?.directory || "", epoch: state.epoch }),
      request: api,
    });
    modelRequestsPanel = createModelRequestsPanel({
      getContext: () => ({ directory: state.data?.directory || "", epoch: state.epoch }),
      request: (path) => api(path, { normalizeResponse: false }),
    });
    modelPanel = window.NovelModels.create({ state, $, $$, api, option, setBusy, refreshAll, syncDisabledControls, toast });
    modelPanel.bindEvents();
    jobsPanel = window.NovelJobs.create({ api, el, button,
      getProjects: () => state.workspaces, getConfig: () => state.config, toast, refresh: refreshAll });
    versionPanel = window.NovelVersions.create({
      api, el, button, getDirectory: () => state.data?.directory || "",
      isBusy, setBusy, refresh: refreshAll, toast,
    });
    bindEvents();
    try {
      const size = Number(localStorage.getItem("ainovel:reader-font-size"));
      if ([16, 18, 20, 22].includes(size)) {
        $("#reader-font-size").value = String(size);
        document.documentElement.style.setProperty("--reader-font-size", `${size}px`);
      }
    } catch (_) {}
    await refreshAll({ quiet: true });
    await jobsPanel.load({ quiet: true });
    const initialView = location.hash.replace(/^#/, "");
    setView(VIEW_META[initialView] ? initialView : "overview", false);
    connectEvents();
    clearInterval(state.refreshTimer);
    state.refreshTimer = setInterval(() => {
      if (!document.hidden) {
        refreshState({ quiet: true });
        if (state.view === "projects") loadWorkspaces();
        if (state.view === "jobs") jobsPanel?.load({ quiet: true });
        if (state.view === "style") loadStyle({ quiet: true });
        if (["overview", "chapters"].includes(state.view)) styleReferencesPanel?.load({ quiet: true });
        if (state.view === "activity" && state.activityTab === "requests") modelRequestsPanel?.load({ quiet: true });
      }
    }, 3500);
  }

  init();
})();
