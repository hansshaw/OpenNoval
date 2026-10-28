"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const script = fs.readFileSync(path.join(__dirname, "../../internal/entry/desktopui/static/android.js"), "utf8");

function load(native) {
  const window = native ? { ainovelAndroid: native } : {};
  vm.runInNewContext(script, { window, document: { documentElement: { classList: { add() {} } } }, Uint8Array, setTimeout, btoa: s => Buffer.from(s, "binary").toString("base64") });
  return window;
}

test("Android adapter leaves the desktop bridge untouched", () => {
  assert.equal(load().ainovelDesktop, undefined);
});

test("Android drafts survive a new WebView origin and report storage errors", () => {
  const saved = new Map();
  const native = {
    storageGet: key => saved.get(key) ?? null,
    storageSet: (key, value) => { saved.set(key, value); return ""; },
    storageKeys: () => JSON.stringify([...saved.keys()]),
    storageRemove: key => saved.delete(key), storageClear: () => saved.clear(),
  };
  load(native).localStorage.setItem("draft", "第二章：雨夜");
  const restarted = load(native);
  assert.equal(restarted.localStorage.getItem("draft"), "第二章：雨夜");
  assert.equal(restarted.localStorage.length, 1);
  assert.equal(restarted.localStorage.key(0), "draft");
  assert.equal(restarted.localStorage.key(1), null);
  native.storageSet = () => "存储空间不足";
  assert.throws(() => restarted.localStorage.setItem("draft", "新稿"), /存储空间不足/);
  restarted.localStorage.removeItem("draft");
  assert.equal(restarted.localStorage.getItem("draft"), null);
});

test("Android export chunks preserve binary bytes and waits for system save result", async () => {
  const chunks = [];
  let current, window;
  const native = {
    beginExport: id => { current = id; return ""; },
    appendExport: (id, base64) => { assert.equal(id, current); assert.ok(base64.length <= 65536); chunks.push(Buffer.from(base64, "base64")); return ""; },
    finishExport: (id, filename, format) => {
      assert.equal(filename, "长篇.epub"); assert.equal(format, "epub");
      setTimeout(() => window.ainovelAndroidExportComplete(id, true, null), 0);
      return "";
    }, cancelExport: () => assert.fail("unexpected cancel"),
  };
  window = load(native);
  const source = Uint8Array.from({ length: 175123 }, (_, i) => i % 256);
  const result = await window.ainovelDesktop.saveExport({ filename: "长篇.epub", format: "EPUB", data: source });
  assert.equal(result.saved, true);
  assert.equal(chunks.length, 4);
  assert.deepEqual(Buffer.concat(chunks), Buffer.from(source));
});

test("Android export cancels native temporary data when transfer fails", async () => {
  let canceled;
  const native = { beginExport: () => "", appendExport: () => "存储空间不足", cancelExport: id => { canceled = id; } };
  await assert.rejects(load(native).ainovelDesktop.saveExport({ filename: "a.txt", format: "txt", data: new Uint8Array([1]) }), /存储空间不足/);
  assert.match(canceled, /^export-/);
});

test("Android system picker cancellation resolves without claiming a saved file", async () => {
  let window;
  window = load({ beginExport: () => "", appendExport: () => "", cancelExport() {}, finishExport: id => {
    setTimeout(() => window.ainovelAndroidExportComplete(id, false, null), 0); return "";
  } });
  const result = await window.ainovelDesktop.saveExport({ filename: "a.txt", format: "txt", data: new Uint8Array() });
  assert.equal(result.saved, false);
});
