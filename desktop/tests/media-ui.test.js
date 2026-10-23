"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div", className = "", textContent = "") {
    Object.assign(this, { tag, className, textContent, value: "", children: [], listeners: new Map(), attributes: {}, dataset: {}, disabled: false, checked: false, files: [] });
    this.classList = { toggle: (name, value) => { this.attributes[name] = value; } };
  }
  append(...nodes) { for (const node of nodes) { node.parent = this; this.children.push(node); } }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((node) => node !== this); }
  replaceWith(node) { if (this.parent) { node.parent = this.parent; this.parent.children = this.parent.children.map((child) => child === this ? node : child); } }
  showModal() { this.open = true; }
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  setAttribute(name, value) { this.attributes[name] = value; }
  removeAttribute(name) { delete this.attributes[name]; }
  querySelectorAll() { return this.children.flatMap((node) => [node, ...node.querySelectorAll()]); }
}
const picture = (chapter = 1, caption = "雨中的旧城") => ({ id: "image-one", chapter, caption, name: "scene.png", placement: "chapter-start", order: 0, width: 80, height: 60, bytes: 100, url: `/api/chapters/${chapter}/media/image-one/content?directory=book` });
const config = () => ({ connections: [{ id: "image-connection", name: "图像服务", kind: "image", protocol: "openai-compatible", baseURL: "https://provider.example/v1", model: "picture-model", hasAPIKey: true, apiKeyHint: "••••" }], selected: { image: "image-connection", video: "", audio: "" }, generationAvailable: true });
function harness() {
  const nodes = new Map(), calls = [], notices = [], downloads = [];
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  let context = { directory: "/books/one", chapter: 1, epoch: 0 };
  let chapters = [1, 2].map((number) => ({ number, title: `章节${number}`, volumeTitle: "第一卷", coreEvent: `事件${number}`, hasContent: true, source: "final" }));
  let respond = async (url) => url === "/api/media-models" ? config() : url.includes("/media") ? { directory: context.directory, chapter: Number(url.match(/chapters\/(\d+)/)[1]), items: [], jobs: [] } : { content: `正文${context.chapter}` };
  const browser = { window: {}, document: { getElementById: $ }, URL, URLSearchParams, FormData, Date, setTimeout: () => 1, clearTimeout: () => {}, navigator: { clipboard: { writeText: async () => {} } } };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/mediaworkspace/ui/media.js"), "utf8"), browser);
  const el = (...args) => new Element(...args);
  const button = (label, name, callback) => { const node = el("button", name, label); node.addEventListener("click", callback); return node; };
  const panel = browser.window.NovelMedia.create({ api: async (url, options) => { calls.push({ url, options }); return respond(url, options); }, el, button, getContext: () => context, getChapters: () => chapters, setView: () => {}, toast: (value) => notices.push(value), download: async (url, filename) => { downloads.push({ url, filename }); } });
  panel.bindEvents();
  for (const kind of ["image", "video", "audio"]) for (const field of ["name", "key", "voice", "save", "saved", "protocol", "id", "url", "description", "activate", "key-action", "delete"]) $(`media-${kind}-form`).append($(`media-${kind}-${field}`));
  return { $, panel, calls, notices, downloads, module: browser.window.NovelMedia,
    context: (values) => { context = { ...context, ...values }; },
    respond: (fn) => { respond = fn; },
    chapters: (values) => { chapters = values; },
    fire: async (id, name) => $(id).listeners.get(name)({ preventDefault() {} }),
  };
}
const flush = () => new Promise((resolve) => setImmediate(resolve));

test("chapter materials export uses the independent media bundle endpoint", async () => {
  const h = harness(); await h.panel.load();
  await h.fire("media-export-bundle", "click");
  assert.equal(h.downloads.length, 1);
  assert.equal(h.downloads[0].url, "/api/chapters/1/media/export?directory=%2Fbooks%2Fone");
  assert.equal(h.downloads[0].filename, "chapter-1-media.zip");
  assert.match(h.$("media-status").textContent, /素材包已导出/);
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
  assert.equal(h.$("media-image-name").disabled, true);
  complete(config()); await pending;
  assert.equal(h.$("media-image-name").disabled, false);
  h.$("media-image-saved").value = "image-connection"; await h.fire("media-image-saved", "change");
  h.$("media-image-key").value = "stray-secret";
  h.respond(async (_url, options) => { assert.equal(options.body.apiKeyAction, "keep"); assert.equal(options.body.apiKey, ""); assert.equal(options.body.voice, ""); return { ...config(), savedID: "image-connection" }; });
  await h.fire("media-image-form", "submit"); await flush();
  assert.equal(h.$("media-image-key").value, "");
});

test("failed default selection restores the saved value and keeps editing drafts", async () => {
  const h = harness(); await h.panel.loadConfig();
  h.$("media-image-name").value = "未保存草稿";
  const select = h.$("media-image-default");
  select.value = "";
  h.respond(async () => { throw new Error("保存失败"); });
  await select.listeners.get("change")();
  assert.equal(h.$("media-image-default").value, "image-connection");
  assert.equal(h.$("media-image-name").value, "未保存草稿");
  assert.equal(h.$("media-image-status").textContent, "保存失败");
});

test("unchanged media polling retains image elements instead of downloading the same image again", async () => {
  const h = harness();
  h.respond(async () => ({ directory: "/books/one", chapter: 1, items: [picture()] }));
  await h.panel.syncReader();
  const firstFigure = h.$("reader-images-start").children[0];
  await h.panel.syncReader({ force: true });
  assert.equal(h.$("reader-images-start").children[0], firstFigure);
});

const allConfig = () => ({
  connections: ["image", "video", "audio"].map((kind) => ({ id: `${kind}-connection`, kind, name: `${kind} 服务`, model: `${kind}-model`, protocol: "openai-compatible", baseURL: `https://${kind}.example/v1`, voice: kind === "audio" ? "alloy" : "", hasAPIKey: true })),
  selected: { image: "image-connection", video: "video-connection", audio: "audio-connection" }, generationAvailable: true,
});
const job = (kind, status = "completed") => ({ id: `${kind}-job`, chapter: 1, kind, source: "final", status, model: `${kind}-model`, createdAt: "2026-10-23T10:00:00Z", url: status === "completed" ? `/api/chapters/1/media/generate/${kind}-job/content?directory=%2Fbooks%2Fone` : "", mime: kind === "audio" ? "audio/mpeg" : kind === "video" ? "video/mp4" : "image/png" });
function mediaResponse(url, { directory = "/books/one", chapter = 1, jobs = [], content = "正文第一段。正文第二段。", models = allConfig() } = {}) {
  if (url === "/api/media-models") return models;
  if (url.includes("/generate")) return { directory, chapter, jobs };
  if (url.includes("/media")) return { directory, chapter, items: [] };
  return { number: chapter, content };
}

test("image, video and speech have independent forms, defaults, drafts and fixed kinds", async () => {
  const h = harness(); h.respond(async (url) => mediaResponse(url)); await h.panel.loadConfig();
  for (const kind of ["image", "video", "audio"]) {
    assert.deepEqual(h.$(`media-${kind}-saved`).children.map((node) => node.value), ["", `${kind}-connection`]);
    assert.equal(h.$(`media-${kind}-default`).value, `${kind}-connection`);
    h.$(`media-${kind}-saved`).value = `${kind}-connection`; await h.fire(`media-${kind}-saved`, "change");
    h.$(`media-${kind}-name`).value = `${kind} 的未保存草稿`;
  }
  h.panel.showSettings("video"); await flush();
  assert.equal(h.$("settings-video-panel").attributes.hidden, false);
  assert.equal(h.$("settings-image-panel").attributes.hidden, true);
  assert.equal(h.$("settings-audio-panel").attributes.hidden, true);
  assert.equal(h.$("media-image-name").value, "image 的未保存草稿");
  assert.equal(h.$("media-audio-voice").value, "alloy");
  h.respond(async (_url, options) => { assert.equal(options.body.kind, "video"); assert.equal(options.body.voice, ""); assert.equal(options.body.name, "video 的未保存草稿"); return { ...allConfig(), savedID: "video-connection" }; });
  await h.fire("media-video-form", "submit");
  assert.equal(h.$("media-image-name").value, "image 的未保存草稿");
  assert.equal(h.$("media-audio-name").value, "audio 的未保存草稿");
});

test("chapter generation sends the selected chapter and saved source without substituting planning or browser text", async () => {
  const h = harness(); h.respond(async (url) => mediaResponse(url)); await h.panel.load();
  h.$("media-generation-instructions").value = "冷色电影质感";
  h.respond(async (url, options) => {
    if (options?.method === "POST") return job("image", "running");
    return mediaResponse(url, { jobs: [job("image", "running")] });
  });
  await h.panel.generate("image");
  const request = h.calls.find((call) => call.options?.method === "POST");
  assert.equal(request.url, "/api/chapters/1/media/generate");
  assert.deepEqual(JSON.parse(JSON.stringify(request.options.body)), { directory: "/books/one", kind: "image", source: "final", instructions: "冷色电影质感", placement: "chapter-end" });
  assert.equal(Object.hasOwn(request.options.body, "content"), false);
  assert.equal(h.$("media-generate-image").disabled, true, "do not submit duplicate running jobs");
  assert.equal(h.$("media-generate-video").disabled, false);
  assert.match(h.$("media-generation-status").textContent, /任务已提交/);
});

test("missing saved prose and custom protocols cannot trigger generation", async () => {
  const h = harness();
  h.respond(async (url) => { if (!url.includes("media")) throw new Error("该章节尚未生成"); return mediaResponse(url); });
  await h.panel.load();
  assert.match(h.$("media-source-text").textContent, /事件1/);
  for (const kind of ["image", "video", "audio"]) assert.equal(h.$(`media-generate-${kind}`).disabled, true);
  await h.panel.generate("image"); assert.equal(h.calls.some((call) => call.options?.method === "POST"), false);
  const models = allConfig(); models.connections[0].protocol = "custom";
  h.respond(async (url) => mediaResponse(url, { models })); await h.panel.load();
  assert.equal(h.$("media-generate-image").disabled, true);
  assert.match(h.$("media-generation-image-model").textContent, /自定义协议暂不能生成/);
  assert.equal(h.$("media-generate-audio").disabled, false);
});

test("draft speech reads and submits the draft source and never sends visual instructions", async () => {
  const h = harness(); h.respond(async (url) => mediaResponse(url)); await h.panel.load();
  h.$("media-generation-source").value = "draft"; await h.fire("media-generation-source", "change"); await flush();
  assert.ok(h.calls.some((call) => call.url === "/api/chapters/1?directory=%2Fbooks%2Fone&source=draft"));
  h.$("media-generation-instructions").value = "仅属于图片的要求";
  h.respond(async (url, options) => options?.method === "POST" ? job("audio", "running") : mediaResponse(url, { jobs: [job("audio", "running")] }));
  await h.panel.generate("audio");
  const request = h.calls.find((call) => call.options?.method === "POST");
  assert.equal(request.options.body.kind, "audio"); assert.equal(request.options.body.source, "draft"); assert.equal(request.options.body.instructions, "");
});

test("late generation responses cannot populate another project's task list", async () => {
  const h = harness(); h.respond(async (url) => mediaResponse(url)); await h.panel.load();
  let oldRead; h.respond(async () => new Promise((resolve) => { oldRead = resolve; }));
  const pending = h.panel.loadGeneration(); await flush();
  h.context({ directory: "/books/two", epoch: 1 });
  h.respond(async (url) => mediaResponse(url, { directory: "/books/two", jobs: [] })); await h.panel.load();
  oldRead(mediaResponse("/generate", { jobs: [job("audio")] })); await pending;
  assert.equal(h.$("media-generation-results").children.length, 0);
});

test("polling another task preserves the existing audio player and current playback", async () => {
  const h = harness(); let progress = 10;
  h.respond(async (url) => mediaResponse(url, { jobs: [job("audio"), { ...job("video", "running"), progress }] }));
  await h.panel.load();
  const audioCard = h.$("media-generation-results").children[0];
  const player = audioCard.children.find((node) => node.tag === "audio"); player.currentTime = 42;
  assert.equal(player.controls, true);
  progress = 60; await h.panel.loadGeneration();
  assert.equal(h.$("media-generation-results").children[0], audioCard);
  assert.equal(h.$("media-generation-results").children[0].children.find((node) => node.tag === "audio"), player);
  assert.equal(player.currentTime, 42);
});

test("completed audio and video can be downloaded and running tasks can be cancelled by chapter", async () => {
  const h = harness(); const jobs = [job("audio"), job("video", "running")];
  h.respond(async (url) => mediaResponse(url, { jobs })); await h.panel.load();
  const audioControls = h.$("media-generation-results").children[0].children.at(-1);
  await audioControls.children[0].listeners.get("click")();
  assert.equal(h.downloads[0].url, jobs[0].url); assert.match(h.downloads[0].filename, /\.mp3$/);
  const videoControls = h.$("media-generation-results").children[1].children.at(-1);
  h.respond(async (url, options) => { if (options?.method === "DELETE") { assert.equal(url, "/api/chapters/1/media/generate/video-job?directory=%2Fbooks%2Fone"); return { ok: true }; } return mediaResponse(url, { jobs: [job("audio")] }); });
  await videoControls.children[0].listeners.get("click")();
  assert.equal(h.$("media-generation-results").children.length, 1);
});
