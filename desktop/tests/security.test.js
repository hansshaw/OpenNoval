"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { isTrustedURL, assertTrustedSender, desktopRequestHeaders } = require("../security");

const baseURL = "http://127.0.0.1:1234";

test("desktop navigation only accepts the backend origin", () => {
  for (const url of [baseURL, `${baseURL}/?project=test`, `${baseURL}/#chapter`]) {
    assert.equal(isTrustedURL(url, baseURL), true, url);
  }
  for (const url of [
    `${baseURL}@external.example`,
    `${baseURL}5/`,
    "http://external.example/",
    "https://127.0.0.1:1234/",
    "file:///etc/passwd",
    "javascript:alert(1)",
    "not a URL",
  ]) {
    assert.equal(isTrustedURL(url, baseURL), false, url);
  }
  assert.equal(isTrustedURL(baseURL, ""), false);
});

test("desktop IPC requires the trusted window's main frame", () => {
  const mainFrame = { url: `${baseURL}/` };
  const webContents = { mainFrame, isDestroyed: () => false };
  const window = { webContents };
  const event = { sender: webContents, senderFrame: mainFrame };
  assert.doesNotThrow(() => assertTrustedSender(event, window, baseURL));
  for (const untrusted of [
    { sender: {}, senderFrame: mainFrame },
    { sender: webContents, senderFrame: { url: baseURL } },
    { sender: webContents, senderFrame: null },
    {},
  ]) {
    assert.throws(() => assertTrustedSender(untrusted, window, baseURL));
  }
  mainFrame.url = `${baseURL}@external.example`;
  assert.throws(() => assertTrustedSender(event, window, baseURL));
  mainFrame.url = baseURL;
  webContents.isDestroyed = () => true;
  assert.throws(() => assertTrustedSender(event, window, baseURL));
  assert.throws(() => assertTrustedSender(event, null, baseURL));
});


test("desktop session capability is attached only to its window and exact backend origin", () => {
  const context = { baseURL, webContentsId: 12, token: "private-session" };
  const details = { webContentsId: 12, url: `${baseURL}/api/events`, requestHeaders: { Accept: "text/event-stream", "x-ainovel-desktop-session": "spoofed" } };
  assert.deepEqual(desktopRequestHeaders(details, context), { Accept: "text/event-stream", "X-AInovel-Desktop-Session": "private-session" });
  assert.equal(details.requestHeaders["x-ainovel-desktop-session"], "spoofed", "request headers were mutated");
  for (const request of [
    { ...details, webContentsId: 13 },
    { ...details, webContentsId: -1 },
    { ...details, url: `${baseURL}5/api/state` },
    { ...details, url: `${baseURL}@external.example/` },
    { ...details, url: "https://external.example/" },
  ]) {
    assert.deepEqual(desktopRequestHeaders(request, context), { Accept: "text/event-stream" });
  }
  assert.deepEqual(desktopRequestHeaders(details, { ...context, token: "" }), { Accept: "text/event-stream" });
});
