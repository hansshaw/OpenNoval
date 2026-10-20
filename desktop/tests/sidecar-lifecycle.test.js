"use strict";

const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const test = require("node:test");
const { stopSidecarProcess } = require("../sidecar-lifecycle");

function childProcess() {
  const child = new EventEmitter();
  child.exitCode = null;
  child.signalCode = null;
  child.signals = [];
  child.exit = (code = 0, signal = null) => {
    child.exitCode = code;
    child.signalCode = signal;
    child.emit("exit", code, signal);
  };
  child.kill = (signal) => {
    child.signals.push(signal);
    child.exit(null, signal);
  };
  return child;
}

test("sidecar exit uses authenticated HTTP and waits for normal process completion", async () => {
  const child = childProcess();
  const result = await stopSidecarProcess({
    child, baseURL: "http://127.0.0.1:1234", token: "private-main-process-capability", timeoutMs: 1000,
    fetchImpl: async (url, options) => {
      assert.equal(url, "http://127.0.0.1:1234/api/desktop/shutdown");
      assert.equal(options.method, "POST");
      assert.equal(options.headers["X-AInovel-Desktop-Token"], "private-main-process-capability");
      assert.equal(options.body, "{}");
      setTimeout(() => child.exit(0), 10);
      return new Response("{}", { status: 202 });
    },
  });
  assert.deepEqual(child.signals, []);
  assert.equal(result.method, "http");
  assert.equal(result.graceful, true);
  assert.equal(result.forced, false);
  assert.equal(child.listenerCount("exit"), 0);
});

test("sidecar is force killed only after its graceful exit deadline", async () => {
  const child = childProcess();
  const started = Date.now();
  const result = await stopSidecarProcess({
    child, baseURL: "http://127.0.0.1:1234", token: "token", timeoutMs: 20, forceTimeoutMs: 100,
    fetchImpl: async () => new Response("{}", { status: 202 }),
  });
  assert.ok(Date.now() - started >= 15);
  assert.deepEqual(child.signals, ["SIGKILL"]);
  assert.equal(result.graceful, false);
  assert.equal(result.forced, true);
});

test("failed authentication cannot be reported as a graceful sidecar exit", async () => {
  const child = childProcess();
  const result = await stopSidecarProcess({
    child, baseURL: "http://127.0.0.1:1234", token: "wrong", timeoutMs: 5,
    fetchImpl: async () => new Response("{}", { status: 403 }),
  });
  assert.equal(result.method, "fallback");
  assert.equal(result.graceful, false);
  assert.equal(result.forced, true);
});
