"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div") {
    this.tag = tag;
    this.children = [];
    this.dataset = {};
    this.style = {};
    this.attributes = {};
    this.listeners = new Map();
    this.className = "";
    this.textContent = "";
    this.value = "";
    this.open = false;
    this.scrollTop = 0;
    this.classList = {
      add: (...values) => { this.className = [...new Set([...this.className.split(" "), ...values])].join(" "); },
      remove: (...values) => { this.className = this.className.split(" ").filter((value) => !values.includes(value)).join(" "); },
      toggle: (value, active) => active ? this.classList.add(value) : this.classList.remove(value),
    };
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  setAttribute(key, value) { this.attributes[key] = value; }
  addEventListener(key, callback) { this.listeners.set(key, callback); }
  reset() { this.resetCount = (this.resetCount || 0) + 1; }
  focus() {}
  querySelector() { return new Element(); }
  querySelectorAll(selector) {
    const match = (node) => selector.split(/,\s*/).some((part) => {
      if (part.startsWith(".")) return node.className.split(" ").includes(part.slice(1));
      const attribute = part.match(/^\[data-([a-z-]+)\]$/);
      if (attribute) return Object.hasOwn(node.dataset, attribute[1].replace(/-([a-z])/g, (_all, letter) => letter.toUpperCase()));
      return node.tag === part;
    });
    return this.children.flatMap((node) => [...(match(node) ? [node] : []), ...node.querySelectorAll(selector)]);
  }
}

const entry = (id, ready = true) => ({ id, name: `风格-${id}`, ready, sourceCount: 1 });
const library = (id = "warm", extra = {}) => ({
  libraryRoot: "/shared/style-library", entries: [entry("warm"), entry("cold")], entryId: id, entry: entry(id),
  sources: [{ name: `${id}.md`, sizeBytes: 123 }], profile: { voice: `${id}的共享风格` },
  busy: false, analysisRunning: false, analysisEntryId: "", ...extra,
});

function harness() {
  const nodes = new Map();
  const $ = (selector) => {
    if (!nodes.has(selector)) nodes.set(selector, new Element());
    return nodes.get(selector);
  };
  const calls = [];
  let respond = (route) => route.startsWith("/api/style?") ? library(new URL(route, "http://local").searchParams.get("entryId")) : route === "/api/style" ? library() : {};
  const browser = {
    window: {}, URLSearchParams, Blob, console,
    document: { querySelector: $, querySelectorAll: (selector) => [...nodes.values()].flatMap((node) => node.querySelectorAll(selector)), createElement: (tag) => new Element(tag), getElementById: (id) => $("#" + id) },
    location: { hash: "" }, history: { replaceState() {} },
    confirm: () => true, setTimeout: () => 1, clearTimeout() {},
    fetch: async (route, options) => {
      calls.push({ route, options, body: options?.body ? JSON.parse(options.body) : undefined });
      const result = await respond(route, options);
      return { ok: true, headers: { get: () => "application/json" }, json: async () => result };
    },
  };
  const source = fs.readFileSync(path.resolve(__dirname, "../../internal/entry/web/static/app.js"), "utf8");
  vm.runInNewContext(source.replace("  init();", "  window.test = { state, api, applyState, loadStyle, selectStyleEntry, createStyleEntry, uploadStyleSource, deleteStyleSource, runStyleAnalysis, renderStyle, syncStyleControls, handleStreamMessage, createStyleReferencesPanel };"), browser);
  const api = browser.window.test;
  api.state.data = { directory: "/books/one", ready: true, snapshot: {}, workflow: {} };
  api.state.config = { provider: "global-provider", model: "global-model" };
  const panel = api.createStyleReferencesPanel({
    getContext: () => ({ directory: api.state.data?.directory || "", epoch: api.state.epoch }), request: api.api,
  });
  const text = (node) => [node.textContent, ...node.children.map(text)].join(" ");
  const inputs = () => $("#style-reference-list").children.filter((node) => node.tag === "label").map((node) => node.children[0]);
  return {
    $, api, calls, panel, text, inputs,
    respond: (callback) => { respond = callback; },
    flush: () => new Promise((resolve) => setImmediate(resolve)),
    choose: async (id, checked) => {
      const input = inputs().find((node) => node.dataset.styleEntry === id);
      assert.ok(input, `missing checkbox ${id}`);
      input.checked = checked;
      await input.listeners.get("change")();
    },
  };
}

test("shared library selection and profile survive project switching and work without a ready Host", async () => {
  const h = harness();
  h.api.state.data = { ready: false, directory: "", workflow: {} };
  await h.api.loadStyle();
  assert.equal(h.calls[0].route, "/api/style");
  assert.equal(h.api.state.styleEntryId, "warm");
  assert.equal(h.$("#run-style-analysis").disabled, false);
  const profile = h.api.state.style;
  h.api.applyState({ directory: "/books/two", ready: false, workflow: {} });
  assert.equal(h.api.state.styleEntryId, "warm");
  assert.equal(h.api.state.style, profile);
  assert.match(h.text(h.$("#style-profile")), /warm的共享风格/);
  assert.match(h.$("#style-library-root").textContent, /\/shared\/style-library/);
});

test("an empty shared library requires a named entry before source editing and supports creation without a project", async () => {
  const h = harness();
  h.api.state.data = { ready: false, directory: "", workflow: {} };
  h.api.state.config = null;
  const upload = new Element("button");
  h.$("#style-upload-form").append(upload);
  h.respond((route) => route === "/api/style" ? library("", { entries: [], entry: null, sources: [], profile: null }) : route === "/api/style/entries" ? entry("new") : library("new"));
  await h.api.loadStyle();
  assert.equal(upload.disabled, true);
  assert.equal(h.$("#run-style-analysis").disabled, true);
  h.$("#style-entry-name").value = "温暖日常";
  await h.api.createStyleEntry({ preventDefault() {} });
  const creation = h.calls.find((call) => call.route === "/api/style/entries");
  assert.deepEqual(creation.body, { name: "温暖日常" });
  assert.equal(h.api.state.styleEntryId, "new");
  assert.equal(upload.disabled, false);
  assert.equal(h.$("#run-style-analysis").disabled, true);
});

test("source upload, deletion and distillation send a shared entry identity without a project directory", async () => {
  const h = harness();
  h.api.state.data = { directory: "", ready: false, workflow: {} };
  await h.api.loadStyle();
  h.$("#style-source-name").value = "sample.md";
  h.$("#style-source-content").value = "授权样本内容";
  await h.api.uploadStyleSource({ preventDefault() {} });
  await h.api.deleteStyleSource("sample.md");
  await h.api.runStyleAnalysis();
  assert.deepEqual(h.calls.find((call) => call.route === "/api/style/upload").body, { entryId: "warm", name: "sample.md", content: "授权样本内容" });
  assert.deepEqual(h.calls.find((call) => call.route === "/api/style/delete").body, { entryId: "warm", name: "sample.md" });
  assert.deepEqual(h.calls.find((call) => call.route === "/api/style/run").body, { entryId: "warm" });
});

test("late entry reads cannot replace the library item selected more recently", async () => {
  const h = harness();
  let finish;
  h.respond((route) => route.includes("entryId=warm") ? new Promise((resolve) => { finish = resolve; }) : library("cold"));
  const old = h.api.loadStyle({ entryId: "warm" });
  await h.api.selectStyleEntry("cold");
  finish(library("warm"));
  await old;
  assert.equal(h.api.state.styleEntryId, "cold");
  assert.match(h.text(h.$("#style-profile")), /cold的共享风格/);
  assert.doesNotMatch(h.text(h.$("#style-profile")), /warm的共享风格/);
});

test("progress and completion for another shared entry cannot overwrite the displayed profile", async () => {
  const h = harness();
  await h.api.loadStyle({ entryId: "cold" });
  h.$("#style-progress-bar").style.width = "7%";
  h.api.handleStreamMessage("style-progress", { entryId: "warm", current: 5, total: 10, message: "warm正在蒸馏" });
  assert.equal(h.api.state.styleBusyLocal, false);
  assert.equal(h.$("#style-progress-bar").style.width, "7%");
  assert.doesNotMatch(h.$("#style-status").textContent, /warm正在蒸馏/);
  h.api.handleStreamMessage("style-done", { entryId: "warm", profile: { voice: "不应覆盖当前画像" } });
  await h.flush();
  assert.equal(h.api.state.styleEntryId, "cold");
  assert.match(h.text(h.$("#style-profile")), /cold的共享风格/);
  assert.doesNotMatch(h.text(h.$("#style-profile")), /不应覆盖当前画像/);
  assert.equal(h.calls.at(-1).route, "/api/style?entryId=cold");
});

test("library warnings and corrupt profiles are visible and unavailable references cannot be newly checked", async () => {
  const h = harness();
  const broken = { ...entry("broken", false), profileError: "画像JSON损坏" };
  h.respond((route) => route.includes("references")
    ? { directory: "/books/one", selectedIds: [], entries: [entry("warm"), broken] }
    : library("warm", { entries: [entry("warm"), broken], warnings: ["历史语料迁移暂未完成"] }));
  await h.api.loadStyle();
  await h.panel.load();
  assert.match(h.$("#style-library-warnings").textContent, /迁移暂未完成/);
  assert.equal(h.$("#style-library-warnings").className.includes("hidden"), false);
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "broken").disabled, true);
  assert.match(h.text(h.$("#style-reference-list")), /画像JSON损坏/);
});

test("reference polling preserves dirty checked entries and saving is allowed while creation runs", async () => {
  const h = harness();
  const entries = [entry("warm"), entry("cold"), entry("pending", false)];
  h.api.state.data.snapshot = { isRunning: true };
  h.respond((_route, options) => ({ directory: "/books/one", selectedIds: options.method === "POST" ? JSON.parse(options.body).selectedIds : ["warm"], entries }));
  await h.panel.load();
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "pending").disabled, true);
  await h.choose("cold", true);
  await h.panel.load({ quiet: true });
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "cold").checked, true);
  assert.match(h.$("#style-reference-status").textContent, /未保存/);
  assert.equal(h.$("#save-style-references").disabled, false);
  await h.panel.save();
  assert.deepEqual(h.calls.find((call) => call.options.method === "POST").body, { directory: "/books/one", selectedIds: ["warm", "cold"] });
  assert.match(h.$("#style-reference-status").textContent, /下次模型请求生效/);
  assert.equal(h.$("#save-style-references").disabled, true);
});

test("damaged legacy references show a warning and can be explicitly cleared without any available entries", async () => {
  const h = harness();
  let saved = false;
  h.respond((_route, options) => {
    if (options.method === "POST") saved = true;
    return { directory: "/books/one", selectedIds: [], entries: [],
      selectionNeedsConfirmation: !saved, warnings: ["原项目风格引用暂不可用，已保留原文件"] };
  });
  await h.panel.load();
  assert.match(h.$("#style-reference-status").textContent, /已保留原文件.*保存空引用/);
  assert.equal(h.$("#save-style-references").disabled, false);
  await h.panel.load({ quiet: true });
  assert.equal(h.$("#save-style-references").disabled, false);
  await h.panel.save();
  assert.deepEqual(h.calls.find((call) => call.options.method === "POST").body.selectedIds, []);
  assert.equal(h.$("#save-style-references").disabled, true);
  await h.panel.load({ quiet: true });
  assert.equal(h.$("#save-style-references").disabled, true);
  assert.match(h.$("#style-reference-status").textContent, /已保留原文件/);
});

test("explicitly clearing migrated references saves an empty array and never restores an older read", async () => {
  const h = harness();
  const entries = [entry("warm")];
  h.respond(() => ({ directory: "/books/one", selectedIds: ["warm"], entries }));
  await h.panel.load();
  let finish;
  h.respond((_route, options) => options.method === "POST"
    ? { directory: "/books/one", selectedIds: [], entries }
    : new Promise((resolve) => { finish = resolve; }));
  const oldRead = h.panel.load({ quiet: true });
  await h.choose("warm", false);
  await h.panel.save();
  finish({ directory: "/books/one", selectedIds: ["warm"], entries });
  await oldRead;
  assert.deepEqual(h.calls.find((call) => call.options.method === "POST").body.selectedIds, []);
  assert.equal(h.inputs()[0].checked, false);
  assert.equal(h.$("#save-style-references").disabled, true);
});

test("a project change clears old references and ignores late reads from the previous project", async () => {
  const h = harness();
  let finish;
  h.respond(() => new Promise((resolve) => { finish = resolve; }));
  const oldRead = h.panel.load();
  h.api.state.epoch++;
  h.api.state.data.directory = "/books/two";
  h.panel.reset();
  assert.equal(h.inputs().length, 0);
  h.respond(() => ({ directory: "/books/two", selectedIds: ["cold"], entries: [entry("warm"), entry("cold")] }));
  await h.panel.load();
  finish({ directory: "/books/one", selectedIds: ["warm"], entries: [entry("warm"), entry("cold")] });
  await oldRead;
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "cold").checked, true);
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "warm").checked, false);
});

test("late saves and failed saves cannot overwrite another project's references or discard dirty choices", async () => {
  const h = harness();
  const entries = [entry("warm"), entry("cold")];
  h.respond(() => ({ directory: "/books/one", selectedIds: [], entries }));
  await h.panel.load();
  await h.choose("warm", true);
  h.respond(() => { throw new Error("disk full"); });
  await h.panel.save();
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "warm").checked, true);
  assert.match(h.$("#style-reference-status").textContent, /保存失败.*disk full/);
  let finish;
  h.respond(() => new Promise((resolve) => { finish = resolve; }));
  const oldSave = h.panel.save();
  h.api.state.epoch++;
  h.api.state.data.directory = "/books/two";
  h.panel.reset();
  h.respond(() => ({ directory: "/books/two", selectedIds: ["cold"], entries }));
  await h.panel.load();
  finish({ directory: "/books/one", selectedIds: ["warm"], entries });
  await oldSave;
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "cold").checked, true);
  assert.equal(h.inputs().find((node) => node.dataset.styleEntry === "warm").checked, false);
  assert.doesNotMatch(h.$("#style-reference-status").textContent, /保存失败/);
});
