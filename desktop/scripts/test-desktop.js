"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { spawn, spawnSync } = require("node:child_process");
const { DatabaseSync } = require("node:sqlite");
const { createTestEnvironment } = require("../test-environment");

const desktopDir = path.resolve(__dirname, "..");
const mode = process.argv.includes("--regression") ? "regression" : "smoke";
const packaged = process.argv.includes("--packaged");
const suite = `${mode}${packaged ? "-packaged" : ""}`;
function electronExecutable() {
  if (!packaged) return require("electron");
  const metadata = require("../package.json");
  const platform = { win32: "win", linux: "linux" }[process.platform];
  if (!platform) throw new Error("Packaged acceptance supports Windows and Linux only");
  const archSuffix = process.arch === "x64" ? "" : `-${process.arch}`;
  const executable = process.platform === "win32"
    ? `${metadata.build.productName}.exe` : metadata.name;
  return path.join(desktopDir, metadata.build.directories.output, `${platform}${archSuffix}-unpacked`, executable);
}
const reportIndex = process.argv.indexOf("--report");
if (reportIndex !== -1 && !process.argv[reportIndex + 1]) throw new Error("--report requires a file path");
const reportPath = path.resolve(reportIndex !== -1
  ? process.argv[reportIndex + 1] : path.join(desktopDir, "dist", "reports", `${suite}.json`));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-desktop-test-"));
const environment = createTestEnvironment(root);
const started = Date.now();
const modelRequests = [];
let truncatedCoCreate = false;
let pendingStartupRequests = 0;
const report = {
  schemaVersion: 1, suite: `desktop-${suite}`, packaged, startedAt: new Date().toISOString(),
  ok: false, failures: 0, phases: [],
  coverage: ["Electron window and preload", "Go sidecar HTTP", "SQLite", "exit cleanup"],
  limitations: ["No live model or paid API is used; mock token counts are deterministic fixtures."],
};
let electronChild;

function createMockModel() {
  return http.createServer(async (request, response) => {
    const start = Date.now();
    if (request.method === "GET" && request.url === "/__test/status") {
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ pendingStartupRequests }));
      return;
    }
    if (request.method === "GET" && request.url === "/v1/models") {
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ data: [
        { id: "mock-fast", context_length: 32000, max_output_tokens: 12000 },
        { id: "mock-careful", context_length: 128000, max_output_tokens: 16000 },
      ] }));
      return;
    }
    let body = "";
    for await (const chunk of request) body += chunk;
    let payload;
    try { payload = JSON.parse(body); } catch (_) { response.writeHead(400).end(); return; }
    if (request.method !== "POST" || request.url !== "/v1/chat/completions" ||
        !["mock-fast", "mock-careful"].includes(payload.model)) {
      response.writeHead(404).end("Unexpected mock endpoint/model");
      modelRequests.push({ model: payload.model, ok: false, durationMs: Date.now() - start });
      return;
    }
    if (request.headers.authorization !== "Bearer ainovel-local-no-key") {
      response.writeHead(401).end("Expected only the non-secret local compatibility credential");
      modelRequests.push({ model: payload.model, ok: false, durationMs: Date.now() - start });
      return;
    }
    const messages = JSON.stringify(payload.messages || []);
    if (!messages.includes("Reply OK.") && !messages.includes("小说共创助手")) {
      // Confirmation starts the real rule-generation path. Hold that request
      // until the desktop pause action cancels it; never fabricate planning.
      pendingStartupRequests++;
      response.once("close", () => {
        pendingStartupRequests--;
        modelRequests.push({ model: payload.model, ok: true, outcome: "startup-cancelled-by-test", durationMs: Date.now() - start });
      });
      return;
    }
    let content = payload.stream
      ? "<reply>故事信息已经齐备，可以确认并生成规划。</reply><draft>## 作品定位\n月球图书馆短篇，第三人称，一章。\n## 核心故事\n林舟在闭馆前找回旧信，与地球亲人和解，温暖结局。\n## 世界与规则\n近未来月球基地，信件保存在封闭书库。\n## 边界与禁忌\n不增加支线，不改写既有设定。</draft><ready>true</ready><suggestions></suggestions>"
      : "OK";
    // Exercise the real retry path once. A truncated XML response must never
    // become the confirmed draft, and the next request must respect the model.
    const finishReason = payload.stream && !truncatedCoCreate ? "length" : "stop";
    if (finishReason === "length") {
      truncatedCoCreate = true;
      content = "<reply>正在整理月球图书馆的创作要求。</reply><draft>## 未完成草稿";
    }
    const usage = { prompt_tokens: 17, completion_tokens: 31, total_tokens: 48 };
    const base = { id: `desktop-mock-${modelRequests.length}`, created: 1, model: payload.model };
    if (payload.stream) {
      response.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
      response.write(`data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: { role: "assistant", content }, finish_reason: null }] })}\n\n`);
      response.write(`data: ${JSON.stringify({ ...base, object: "chat.completion.chunk", choices: [{ index: 0, delta: {}, finish_reason: finishReason }], usage })}\n\n`);
      response.end("data: [DONE]\n\n");
    } else {
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ ...base, object: "chat.completion", choices: [{ index: 0, message: { role: "assistant", content }, finish_reason: "stop" }], usage }));
    }
    modelRequests.push({ model: payload.model, stream: Boolean(payload.stream), maxTokens: payload.max_tokens, finishReason, ok: true, usage, durationMs: Date.now() - start });
  });
}

async function runElectron(phase, mockURL) {
  const args = [mode === "smoke" ? "--smoke-test" : "--desktop-regression"];
  if (!packaged) args.unshift(desktopDir);
  if (process.argv.includes("--no-sandbox")) args.push("--no-sandbox");
  // Opt-in screenshot runs need compositor frames even when the test window
  // is unfocused. X11 avoids occluded Wayland capture stalls on Linux desktops.
  if (process.env.AINOVEL_APPEARANCE_SCREENSHOTS && process.platform === "linux") args.push("--ozone-platform=x11");
  let stdout = "";
  let stderr = "";
  const exit = await new Promise((resolve, reject) => {
    electronChild = spawn(electronExecutable(), args, {
      cwd: desktopDir,
      env: { ...environment.env, AINOVEL_DESKTOP_TEST_PHASE: phase, AINOVEL_DESKTOP_MOCK_URL: mockURL },
      stdio: ["ignore", "pipe", "pipe"], windowsHide: true,
    });
    const timer = setTimeout(() => {
      electronChild.kill("SIGKILL");
      reject(new Error(`Electron ${phase} timed out after 180 seconds\n${stderr.slice(-8000)}`));
    }, 180000);
    electronChild.stdout.on("data", (chunk) => {
      stdout += chunk;
      if (process.env.AINOVEL_DESKTOP_TEST_TRACE) {
        for (const line of chunk.toString().split(/\r?\n/)) {
          if (line.startsWith("AINOVEL_DESKTOP_CHECK ")) process.stdout.write(line + "\n");
        }
      }
    });
    electronChild.stderr.on("data", (chunk) => { stderr += chunk; });
    electronChild.once("error", (error) => { clearTimeout(timer); reject(error); });
    electronChild.once("exit", (code, signal) => { clearTimeout(timer); resolve({ code, signal }); });
  });
  electronChild = null;
  assert.equal(exit.code, 0, `Electron ${phase} exited ${JSON.stringify(exit)}\n${stderr.slice(-12000)}\n${stdout.slice(-4000)}`);
  const resultLine = stdout.split(/\r?\n/).find((line) => line.startsWith("AINOVEL_DESKTOP_TEST_RESULT "));
  assert.ok(resultLine, `Electron did not report a completed check\n${stderr}`);
  const result = JSON.parse(resultLine.slice("AINOVEL_DESKTOP_TEST_RESULT ".length));
  assert.equal(result.ok, true);
  assert.equal(result.sidecarStopped, true);
  assert.equal(result.sidecarShutdown?.method, "http");
  assert.equal(result.sidecarShutdown?.graceful, true);
  assert.equal(result.sidecarShutdown?.forced, false);
  try {
    process.kill(result.sidecarPID, 0);
    throw new Error(`Sidecar ${result.sidecarPID} is still running after Electron exit`);
  } catch (error) { if (error.code !== "ESRCH") throw error; }
  await assert.rejects(fetch(`${result.url}/api/state`, { signal: AbortSignal.timeout(2000) }), "Sidecar listener remained open");
  const sqlite = new DatabaseSync(result.sqlitePath);
  try {
    assert.equal(sqlite.prepare("PRAGMA quick_check").get().quick_check, "ok");
    assert.ok(sqlite.prepare("SELECT value FROM desktop_settings WHERE key = 'lastCleanExitAt'").get());
  } finally { sqlite.close(); }
  result.steps.push({ name: "process-listener-and-sqlite-clean-exit", ok: true });
  report.phases.push(result);
}

function cleanupProcesses() {
  if (electronChild) {
    if (process.platform === "win32") spawnSync("taskkill", ["/PID", String(electronChild.pid), "/T", "/F"], { stdio: "ignore" });
    else electronChild.kill("SIGKILL");
  }
  try {
    const { pid } = JSON.parse(fs.readFileSync(path.join(root, "sidecar.json"), "utf8"));
    process.kill(pid, "SIGKILL");
  } catch (_) { /* A clean shutdown has already reaped the sidecar. */ }
}

async function main() {
  const mock = mode === "regression" ? createMockModel() : null;
  try {
    let mockURL = "";
    if (mock) {
      await new Promise((resolve) => mock.listen(0, "127.0.0.1", resolve));
      mockURL = `http://127.0.0.1:${mock.address().port}/v1`;
      report.coverage.push("empty project center and deletion of the last project", "project copy and isolation", "multiple model libraries and selection", "streaming co-create and confirmation", "project creation", "last project and empty selection restart persistence", "readable planning and review records", "native fullscreen text and PDF reading");
      report.limitations.push("Confirmation is followed by cancellation of pending startup. Planning, writing, chapter feedback/acceptance and export are not exercised by this desktop scenario; use Go workflow tests as complementary checks.");
    }
    await runElectron("initial", mockURL);
    if (mode === "regression") {
      await runElectron("restart", mockURL);
      assert.ok(modelRequests.some((request) => request.stream));
      assert.ok(modelRequests.some((request) => request.model === "mock-careful"));
      const coCreateRequests = modelRequests.filter(request => request.stream);
      assert.equal(coCreateRequests[0].finishReason, "length");
      assert.equal(coCreateRequests[1].finishReason, "stop");
      assert.ok(coCreateRequests[0].maxTokens > 2048, "Co-create still uses the obsolete 2048-token cap");
      assert.ok(coCreateRequests[1].maxTokens >= coCreateRequests[0].maxTokens);
      assert.ok(coCreateRequests[1].maxTokens <= 12000, "Recovery exceeded the discovered model output limit");
      const cancellationDeadline = Date.now() + 3000;
      while (pendingStartupRequests > 0 && Date.now() < cancellationDeadline) {
        await new Promise((resolve) => setTimeout(resolve, 20));
      }
      assert.equal(pendingStartupRequests, 0, "graceful desktop shutdown left a model request active");
      assert.equal(modelRequests.filter((request) => request.outcome === "startup-cancelled-by-test").length, 2,
        "expected both explicit pause and application shutdown to cancel their active model requests");
      await runElectron("empty-prepare", mockURL);
      await runElectron("empty-restart", mockURL);
    }
    report.ok = true;
  } catch (error) {
    report.failures++;
    report.error = error.stack || String(error);
    process.stderr.write(`${report.error}\n`);
    process.exitCode = 1;
  } finally {
    cleanupProcesses();
    if (mock) await new Promise((resolve) => mock.close(resolve));
    report.durationMs = Date.now() - started;
    report.model = { implementation: "deterministic-local-openai-mock", version: 1, requests: modelRequests, failedRequests: modelRequests.filter((request) => !request.ok).length };
    fs.mkdirSync(path.dirname(reportPath), { recursive: true });
    fs.writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`);
    fs.rmSync(root, { recursive: true, force: true, maxRetries: 4, retryDelay: 200 });
    process.stdout.write(`${report.ok ? "PASS" : "FAIL"} desktop ${suite}; report: ${reportPath}\n`);
  }
}

main().catch((error) => { cleanupProcesses(); process.stderr.write(`${error.stack}\n`); process.exitCode = 1; });
