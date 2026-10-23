"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { waitForPDFCanvas } = require("../scripts/reader-checks.js");

const pending = { canvas: { width: 400, height: 500 }, note: "正在绘制 PDF 原页…", hidden: false };
const finished = { ...pending, note: "" };
function clock() { let time = 0; return { now: () => time, sleep: async ms => { time += ms; } }; }

test("PDF canvas readiness does not require copying an already rendered compositor surface", async () => {
  let captures = 0;
  const result = await waitForPDFCanvas({ ...clock(), readState: async () => finished, captureFrame: async () => { captures++; throw new Error("UnknownVizError"); } });
  assert.equal(result, finished); assert.equal(captures, 0);
});

test("a transient Xvfb capture failure can recover only after actual canvas readiness", async () => {
  let reads = 0, captures = 0;
  const result = await waitForPDFCanvas({ ...clock(), readState: async () => ++reads < 4 ? pending : finished, captureFrame: async () => { if (++captures === 1) throw new Error("UnknownVizError"); } });
  assert.equal(result, finished); assert.equal(captures, 2); assert.equal(reads, 4);
});

test("persistent capture failure cannot hide a PDF that never renders and retries stay bounded", async () => {
  let captures = 0;
  await assert.rejects(waitForPDFCanvas({ ...clock(), timeoutMs: 1000, readState: async () => pending, captureFrame: async () => { captures++; throw new Error("UnknownVizError"); } }), error => /did not render/.test(error.message) && /"captureFailures":3/.test(error.message) && /UnknownVizError/.test(error.message));
  assert.equal(captures, 3);
});

test("PDF failure and unexpected capture errors remain fatal with useful context", async () => {
  await assert.rejects(waitForPDFCanvas({ ...clock(), readState: async () => ({ ...pending, note: "原页读取失败：文件损坏" }), captureFrame: async () => {} }), /文件损坏/);
  await assert.rejects(waitForPDFCanvas({ ...clock(), readState: async () => pending, captureFrame: async () => { throw new Error("Renderer crashed"); } }), /PDF compositor capture failed: Renderer crashed/);
});
