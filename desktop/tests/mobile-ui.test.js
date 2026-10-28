"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function harness({ narrow = true, android = false } = {}) {
  let active, dialog = null;
  class Element {
    constructor(tag = "DIV") {
      this.tagName = tag; this.dataset = {}; this.attributes = {}; this.listeners = new Map(); this.inert = false;
      this.classes = new Set(); this.classList = { toggle: (name, value) => value ? this.classes.add(name) : this.classes.delete(name) };
      this.style = { setProperty() {} };
    }
    addEventListener(name, callback) { this.listeners.set(name, callback); }
    setAttribute(name, value) { this.attributes[name] = value; }
    removeAttribute(name) { delete this.attributes[name]; }
    focus() { active = this; }
    click() { this.listeners.get("click")?.({ target: this }); }
    getClientRects() { return [{}]; }
    querySelectorAll() { return this.controls || []; }
  }
  const nodes = new Map();
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  const root = new Element(), workspace = new Element(), nav = new Element(), projectButton = new Element("BUTTON");
  nav.controls = ["projects", "overview", "chapters", "reader"].map(view => {
    const button = new Element("BUTTON"); button.dataset.goView = view; return button;
  });
  const phone = { matches: narrow, addEventListener() {} }, compact = { matches: narrow, addEventListener() {} };
  const handlers = new Map(), windowHandlers = new Map();
  const document = {
    documentElement: root, getElementById: $, get activeElement() { return active; },
    querySelector: (selector) => ({ ".workspace": workspace, ".mobile-bottom-nav": nav, 'dialog[open]': dialog, '.nav-item[data-view="projects"]': projectButton })[selector],
    querySelectorAll: () => dialog ? [dialog] : [], addEventListener: (name, callback) => handlers.set(name, callback),
  };
  const window = { innerWidth: narrow ? 390 : 1440, innerHeight: 844, visualViewport: { height: 844, addEventListener: (name, callback) => windowHandlers.set("viewport:" + name, callback) },
    matchMedia: (query) => query.includes("pointer") ? phone : compact, addEventListener: (name, callback) => windowHandlers.set(name, callback) };
  if (android) window.ainovelAndroid = {};
  const source = fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/mobile.js"), "utf8");
  vm.runInNewContext(source, { window, document, location: { search: "" }, URLSearchParams, Event, requestAnimationFrame: callback => callback() });
  const api = window.ainovelMobile;
  projectButton.addEventListener("click", () => api.onViewChange("projects"));
  $("main-navigation").controls = [$("mobile-close-menu"), projectButton];
  return { $, root, workspace, nav, api, window, phone, handlers, windowHandlers,
    dialog: (cancelled = false) => { dialog = new Element("DIALOG"); dialog.open = true; dialog.dispatchEvent = () => !cancelled; dialog.close = () => { dialog.open = false; dialog = null; }; return dialog; },
    focusInput: () => { active = new Element("TEXTAREA"); },
    key: (event) => handlers.get("keydown")(event),
  };
}

test("phone drawer traps focus, hides background from interaction and closes without page reload", () => {
  const h = harness();
  assert.equal(h.api.isMobile(), true);
  assert.equal(h.$("main-navigation").inert, true);
  h.$("mobile-open-menu").click();
  assert.equal(h.$("main-navigation").inert, false);
  assert.equal(h.workspace.inert, true);
  assert.equal(h.nav.inert, true);
  assert.equal(h.$("mobile-open-menu").attributes["aria-expanded"], "true");
  let prevented = false;
  h.key({ key: "Tab", shiftKey: true, preventDefault: () => { prevented = true; } });
  assert.equal(prevented, true);
  assert.equal(h.api.handleBack(), true);
  assert.equal(h.workspace.inert, false);
  assert.equal(h.$("main-navigation").inert, true);
  assert.equal(h.api.handleBack(), false);
});

test("Android back respects dialog cancellation before navigating to the project shelf", () => {
  const h = harness({ android: true });
  assert.equal(h.root.classes.has("android-app"), true);
  h.api.onViewChange("chapters");
  const blocked = h.dialog(true);
  assert.equal(h.api.handleBack(), true);
  assert.equal(blocked.open, true);
  assert.equal(h.root.dataset.mobileView, "chapters");
  const open = h.dialog();
  assert.equal(h.api.handleBack(), true);
  assert.equal(open.open, false);
  assert.equal(h.root.dataset.mobileView, "chapters");
  assert.equal(h.api.handleBack(), true);
  assert.equal(h.root.dataset.mobileView, "projects");
  assert.equal(h.nav.controls[0].attributes["aria-current"], "page");
  assert.equal(h.api.handleBack(), false);
});

test("chapter shortcuts select one phone region and reject unrelated region names", () => {
  const h = harness();
  h.api.showChapterRegion("editor");
  assert.equal(h.$("chapter-workspace").dataset.mobileRegion, "editor");
  assert.equal(h.$("chapter-jump-editor").attributes["aria-pressed"], "true");
  assert.equal(h.$("chapter-jump-directory").attributes["aria-pressed"], "false");
  h.api.showChapterRegion("unknown");
  assert.equal(h.$("chapter-workspace").dataset.mobileRegion, "editor");
});

test("keyboard hides bottom controls only on phones and resizing back restores desktop navigation", () => {
  const h = harness();
  h.focusInput();
  h.window.visualViewport.height = 420;
  h.windowHandlers.get("viewport:resize")();
  assert.equal(h.root.classes.has("mobile-keyboard-open"), true);
  h.window.visualViewport.height = 844;
  h.windowHandlers.get("viewport:resize")();
  assert.equal(h.root.classes.has("mobile-keyboard-open"), false);
  h.phone.matches = false;
  h.windowHandlers.get("resize")();
  assert.equal(h.api.isMobile(), false);
  assert.equal(h.$("main-navigation").inert, false);
  assert.match(h.$("prompt-input-hint").textContent, /Enter/);
  const desktop = harness({ narrow: false });
  assert.equal(desktop.root.classes.has("mobile-layout"), false);
  assert.equal(desktop.$("main-navigation").inert, false);
});
