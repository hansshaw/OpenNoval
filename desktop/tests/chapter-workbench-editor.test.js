"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div", className = "", textContent = "") {
    Object.assign(this, { tag, className, textContent, children: [], value: "", checked: false, disabled: false, listeners: new Map(), attributes: {} });
    this.classList = {
      add: (name) => { this.className += ` ${name}`; },
      remove: (name) => { this.className = this.className.split(" ").filter((item) => item !== name).join(" "); },
      toggle: (name, enabled) => enabled ? this.classList.add(name) : this.classList.remove(name),
    };
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  setAttribute(name, value) { this.attributes[name] = value; }
  focus() {}
}

const completed = (number) => ({ number, source: "final", status: "completed", hasContent: true });
const edit = (chapter = 1, extra = {}) => ({ directory: "/books/one", chapter, revision: `revision-${chapter}`, title: `章节${chapter}`, volumeTitle: "第一卷", coreEvent: "原始事件", hook: "原始悬念", scenes: ["场景一"], content: `正文${chapter}`, hasPlan: true, hasContent: true, ...extra });
const versions = (chapter = 1, extra = {}) => ({ directory: "/books/one", chapter, currentContentHash: `hash-${chapter}`, syncRequired: false, versions: [
  { id: "original", number: 1, title: "原稿", content: "原稿正文", active: true },
  { id: "candidate", number: 2, title: "新稿", content: "新稿正文", active: false, instruction: "改写结尾" },
], ...extra });

function harness() {
  const nodes = new Map(), calls = [], notices = [];
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  const browser = { window: {}, document: { getElementById: $ }, URLSearchParams, Date, structuredClone };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/chapter-workbench.js"), "utf8"), browser);
  let context = { directory: "/books/one", chapter: 1, epoch: 0, approved: true };
  let chapters = [completed(1), completed(2), { number: 3, source: "planned" }];
  let busy = false;
  let respond = async (url, options) => options?.method ? {} : url.includes("/versions") ? versions() : edit();
  const el = (tag, name = "", text = "") => new Element(tag, name, text);
  const button = (text, name, callback) => { const node = el("button", name, text); node.addEventListener("click", callback); return node; };
  const panel = browser.window.NovelChapterWorkbench.create({
    api: async (url, options) => { calls.push({ url, options }); return respond(url, options); },
    el, button, getContext: () => context, getChapters: () => chapters,
    canRewrite: (chapter) => Boolean(chapter?.hasContent && chapter.source === "final"),
    isBusy: () => busy, setBusy: (_name, value) => { busy = value; },
    refresh: async () => {}, repaint: () => panel.sync(), syncRevisions: () => { notices.push("sync"); }, toast: (message) => notices.push(message),
  });
  panel.bindEvents();
  const flush = () => new Promise((resolve) => setImmediate(resolve));
  return { $, panel, calls, notices, flush,
    respond: (callback) => { respond = callback; },
    context: (value) => { context = { ...context, ...value }; },
    chapters: (value) => { chapters = value; },
    busy: (value) => { busy = value; },
    input: async (id, value) => { $(id).value = value; await $(id).listeners.get("input")(); },
    choose: async (chapter, checked) => { const row = panel.chapterRow(chapter, new Element("button")); const checkbox = row.children[0].children[0]; checkbox.checked = checked; await checkbox.listeners.get("change")(); return checkbox; },
    click: async (id) => $(id).listeners.get("click")(),
  };
}

test("batch rewrite sends explicit selection and instruction, preserving original versions by default", async () => {
  const h = harness();
  h.panel.sync(); await h.flush();
  await h.choose(completed(2), true);
  await h.choose(completed(1), true);
  await h.input("chapter-feedback", "让两章结尾衔接更自然");
  await h.panel.rewrite();
  assert.deepEqual(JSON.parse(JSON.stringify(h.calls.find((call) => call.url === "/api/chapter-rewrites").options.body)), {
    directory: "/books/one", chapters: [1, 2], instruction: "让两章结尾衔接更自然", autoApply: false,
  });
  assert.match(h.$("chapter-feedback-status").textContent, /候选版本.*原稿保持/);
  assert.match(h.$("rewrite-selected-count").textContent, /2 章/);
});

test("single chapter fallback and automatic adoption are explicit, and empty instructions never submit", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  await h.panel.rewrite();
  assert.equal(h.calls.some((call) => call.options?.method), false);
  await h.input("chapter-feedback", "收紧第一章开头");
  h.$("rewrite-auto-apply").checked = true;
  await h.panel.rewrite();
  const payload = h.calls.find((call) => call.url === "/api/chapter-rewrites").options.body;
  assert.deepEqual(Array.from(payload.chapters), [1]);
  assert.equal(payload.autoApply, true);
});

test("viewing a candidate is read-only and adoption includes the current content hash", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  assert.match(h.$("reader-content").textContent, /原稿正文/);
  h.panel.selectVersion("candidate");
  assert.match(h.$("reader-content").textContent, /新稿正文/);
  assert.match(h.$("reader-meta").textContent, /候选版，尚未覆盖/);
  assert.equal(h.calls.some((call) => call.options?.method), false);
  assert.equal(h.$("apply-chapter-version").disabled, false);
  await h.panel.applyVersion();
  const call = h.calls.find((request) => request.url.endsWith("/candidate/apply"));
  assert.equal(call.options.body.expectedContentHash, "hash-1");
  assert.equal(call.options.body.directory, "/books/one");
});

test("the current manuscript displays its saved chapter name while historical candidate titles stay intact", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  await h.input("edit-chapter-title", "尚未保存的标题");
  h.panel.renderReading();
  assert.equal(h.$("reader-title").textContent, "章节1");
  h.chapters([{ ...completed(1), title: "雨夜来信" }, completed(2)]);
  h.panel.sync(); await h.flush();
  h.panel.renderReading();
  assert.equal(h.$("reader-title").textContent, "雨夜来信");
  h.panel.selectVersion("candidate");
  assert.equal(h.$("reader-title").textContent, "新稿");
});

test("manual plan save uses the loaded revision and retains an unsaved body draft", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  await h.input("edit-core-event", "手动调整的核心事件");
  await h.input("edit-volume-title", "归途");
  await h.input("edit-chapter-content", "尚未保存的正文草稿");
  h.respond(async (url, options) => options?.method ? edit(1, { revision: "revision-new", coreEvent: "手动调整的核心事件", volumeTitle: "归途" }) : versions());
  await h.panel.saveEdits(true);
  const payload = h.calls.find((call) => call.options?.method === "POST").options.body;
  assert.equal(payload.expectedRevision, "revision-1");
  assert.equal(payload.savePlan, true); assert.equal(payload.saveContent, false);
  assert.equal(payload.coreEvent, "手动调整的核心事件");
  assert.equal(h.$("edit-chapter-content").value, "尚未保存的正文草稿");
  assert.equal(h.$("save-chapter-content").disabled, false);
  assert.match(h.$("chapter-editor-status").textContent, /重新审查并确认/);
});

test("failed manual saves retain drafts and do not show success", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  await h.input("edit-chapter-content", "手工修改正文");
  h.respond(async () => { throw new Error("章节已被其他操作修改"); });
  await h.panel.saveEdits(false);
  assert.equal(h.$("edit-chapter-content").value, "手工修改正文");
  assert.match(h.$("chapter-editor-status").textContent, /保存失败.*未保存修改已保留/);
  assert.equal(h.$("save-chapter-content").disabled, false);
  assert.equal(h.notices.length, 0);
});

test("late chapter reads cannot replace another chapter and dirty drafts survive project switches", async () => {
  const h = harness(); let resolveOld;
  h.respond(async (url) => url.includes("/edit") ? new Promise((resolve) => { resolveOld = resolve; }) : versions());
  h.panel.sync(); await h.flush();
  h.context({ chapter: 2 });
  h.respond(async (url) => url.includes("/edit") ? edit(2) : versions(2));
  h.panel.sync(); await h.flush();
  resolveOld(edit(1)); await h.flush();
  assert.equal(h.$("edit-chapter-title").value, "章节2");
  await h.input("edit-core-event", "保留第二章修改");
  h.context({ directory: "/books/two", chapter: 1, epoch: 1 });
  h.respond(async (url) => url.includes("/edit") ? edit(1, { directory: "/books/two", coreEvent: "另一个项目的规划" }) : versions(1, { directory: "/books/two" }));
  h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-core-event").value, "另一个项目的规划");
  h.context({ directory: "/books/one", chapter: 2, epoch: 2 });
  h.respond(async (url) => url.includes("/edit") ? edit(2) : versions(2));
  h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-core-event").value, "保留第二章修改");
  assert.equal(h.$("save-chapter-plan").disabled, false);
});

test("polling does not overwrite unsaved edits and a stale save cannot mutate the new project's form", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  await h.input("edit-chapter-title", "未保存标题");
  h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-chapter-title").value, "未保存标题");
  let resolveSave;
  h.respond(async (_url, options) => options?.method ? new Promise((resolve) => { resolveSave = resolve; }) : {});
  const pending = h.panel.saveEdits(true);
  h.context({ directory: "/books/two", chapter: 1, epoch: 1 });
  h.respond(async (url) => url.includes("/edit") ? edit(1, { directory: "/books/two", title: "项目二标题" }) : versions(1, { directory: "/books/two" }));
  h.panel.sync(); await h.flush();
  resolveSave(edit(1, { title: "已保存旧项目标题" })); await pending;
  assert.equal(h.$("edit-chapter-title").value, "项目二标题");
  assert.equal(h.notices.length, 0);
});

test("unwritten plans remain editable while body saves are disabled; adopted bodies expose the sync action", async () => {
  const h = harness();
  h.respond(async (url) => url.includes("/edit") ? edit(1, { hasContent: false, content: "" }) : versions(1, { versions: [], syncRequired: true }));
  h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-core-event").disabled, false);
  assert.equal(h.$("edit-chapter-content").disabled, true);
  assert.equal(h.$("save-chapter-content").disabled, true);
  assert.equal(h.$("chapter-sync-required").className.includes("hidden"), false);
  await h.click("sync-chapter-revisions");
  assert.deepEqual(h.notices, ["sync"]);
});

test("a selected planned chapter becomes body-editable as generation completes without switching chapters", async () => {
  const h = harness();
  h.chapters([{ number: 1, source: "planned", title: "未完成章" }]);
  h.respond(async (url) => url.includes("/edit") ? edit(1, { hasContent: false, content: "" }) : versions(1, { versions: [] }));
  h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-chapter-content").disabled, true);
  h.context({ busy: true }); h.busy(true); h.panel.sync(); await h.flush();
  const readsBefore = h.calls.length;
  h.chapters([{ ...completed(1), title: "已完成章", words: 500 }]);
  h.respond(async (url) => url.includes("/edit") ? edit(1, { content: "刚生成完成的正文", revision: "generated" }) : versions());
  h.context({ busy: false }); h.busy(false); h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-chapter-content").disabled, false);
  assert.equal(h.$("edit-chapter-content").value, "刚生成完成的正文");
  assert.equal(h.calls.length > readsBefore, true);
});

test("clean editor revisions refresh after metadata changes and retry a read rejected during job completion", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  let fail = true;
  h.respond(async (url) => {
    if (!url.includes("/edit")) return versions();
    if (fail) throw new Error("本项目操作尚未结束");
    return edit(1, { title: "外部保存的新标题", revision: "external-new" });
  });
  h.context({ fingerprint: "new-plan" }); h.panel.sync(); await h.flush();
  assert.match(h.$("chapter-editor-status").textContent, /读取失败/);
  fail = false; h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-chapter-title").value, "外部保存的新标题");
  await h.input("edit-core-event", "基于新规划修改");
  h.respond(async (_url, options) => options?.method ? edit(1, { title: "外部保存的新标题", revision: "saved", coreEvent: "基于新规划修改" }) : versions());
  await h.panel.saveEdits(true);
  assert.equal(h.calls.find((call) => call.options?.method === "POST").options.body.expectedRevision, "external-new");
});

test("the project sync warning survives selecting another chapter and clears after a successful sync", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  h.panel.onResult({ action: "apply-chapter-version", directory: "/books/one", syncRequired: true }); await h.flush();
  assert.equal(h.$("chapter-sync-required").className.includes("hidden"), false);
  h.context({ chapter: 2 });
  h.respond(async (url) => url.includes("/edit") ? edit(2) : versions(2));
  h.panel.sync(); await h.flush();
  assert.equal(h.$("chapter-sync-required").className.includes("hidden"), false);
  h.panel.onResult({ action: "sync", directory: "/books/one", ok: true }); await h.flush();
  assert.equal(h.$("chapter-sync-required").className.includes("hidden"), true);
});

test("a save finishing after chapter navigation advances the cached revision while preserving other draft edits", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  await h.input("edit-chapter-title", "保存后的标题");
  await h.input("edit-chapter-content", "尚未保存的正文");
  let finishSave;
  h.respond(async (_url, options) => options?.method ? new Promise((resolve) => { finishSave = resolve; }) : {});
  const pending = h.panel.saveEdits(true);
  h.context({ chapter: 2 });
  h.respond(async (url) => url.includes("/edit") ? edit(2) : versions(2));
  h.panel.sync(); await h.flush();
  finishSave(edit(1, { title: "保存后的标题", revision: "revision-saved" }));
  await pending;
  assert.equal(h.$("edit-chapter-title").value, "章节2");
  h.context({ chapter: 1 });
  h.respond(async (url) => url.includes("/edit") ? edit(1, { title: "保存后的标题", revision: "revision-saved" }) : versions(1));
  h.panel.sync(); await h.flush();
  assert.equal(h.$("save-chapter-plan").disabled, true);
  assert.equal(h.$("edit-chapter-content").value, "尚未保存的正文");
  assert.equal(h.$("save-chapter-content").disabled, false);
  h.respond(async (_url, options) => options?.method ? edit(1, { title: "保存后的标题", revision: "revision-body", content: "尚未保存的正文" }) : versions(1));
  await h.panel.saveEdits(false);
  const saves = h.calls.filter((call) => call.options?.method === "POST");
  assert.equal(saves[1].options.body.expectedRevision, "revision-saved");
});
