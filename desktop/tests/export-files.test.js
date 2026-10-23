"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { registerExportIPC } = require("../export-files");

function harness(t, { canceled = false, isSmokeTest = false } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-export-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const mainFrame = { url: "http://127.0.0.1:12345/" };
  const webContents = { mainFrame, isDestroyed: () => false };
  const window = { webContents }, dialogs = [];
  let handler;
  registerExportIPC({
    ipcMain: { handle: (_name, value) => { handler = value; } },
    getWindow: () => window, getBaseURL: () => mainFrame.url, getDocumentsPath: () => root, isSmokeTest,
    dialog: { showSaveDialog: async (owner, options) => {
      assert.equal(owner, window); dialogs.push(options);
      return { canceled, filePath: options.defaultPath };
    } },
  });
  return { root, dialogs, call: (payload, event = { sender: webContents, senderFrame: mainFrame }) => handler(event, payload) };
}

test("chapter image, audio and video downloads save their exact bytes with matching file filters", async t => {
  const h = harness(t);
  for (const format of ["png", "jpg", "jpeg", "gif", "webp", "mp3", "mp4", "zip", "txt", "epub"]) {
    const data = new Uint8Array([0, 1, 137, 255, 13, 10]);
    const result = await h.call({ format, filename: `chapter-2.${format}`, data });
    assert.equal(result.saved, true);
    assert.deepEqual(fs.readFileSync(result.path), Buffer.from(data));
    assert.deepEqual(h.dialogs.at(-1).filters[0].extensions, [format]);
    assert.equal(path.extname(result.path), `.${format}`);
  }
});

test("unsupported files, invalid data and foreign frames cannot prompt for or write exports", async t => {
  const h = harness(t);
  await assert.rejects(h.call({ format: "exe", data: [1] }), /不支持/);
  await assert.rejects(h.call({ format: "mp3", data: "invalid" }), /内容无效/);
  await assert.rejects(h.call({ format: "mp4", data: [1] }, {}), /不可信/);
  assert.equal(h.dialogs.length, 0);
  assert.deepEqual(fs.readdirSync(h.root), []);
});

test("cancelled and test-mode media saves do not write files", async t => {
  const h = harness(t, { canceled: true });
  assert.deepEqual(await h.call({ format: "mp3", data: [1] }), { saved: false });
  assert.deepEqual(fs.readdirSync(h.root), []);
  const smoke = harness(t, { isSmokeTest: true });
  assert.deepEqual(await smoke.call({ format: "mp4", data: [1] }), { saved: false, smokeTest: true });
  await assert.rejects(smoke.call({ format: "exe", data: [1] }), /不支持/);
  assert.equal(smoke.dialogs.length, 0);
});
