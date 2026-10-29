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
  set textContent(value) { this._textContent = value; this.children = []; }
  get textContent() { return this.children.length ? this.children.map(child => child.textContent).join("") : this._textContent; }
  append(...nodes) { nodes.forEach(node => { node.remove(); node.parent = this; this.children.push(node); }); }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(node => node !== this); }
  setAttribute(name, value) { this.attributes[name] = value; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  fire(type, event = {}) { return Promise.all((this.listeners[type] || []).map(fn => fn({ target: this, preventDefault() {}, ...event }))); }
  showModal() { this.open = true; }
  close() { if (this.open) { this.open = false; void this.fire("close"); } }
  focus() { this.document.activeElement = this; }
  getBoundingClientRect() { return { left: 0, top: 0, right: this.clientWidth, bottom: this.clientHeight, width: this.clientWidth, height: this.clientHeight }; }
}
function harness({ loadPDF, storage, view, mobile } = {}) {
  const document = { ids: new Map(), listeners: new Map(), addEventListener(type, fn) { this.listeners.set(type, fn); }, removeEventListener(type) { this.listeners.delete(type); }, createElement(tag) { return new Element(this, tag); }, getElementById(id) { return this.ids.get(id); } };
  if (view) document.defaultView = view;
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
  const panel = reader.create({ document, root, storage, mobile, api: async (url, options) => { calls.push({ url, options }); return respond(url, options); }, fetch: async () => {}, loadPDF,
    getContext: () => context, onSelectProject: async directory => { actions.push(["select", directory]); context = { directory }; },
    onOpenMedia: async (...args) => actions.push(["media", ...args]), onEditChapter: async (...args) => actions.push(["edit", ...args]), onDistill: async (...args) => actions.push(["distill", ...args]), toast: message => notices.push(message) });
  return { panel, calls, actions, notices, root, document, $: id => document.getElementById(id), respond: fn => { respond = fn; }, getRespond: () => respond, context: value => { context = value; }, chapters };
}
const flush = () => new Promise(resolve => setImmediate(resolve));
function mobileEnvironment() {
  let controller, narrow = true, visible = true, active = false;
  return {
    registerReader(value) { controller = value; value.setEnvironment(narrow, visible); return () => { controller = null; active = false; }; },
    setReaderImmersive(value) { active = value; },
    resize(value) { narrow = value; controller.setEnvironment(narrow, visible); },
    navigate(value) { visible = value; controller.setEnvironment(narrow, visible); },
    back() { return controller?.handleBack(); }, active: () => active, registered: () => Boolean(controller),
  };
}
async function tapCenter(h, overrides = {}) {
  const event = { pointerId: 1, isPrimary: true, button: 0, clientX: 300, clientY: 200, ...overrides };
  await h.$("nr-page").fire("pointerdown", event); await h.$("nr-page").fire("pointerup", event); await h.$("nr-page").fire("click", event);
}

test("phone reading opens cleanly and a center tap opens an accessible settings sheet without moving progress", async () => {
  const mobile = mobileEnvironment(), h = harness({ mobile }); await h.panel.openBook("book");
  assert.equal(h.panel.getState().mobileReading, true); assert.equal(mobile.active(), true);
  assert.equal(Boolean(h.$("nr-mobile-controls").open), false); assert.equal(h.$("nr-sidebar").parent, h.$("nr-mobile-directory"));
  h.$("nr-page").scrollTop = 300; await h.$("nr-page").fire("scroll");
  await tapCenter(h);
  assert.equal(h.$("nr-mobile-controls").open, true); assert.equal(h.panel.getState().mobilePanel, "controls");
  assert.equal(h.document.activeElement, h.$("nr-close-controls"));
  h.$("nr-font-size").value = "24"; await h.$("nr-font-size").fire("change");
  assert.equal(h.panel.getState().progress.offset, .5); assert.equal(h.$("nr-page").scrollTop, 300);
  await h.$("nr-mobile-controls").fire("cancel");
  assert.equal(h.$("nr-mobile-controls").open, false); assert.equal(h.document.activeElement, h.$("nr-page"));
  assert.equal(h.$("nr-open-controls").attributes["aria-expanded"], "false"); h.panel.destroy();
});

test("phone menu distinguishes center taps from scrolling, selection, long presses and multi-touch", async () => {
  let selection = ""; const h = harness({ mobile: mobileEnvironment(), view: { getSelection: () => ({ toString: () => selection }) } });
  await h.panel.openBook("book"); const page = h.$("nr-page");
  const event = { pointerId: 1, isPrimary: true, button: 0, clientX: 300, clientY: 200 };
  await tapCenter(h, { clientX: 30 }); assert.equal(h.panel.getState().mobilePanel, "");
  await page.fire("pointerdown", event); await page.fire("pointermove", { ...event, clientY: 220 }); await page.fire("pointerup", event);
  assert.equal(h.panel.getState().mobilePanel, "");
  await page.fire("pointerdown", event); await page.fire("pointerup", { ...event, clientX: 340 }); assert.equal(h.panel.getState().mobilePanel, "");
  await page.fire("pointerdown", event); page.scrollTop = 20; await page.fire("pointerup", event); assert.equal(h.panel.getState().mobilePanel, "");
  selection = "星光"; await tapCenter(h); assert.equal(h.panel.getState().mobilePanel, ""); selection = "";
  await page.fire("pointerdown", event); await new Promise(resolve => setTimeout(resolve, 460)); await page.fire("pointerup", event); assert.equal(h.panel.getState().mobilePanel, "");
  await page.fire("pointerdown", event); await page.fire("pointerdown", { ...event, pointerId: 2, isPrimary: false }); await page.fire("pointerup", event); assert.equal(h.panel.getState().mobilePanel, "");
  await page.fire("pointerdown", event); await page.fire("pointercancel"); await page.fire("pointerup", event); assert.equal(h.panel.getState().mobilePanel, "");
  await page.fire("keydown", { key: "Enter" }); assert.equal(h.panel.getState().mobilePanel, "controls"); h.panel.destroy();
});

test("phone center touch opens on the completed click so its compatibility click cannot hit the new backdrop", async () => {
  const h = harness({ mobile: mobileEnvironment() }); await h.panel.openBook("book");
  const page = h.$("nr-page"), event = { pointerId: 2, pointerType: "touch", isPrimary: true, button: 0, clientX: 300, clientY: 200 };
  await page.fire("pointerdown", event); await page.fire("pointerup", event);
  assert.equal(Boolean(h.$("nr-mobile-controls").open), false, "The backdrop must not exist before the browser dispatches its compatibility click");
  await page.fire("click", event); assert.equal(h.$("nr-mobile-controls").open, true);
  await h.$("nr-close-controls").fire("click"); await page.fire("click", event);
  assert.equal(h.$("nr-mobile-controls").open, false, "A completed gesture cannot be reused by a later click"); h.panel.destroy();
});

test("phone directory is a separate modal menu and chapter selection returns to the manuscript", async () => {
  const mobile = mobileEnvironment(), h = harness({ mobile }); await h.panel.openBook("book");
  await h.$("nr-open-controls").fire("click"); await h.$("nr-open-directory").fire("click");
  assert.equal(h.$("nr-mobile-controls").open, false); assert.equal(h.$("nr-mobile-directory").open, true);
  assert.equal(h.$("nr-sidebar").open, true); assert.equal(h.$("nr-toc").children.length, 2);
  await h.$("nr-toc").children[1].fire("click");
  assert.equal(h.panel.getState().chapter, 2); assert.equal(h.$("nr-mobile-directory").open, false); assert.equal(h.document.activeElement, h.$("nr-page"));
  assert.equal(mobile.back(), true); assert.equal(h.panel.getState().mobileReading, false); assert.equal(h.root.dataset.mobileMode, "shelf");
  assert.equal(h.$("nr-shelf-panel").open, true); assert.equal(mobile.active(), false); assert.equal(mobile.back(), false);
  assert.equal(h.document.activeElement, h.$("nr-shelf-panel").children[0], "Returning to the shelf focuses its summary without opening the phone keyboard");
  assert.equal(h.document.activeElement.tagName, "summary");
  await h.panel.openBook("book"); assert.equal(mobile.active(), true); h.panel.destroy(); assert.equal(mobile.registered(), false);
});

test("mobile overlays cannot survive view changes, desktop resizing or disposal", async () => {
  const mobile = mobileEnvironment(), h = harness({ mobile }); await h.panel.openBook("book");
  h.$("nr-page").scrollTop = 300; await h.$("nr-page").fire("scroll"); await tapCenter(h);
  mobile.navigate(false); assert.equal(h.$("nr-mobile-controls").open, false); assert.equal(mobile.active(), false);
  mobile.navigate(true); assert.equal(mobile.active(), true); assert.equal(h.panel.getState().progress.offset, .5);
  await h.$("nr-open-controls").fire("click"); await h.$("nr-open-directory").fire("click");
  mobile.resize(false);
  assert.equal(h.$("nr-mobile-directory").open, false); assert.equal(h.root.dataset.mobileMode, "desktop");
  assert.equal(h.document.activeElement, h.$("nr-page"));
  assert.notEqual(h.$("nr-sidebar").parent, h.$("nr-mobile-directory")); assert.equal(h.panel.getState().progress.offset, .5);
  mobile.resize(true); assert.equal(h.panel.getState().mobileReading, true); assert.equal(h.$("nr-page").scrollTop, 300);
  h.panel.destroy(); assert.equal(mobile.active(), false);
});
function deferred() { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }

function enableFullscreen(h) {
  let enters = 0, exits = 0;
  h.document.fullscreenEnabled = true;
  h.root.requestFullscreen = async () => {
    enters++; h.document.fullscreenElement = h.root; h.$("nr-page").clientHeight = 800;
    h.document.listeners.get("fullscreenchange")?.();
  };
  h.document.exitFullscreen = async () => {
    exits++; h.document.fullscreenElement = null; h.$("nr-page").clientHeight = 400;
    h.document.listeners.get("fullscreenchange")?.();
  };
  return () => ({ enters, exits });
}
function resizeView() {
  const listeners = new Map(); let observer, disconnected = false;
  return {
    ResizeObserver: class { constructor(callback) { observer = callback; } observe() {} disconnect() { disconnected = true; } },
    addEventListener: (type, callback) => listeners.set(type, callback), removeEventListener: type => listeners.delete(type),
    resize() { listeners.get("resize")?.(); observer?.(); },
    cleaned: () => disconnected && listeners.size === 0,
  };
}
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function settledFullscreen(h) {
  const deadline = Date.now() + 1000;
  while (h.root.dataset.fullscreenLayout !== "ready" && Date.now() < deadline) await delay(10);
  assert.equal(h.root.dataset.fullscreenLayout, "ready");
}

test("native Escape preserves position when resize and scroll arrive before fullscreenchange", async () => {
  const view = resizeView(), h = harness({ view }); enableFullscreen(h); await h.panel.openBook("book");
  const page = h.$("nr-page"); page.scrollTop = 270; await page.fire("scroll");
  await h.$("nr-fullscreen").fire("click"); await settledFullscreen(h);
  assert.equal(h.panel.getState().progress.offset, .45);
  h.document.fullscreenElement = null; page.clientHeight = 600; page.scrollTop = 90;
  await page.fire("scroll"); h.panel.flushProgress();
  assert.equal(h.panel.getState().progress.offset, .45, "Native layout scroll must not replace the saved reading offset");
  await delay(20); h.document.listeners.get("fullscreenchange")();
  await delay(60); page.clientHeight = 500; view.resize(); await page.fire("scroll");
  await delay(60); page.clientHeight = 400; view.resize(); await page.fire("scroll");
  await settledFullscreen(h);
  assert.equal(page.scrollTop, 270); assert.equal(h.panel.getState().progress.offset, .45);
  await h.panel.saveProgress();
  assert.equal(h.calls.filter(({ url }) => url === "/api/reader/progress").at(-1).options.body.offset, .45);
  h.panel.destroy(); assert.equal(view.cleaned(), true);
});

test("fullscreen promises do not unlock position before delayed resizes, and later viewport changes preserve it", async () => {
  const view = resizeView(), h = harness({ view }); enableFullscreen(h); await h.panel.openBook("book");
  const page = h.$("nr-page"); page.scrollTop = 300; await page.fire("scroll");
  h.root.requestFullscreen = async () => { h.document.fullscreenElement = h.root; h.document.listeners.get("fullscreenchange")(); };
  await h.$("nr-fullscreen").fire("click");
  assert.equal(h.root.dataset.fullscreenLayout, "pending");
  await delay(60); page.clientHeight = 700; page.scrollTop = 200; await page.fire("scroll"); view.resize();
  await delay(60); page.clientHeight = 800; page.scrollTop = 150; await page.fire("scroll"); view.resize();
  await settledFullscreen(h);
  assert.equal(page.scrollTop, 100); assert.equal(h.panel.getState().progress.offset, .5);
  // A late native resize can arrive after the first stable interval. Keeping
  // a ResizeObserver protects progress without freezing subsequent user input.
  page.clientWidth = 500; page.scrollHeight = 1400; page.clientHeight = 400;
  view.resize(); await page.fire("scroll");
  assert.equal(page.scrollTop, 500); assert.equal(h.panel.getState().progress.offset, .5);
  page.scrollTop = 250; await page.fire("scroll");
  assert.equal(h.panel.getState().progress.offset, .25, "User scrolling must resume once the transition settles");
  page.clientHeight = 600; view.resize();
  assert.equal(page.scrollTop, 200); assert.equal(h.panel.getState().progress.offset, .25, "A later resize must preserve the new user position, not the old fullscreen target");
  h.panel.destroy(); assert.equal(view.cleaned(), true);
});

test("a stale native request rejection cannot release a newer fullscreen event's layout restoration", async () => {
  const view = resizeView(), h = harness({ view }), pending = deferred(); enableFullscreen(h); await h.panel.openBook("book");
  h.$("nr-page").scrollTop = 300; await h.$("nr-page").fire("scroll");
  h.root.requestFullscreen = () => pending.promise;
  const clicking = h.$("nr-fullscreen").fire("click");
  h.document.fullscreenElement = h.root; h.document.listeners.get("fullscreenchange")();
  pending.reject(new Error("native request superseded")); await clicking;
  assert.equal(h.root.dataset.fullscreenLayout, "pending");
  h.$("nr-page").clientHeight = 800; view.resize();
  await settledFullscreen(h); assert.equal(h.$("nr-page").scrollTop, 100);
  h.panel.destroy();
});

test("reader fullscreen preserves chapter, relative position and preferences across entry and Escape", async () => {
  const h = harness(), requests = enableFullscreen(h);
  assert.equal(h.$("nr-fullscreen").disabled, true);
  await h.panel.openBook("book"); h.$("nr-page").scrollTop = 300;
  h.$("nr-theme").value = "sepia"; await h.$("nr-theme").fire("change");
  await h.$("nr-fullscreen").fire("click"); await flush();
  assert.equal(h.document.fullscreenElement, h.root); assert.equal(h.panel.getState().fullscreen, true);
  assert.equal(h.$("nr-fullscreen").attributes["aria-pressed"], "true");
  assert.equal(h.$("nr-page").scrollTop, 100); assert.equal(h.panel.getState().progress.offset, .5);
  // Native Escape changes fullscreenElement independently of the button.
  await h.document.exitFullscreen(); await flush();
  assert.equal(h.$("nr-fullscreen").textContent, "全屏阅读"); assert.equal(h.$("nr-page").scrollTop, 300);
  assert.equal(h.panel.getState().chapter, 1); assert.equal(h.panel.getState().progress.theme, "sepia");
  assert.deepEqual(requests(), { enters: 1, exits: 1 }); h.panel.destroy();
});

test("fullscreen exits before media, editing and style navigation and destroy removes its listener", async () => {
  const h = harness(), requests = enableFullscreen(h); await h.panel.openBook("book");
  for (const id of ["nr-audio", "nr-edit", "nr-distill-chapter"]) {
    await h.$("nr-fullscreen").fire("click"); await flush(); assert.equal(h.panel.getState().fullscreen, true);
    await h.$(id).fire("click"); await flush(); assert.equal(h.panel.getState().fullscreen, false, id);
  }
  assert.equal(h.actions.filter(action => ["media", "edit", "distill"].includes(action[0])).length, 3);
  await h.$("nr-fullscreen").fire("click"); h.panel.destroy(); await flush();
  assert.deepEqual(requests(), { enters: 4, exits: 4 }); assert.equal(h.document.listeners.size, 0);
});

test("fullscreen denial leaves normal reading and progress usable without a stale active button", async () => {
  const h = harness(); await h.panel.openBook("book"); h.$("nr-page").scrollTop = 300;
  h.root.requestFullscreen = async () => { throw new Error("permission denied"); };
  await h.$("nr-fullscreen").fire("click");
  assert.equal(h.panel.getState().fullscreen, false); assert.equal(h.$("nr-fullscreen").disabled, false);
  assert.equal(h.$("nr-fullscreen").attributes["aria-pressed"], "false"); assert.match(h.notices.at(-1), /无法进入全屏阅读/);
  h.$("nr-page").scrollTop = 150; await h.$("nr-page").fire("scroll"); assert.equal(h.panel.getState().progress.offset, .25);
  h.panel.destroy();
});

test("leaving the reader cancels an outstanding fullscreen request before it can reopen a hidden view", async () => {
  const h = harness(), requested = deferred(); enableFullscreen(h); await h.panel.openBook("book");
  h.root.requestFullscreen = async () => {
    await requested.promise; h.document.fullscreenElement = h.root;
    h.document.listeners.get("fullscreenchange")?.();
  };
  const opening = h.$("nr-fullscreen").fire("click");
  await h.panel.leaveFullscreen(); requested.resolve(); await opening; await flush();
  assert.equal(h.panel.getState().fullscreen, false); assert.equal(h.$("nr-fullscreen").attributes["aria-pressed"], "false");
  h.panel.destroy();
});

test("reader normalizes stale progress, limits values and drops invalid chapter bookmarks", () => {
  const state = reader.normalizeProgress({ chapter: 99, offset: 4, fontSize: 4, lineHeight: 9, theme: "<script>", bookmarks: [{ chapter: 1, offset: -2, label: "x".repeat(100) }, { chapter: 4 }] }, [{ number: 1 }]);
  assert.equal(state.chapter, 1); assert.equal(state.offset, 1); assert.equal(state.fontSize, 16); assert.equal(state.lineHeight, 2.2); assert.equal(state.theme, "auto"); assert.equal(state.bookmarks.length, 1); assert.equal(state.bookmarks[0].label.length, 80); assert.equal(state.bookmarks[0].offset, 0);
});

test("deleting the current book cancels queued progress and leaves an empty importable reader", async () => {
  const h = harness(); enableFullscreen(h); await h.panel.openBook("book");
  await h.$("nr-fullscreen").fire("click"); await flush();
  const writesBefore = h.calls.filter(({ url }) => url === "/api/reader/progress").length;
  const queued = h.panel.saveProgress();
  h.context({ directory: "" }); h.panel.forgetBook("book");
  await queued; await flush();
  assert.equal(h.panel.getState().directory, ""); assert.equal(h.panel.getState().chapter, 0); assert.equal(h.panel.getState().fullscreen, false);
  for (const id of ["nr-audio", "nr-image", "nr-video", "nr-edit", "nr-add-bookmark", "nr-fullscreen"]) assert.equal(h.$(id).disabled, true, id);
  assert.equal(h.calls.filter(({ url }) => url === "/api/reader/progress").length, writesBefore);
  const original = h.getRespond(); h.respond(async (url, options) => url === "/api/reader/books" ? { books: [] } : original(url, options));
  const callsBefore = h.calls.length; await h.panel.load();
  assert.equal(h.calls.slice(callsBefore).some(({ url }) => url.startsWith("/api/reader/book?")), false);
  assert.notEqual(h.$("nr-import-submit").disabled, true);
  h.panel.destroy();
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

test("entering a reader disables old chapter actions before its first asynchronous read", async () => {
  const h = harness(); await h.panel.openBook("book"); await h.panel.openChapter(2);
  const original = h.getRespond(), shelf = deferred();
  h.respond(async (url, options) => url === "/api/reader/books" ? shelf.promise : original(url, options));
  const pending = h.panel.load();
  assert.equal(h.root.attributes["aria-busy"], "true");
  for (const id of ["nr-audio", "nr-video", "nr-image", "nr-edit", "nr-add-bookmark", "nr-distill-chapter", "nr-prev"]) assert.equal(h.$(id).disabled, true, id);
  await h.$("nr-audio").fire("click"); h.panel.addBookmark();
  assert.equal(h.actions.some(action => action[0] === "media"), false); assert.equal(h.panel.getState().progress.bookmarks.length, 0);
  shelf.resolve({ books: [{ directory: "book", title: "夜航", chapters: 2 }] }); await pending;
  assert.equal(h.root.attributes["aria-busy"], "false"); assert.equal(h.$("nr-audio").disabled, false);
  await h.$("nr-audio").fire("click"); assert.deepEqual(h.actions.at(-1), ["media", "audio", 1]); h.panel.destroy();
});

test("quick book selection assigns intent before a slow save and an older finally cannot unlock it", async () => {
  const h = harness(); await h.panel.openBook("book"); const original = h.getRespond(), save = deferred(), next = deferred(); let saves = 0;
  h.respond(async (url, options) => {
    if (url === "/api/reader/progress") return ++saves === 1 ? save.promise : {};
    if (url === "/api/reader/book?directory=new") return next.promise;
    return original(url, options);
  });
  const first = h.panel.openBook("old"), second = h.panel.openBook("new");
  assert.equal(h.panel.getState().loading, true); assert.equal(h.$("nr-edit").disabled, true);
  await flush(); save.resolve({}); await first; await flush();
  assert.equal(h.panel.getState().loading, true, "Old save completion must not clear the new book's busy state");
  assert.equal(h.calls.some(row => row.url === "/api/reader/book?directory=old"), false, "Superseded book must not be read or selected");
  next.resolve({ directory: "new", title: "新书", chapters: h.chapters, progress: {} }); await second;
  assert.equal(h.panel.getState().directory, "new"); assert.equal(h.panel.getState().loading, false); assert.equal(h.actions.some(action => action[1] === "old"), false); h.panel.destroy();
});

test("a late shelf refresh cannot override an explicit book choice or clear its loading state", async () => {
  const h = harness(); await h.panel.openBook("book"); const original = h.getRespond(), shelf = deferred(), next = deferred();
  h.respond(async (url, options) => url === "/api/reader/books" ? shelf.promise : url === "/api/reader/book?directory=new" ? next.promise : original(url, options));
  const refreshing = h.panel.load(), opening = h.panel.openBook("new"); await flush();
  const oldReads = h.calls.filter(row => row.url === "/api/reader/book?directory=book").length;
  shelf.resolve({ books: [{ directory: "book", title: "旧书", chapters: 2 }] }); await refreshing;
  assert.equal(h.calls.filter(row => row.url === "/api/reader/book?directory=book").length, oldReads);
  assert.equal(h.root.attributes["aria-busy"], "true");
  next.resolve({ directory: "new", title: "新书", chapters: h.chapters, progress: {} }); await opening;
  await h.panel.openChapter(2); const before = h.$("nr-manuscript").children[1]; await h.panel.load({ refreshBook: false });
  assert.equal(h.panel.getState().directory, "new"); assert.equal(h.panel.getState().chapter, 2); assert.equal(h.$("nr-manuscript").children[1], before); h.panel.destroy();
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
  await dialog.fire("cancel"); await pending; assert.equal(destroyed, true); assert.equal(h.root.children.includes(dialog), false); assert.equal(h.$("nr-import-submit").disabled, false); h.panel.destroy();
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

const descendants = node => [node, ...node.children.flatMap(descendants)];
const marks = h => descendants(h.$("nr-manuscript")).filter(node => node.tagName === "mark");
const changeReader = async (h, id, value) => { h.$(id).value = String(value); await h.$(id).fire("change"); };
const readerKey = async (h, key, extra = {}) => { let prevented = false; await h.root.fire("keydown", { target: h.$("nr-page"), key, preventDefault() { prevented = true; }, ...extra }); return prevented; };

test("desktop typography and collapsed directory persist without accepting arbitrary CSS", async () => {
  const h = harness(); await h.panel.openBook("book");
  h.$("nr-page").scrollTop = 300; await h.$("nr-page").fire("scroll");
  await changeReader(h, "nr-font-family", "sans"); await changeReader(h, "nr-page-width", "compact"); await changeReader(h, "nr-paragraph-spacing", 1);
  await h.$("nr-toggle-sidebar").fire("click"); await h.panel.saveProgress();
  const saved = h.calls.filter(call => call.url === "/api/reader/progress").at(-1).options.body;
  assert.equal(saved.fontFamily, "sans"); assert.equal(saved.pageWidth, "compact"); assert.equal(saved.paragraphSpacing, 1); assert.equal(saved.sidebarCollapsed, true);
  assert.equal(saved.offset, .5); assert.equal(h.root.dataset.sidebarCollapsed, "true");
  const normalized = reader.normalizeProgress({ fontFamily: "url(evil)", pageWidth: "999999px", paragraphSpacing: 100 });
  assert.equal(normalized.fontFamily, "serif"); assert.equal(normalized.pageWidth, "comfortable"); assert.equal(normalized.paragraphSpacing, .5);
  h.panel.destroy();
});

test("older pending local progress merges with the saved new typography for the same book", async () => {
  const cached = { directory: "book", chapter: 2, offset: .4, fontSize: 24, lineHeight: 1.8, theme: "sepia", mode: "text", bookmarks: [] };
  const h = harness({ storage: { getItem: () => JSON.stringify(cached), setItem() {}, removeItem() {} } }), respond = h.getRespond();
  h.respond(async (url, options) => {
    const result = await respond(url, options);
    if (url.startsWith("/api/reader/book?")) result.progress = { fontFamily: "sans", pageWidth: "wide", paragraphSpacing: 1, sidebarCollapsed: true };
    return result;
  });
  await h.panel.openBook("book");
  const state = h.panel.getState().progress;
  assert.equal(state.chapter, 2); assert.equal(state.offset, .4); assert.equal(state.fontFamily, "sans"); assert.equal(state.pageWidth, "wide"); assert.equal(state.paragraphSpacing, 1); assert.equal(state.sidebarCollapsed, true);
  h.panel.destroy();
});

test("screen turns preserve an overlap, cross chapter boundaries, and seeking can return to the prior location", async () => {
  const h = harness(); await h.panel.openBook("book"); const page = h.$("nr-page");
  assert.equal(await readerKey(h, "PageDown"), true); assert.ok(page.scrollTop > 0 && page.scrollTop < page.clientHeight);
  assert.equal(await readerKey(h, " ", { shiftKey: true }), true); assert.equal(page.scrollTop, 0);
  await changeReader(h, "nr-progress", 500); assert.equal(h.panel.getState().progress.offset, .5);
  await h.$("nr-return-location").fire("click"); assert.equal(h.panel.getState().progress.offset, 0);
  await changeReader(h, "nr-progress", 1000); await h.$("nr-page-next").fire("click"); assert.equal(h.panel.getState().chapter, 2);
  await h.$("nr-page-prev").fire("click"); assert.equal(h.panel.getState().chapter, 1); assert.equal(h.panel.getState().progress.offset, 1);
  assert.equal(reader.bookRatio([{ number: 1, words: 100 }, { number: 2, words: 300 }], 2, .5), .625);
  assert.equal(reader.bookRatio([{ number: 1 }, { number: 2 }], 2, .5), .75); h.panel.destroy();
});

test("desktop key handling respects editable targets, selection, composition and modal dialogs", async () => {
  let selection = ""; const h = harness({ view: { getSelection: () => ({ toString: () => selection }) } }); await h.panel.openBook("book");
  for (const id of ["nr-search-input", "nr-font-family", "nr-add-bookmark"]) assert.equal(await readerKey(h, "PageDown", { target: h.$(id) }), false);
  assert.equal(await readerKey(h, "ArrowRight", { shiftKey: true }), false);
  assert.equal(await readerKey(h, "ArrowRight", { ctrlKey: true, shiftKey: true }), false);
  assert.equal(await readerKey(h, " ", { target: { tagName: "span", isContentEditable: true } }), false);
  assert.equal(await readerKey(h, "PageDown", { isComposing: true }), false);
  selection = "选中文字"; assert.equal(await readerKey(h, "PageDown"), false); selection = "";
  h.document.querySelector = () => ({ open: true }); assert.equal(await readerKey(h, "PageDown"), false); h.document.querySelector = () => null;
  assert.equal(h.panel.getState().chapter, 1); assert.equal(h.panel.getState().progress.offset, 0);
  assert.equal(await readerKey(h, "f", { ctrlKey: true }), true); assert.equal(h.document.activeElement, h.$("nr-search-input"));
  assert.equal(await readerKey(h, "b", { ctrlKey: true }), true); assert.equal(h.panel.getState().progress.bookmarks.length, 1);
  h.panel.destroy();
});

test("focus mode retains progress and exits for media actions and phone layout", async () => {
  const mobile = mobileEnvironment(); mobile.resize = mobile.resize.bind(mobile);
  const h = harness({ mobile }); mobile.resize(false); await h.panel.openBook("book");
  h.$("nr-page").scrollTop = 300; await h.$("nr-page").fire("scroll");
  await readerKey(h, "f"); assert.equal(h.panel.getState().focusMode, true); assert.equal(h.panel.getState().progress.offset, .5);
  await readerKey(h, "Escape"); assert.equal(h.panel.getState().focusMode, false);
  await h.$("nr-focus").fire("click"); await h.$("nr-audio").fire("click"); assert.equal(h.panel.getState().focusMode, false); assert.ok(h.actions.some(action => action[0] === "media"));
  await h.$("nr-focus").fire("click"); mobile.resize(true); assert.equal(h.panel.getState().focusMode, false); assert.equal(h.panel.getState().mobileReading, true); h.panel.destroy();
});

test("shelf sorts by real reading history and exposes a continue entry", async () => {
  const h = harness(), respond = h.getRespond();
  h.respond((url, options) => url === "/api/reader/books" ? { books: [
    { directory: "old", title: "旧书", chapters: 2, lastReadAt: "2026-10-25T10:00:00Z", progress: { chapter: 1, ratio: .2 } },
    { directory: "recent", title: "新书", chapters: 2, lastReadAt: "2026-10-29T10:00:00Z", progress: { chapter: 2, ratio: .7 } },
    { directory: "unread", title: "未读", chapters: 2, updatedAt: "2099-01-01T00:00:00Z" },
  ] } : respond(url, options));
  await h.panel.load({ refreshBook: false }); assert.equal(h.$("nr-shelf").children[0].dataset.directory, "recent");
  assert.equal(h.$("nr-continue").hidden, false); assert.equal(h.$("nr-continue").dataset.directory, "recent");
  await h.$("nr-continue").fire("click"); assert.equal(h.panel.getState().directory, "recent"); h.panel.destroy();
});

test("Unicode search highlights only source text and clears stale or removed results", async () => {
  const h = harness(), respond = h.getRespond(), content = "前文😀<img src=x onerror=alert(1)>\n\n夜色中的星光。";
  const start = Array.from(content).indexOf("星"); let hash = "old";
  h.respond((url, options) => {
    if (url.startsWith("/api/reader/search?")) return { matches: [{ chapter: 1, title: "一章", excerpt: "星光", start, length: 2, contentHash: "old" }] };
    if (url.startsWith("/api/reader/chapter/")) return { number: 1, title: "一章", content, contentHash: hash };
    return respond(url, options);
  });
  await h.panel.openBook("book"); h.$("nr-search-input").value = "星光"; await h.$("nr-search-submit").fire("click"); await h.$("nr-search-next").fire("click");
  assert.equal(marks(h).length, 1); assert.equal(marks(h)[0].textContent, "星光");
  assert.equal(descendants(h.$("nr-manuscript")).some(node => node.tagName === "img"), false);
  h.$("nr-search-input").value = ""; await h.$("nr-search-submit").fire("click"); assert.equal(marks(h).length, 0);
  h.$("nr-search-input").value = "星光"; await h.$("nr-search-submit").fire("click"); hash = "edited"; await h.$("nr-search-next").fire("click");
  assert.equal(marks(h).length, 0); assert.match(h.$("nr-status").textContent, /正文已更新/); h.panel.destroy();
});

test("the most recent same-chapter search hit wins when chapter responses arrive out of order", async () => {
  const h = harness(), respond = h.getRespond(), requests = [];
  const content = "甲星光乙星光丙";
  h.respond((url, options) => {
    if (url.startsWith("/api/reader/search?")) return { matches: [1, 4].map(start => ({ chapter: 1, title: "章", excerpt: "星光", start, length: 2, contentHash: "same" })) };
    if (url.startsWith("/api/reader/chapter/")) return { number: 1, title: "章", content, contentHash: "same" };
    return respond(url, options);
  });
  await h.panel.openBook("book"); h.$("nr-search-input").value = "星光"; await h.$("nr-search-submit").fire("click");
  const previous = h.getRespond(); h.respond((url, options) => { if (url.startsWith("/api/reader/chapter/")) { const request = deferred(); requests.push(request); return request.promise; } return previous(url, options); });
  const rows = h.$("nr-search-results").children.slice(1), first = rows[0].fire("click"), second = rows[1].fire("click");
  requests[1].resolve({ number: 1, title: "章", content, contentHash: "same" }); await second;
  requests[0].resolve({ number: 1, title: "章", content, contentHash: "same" }); await first;
  assert.equal(rows[1].attributes["aria-current"], "true"); assert.equal(rows[0].attributes["aria-current"], "false");
  assert.equal(marks(h).length, 1); h.panel.destroy();
});
