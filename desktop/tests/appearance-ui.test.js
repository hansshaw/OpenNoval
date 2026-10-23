"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const source = fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/appearance.js"), "utf8");
const cacheKey = "ainovel.appearance.v1";
const plain = (value) => JSON.parse(JSON.stringify(value));
const flush = () => new Promise((resolve) => setImmediate(resolve));
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function harness({ cached, saved, restore, persist, reduced = false, storageFails = false } = {}) {
  const values = new Map(cached === undefined ? [] : [[cacheKey, cached]]);
  const writes = [], nodes = new Map(), documentEvents = new Map(), mediaEvents = new Map();
  const root = { dataset: {} }, meta = { content: "dark" };
  let mounted = false, focused = null;
  const element = (id) => {
    if (!nodes.has(id)) nodes.set(id, {
      value: "", checked: false, disabled: false, open: false, textContent: "", listeners: new Map(),
      addEventListener(name, callback) { this.listeners.set(name, callback); },
      focus() { focused = this; }, showModal() { this.open = true; }, close() { this.open = false; },
      querySelector() { return radios.find((radio) => radio.checked); },
    });
    return nodes.get(id);
  };
  const radios = ["original", "ink", "fresh", "romance", "genshin", "starry", "aurora", "silk"].map((theme) => {
    const radio = element(`theme-${theme}`); radio.value = theme; radio.checked = theme === "original"; return radio;
  });
  const media = { matches: reduced, addEventListener: (name, callback) => mediaEvents.set(name, callback) };
  const desktop = {
    getAppearance: () => restore ? restore() : Promise.resolve(saved ?? { theme: "original", motion: false }),
    setAppearance: (preference) => { writes.push(plain(preference)); return persist ? persist(plain(preference)) : Promise.resolve(preference); },
  };
  const document = {
    documentElement: root, hidden: false,
    querySelector: () => meta,
    querySelectorAll: () => mounted ? radios : [],
    getElementById: (id) => mounted ? element(id) : null,
    addEventListener: (name, callback) => documentEvents.set(name, callback),
  };
  const window = {
    matchMedia: () => media, ainovelDesktop: desktop,
    localStorage: {
      getItem: (key) => { if (storageFails) throw new Error("storage denied"); return values.get(key) ?? null; },
      setItem: (key, value) => { if (storageFails) throw new Error("storage denied"); values.set(key, value); },
    },
  };
  vm.runInNewContext(source, { window, document, console });
  return {
    root, meta, writes, element, radios, values,
    mount() { mounted = true; documentEvents.get("DOMContentLoaded")(); },
    choose(theme) { for (const radio of radios) radio.checked = radio.value === theme; element(`theme-${theme}`).listeners.get("change")(); },
    motion(checked) { const toggle = element("appearance-motion"); toggle.checked = checked; toggle.listeners.get("change")({ target: toggle }); },
    click(id) { element(id).listeners.get("click")(); },
    reduce(value) { media.matches = value; mediaEvents.get("change")(); },
    hide(value) { document.hidden = value; documentEvents.get("visibilitychange")(); },
    focused: () => focused,
  };
}

test("appearance boots safely before the DOM exists and restores durable preferences over the cache", async () => {
  const h = harness({ cached: JSON.stringify({ theme: "ink", motion: true }), saved: { theme: "genshin", motion: false } });
  assert.equal(h.root.dataset.theme, "ink");
  assert.equal(h.root.dataset.motion, "on");
  await flush();
  h.mount();
  assert.equal(h.root.dataset.theme, "genshin");
  assert.equal(h.root.dataset.motion, "off");
  assert.equal(h.element("appearance-label").textContent, "原神风 · 星旅");
  assert.equal(h.element("theme-genshin").checked, true);
  assert.equal(h.meta.content, "light");
  assert.deepEqual(JSON.parse(h.values.get(cacheKey)), { theme: "genshin", motion: false });
  assert.deepEqual(h.writes, [], "restore must not write back or touch project state");
});

test("a delayed startup restore never overrides a theme chosen by the user", async () => {
  const saved = deferred();
  const h = harness({ restore: () => saved.promise });
  h.mount();
  h.choose("romance");
  h.motion(true);
  saved.resolve({ theme: "ink", motion: false });
  await flush();
  assert.equal(h.root.dataset.theme, "romance");
  assert.equal(h.root.dataset.motion, "on");
  assert.equal(h.meta.content, "dark");
  assert.deepEqual(h.writes, [{ theme: "romance", motion: false }, { theme: "romance", motion: true }]);
  assert.deepEqual(JSON.parse(h.values.get(cacheKey)), { theme: "romance", motion: true });
});

test("rapid choices preview immediately and persist in order even when an earlier write fails", async () => {
  const first = deferred(), second = deferred();
  let calls = 0;
  const h = harness({ persist: () => ++calls === 1 ? first.promise : second.promise });
  h.mount(); await flush();
  h.choose("ink");
  h.choose("fresh");
  await flush();
  assert.equal(h.root.dataset.theme, "fresh");
  assert.deepEqual(h.writes, [{ theme: "ink", motion: false }]);
  first.reject(new Error("disk error"));
  await flush();
  assert.deepEqual(h.writes, [{ theme: "ink", motion: false }, { theme: "fresh", motion: false }]);
  assert.equal(h.element("appearance-status").textContent, "", "an old write error must not describe the new choice");
  second.resolve({ theme: "fresh", motion: false });
  await flush();
  assert.equal(h.element("appearance-status").textContent, "外观已保存");
});

test("a failed current write reports the unsaved state and the next choice retries normally", async () => {
  let fail = true;
  const h = harness({ persist: () => fail ? Promise.reject(new Error("disk error")) : Promise.resolve() });
  h.mount(); await flush();
  h.choose("ink"); await flush();
  assert.equal(h.root.dataset.theme, "ink");
  assert.match(h.element("appearance-status").textContent, /未能保存/);
  fail = false;
  h.choose("ink"); await flush();
  assert.equal(h.element("appearance-status").textContent, "外观已保存");
  assert.equal(h.writes.length, 2);
});

test("reduced motion overrides playback without erasing the preference and hidden windows pause", async () => {
  const h = harness({ saved: { theme: "romance", motion: true }, reduced: true });
  h.mount(); await flush();
  assert.equal(h.root.dataset.motion, "off");
  assert.equal(h.element("appearance-motion").checked, true);
  assert.equal(h.element("appearance-motion").disabled, true);
  assert.match(h.element("appearance-motion-help").textContent, /减少动态效果/);
  h.reduce(false);
  assert.equal(h.root.dataset.motion, "on");
  assert.equal(h.element("appearance-motion").disabled, false);
  h.hide(true);
  assert.equal(h.root.dataset.appearancePaused, "true");
  h.hide(false);
  assert.equal(h.root.dataset.appearancePaused, "false");
  assert.deepEqual(h.writes, []);
  assert.deepEqual(JSON.parse(h.values.get(cacheKey)), { theme: "romance", motion: true });
});

test("reset restores the static original theme and the dialog focuses the selected native radio", async () => {
  const h = harness({ saved: { theme: "fresh", motion: true } });
  h.mount(); await flush();
  h.click("open-appearance");
  assert.equal(h.element("appearance-dialog").open, true);
  assert.equal(h.focused(), h.element("theme-fresh"));
  h.click("close-appearance");
  assert.equal(h.element("appearance-dialog").open, false);
  h.click("reset-appearance"); await flush();
  assert.equal(h.root.dataset.theme, "original");
  assert.equal(h.root.dataset.motion, "off");
  assert.equal(h.element("appearance-motion").disabled, true);
  assert.equal(h.element("appearance-motion").checked, false);
  assert.equal(h.meta.content, "dark");
  assert.deepEqual(h.writes, [{ theme: "original", motion: false }]);
});

test("unavailable or corrupt cache and rejected restoration keep the static interface operable", async () => {
  for (const options of [
    { cached: "invalid json" },
    { cached: JSON.stringify({ theme: "unsupported", motion: true }) },
    { cached: JSON.stringify({ theme: "original", motion: true }) },
    { storageFails: true },
  ]) {
    const h = harness({ ...options, restore: () => Promise.reject(new Error("unavailable")) });
    h.mount(); await flush();
    assert.equal(h.root.dataset.theme, "original");
    assert.equal(h.root.dataset.motion, "off");
    assert.match(h.element("appearance-status").textContent, /未能读取/);
    h.choose("genshin"); await flush();
    assert.equal(h.root.dataset.theme, "genshin");
    assert.equal(h.element("appearance-status").textContent, "外观已保存");
  }
});

test("choosing a dynamic skin enables motion, while a saved pause survives restoration", async () => {
  for (const theme of ["starry", "aurora", "silk"]) {
    const h = harness();
    h.mount(); await flush();
    h.choose(theme); await flush();
    assert.equal(h.root.dataset.theme, theme);
    assert.equal(h.root.dataset.motion, "on");
    assert.equal(h.meta.content, "dark");
    assert.deepEqual(h.writes, [{ theme, motion: true }]);
    h.motion(false); await flush();
    h.choose(theme); await flush();
    assert.equal(h.root.dataset.motion, "off", "reselecting the current skin must not cancel a manual pause");

    const restored = harness({ cached: JSON.stringify({ theme, motion: true }), saved: { theme, motion: false } });
    restored.mount(); await flush();
    assert.equal(restored.root.dataset.theme, theme);
    assert.equal(restored.root.dataset.motion, "off");
    assert.equal(restored.element("appearance-motion").checked, false);
    assert.deepEqual(restored.writes, []);
    restored.motion(true); await flush();
    restored.reduce(true);
    assert.equal(restored.root.dataset.motion, "off");
    assert.deepEqual(JSON.parse(restored.values.get(cacheKey)), { theme, motion: true });
    restored.reduce(false);
    assert.equal(restored.root.dataset.motion, "on");
  }
});
