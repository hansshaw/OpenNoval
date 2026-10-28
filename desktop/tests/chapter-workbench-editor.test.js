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
  focus(options) { this.focusOptions = options; this.focused = true; }
  scrollIntoView(options) { this.scrollOptions = options; }
}

const completed = (number) => ({ number, source: "final", status: "completed", hasContent: true });
const edit = (chapter = 1, extra = {}) => ({ directory: "/books/one", chapter, revision: `revision-${chapter}`, title: `章节${chapter}`, volumeTitle: "第一卷", coreEvent: "原始事件", hook: "原始悬念", scenes: ["场景一"], content: `正文${chapter}`, hasPlan: true, hasContent: true, ...extra });
const versions = (chapter = 1, extra = {}) => ({ directory: "/books/one", chapter, currentContentHash: `hash-${chapter}`, syncRequired: false, versions: [
  { id: "original", number: 1, title: "原稿", content: "原稿正文", active: true },
  { id: "candidate", number: 2, title: "新稿", content: "新稿正文", active: false, instruction: "改写结尾" },
], ...extra });

function harness({ androidStorage = null } = {}) {
  const nodes = new Map(), calls = [], notices = [];
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  const lifecycle = new Map();
  const browser = { window: {}, document: { getElementById: $, addEventListener: (name, callback) => lifecycle.set(name, callback) }, URLSearchParams, Date, structuredClone, setTimeout, clearTimeout };
  if (androidStorage) Object.assign(browser.window, {
    ainovelAndroid: {}, confirm: () => true, addEventListener: (name, callback) => lifecycle.set(name, callback),
    localStorage: { getItem: (key) => androidStorage.get(key) ?? null, setItem: (key, value) => androidStorage.set(key, value), removeItem: (key) => androidStorage.delete(key) },
  });
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
    flushBackups: () => lifecycle.get("pagehide")?.(),
    respond: (callback) => { respond = callback; },
    context: (value) => { context = { ...context, ...value }; },
    chapters: (value) => { chapters = value; },
    busy: (value) => { busy = value; },
    input: async (id, value) => { $(id).value = value; await $(id).listeners.get("input")(); },
    choose: async (chapter, checked) => { const row = panel.chapterRow(chapter, new Element("button")); const checkbox = row.children[0].children[0]; checkbox.checked = checked; await checkbox.listeners.get("change")(); return checkbox; },
    click: async (id) => $(id).listeners.get("click")(),
  };
}

test("Android restores unsaved chapter text and plan after process restart without saving official content", async () => {
  const androidStorage = new Map();
  const first = harness({ androidStorage }); first.panel.sync(); await first.flush();
  await first.input("edit-chapter-content", "手机退出前尚未保存的正文");
  await first.input("edit-core-event", "手机草稿中的核心事件");
  first.flushBackups();
  assert.equal(androidStorage.size, 1);
  const reopened = harness({ androidStorage }); reopened.panel.sync(); await reopened.flush();
  assert.equal(reopened.$("edit-chapter-content").value, "手机退出前尚未保存的正文");
  assert.equal(reopened.$("edit-core-event").value, "手机草稿中的核心事件");
  assert.match(reopened.$("chapter-editor-status").textContent, /已恢复.*手机草稿/);
  assert.equal(reopened.calls.some((call) => call.options?.method), false);
  assert.equal(reopened.$("save-chapter-content").disabled, false);
  assert.equal(reopened.$("save-chapter-plan").disabled, false);
});

test("changed backend revision keeps Android recovery explicit and survives editing the newer manuscript", async () => {
  const androidStorage = new Map();
  const first = harness({ androidStorage }); first.panel.sync(); await first.flush();
  await first.input("edit-chapter-content", "旧版本基础上的手机草稿"); first.flushBackups();
  const changed = harness({ androidStorage });
  changed.respond(async (url) => url.includes("/edit") ? edit(1, { revision: "server-updated", content: "已更新的正式版" }) : versions());
  changed.panel.sync(); await changed.flush();
  assert.equal(changed.$("edit-chapter-content").value, "已更新的正式版");
  assert.equal(changed.$("chapter-draft-recovery").className.includes("hidden"), false);
  assert.equal(changed.$("save-chapter-content").disabled, true);
  await changed.input("edit-chapter-content", "基于新正式版的修改"); changed.flushBackups();
  const reopened = harness({ androidStorage });
  reopened.respond(async (url) => url.includes("/edit") ? edit(1, { revision: "server-updated", content: "已更新的正式版" }) : versions());
  reopened.panel.sync(); await reopened.flush();
  assert.equal(reopened.$("edit-chapter-content").value, "基于新正式版的修改");
  assert.equal(reopened.$("chapter-draft-recovery").className.includes("hidden"), false);
  await reopened.click("restore-local-chapter-draft");
  assert.equal(reopened.$("edit-chapter-content").value, "旧版本基础上的手机草稿");
  assert.equal(reopened.$("chapter-draft-recovery").className.includes("hidden"), true);
  assert.equal(reopened.calls.some((call) => call.options?.method), false);
  assert.match(reopened.$("chapter-editor-status").textContent, /正式内容尚未改变/);
});

test("Android partial saves retain remaining draft fields with the new revision and clear fully saved backups", async () => {
  const androidStorage = new Map();
  const h = harness({ androidStorage }); h.panel.sync(); await h.flush();
  await h.input("edit-core-event", "更新规划");
  await h.input("edit-chapter-content", "未保存正文"); h.flushBackups();
  h.respond(async (url, options) => options?.method ? edit(1, { revision: "after-plan", coreEvent: "更新规划" }) : versions());
  await h.panel.saveEdits(true);
  const saved = JSON.parse([...androidStorage.values()][0]);
  assert.equal(saved.revision, "after-plan");
  assert.equal(saved.value.content, "未保存正文");
  h.respond(async (url, options) => options?.method ? edit(1, { revision: "after-body", coreEvent: "更新规划", content: "未保存正文" }) : versions());
  await h.panel.saveEdits(false);
  assert.equal(androidStorage.size, 0);
});

test("one Android backup failure remains visible when another chapter succeeds and can be retried", async () => {
  const androidStorage = new Map();
  const h = harness({ androidStorage }); h.panel.sync(); await h.flush();
  await h.input("edit-chapter-content", "第一章未保存内容");
  h.context({ chapter: 2 });
  h.respond(async (url) => url.includes("/edit") ? edit(2) : versions(2));
  h.panel.sync(); await h.flush();
  await h.input("edit-chapter-content", "第二章未保存内容");
  androidStorage.set = function (key, value) { if (key.endsWith("%0A1")) throw new Error("storage full"); return Map.prototype.set.call(this, key, value); };
  h.flushBackups();
  assert.equal(androidStorage.size, 1);
  assert.match(h.$("chapter-editor-status").textContent, /备份未成功/);
  androidStorage.set = Map.prototype.set;
  h.flushBackups();
  assert.equal(androidStorage.size, 2);
});

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

test("read and edit modes preserve drafts, and command shortcuts do not scroll the manuscript", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  await h.click("chapter-edit-tab");
  assert.equal(h.$("chapter-editor").className.includes("hidden"), false);
  assert.equal(h.$("chapter-reader").className.includes("hidden"), true);
  assert.equal(h.$("chapter-edit-tab").attributes["aria-pressed"], "true");
  await h.input("edit-chapter-content", "这段修改尚未保存");
  h.$("edit-chapter-content").scrollTop = 123;
  await h.click("chapter-read-tab");
  assert.equal(h.$("chapter-reader").className.includes("hidden"), false);
  h.$("chapter-reader-scroll").scrollTop = 456;
  await h.click("chapter-jump-feedback");
  assert.equal(h.$("chapter-feedback").focused, true);
  assert.equal(h.$("chapter-actions").scrollTop, 0);
  assert.equal(h.$("chapter-actions").scrollOptions.block, "start");
  assert.equal(h.$("chapter-reader-scroll").scrollTop, 456);
  await h.click("chapter-edit-tab");
  assert.equal(h.$("edit-chapter-content").value, "这段修改尚未保存");
  assert.equal(h.$("edit-chapter-content").scrollTop, 123);
  assert.equal(h.calls.some(call => call.options?.method), false);
  h.panel.selectVersion("candidate");
  assert.equal(h.$("chapter-reader-scroll").scrollTop, 0);
  assert.equal(h.$("chapter-reader").className.includes("hidden"), false);
  assert.equal(h.$("edit-chapter-content").value, "这段修改尚未保存");
  h.$("chapter-reader-scroll").scrollTop = 234;
  h.panel.selectVersion("candidate");
  assert.equal(h.$("chapter-reader-scroll").scrollTop, 234);
});

test("switching chapter resets only reading and editor scroll while preserving independent control regions", async () => {
  const h = harness(); h.panel.sync(); await h.flush();
  for (const id of ["chapter-reader-scroll", "chapter-editor-fields", "edit-chapter-content", "chapter-workspace", "chapter-list", "chapter-actions"]) h.$(id).scrollTop = 432;
  h.context({ chapter: 2 });
  h.respond(async url => url.includes("/edit") ? edit(2) : versions(2));
  h.panel.sync(); await h.flush();
  for (const id of ["chapter-reader-scroll", "chapter-editor-fields", "edit-chapter-content"]) assert.equal(h.$(id).scrollTop, 0, id);
  for (const id of ["chapter-workspace", "chapter-list", "chapter-actions"]) assert.equal(h.$(id).scrollTop, 432, id);
});

test("a manual empty chapter is writable without planning approval and saving does not request AI synchronization", async () => {
  const h = harness();
  h.context({ manual: true, approved: false, chapter: 3 });
  h.chapters([{ number: 3, source: "planned", hasContent: false, title: "手写新章" }]);
  let body = "";
  h.respond(async (url, options) => {
    if (options?.method) {
      body = options.body.content;
      h.chapters([{ ...completed(3), title: "手写新章" }]);
    }
    return url.includes("/versions")
      ? versions(3, { currentContentHash: body ? "manual-first" : "manual-empty", versions: body ? [{ id: "manual-first", number: 1, content: body, active: true }] : [] })
      : edit(3, { manual: true, title: "手写新章", content: body, hasContent: Boolean(body), revision: body ? "saved-first" : "empty-first", syncRequired: false });
  });
  h.panel.sync(); await h.flush();
  assert.equal(h.$("edit-chapter-title").value, "手写新章");
  assert.equal(h.$("edit-chapter-content").disabled, false);
  assert.equal(h.$("save-chapter-content").disabled, true);
  assert.equal(h.$("send-chapter-feedback").disabled, true);
  assert.equal(h.$("chapter-sync-required").className.includes("hidden"), true);
  await h.input("edit-chapter-content", "无需模型即可写下第一段正文。");
  assert.equal(h.$("save-chapter-content").disabled, false);
  await h.panel.saveEdits(false);
  const saved = h.calls.find(call => call.options?.method === "POST");
  assert.equal(saved.url, "/api/chapters/3/edit");
  assert.equal(saved.options.body.expectedRevision, "empty-first");
  assert.equal(saved.options.body.saveContent, true);
  assert.equal(body, "无需模型即可写下第一段正文。");
  assert.equal(h.$("chapter-sync-required").className.includes("hidden"), true);
  assert.equal(h.$("sync-chapter-revisions").disabled, true);
  assert.doesNotMatch(h.$("chapter-editor-status").textContent, /同步修订|重新审查/);
  assert.equal(h.$("save-chapter-content").disabled, true);
});

test("manual chapters retain restorable versions after clearing and completion events bypass version throttling", async () => {
  const h = harness();
  h.context({ manual: true, approved: false });
  let body = "需要恢复的手写原文", revision = "manual-original";
  const versionView = () => versions(1, { currentContentHash: body ? "original-hash" : "empty-hash", versions: [
    { id: "original", number: 1, title: "手写章", content: "需要恢复的手写原文", active: Boolean(body) },
  ] });
  h.respond(async (url, options) => {
    if (url.endsWith("/original/apply")) return { ok: true };
    if (options?.method) {
      body = options.body.content;
      revision = "manual-empty";
      h.chapters([{ number: 1, source: "planned", hasContent: false, title: "手写章" }]);
    }
    return url.includes("/versions") ? versionView() : edit(1, { manual: true, title: "手写章", content: body, revision, hasContent: Boolean(body), syncRequired: false });
  });
  h.panel.sync(); await h.flush();
  await h.input("edit-chapter-content", "");
  await h.panel.saveEdits(false);
  assert.equal(h.$("edit-chapter-content").value, "");
  assert.equal(h.$("edit-chapter-content").disabled, false);
  assert.equal(h.$("chapter-version-list").children.some(node => /版本 1/.test(node.textContent)), true, "clearing must retain history in the version picker");
  h.panel.selectVersion("original");
  assert.equal(h.$("apply-chapter-version").disabled, false);
  await h.panel.applyVersion();
  const apply = h.calls.find(call => call.url.endsWith("/original/apply"));
  assert.equal(apply.options.body.expectedContentHash, "empty-hash");
  assert.doesNotMatch(h.$("chapter-version-status").textContent, /需同步修订/);
  const versionReads = h.calls.filter(call => call.url.includes("/versions?")).length;
  body = "需要恢复的手写原文";
  revision = "manual-restored";
  h.chapters([{ ...completed(1), title: "手写章" }]);
  h.panel.onResult({ action: "apply-chapter-version", directory: "/books/one", ok: true, syncRequired: false });
  await h.flush();
  assert.equal(h.calls.filter(call => call.url.includes("/versions?")).length > versionReads, true, "a completion event must immediately read the new active version");
  assert.equal(h.$("edit-chapter-content").value, body);
  assert.equal(h.$("reader-content").textContent, body);
  assert.equal(h.$("apply-chapter-version").disabled, true);
  assert.equal(h.$("chapter-sync-required").className.includes("hidden"), true);
  assert.equal(h.$("sync-chapter-revisions").disabled, true);
});
