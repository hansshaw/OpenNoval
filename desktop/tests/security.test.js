"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { isTrustedURL, assertTrustedSender } = require("../security");

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
