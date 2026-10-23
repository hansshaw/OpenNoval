"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { openDatabase } = require("../database");
const { readAppearance, registerAppearanceIPC } = require("../appearance-preferences");

function harness(database) {
  const handlers = new Map();
  const baseURL = "http://127.0.0.1:4321";
  const mainFrame = { url: baseURL };
  const webContents = { mainFrame, isDestroyed: () => false };
  registerAppearanceIPC({
    ipcMain: { handle: (name, handler) => handlers.set(name, handler) },
    getWindow: () => ({ webContents }), getBaseURL: () => baseURL, database,
  });
  const event = { sender: webContents, senderFrame: mainFrame };
  return { event, handlers, call: (name, value) => handlers.get(`desktop:${name}`)(event, value) };
}

test("appearance survives a database reopen and leaves unrelated settings intact", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-appearance-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const file = path.join(root, "desktop.sqlite");
  let database = openDatabase(file);
  try {
    database.setSetting("workspaceDir", "/books/original");
    const h = harness(database);
    assert.deepEqual(h.call("get-appearance"), { theme: "original", motion: false });
    for (const theme of ["ink", "fresh", "romance", "genshin", "original"]) {
      assert.deepEqual(h.call("set-appearance", { theme, motion: true, workspaceDir: "/untrusted" }), { theme, motion: true });
    }
    h.call("set-appearance", { theme: "ink", motion: false });
    database.close();
    database = openDatabase(file);
    assert.deepEqual(readAppearance(database), { theme: "ink", motion: false });
    assert.equal(database.getSetting("workspaceDir"), "/books/original");
  } finally { database.close(); }
});

test("appearance rejects invalid payloads and untrusted IPC without writing", () => {
  const values = new Map();
  const database = { getSetting: (key) => values.get(key) ?? null, setSetting: (key, value) => values.set(key, value) };
  const h = harness(database);
  h.call("set-appearance", { theme: "fresh", motion: true });
  for (const payload of [null, [], "ink", {}, { theme: "unknown", motion: true }, { theme: "ink", motion: 1 }, { theme: "ink", motion: "false" }]) {
    assert.throws(() => h.call("set-appearance", payload), /界面偏好无效/);
  }
  const alien = { sender: h.event.sender, senderFrame: { url: h.event.senderFrame.url } };
  assert.throws(() => h.handlers.get("desktop:get-appearance")(alien), /不可信/);
  assert.throws(() => h.handlers.get("desktop:set-appearance")(alien, { theme: "original", motion: false }), /不可信/);
  assert.deepEqual(h.call("get-appearance"), { theme: "fresh", motion: true });
  assert.deepEqual([...values.keys()], ["appearance"]);
});

test("corrupt or obsolete appearance data falls back to the static original theme", () => {
  for (const stored of [null, "invalid json", "null", '{"theme":"removed","motion":true}', '{"theme":"ink","motion":"true"}']) {
    assert.deepEqual(readAppearance({ getSetting: () => stored }), { theme: "original", motion: false });
  }
});
