"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div", className = "", textContent = "") {
    Object.assign(this, { tag, className, textContent, value: "", children: [], listeners: new Map(), attributes: {}, disabled: false, checked: false, files: [] });
    this.classList = { toggle: (name, value) => { this.attributes[name] = value; } };
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  setAttribute(name, value) { this.attributes[name] = value; }
  removeAttribute(name) { delete this.attributes[name]; }
  querySelectorAll() { return this.children.flatMap((node) => [node, ...node.querySelectorAll()]); }
}
const picture = (chapter = 1, caption = "雨中的旧城") => ({ id: "image-one", chapter, caption, name: "scene.png", placement: "chapter-start", order: 0, width: 80, height: 60, bytes: 100, url: `/api/chapters/${chapter}/media/image-one/content?directory=book` });
const config = () => ({ connections: [{ id: "image-connection", name: "图像服务", kind: "image", protocol: "openai-compatible", baseURL: "https://provider.example/v1", model: "picture-model", hasAPIKey: true, apiKeyHint: "••••" }], selected: { image: "image-connection", video: "", audio: "" }, canvasURL: "http://localhost:3000/canvas", generationAvailable: false });
function harness() {
  const nodes = new Map(), calls = [], notices = [];
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  let context = { directory: "/books/one", chapter: 1, epoch: 0 };
  let chapters = [1, 2].map((number) => ({ number, title: `章节${number}`, volumeTitle: "第一卷", coreEvent: `事件${number}`, hasContent: true, source: "final" }));
  let respond = async (url) => url === "/api/media-models" ? config() : url.includes("/media") ? { directory: context.directory, chapter: Number(url.match(/chapters\/(\d+)/)[1]), items: [] } : { content: `正文${context.chapter}` };
  const browser = { window: {}, document: { getElementById: $ }, URL, URLSearchParams, FormData, Date, navigator: { clipboard: { writeText: async () => {} } } };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/entry/web/static/media.js"), "utf8"), browser);
  const el = (...args) => new Element(...args);
  const button = (label, name, callback) => { const node = el("button", name, label); node.addEventListener("click", callback); return node; };
  const panel = browser.window.NovelMedia.create({ api: async (url, options) => { calls.push({ url, options }); return respond(url, options); }, el, button, getContext: () => context, getChapters: () => chapters, setView: () => {}, toast: (value) => notices.push(value), download: async () => {} });
  panel.bindEvents();
  for (const id of ["media-model-name", "media-model-key", "media-model-kind", "media-model-voice", "media-model-save"]) $("media-model-form").append($(id));
  return { $, panel, calls, notices, module: browser.window.NovelMedia,
    context: (values) => { context = { ...context, ...values }; },
    respond: (fn) => { respond = fn; },
    chapters: (values) => { chapters = values; },
    fire: async (id, name) => $(id).listeners.get(name)({ preventDefault() {} }),
  };
}
const flush = () => new Promise((resolve) => setImmediate(resolve));

test("canvas links allow HTTP(S) only and never transport query credentials", () => {
  const h = harness();
  for (const value of ["javascript:alert(1)", "https://example.com/?apiKey=secret", "http://user:pass@localhost:3000", "//example.com", "http://localhost/#key"]) assert.equal(h.module.canvasURL(value), "");
  assert.equal(h.module.canvasURL("http://localhost:3000/canvas"), "http://localhost:3000/canvas");
});

test("a late chapter read cannot replace another project's gallery or text", async () => {
  const h = harness(); let oldRead;
  h.respond(async (url) => url.includes("/media?") ? new Promise((resolve) => { oldRead = resolve; }) : { content: "旧项目正文" });
  const pending = h.panel.load(); await flush();
  h.context({ directory: "/books/two", epoch: 1 });
  h.respond(async (url) => url === "/api/media-models" ? config() : url.includes("/media?") ? { directory: "/books/two", chapter: 1, items: [] } : { content: "新项目正文" });
  await h.panel.load(); oldRead({ directory: "/books/one", chapter: 1, items: [picture()] }); await pending;
  assert.equal(h.$("media-source-text").textContent, "新项目正文");
  assert.equal(h.$("media-count").textContent, "0 张配图");
});

test("upload sends multipart bytes and explicit chapter location without changing prose", async () => {
  const h = harness(); await h.panel.load();
  h.$("media-file").files = [new File([new Uint8Array([137, 80, 78, 71])], "scene.png", { type: "image/png" })];
  h.$("media-caption").value = "雨夜"; h.$("media-placement").value = "chapter-end";
  h.respond(async (url, options) => ({ directory: "/books/one", chapter: 1, items: options?.method ? [picture(1, "雨夜")] : [] }));
  await h.panel.upload({ preventDefault() {} });
  const call = h.calls.find((row) => row.options?.method === "POST");
  assert.equal(call.url, "/api/chapters/1/media");
  assert.ok(call.options.body instanceof FormData);
  assert.equal(call.options.body.get("directory"), "/books/one");
  assert.equal(call.options.body.get("placement"), "chapter-end");
  assert.equal(call.options.body.get("caption"), "雨夜");
  assert.equal(call.options.body.get("file").size, 4);
  assert.equal(h.calls.some((row) => row.url.includes("/edit")), false);
  assert.match(h.$("media-status").textContent, /已导入/);
});

test("reader rejects late media from the previous chapter", async () => {
  const h = harness(); let first;
  h.respond(async () => new Promise((resolve) => { first = resolve; }));
  const old = h.panel.syncReader(); await flush();
  h.context({ chapter: 2 });
  h.respond(async () => ({ directory: "/books/one", chapter: 2, items: [picture(2, "第二章配图")] }));
  await h.panel.syncReader(); first({ directory: "/books/one", chapter: 1, items: [picture()] }); await old;
  assert.equal(h.$("reader-images-start").children[0].children[1].textContent, "第二章配图");
});

test("first media config load disables drafts, keep omits a stray key, and successful save clears it", async () => {
  const h = harness(); let complete;
  h.respond(async () => new Promise((resolve) => { complete = resolve; }));
  const pending = h.panel.loadConfig();
  assert.equal(h.$("media-model-name").disabled, true);
  complete(config()); await pending;
  assert.equal(h.$("media-model-name").disabled, false);
  h.$("media-model-saved").value = "image-connection"; await h.fire("media-model-saved", "change");
  h.$("media-model-key").value = "stray-secret";
  h.$("media-model-voice").value = "old-voice";
  h.respond(async (_url, options) => { assert.equal(options.body.apiKeyAction, "keep"); assert.equal(options.body.apiKey, ""); assert.equal(options.body.voice, ""); return { ...config(), savedID: "image-connection" }; });
  await h.fire("media-model-form", "submit"); await flush();
  assert.equal(h.$("media-model-key").value, "");
  assert.equal(h.$("media-canvas-link").href, "http://localhost:3000/canvas");
});

test("failed default selection restores the saved value and keeps editing drafts", async () => {
  const h = harness(); await h.panel.loadConfig();
  h.$("media-model-name").value = "未保存草稿";
  const select = h.$("media-model-defaults").children[0].children[0];
  select.value = "";
  h.respond(async () => { throw new Error("保存失败"); });
  await select.listeners.get("change")();
  assert.equal(h.$("media-model-defaults").children[0].children[0].value, "image-connection");
  assert.equal(h.$("media-model-name").value, "未保存草稿");
  assert.equal(h.$("media-model-status").textContent, "保存失败");
});

test("unchanged media polling retains image elements instead of downloading the same image again", async () => {
  const h = harness();
  h.respond(async () => ({ directory: "/books/one", chapter: 1, items: [picture()] }));
  await h.panel.syncReader();
  const firstFigure = h.$("reader-images-start").children[0];
  await h.panel.syncReader({ force: true });
  assert.equal(h.$("reader-images-start").children[0], firstFigure);
});
