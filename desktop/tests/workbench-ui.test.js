"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div") {
    this.tag = tag;
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.style = {};
    this.attributes = {};
    this.listeners = new Map();
    this.className = "";
    this.value = "";
    this.textContent = "";
    this.open = false;
    this.classList = {
      add: (...names) => { this.className = [...new Set([...this.className.split(" "), ...names])].join(" "); },
      remove: (...names) => { this.className = this.className.split(" ").filter((name) => !names.includes(name)).join(" "); },
      toggle: (name, force) => { if (force) this.classList.add(name); else this.classList.remove(name); },
    };
  }
  append(...nodes) { this.children.push(...nodes); }
  get lastChild() { return this.children.at(-1); }
  replaceChildren(...nodes) { this.children = nodes; }
  setAttribute(name, value) { this.attributes[name] = value; }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  focus() {}
  querySelector() { return new Element(); }
  querySelectorAll(selector) {
    const match = (node) => selector.split(/,\s*/).some((part) => {
      if (part.startsWith(".")) return node.className.split(" ").includes(part.slice(1).replace("[open]", "")) && (!part.endsWith("[open]") || node.open);
      const data = part.match(/^\[data-([a-z-]+)\]$/);
      if (data) return Object.hasOwn(node.dataset, data[1].replace(/-([a-z])/g, (_all, letter) => letter.toUpperCase()));
      return node.tag === part;
    });
    return this.children.flatMap((child) => [...(match(child) ? [child] : []), ...child.querySelectorAll(selector)]);
  }
}

function harness(chapters = []) {
  const nodes = new Map();
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  const requests = [];
  const scrolls = [];
  const scrollRoot = new Element("html");
  let response = async (url) => ({ number: Number(url.match(/chapters\/(\d+)/)?.[1]), content: "正文内容" });
  const browser = {
    window: { scrollTo: (position) => scrolls.push({ ...position }), addEventListener() {} }, URLSearchParams, console,
    document: { querySelector: $, querySelectorAll: (selector) => [...nodes.values()].flatMap((node) => node.querySelectorAll(selector)), createElement: (tag) => new Element(tag), getElementById: (id) => $("#" + id), scrollingElement: scrollRoot, addEventListener() {} },
    location: { hash: "" }, history: { replaceState() {} },
    setTimeout: () => 1, clearTimeout() {},
    fetch: async (url, options) => {
      requests.push({ url, options });
      const data = await response(url, options);
      return { ok: true, headers: { get: () => "application/json" }, json: async () => data };
    },
  };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/readable-data.js"), "utf8"), browser);
  const source = fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/app.js"), "utf8");
  vm.runInNewContext(source.replace("  init();", "  window.test = { state, renderChapters, renderPlanning, renderAudit, renderPlanOutline, chapterEntry, readChapter, syncDisabledControls, hasExportableChapters, sendChapterFeedback, runAction, renderCoCreate, renderProjects, projectCard, switchProject, selectReaderProject, renderStyle, applyState, refreshState, refreshAll, bindEvents, loadCoCreate, download, setView };"), browser);
  const api = browser.window.test;
  api.state.data = { directory: "/books/test", ready: true, chapters, workflow: { stage: "ready", approved: true, canRevise: true } };
  const text = (node) => [node.textContent, ...node.children.map(text)].join(" ");
  return { $, api, requests, scrolls, scrollRoot, respond: (fn) => { response = fn; }, text };
}

const planned = (number, volume = 1) => ({ number, title: `计划章${number}`, source: "planned", status: "planned", has_content: false,
  volume_id: volume, volume_title: `卷名${volume}`, arc_title: "故事弧", core_event: "主角发现线索", hook: "门后是谁", scenes: ["抵达书店", "发现旧信"] });

test("an empty workspace routes project-only views to project center while reading and global tools remain available", async () => {
  const h = harness();
  h.respond(async (url) => url === "/api/workspaces" ? [] : {});
  const projectViews = ["overview", "versions", "profile", "planning", "chapters", "illustrations", "audit", "activity"];
  const globalViews = ["projects", "reader", "style", "jobs"];
  const navs = [...projectViews, ...globalViews].map((view) => { const nav = new Element("button"); nav.className = "nav-item"; nav.dataset.view = view; return nav; });
  h.$("#test-nav").append(...navs);
  h.api.applyState({ directory: "", project: null, hasProject: false, chapters: [], workflow: { stage: "empty" } });
  assert.equal(h.api.state.view, "projects");
  assert.equal(h.$("#current-project-name").textContent, "尚未选择项目");
  assert.match(h.$("#current-project-path").textContent, /创建项目或导入/);
  assert.equal(h.$("#resume-run").disabled, true);
  assert.equal(h.$("#create-project").disabled, false);
  for (const nav of navs) assert.equal(nav.disabled, projectViews.includes(nav.dataset.view), nav.dataset.view);
  for (const view of projectViews) {
    h.api.setView(view);
    assert.equal(h.api.state.view, "projects", view);
    assert.equal(h.$("#view-" + view).inert, true, view);
  }
  h.api.setView("reader"); assert.equal(h.api.state.view, "reader");
  await h.api.runAction({ action: "resume" });
  assert.equal(h.requests.some(({ url }) => url === "/api/action" || url.startsWith("/api/planning") || url.startsWith("/api/audit")), false);
  h.api.renderProjects();
  assert.equal(h.$("#project-empty").className.includes("hidden"), false);
  assert.equal(h.$("#project-catalog").className.includes("hidden"), true);
});

test("removing the selected last project clears chapter and co-creation state and a later selection restores controls", async () => {
  const h = harness([planned(1)]);
  h.respond(async (url) => url === "/api/workspaces" ? [] : {});
  Object.assign(h.api.state, { view: "chapters", selectedChapter: 1, reading: { content: "旧作品正文" }, cocreate: { transcript: ["旧对话"] }, cocreatePreview: "旧输出", events: ["旧事件"], stream: "旧内容" });
  h.api.applyState({ directory: "", project: null, chapters: [], workflow: { stage: "empty" } });
  assert.equal(h.api.state.view, "projects"); assert.equal(h.api.state.selectedChapter, 0);
  assert.equal(h.api.state.reading, null); assert.equal(h.api.state.cocreate, null);
  assert.equal(h.api.state.cocreatePreview, ""); assert.equal(h.api.state.stream, ""); assert.equal(h.api.state.events.length, 0);
  h.api.applyState({ directory: "/books/new", ready: true, chapters: [], workflow: { stage: "new" } });
  h.api.setView("overview"); assert.equal(h.api.state.view, "overview");
  assert.equal(h.$("#view-overview").inert, false); assert.equal(h.$("#resume-run").disabled, false);
});

test("confirmed unwritten chapters appear immediately under their volume, with their plan and no body request", () => {
  const h = harness([planned(1), planned(2), planned(3, 2)]);
  h.api.renderChapters();
  assert.equal(h.$("#chapter-count").textContent, "2 卷 · 3 章");
  assert.equal(h.$("#chapter-list").children.length, 2);
  assert.equal(h.$("#chapter-list").children[0].children.length, 3);
  assert.equal(h.$("#reader-title").textContent, "计划章1");
  assert.match(h.text(h.$("#reader-content")), /主角发现线索.*门后是谁.*抵达书店/);
  assert.equal(h.requests.length, 0);
  assert.equal(h.$("#send-chapter-feedback").disabled, true);
  assert.equal(h.api.hasExportableChapters("/books/test"), false);
});

test("search and status filters retain volume groups and selected chapter", async () => {
  const h = harness([planned(1), { ...planned(2), source: "final", status: "completed", has_content: true, words: 560 }, planned(3, 2)]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "planned";
  h.api.renderChapters();
  h.$("#chapter-search").value = "卷名2";
  h.api.renderChapters();
  assert.equal(h.$("#chapter-list").children.length, 1);
  assert.equal(h.$("#chapter-list").children[0].dataset.volume, "2");
  assert.equal(h.api.state.selectedChapter, 1);
  h.$("#chapter-search").value = "";
  h.api.state.chapterFilter = "written";
  h.api.renderChapters();
  assert.equal(h.$("#chapter-list").children[0].children.length, 2);
  assert.match(h.$("#chapter-progress").textContent, /已完成 1 \/ 3/);
  assert.equal(h.api.hasExportableChapters("/books/test"), true);
});

test("completed chapter feedback is enabled outside review only with approved planning", () => {
  const h = harness([{ ...planned(1), source: "final", status: "completed", has_content: true }]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "final";
  h.api.syncDisabledControls();
  assert.equal(h.$("#send-chapter-feedback").disabled, false);
  assert.equal(h.$("#revise-plan").disabled, false);
  h.api.state.data.workflow.approved = false;
  h.api.syncDisabledControls();
  assert.equal(h.$("#send-chapter-feedback").disabled, true);
  assert.equal(h.$("#start-batch").disabled, true);
});

test("a late body response cannot replace a newly selected planned chapter", async () => {
  const h = harness([{ ...planned(1), source: "final", status: "completed", has_content: true }, planned(2)]);
  let finish;
  h.respond(() => new Promise((resolve) => { finish = resolve; }));
  const pending = h.api.readChapter(1, "final");
  await h.api.readChapter(2, "planned");
  finish({ number: 1, content: "旧章正文" });
  await pending;
  assert.equal(h.api.state.selectedChapter, 2);
  assert.equal(h.$("#reader-title").textContent, "计划章2");
  assert.equal(h.api.state.loading.chapter, false);
});

test("chapter selection resets only the manuscript while directory, commands and workspace keep their position", async () => {
  const h = harness([planned(1), planned(2), { ...planned(3), source: "final", status: "completed", has_content: true }]);
  h.api.renderChapters();
  const chapterButton = (number) => h.$("#chapter-list").children[0].children[number];
  const scrollDown = () => {
    h.$("#chapter-reader-scroll").scrollTop = 180;
    h.$("#reader-content").scrollTop = 220;
    h.$("#chapter-list").scrollTop = 250;
    h.$("#chapter-actions").scrollTop = 300;
    h.$("#chapter-workspace").scrollTop = 350;
    h.scrollRoot.scrollTop = 500;
  };
  const otherPositionsAreRetained = () => {
    assert.equal(h.$("#chapter-list").scrollTop, 250);
    assert.equal(h.$("#chapter-actions").scrollTop, 300);
    assert.equal(h.$("#chapter-workspace").scrollTop, 350);
    assert.equal(h.scrollRoot.scrollTop, 500);
    assert.equal(h.scrolls.length, 0);
  };
  scrollDown();
  await chapterButton(2).listeners.get("click")();
  assert.equal(h.$("#reader-title").textContent, "计划章2");
  assert.equal(h.$("#chapter-reader-scroll").scrollTop, 0);
  assert.equal(h.$("#reader-content").scrollTop, 0);
  otherPositionsAreRetained();
  scrollDown();
  h.api.renderChapters();
  await chapterButton(2).listeners.get("click")();
  assert.equal(h.$("#chapter-reader-scroll").scrollTop, 180, "Selecting the same chapter must retain reading position");
  otherPositionsAreRetained();
  await chapterButton(3).listeners.get("click")();
  assert.equal(h.$("#reader-title").textContent, "计划章3");
  assert.equal(h.$("#chapter-reader-scroll").scrollTop, 0);
  otherPositionsAreRetained();
  scrollDown();
  await h.api.readChapter(3, "final");
  h.api.renderChapters();
  assert.equal(h.$("#chapter-reader-scroll").scrollTop, 180, "Background refresh must retain reading position");
  otherPositionsAreRetained();
});

test("other project background activity does not disable current planning and writing controls", () => {
  const h = harness([{ ...planned(1), source: "final", status: "completed", has_content: true }]);
  h.api.state.data.backgroundBusy = true;
  h.api.state.data.workflow.canApprove = true;
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "final";
  h.api.syncDisabledControls();
  for (const id of ["#start-batch", "#revise-plan", "#resume-run", "#send-chapter-feedback", "#runtime-model"]) assert.equal(h.$(id).disabled, false, id);
  h.api.state.data.workflow.approved = false;
  h.api.syncDisabledControls();
  assert.equal(h.$("#approve-plan").disabled, false);
  h.api.state.data.snapshot = { isRunning: true };
  h.api.syncDisabledControls();
  assert.equal(h.$("#approve-plan").disabled, true);
  assert.equal(h.$("#create-project").disabled, false);
});

test("style analysis and source editing stay available during writing and lock only for style operations", () => {
  const h = harness();
  h.api.state.data.snapshot = { isRunning: true };
  h.api.state.config = { provider: "local", model: "model" };
  h.api.state.styleEntryId = "shared-entry";
  h.api.state.style = { sources: [{ name: "sample.md", sizeBytes: 500 }] };
  const upload = new Element("button");
  h.$("#style-upload-form").append(upload);
  h.api.renderStyle();
  h.api.syncDisabledControls();
  const remove = h.$("#style-source-list").children[0].children[1];
  assert.equal(h.$("#run-style-analysis").disabled, false);
  assert.equal(remove.disabled, false);
  assert.equal(upload.disabled, false);
  h.api.state.styleBusyLocal = true;
  h.api.syncDisabledControls();
  assert.equal(h.$("#run-style-analysis").disabled, true);
  assert.equal(remove.disabled, true);
  assert.equal(upload.disabled, true);
});

test("project cards permit opening retained running hosts while preventing same-project competing tasks", () => {
  const h = harness();
  h.api.state.data.snapshot = { isRunning: true };
  const running = h.api.projectCard({ path: "/books/running", title: "运行中的项目", status: "running", completed: 1 }, false);
  const idle = h.api.projectCard({ path: "/books/idle", title: "空闲项目" }, false);
  const scheduled = h.api.projectCard({ path: "/books/scheduled", title: "后台项目", backgroundBusy: true }, false);
  h.$("#active-project-list").append(running, idle, scheduled);
  h.api.syncDisabledControls();
  const controls = (card) => Object.fromEntries(card.querySelectorAll("[data-project-action]").map((node) => [node.dataset.projectAction, node]));
  assert.equal(controls(running).open.disabled, false);
  assert.equal(controls(running).archive.disabled, true);
  assert.equal(controls(running).background.disabled, true);
  assert.equal(controls(idle).open.disabled, false);
  assert.equal(controls(idle).background.disabled, false);
  assert.equal(controls(scheduled).open.disabled, true);
  h.api.state.data.busy = true;
  h.api.syncDisabledControls();
  assert.equal(controls(idle).open.disabled, true);
});

test("switching projects preserves an active host and is blocked only while a control operation is pending", async () => {
  const h = harness();
  h.api.state.data.snapshot = { isRunning: true };
  h.respond(async (url, options) => url === "/api/state"
    ? { ...h.api.state.data, directory: "/books/other", snapshot: { isRunning: false } }
    : url === "/api/workspaces" && !options.method ? [] : {});
  await h.api.switchProject({ path: "/books/other", title: "另一个项目" });
  assert.equal(h.api.state.data.directory, "/books/other");
  assert.equal(h.requests.some((request) => request.url === "/api/workspaces" && request.options.method === "POST"), true);
  h.api.state.data.busy = true;
  const before = h.requests.length;
  await h.api.switchProject({ path: "/books/third", title: "第三个项目" });
  assert.equal(h.requests.length, before);
});

test("planning review displays snake_case volume, event and scene fields", () => {
  const h = harness();
  h.api.renderPlanOutline([planned(1), planned(2, 2)].map(h.api.chapterEntry));
  assert.equal(h.$("#plan-outline").children.length, 2);
  const first = h.$("#plan-outline").children[0];
  assert.match(h.text(first.children[0]), /第 1 卷 · 卷名1/);
  assert.match(h.text(first.children[1]), /主角发现线索.*章节悬念：门后是谁.*抵达书店/);
});

test("planning review displays every layered chapter across volumes and arcs", () => {
  const h = harness();
  h.api.state.planning = {
    outline: [planned(99)],
    layered: [
      { index: 1, title: "启程", arcs: [
        { index: 1, title: "发现线索", chapters: [planned(10)] },
        { index: 2, title: "追查真相", chapters: [planned(20)] },
      ] },
      { index: 2, title: "归途", final: true, arcs: [
        { index: 1, title: "揭开谜底", chapters: [planned(30)] },
      ] },
    ],
  };
  h.api.renderPlanning();
  assert.equal(h.$("#outline-count").textContent, "2 卷 · 3 弧 · 3 章");
  assert.equal(h.$("#plan-outline").children.length, 2);
  const chapters = h.$("#plan-outline").querySelectorAll(".outline-detail");
  assert.deepEqual(chapters.map((chapter) => chapter.children[0].textContent), [
    "第 1 章 · 计划章10", "第 2 章 · 计划章20", "第 3 章 · 计划章30",
  ]);
  assert.match(h.text(chapters[0]), /主角发现线索.*章节悬念：门后是谁.*抵达书店/);
  assert.match(h.text(h.$("#plan-outline").children[1]), /归途.*收官卷.*揭开谜底/);
  assert.doesNotMatch(h.text(h.$("#plan-outline")), /计划章99|历史骨架待补齐/);
});

test("planning review keeps historical arc estimates visible without inventing chapters", () => {
  const h = harness();
  h.api.state.planning = {
    layered: [{ index: 1, title: "历史规划", arcs: [
      { index: 1, title: "待展开故事弧", goal: "找到失踪的人", estimated_chapters: 8 },
    ] }],
  };
  h.api.renderPlanning();
  assert.equal(h.$("#outline-count").textContent, "1 卷 · 1 弧 · 0 章 · 1 弧待补齐");
  assert.equal(h.$("#plan-outline").querySelectorAll(".outline-detail").length, 0);
  assert.match(h.text(h.$("#plan-outline")), /待展开故事弧.*预估 8 章.*历史骨架待补齐.*找到失踪的人/);
});

test("planning renders readable rules, characters and foundation issues while preserving approval and edit state", () => {
  const h = harness();
  h.api.state.planning = {
    workflow: { approved: false, canApprove: true, fingerprint: "current-plan", auditState: "ready" },
    premise: "## 故事内核\n少年决定守护故乡。",
    foundationAudit: { ready: true, summary: "设定一致", issues: [] },
    characters: [{ name: "林舟", tier: "core", description: "少年守护者" }],
    worldRules: [{ category: "magic", rule: "能力需付出代价", boundary: "不能无限复活" }],
  };
  h.api.state.data.workflow = h.api.state.planning.workflow;
  const before = JSON.stringify(h.api.state.planning);
  h.$("#planning-feedback").value = "保留我的未提交修改意见";
  h.api.renderPlanning();
  assert.match(h.text(h.$("#plan-foundation-audit")), /就绪状态.*已就绪.*设定一致/);
  assert.match(h.text(h.$("#plan-characters")), /林舟.*核心人物.*少年守护者/);
  assert.match(h.text(h.$("#plan-world-rules")), /类别.*魔法与力量.*规则.*能力需付出代价.*不可违反的边界.*不能无限复活/);
  assert.equal(h.$("#plan-premise").querySelectorAll("h5").length, 1);
  for (const id of ["#plan-foundation-audit", "#plan-world-rules", "#plan-characters", "#plan-layered"]) assert.equal(h.$(id).querySelectorAll("pre").length, 0);
  assert.equal(h.$("#approve-plan").disabled, false);
  assert.equal(h.$("#plan-fingerprint").textContent, "current-plan");
  assert.equal(h.$("#planning-feedback").value, "保留我的未提交修改意见");
  assert.equal(JSON.stringify(h.api.state.planning), before);
});

test("audit summaries, verdicts and fact projections remain readable without replacing chapter selection", () => {
  const h = harness();
  h.api.state.audit = {
    chapter: 3, progress: { phase: "writing" },
    review: { verdict: "polish", summary: "补充人物反应", issues: [{ severity: "warning", description: "情绪过渡略急", suggestion: "增加一处停顿" }] },
    worldRules: [{ category: "society", rule: "誓言不可轻许" }],
    chapterRecord: { chapter: 3, origin: "user", facts: { summary: "人工修订后的事实" } },
    decisions: [{ kind: "intervention", reason: "作者选择保留结尾" }],
  };
  h.$("#audit-chapter").value = "3";
  const before = JSON.stringify(h.api.state.audit);
  h.api.renderAudit();
  assert.equal(h.$("#audit-ready-state").textContent, "创作中");
  assert.equal(h.$("#audit-review-verdict").textContent, "建议打磨");
  assert.match(h.text(h.$("#audit-review")), /建议打磨.*补充人物反应.*提醒.*情绪过渡略急.*增加一处停顿/);
  assert.match(h.text(h.$("#audit-facts")), /世界规则.*社会.*誓言不可轻许/);
  assert.match(h.text(h.$("#audit-record")), /人工修订.*人工修订后的事实/);
  assert.match(h.text(h.$("#audit-decisions")), /用户干预.*作者选择保留结尾/);
  for (const id of ["#audit-review", "#audit-facts", "#audit-record", "#audit-decisions"]) assert.equal(h.$(id).querySelectorAll("pre").length, 0);
  assert.equal(h.$("#audit-chapter").value, "3");
  assert.equal(JSON.stringify(h.api.state.audit), before);
});


test("an unfinished draft is readable but cannot receive completed-chapter rewrite instructions", async () => {
  const h = harness([{ ...planned(1), source: "draft", status: "draft", has_content: true }]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "draft";
  h.api.syncDisabledControls();
  assert.equal(h.$("#send-chapter-feedback").disabled, true);
  h.$("#chapter-feedback").value = "重写开头";
  await h.api.sendChapterFeedback();
  assert.equal(h.requests.length, 0);
  assert.match(h.$("#toast").textContent, /请选择已完成/);
});


test("approval refreshes state and opens the chapter workbench with the confirmed plan", async () => {
  const h = harness();
  h.api.state.data.workflow.approved = false;
  h.respond(async (url) => url === "/api/state"
    ? { ...h.api.state.data, chapters: [planned(1)], workflow: { approved: true, stage: "ready" } }
    : {});
  await h.api.runAction({ action: "approve-plan", fingerprint: "reviewed-version" });
  assert.equal(h.api.state.view, "chapters");
  assert.equal(h.$("#reader-title").textContent, "计划章1");
  assert.equal(h.$("#workbench-plan-state").textContent, "当前规划已确认");
  assert.deepEqual(JSON.parse(h.requests[0].options.body), { action: "approve-plan", fingerprint: "reviewed-version", directory: "/books/test" });
});

test("completed chapter rewrite sends only the selected chapter and explicit user instructions", async () => {
  const h = harness([{ ...planned(1), source: "final", status: "completed", has_content: true }, { ...planned(2), source: "final", status: "completed", has_content: true }]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "final";
  h.$("#chapter-feedback").value = "只改第一章结尾的对话";
  h.respond(async (url) => url === "/api/state" ? h.api.state.data : {});
  await h.api.sendChapterFeedback();
  assert.deepEqual(JSON.parse(h.requests[0].options.body), {
    action: "chapter-feedback", chapter: 1, text: "只改第一章结尾的对话", directory: "/books/test",
  });
});

test("the original cocreation record remains accessible after formal planning starts", () => {
  const h = harness();
  h.api.state.cocreate = { started: true, confirmed: true, draftPrompt: "保留温暖结局和第三人称原则", transcript: [{ role: "user", content: "第三人称，温暖结局" }] };
  h.api.renderCoCreate();
  assert.equal(h.$("#cocreate-workspace").className.includes("hidden"), false);
  assert.match(h.$("#cocreate-history-title").textContent, /第一阶段共创记录.*已保留/);
  assert.equal(h.$("#cocreate-draft").value, "保留温暖结局和第三人称原则");
  assert.equal(h.$("#cocreate-draft").disabled, true);
  assert.equal(h.$("#cocreate-history").open, false);
});


test("style analysis can start with saved models after the creation Host was paused", () => {
  const h = harness();
  h.api.state.data.ready = false;
  h.api.state.config = { provider: "local", model: "model" };
  h.api.state.styleEntryId = "shared-entry";
  h.api.state.style = { busy: false, sources: [{ name: "shared.md" }] };
  h.api.renderStyle();
  assert.equal(h.$("#run-style-analysis").disabled, false);
  h.api.state.styleAnalysisRunning = true;
  h.api.state.styleAnalysisEntryId = "another-entry";
  h.api.renderStyle();
  assert.equal(h.$("#run-style-analysis").disabled, true);
  assert.match(h.$("#style-status").textContent, /其他条目/);
});

test("an older state poll cannot undo a newer refresh or expose stale workflow actions", async () => {
  const h = harness();
  const initial = { ...h.api.state.data };
  let finishOld;
  h.respond(() => new Promise((resolve) => { finishOld = resolve; }));
  const pending = h.api.refreshState();
  h.respond(async () => ({ ...initial, workflow: { stage: "planning", approved: false } }));
  await h.api.refreshState();
  finishOld({ ...initial, workflow: { stage: "ready", approved: true } });
  await pending;
  assert.equal(h.api.state.data.workflow.approved, false);
  assert.equal(h.$("#start-batch").disabled, true);
});

test("a bulk refresh finishing after a newer state poll cannot roll back current workflow", async () => {
  const h = harness();
  const initial = { ...h.api.state.data };
  let finishOld;
  h.respond((url) => url === "/api/state" ? new Promise((resolve) => { finishOld = resolve; }) : Promise.resolve(url === "/api/config" ? {} : []));
  const pending = h.api.refreshAll();
  h.respond(async () => ({ ...initial, workflow: { stage: "chapter-review", approved: true } }));
  await h.api.refreshState();
  finishOld({ ...initial, workflow: { stage: "ready", approved: true } });
  await pending;
  assert.equal(h.api.state.data.workflow.stage, "chapter-review");
  assert.equal(h.$("#accept-chapter").disabled, false);
});

test("bulk refresh waits for the newest in-flight state instead of exposing an unfinished refresh", async () => {
  const h = harness(), initial = { ...h.api.state.data }, reads = [];
  h.respond((url) => url === "/api/state" ? new Promise(resolve => reads.push(resolve)) : Promise.resolve(url === "/api/config" ? {} : []));
  let completed = false;
  const bulk = h.api.refreshAll().then(() => { completed = true; });
  const poll = h.api.refreshState();
  reads[0]({ ...initial, workflow: { stage: "ready", approved: true } });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(completed, false, "Superseded bulk refresh must await the poll that owns state");
  const newest = h.api.refreshState();
  reads[1]({ ...initial, workflow: { stage: "planning", approved: false } });
  await poll;
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(completed, false, "A second superseding poll must also finish before dependent actions proceed");
  reads[2]({ ...initial, workflow: { stage: "chapter-review", approved: true } });
  await Promise.all([bulk, newest]);
  assert.equal(completed, true);
  assert.equal(h.api.state.data.workflow.stage, "chapter-review");
});

test("reader project selection waits for a superseding poll and preserves exact Windows directory identity", async () => {
  const h = harness(), directory = "D:\\a\\小说\\web-books\\reader", reads = [];
  h.respond((url) => url === "/api/state" ? new Promise(resolve => reads.push(resolve)) : Promise.resolve(url === "/api/workspaces" ? [] : {}));
  let completed = false;
  const selection = h.api.selectReaderProject(directory).then(() => { completed = true; });
  await new Promise(resolve => setImmediate(resolve));
  const poll = h.api.refreshState();
  reads[0]({ directory, ready: true, chapters: [], workflow: {} });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(completed, false);
  assert.equal(h.api.state.loading.switch, true);
  reads[1]({ directory, ready: true, chapters: [], workflow: {}, project: { mode: "manual", name: "阅读器TXT验收" } });
  await Promise.all([selection, poll]);
  assert.equal(h.api.state.data.directory, directory);
  assert.equal(h.api.state.loading.switch, false);
  assert.deepEqual(JSON.parse(h.requests.find(request => request.url === "/api/workspaces" && request.options?.method === "POST").options.body), { path: directory });
});

test("reader selection still rejects a different project and a failed superseding state read settles", async () => {
  const h = harness(), reads = [];
  h.respond((url) => url === "/api/state" ? new Promise((resolve, reject) => reads.push({ resolve, reject })) : Promise.resolve(url === "/api/workspaces" ? [] : {}));
  const selection = h.api.selectReaderProject("/books/imported");
  const rejected = assert.rejects(selection, /项目切换未完成/);
  await new Promise(resolve => setImmediate(resolve));
  const poll = h.api.refreshState();
  reads[0].resolve({ directory: "/books/imported", ready: true, chapters: [], workflow: {} });
  reads[1].reject(new Error("state network failed"));
  await Promise.all([rejected, poll]);
  assert.equal(h.api.state.data.directory, "/books/test");
  assert.equal(h.api.state.loading.switch, false);
});

test("reader selection cannot report success after a newer navigation epoch", async () => {
  const h = harness(); let finish;
  h.respond((url) => url === "/api/state" ? new Promise(resolve => { finish = resolve; }) : Promise.resolve(url === "/api/workspaces" ? [] : {}));
  const selection = h.api.selectReaderProject("/books/imported");
  const rejected = assert.rejects(selection, /项目切换未完成/);
  await new Promise(resolve => setImmediate(resolve));
  h.api.state.epoch++;
  h.api.state.data = { directory: "/books/imported", chapters: [], workflow: {} };
  finish({ directory: "/books/imported", chapters: [], workflow: {} });
  await rejected;
  assert.equal(h.api.state.loading.switch, false);
});

test("Chinese IME confirmation and a disabled start action never submit the story prompt", () => {
  const h = harness();
  h.api.bindEvents();
  h.$("#prompt").value = "尚在输入中的故事设定";
  const submit = h.$("#prompt").listeners.get("keydown");
  let prevented = 0;
  const event = { key: "Enter", preventDefault: () => prevented++ };
  submit({ ...event, isComposing: true });
  submit({ ...event, keyCode: 229 });
  h.$("#start-project").disabled = true;
  submit(event);
  assert.equal(prevented, 0);
  assert.equal(h.requests.length, 0);
});

test("a forced cocreation refresh preserves the user's unsaved creative instruction", async () => {
  const h = harness();
  h.api.state.cocreateDraftDirty = true;
  h.$("#cocreate-draft").value = "我手动修改后尚未确认的创作原则";
  h.respond(async () => ({ draftPrompt: "服务器中较早的指令", ready: true, canConfirm: true, transcript: [] }));
  await h.api.loadCoCreate(true);
  assert.equal(h.api.state.cocreateDraftDirty, true);
  assert.equal(h.$("#cocreate-draft").value, "我手动修改后尚未确认的创作原则");
  assert.equal(h.api.state.cocreate.draftPrompt, "服务器中较早的指令");
});


test("project card exports require an idle target while other projects may keep writing", () => {
  const h = harness();
  h.api.state.data.snapshot = { isRunning: true };
  const projects = [
    { path: "/books/running", status: "running", completed: 1 },
    { path: "/books/queued", backgroundBusy: true, completed: 1 },
    { path: "/books/background", status: "background", completed: 1 },
    { path: "/books/idle", status: "paused", completed: 1 },
    { path: "/books/archived", status: "paused", completed: 1, archived: true },
  ];
  for (const project of projects) {
    const card = h.api.projectCard(project, Boolean(project.archived));
    h.$("#active-project-list").append(card);
    h.api.syncDisabledControls();
    const controls = card.querySelectorAll("[data-project-action]").filter((node) => node.dataset.projectAction.startsWith("export-"));
    assert.equal(controls.length, 2);
    const blocked = ["running", "background"].includes(project.status) || Boolean(project.backgroundBusy);
    for (const control of controls) {
      assert.equal(control.disabled, blocked, project.path);
      assert.equal(control.dataset.persistentDisabled, String(blocked), project.path);
      assert.match(control.title, blocked ? /暂停.*操作结束.*导出/ : /导出已完成章节/);
    }
  }
});

test("current project exports and direct download calls respect generation, stopping, and background work", async () => {
  const chapter = { ...planned(1), source: "final", status: "completed", has_content: true };
  for (const condition of ["running", "stopping", "busy", "background"]) {
    const h = harness([chapter]);
    if (condition === "running") h.api.state.data.snapshot = { isRunning: true };
    else if (condition === "background") h.api.state.workspaces = [{ path: "/books/test", completed: 1, backgroundBusy: true }];
    else h.api.state.data[condition] = true;
    h.api.syncDisabledControls();
    for (const format of ["txt", "epub"]) {
      assert.equal(h.$(`#export-${format}`).disabled, true, condition);
      assert.match(h.$(`#export-${format}`).title, /暂停.*操作结束/);
      await h.api.download(format);
    }
    assert.equal(h.requests.length, 0, condition);
    assert.match(h.$("#toast").textContent, /暂停.*操作结束/);
  }
  const idle = harness([chapter]);
  idle.api.state.data.backgroundBusy = true; // Another project's queue is independent.
  idle.api.syncDisabledControls();
  assert.equal(idle.$("#export-txt").disabled, false);
  assert.equal(idle.$("#export-epub").disabled, false);
});


test("navigation resets page scroll only when switching views and preserves entered drafts", () => {
  const h = harness();
  h.api.state.view = "chapters";
  h.$("#prompt").value = "保留创作草稿";
  h.api.setView("illustrations");
  assert.equal(h.scrolls.length, 1);
  assert.equal(h.scrolls[0].top, 0);
  h.api.setView("illustrations");
  assert.equal(h.scrolls.length, 1);
  h.api.setView("versions");
  assert.equal(h.scrolls.length, 2);
  assert.equal(h.$("#prompt").value, "保留创作草稿");
});

test("chapter workbench starts with compact expandable style references while overview keeps its full controls", () => {
  const h = harness();
  h.api.setView("chapters");
  assert.equal(h.$("#style-reference-panel").open, false);
  assert.equal(h.$("#style-reference-summary").className.includes("hidden"), false);
  h.$("#style-reference-panel").open = true;
  h.api.setView("chapters");
  assert.equal(h.$("#style-reference-panel").open, true, "Refreshing the same page must retain manual expansion");
  h.api.setView("overview");
  assert.equal(h.$("#style-reference-panel").open, true);
  assert.equal(h.$("#style-reference-summary").className.includes("hidden"), true);
  h.api.setView("chapters");
  assert.equal(h.$("#style-reference-panel").open, false);
});
