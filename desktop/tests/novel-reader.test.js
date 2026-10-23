"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const reader = require("../../internal/entry/desktopui/static/novel-reader.js");

class Element {
  constructor(document, tag) { Object.assign(this, { document, tagName: tag, children: [], listeners: {}, attributes: {}, dataset: {}, value: "", textContent: "", files: [], hidden: false, scrollTop: 0, scrollHeight: 1000, clientHeight: 400, clientWidth: 600 }); this.classList = { add() {} }; this.style = { setProperty() {} }; }
  set id(id) { this._id = id; this.document.ids.set(id, this); } get id() { return this._id; }
  append(...nodes) { nodes.forEach(node => { node.parent = this; this.children.push(node); }); }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(node => node !== this); }
  setAttribute(name, value) { this.attributes[name] = value; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  fire(type) { return Promise.all((this.listeners[type] || []).map(fn => fn({ preventDefault() {} }))); }
  showModal() { this.open = true; } focus() {}
}
function harness({ loadPDF, storage } = {}) {
  const document = { ids: new Map(), listeners: new Map(), addEventListener(type, fn) { this.listeners.set(type, fn); }, removeEventListener(type) { this.listeners.delete(type); }, createElement(tag) { return new Element(this, tag); }, getElementById(id) { return this.ids.get(id); } };
  const root = document.createElement("div"); root.id = "novel-reader-root";
  const calls = [], actions = [], notices = []; let context = { directory: "" };
  const chapters = [{ number: 1, title: "第一章", hasText: true }, { number: 2, title: "第二章", hasText: true }];
  let respond = async (url, options) => {
    if (url === "/api/reader/books") return { books: [{ directory: "book", title: "夜航", format: "txt", chapters: 2 }] };
    if (url.startsWith("/api/reader/book?")) return { directory: new URL(url, "http://test").searchParams.get("directory"), title: "夜航", chapters, progress: {} };
    if (url.startsWith("/api/reader/chapter/")) return { number: Number(url.match(/chapter\/(\d+)/)[1]), title: "一章", content: "<script>never run()</script>\n星光照亮夜空。" };
    if (url.startsWith("/api/reader/text?")) return { title: "语料", content: "星光照亮夜空。" };
    return {};
  };
  const panel = reader.create({ document, root, storage, api: async (url, options) => { calls.push({ url, options }); return respond(url, options); }, fetch: async () => {}, loadPDF,
    getContext: () => context, onSelectProject: async directory => { actions.push(["select", directory]); context = { directory }; },
    onOpenMedia: async (...args) => actions.push(["media", ...args]), onEditChapter: async (...args) => actions.push(["edit", ...args]), onDistill: async (...args) => actions.push(["distill", ...args]), toast: message => notices.push(message) });
  return { panel, calls, actions, notices, root, document, $: id => document.getElementById(id), respond: fn => { respond = fn; }, getRespond: () => respond, context: value => { context = value; }, chapters };
}
const flush = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }

test("reader normalizes stale progress, limits values and drops invalid chapter bookmarks", () => {
  const state = reader.normalizeProgress({ chapter: 99, offset: 4, fontSize: 4, lineHeight: 9, theme: "<script>", bookmarks: [{ chapter: 1, offset: -2, label: "x".repeat(100) }, { chapter: 4 }] }, [{ number: 1 }]);
  assert.equal(state.chapter, 1); assert.equal(state.offset, 1); assert.equal(state.fontSize, 16); assert.equal(state.lineHeight, 2.2); assert.equal(state.theme, "auto"); assert.equal(state.bookmarks.length, 1); assert.equal(state.bookmarks[0].label.length, 80); assert.equal(state.bookmarks[0].offset, 0);
});

test("PDF extraction preserves split words, geometric word gaps and Chinese line breaks", () => {
  const item = (str, x, y, width) => ({ str, transform: [1, 0, 0, 1, x, y], width, height: 10 });
  assert.equal(reader.textFromPDFItems([item("Read", 0, 20, 20), item("er", 20, 20, 10), item("book", 35, 20, 20), item("第一行", 0, 0, 30), item("。", 30, 0, 10)]), "Reader book\n第一行。");
});

test("real offline PDF.js extracts both PDF pages and renders nonwhite text pixels", async () => {
  const base = path.resolve(__dirname, "../node_modules/pdfjs-dist");
  const pdfjs = await import(pathToFileURL(path.join(base, "legacy/build/pdf.mjs")));
  const { createCanvas } = require("@napi-rs/canvas");
  const data = new Uint8Array(fs.readFileSync(path.join(__dirname, "../scripts/fixtures/reader.pdf")));
  const options = reader.pdfOptions(data, `${base}/`);
  assert.equal(options.useWasm, false); assert.equal(options.enableXfa, false); assert.equal(options.disableFontFace, true);
  const loading = pdfjs.getDocument(options);
  try {
    const pdf = await loading.promise; assert.equal(pdf.numPages, 2);
    const page = await pdf.getPage(1); assert.match(reader.textFromPDFItems((await page.getTextContent()).items), /First chapter: a light in the library/);
    assert.match(reader.textFromPDFItems((await (await pdf.getPage(2)).getTextContent()).items), /Second chapter/);
    const viewport = page.getViewport({ scale: 1 }), canvas = createCanvas(viewport.width, viewport.height), context = canvas.getContext("2d");
    await page.render({ canvasContext: context, viewport, annotationMode: 0 }).promise;
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
    let dark = 0; for (let i = 0; i < pixels.length; i += 4) if (pixels[i] < 150 && pixels[i + 3] > 0) dark++;
    assert.ok(dark > 300, `Expected text pixels, found ${dark}`);
  } finally { await loading.destroy(); }
});

test("late chapter response cannot replace a newer chapter and user text stays plain text", async () => {
  const h = harness(); await h.panel.openBook("book"); const first = deferred();
  h.respond(async url => url.includes("/chapter/1?") ? first.promise : url.includes("/chapter/2?") ? { number: 2, title: "第二章", content: "<img src=x onerror=alert(1)>" } : {});
  const pending = h.panel.openChapter(1); await flush(); await h.panel.openChapter(2); first.resolve({ number: 1, content: "旧章" }); await pending;
  assert.equal(h.panel.getState().chapter, 2); assert.equal(h.$("nr-manuscript").children[1].textContent, "<img src=x onerror=alert(1)>");
  h.panel.destroy();
});

test("bookmarks capture independent scroll, persist full settings, and navigate to saved position", async () => {
  const h = harness(); await h.panel.openBook("book"); h.$("nr-page").scrollTop = 300; h.panel.addBookmark();
  h.$("nr-theme").value = "sepia"; await h.$("nr-theme").fire("change"); await h.panel.saveProgress();
  const saved = h.calls.filter(row => row.url === "/api/reader/progress").at(-1).options.body;
  assert.equal(saved.directory, "book"); assert.equal(saved.offset, .5); assert.equal(saved.bookmarks[0].offset, .5); assert.equal(saved.theme, "sepia"); assert.equal(saved.fontSize, 20);
  await h.panel.openChapter(2); await h.$("nr-bookmarks").children[0].children[0].fire("click"); assert.equal(h.panel.getState().chapter, 1); assert.equal(h.$("nr-page").scrollTop, 300);
  h.panel.destroy();
});

test("media, editing and style extraction remain explicit and target the selected chapter", async () => {
  const h = harness(); await h.panel.openBook("book"); await h.panel.openChapter(2);
  assert.deepEqual(h.actions, [["select", "book"]]);
  for (const kind of ["audio", "image", "video"]) await h.$(`nr-${kind}`).fire("click");
  await h.$("nr-edit").fire("click"); await h.$("nr-distill-chapter").fire("click");
  assert.deepEqual(h.actions.slice(1, 5), [["media", "audio", 2], ["media", "image", 2], ["media", "video", 2], ["edit", 2]]);
  assert.deepEqual(h.actions.at(-1)[1].chapters, [2]); assert.ok(h.calls.some(row => row.url.endsWith("&chapter=2")));
  h.panel.destroy();
});

test("refresh after editing loads latest prose without returning to an older project", async () => {
  const h = harness(); await h.panel.openBook("old"); h.context({ directory: "book" }); await h.panel.load();
  assert.equal(h.panel.getState().directory, "book"); assert.deepEqual(h.actions, [["select", "old"]]);
  h.panel.destroy();
});

test("failed imports reset controls and can be retried without switching books", async () => {
  const h = harness(); await h.panel.openBook("book"); h.respond(async () => { throw new Error("文档损坏"); });
  const file = new File(["invalid"], "bad.epub"); await h.panel.importFile(file);
  assert.match(h.$("nr-import-status").textContent, /文档损坏/); assert.equal(h.$("nr-import-submit").disabled, false); assert.equal(h.$("nr-file").value, ""); assert.equal(h.panel.getState().directory, "book");
  await h.panel.importFile(file); assert.equal(h.calls.filter(row => row.url === "/api/reader/import").length, 2); h.panel.destroy();
});

test("cancelled imports ignore a late upload result and allow a new selection", async () => {
  const h = harness(), waiting = deferred(); h.respond(async () => waiting.promise);
  const pending = h.panel.importFile(new File(["正文"], "novel.txt")); await flush(); h.panel.cancelImport();
  assert.equal(h.panel.getState().importing, false); assert.equal(h.$("nr-import-cancel").hidden, true);
  waiting.resolve({ directory: "unexpected" }); await pending;
  assert.equal(h.panel.getState().directory, ""); assert.equal(h.actions.length, 0); assert.match(h.$("nr-import-status").textContent, /已取消/); h.panel.destroy();
});

test("PDF import sends original bytes and ordered extracted pages; no text remains valid", async () => {
  let destroyed = 0; const lib = { GlobalWorkerOptions: {}, getDocument: () => ({ promise: Promise.resolve({ numPages: 2, getPage: async number => ({ getTextContent: async () => ({ items: number === 1 ? [{ str: "Text", hasEOL: true }] : [] }), cleanup() {} }) }), async destroy() { destroyed++; } }) };
  const h = harness({ loadPDF: async () => lib }); h.respond(async (url, options) => { if (url === "/api/reader/import") { const form = options.body; assert.equal(form.get("file").size, 4); assert.deepEqual(JSON.parse(form.get("pdfPages")), [{ page: 1, title: "第 1 页", text: "Text" }, { page: 2, title: "第 2 页", text: "" }]); throw new Error("fixture stops after validation"); } return {}; });
  await h.panel.importFile(new File(["%PDF"], "book.pdf")); assert.equal(destroyed, 1); assert.equal(lib.GlobalWorkerOptions.workerSrc, "/vendor/pdfjs/pdf.worker.mjs"); h.panel.destroy();
});

test("cancelling a PDF password dialog destroys pending work and leaves no password in DOM", async () => {
  const waiting = deferred(); let task, destroyed = false;
  const lib = { GlobalWorkerOptions: {}, getDocument: () => (task = { promise: waiting.promise, async destroy() { destroyed = true; } }) };
  const h = harness({ loadPDF: async () => lib }); const pending = h.panel.importFile(new File(["%PDF"], "encrypted.pdf")); await flush();
  task.onPassword(() => {}, 1); const dialog = h.root.children.at(-1); assert.equal(dialog.tagName, "dialog");
  await dialog.fire("cancel"); await pending; assert.equal(destroyed, true); assert.equal(h.root.children.some(node => node.tagName === "dialog"), false); assert.equal(h.$("nr-import-submit").disabled, false); h.panel.destroy();
});

test("busy project retains progress locally, refresh preserves it, and closing uses keepalive", async () => {
  const saved = new Map(), storage = { setItem: (key, value) => saved.set(key, value), getItem: key => saved.get(key), removeItem: key => saved.delete(key) };
  const h = harness({ storage }); await h.panel.openBook("book"); const original = h.getRespond();
  h.respond(async (url, options) => { if (url === "/api/reader/progress") throw new Error("项目正在运行"); return original(url, options); });
  h.$("nr-page").scrollTop = 300; h.panel.addBookmark(); await h.panel.saveProgress(); assert.equal(h.notices.length, 0); assert.equal(saved.size, 1);
  await h.panel.openBook("book"); assert.equal(h.panel.getState().progress.offset, .5); assert.equal(h.panel.getState().progress.bookmarks.length, 1);
  h.$("nr-page").clientHeight = 0; h.$("nr-page").scrollHeight = 0; h.document.hidden = true; h.document.listeners.get("visibilitychange")(); await flush();
  const request = h.calls.filter(row => row.options?.keepalive).at(-1); assert.equal(request.options.body.offset, .5); assert.equal(request.options.body.bookmarks.length, 1);
  h.panel.destroy(); assert.equal(h.document.listeners.size, 0);
  const reopened = harness({ storage }); await reopened.panel.openBook("book"); assert.equal(reopened.panel.getState().progress.bookmarks.length, 1); assert.equal(reopened.panel.getState().progress.offset, .5);
  await reopened.panel.saveProgress(); assert.equal(saved.size, 0, "Successful disk save clears local recovery record"); reopened.panel.destroy();
});

test("a successful import followed by an open error does not invite duplicate imports", async () => {
  const h = harness(); h.respond(async url => { if (url === "/api/reader/import") return { directory: "imported" }; throw new Error("网络暂不可用"); });
  await h.panel.importFile(new File(["正文"], "novel.txt")); assert.match(h.$("nr-import-status").textContent, /文档已导入.*无需重复导入/); h.panel.destroy();
});
