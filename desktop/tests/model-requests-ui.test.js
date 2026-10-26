"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div") {
    this.tag = tag;
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.listeners = new Map();
    this.className = "";
    this.value = "";
    this.textContent = "";
    this.open = false;
    this.scrollTop = 0;
    this.scrollLeft = 0;
    this.classList = {
      toggle: (name, active) => {
        const names = this.className.split(" ").filter((value) => value && value !== name);
        if (active) names.push(name);
        this.className = names.join(" ");
      },
    };
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  setAttribute(key, value) { this.attributes[key] = value; }
  addEventListener(key, listener) { this.listeners.set(key, listener); }
  focus() {}
}

const record = (id, extra = {}) => ({
  id, provider: "local-gateway", model: "writing-model", agent: "architect",
  endpoint: "https://example.com/v1/chat/completions", stream: true,
  startedAt: "2026-10-22T12:00:00+08:00", finishedAt: "2026-10-22T12:00:02+08:00",
  firstChunkAt: "2026-10-22T12:00:00.200+08:00", lastChunkAt: "2026-10-22T12:00:01+08:00",
  durationMs: 2000, firstChunkMs: 200, idleTimeoutMs: 300000, maxOutputTokens: 16000,
  messageCount: 3, toolCount: 8, chunks: 12, outputBytes: 4096,
  inputTokens: 600, outputTokens: 1200, stopReason: "stop", status: "success",
  request: { messages: [{ role: "user", content: "请规划故事" }] },
  response: { content: "模型响应" }, ...extra,
});

function harness() {
  let now = Date.parse("2026-10-22T12:00:10+08:00");
  const nodes = new Map();
  const $ = (id) => {
    if (!nodes.has(id)) nodes.set(id, new Element());
    return nodes.get(id);
  };
  const browser = {
    window: {}, URL, URLSearchParams,
    Date: class extends Date {
      constructor(...args) { super(...(args.length ? args : [now])); }
      static now() { return now; }
    },
    document: { querySelector: $, querySelectorAll: () => [], createElement: (tag) => new Element(tag) },
  };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/readable-data.js"), "utf8"), browser);
  const source = fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/app.js"), "utf8");
  vm.runInNewContext(source.replace("  init();", "  window.test = { createModelRequestsPanel };"), browser);
  let context = { directory: "/books/one", epoch: 1 };
  let items = [];
  let requestOverride = null;
  const calls = [];
  const panel = browser.window.test.createModelRequestsPanel({
    getContext: () => ({ ...context }),
    request: async (route) => {
      calls.push(route);
      if (requestOverride) return requestOverride(route);
      if (route.startsWith("/api/model-requests?")) return { directory: context.directory, requests: items };
      const id = decodeURIComponent(route.slice("/api/model-requests/".length));
      return items.find((item) => item.id === id) || record(id);
    },
  });
  const all = (node) => [node, ...node.children.flatMap(all)];
  const find = (className) => all($("#model-request-detail")).find((node) => node.className.split(" ").includes(className));
  const text = (node) => [node.textContent, ...node.children.map(text)].join(" ");
  return {
    $, panel, calls, find, text,
    setItems: (value) => { items = value; },
    advanceTime: (ms) => { now += ms; },
    setContext: (value) => { context = value; },
    respond: (fn) => { requestOverride = fn; },
    fields: () => find("model-request-fields").children.map((node) => [node.children[0].textContent, node.children[1].textContent]),
    json: () => all($("#model-request-detail")).filter((node) => node.tag === "details"),
  };
}

test("request empty, loading and failed states are explicit and do not clear event activity", async () => {
  const h = harness();
  h.$("#activity-list").textContent = "已保存活动事件";
  let finish;
  h.respond(() => new Promise((resolve) => { finish = resolve; }));
  const pending = h.panel.load();
  assert.match(h.$("#model-requests-message").textContent, /正在读取/);
  finish({ directory: "/books/one", requests: [] });
  await pending;
  assert.match(h.text(h.$("#model-request-list")), /暂无模型请求/);
  assert.equal(h.$("#activity-list").textContent, "已保存活动事件");
  h.respond(() => { throw new Error("network offline"); });
  await h.panel.load({ quiet: true });
  assert.match(h.$("#model-requests-message").textContent, /读取失败.*network offline/);
  h.setContext({ directory: "", epoch: 2 });
  const before = h.calls.length;
  await h.panel.load();
  assert.equal(h.calls.length, before);
  assert.match(h.$("#model-requests-message").textContent, /选择一个创作项目/);
});

test("detail shows stream timing, limits and stop reason and renders JSON text with credentials hidden", async () => {
  const h = harness();
  h.setItems([record("request-one", {
    endpoint: "https://user:password@example.com/v1?api_key=secret-value",
    stopReason: "length", error: "unexpected EOF", truncated: true,
    request: { headers: { Authorization: "Bearer private-value", "X-Api-Key": "private-key" }, nested: { api_key: "private-nested" }, messages: [{ content: "<img src=x onerror=alert(1)>" }] },
  })]);
  await h.panel.load();
  const fields = new Map(h.fields());
  assert.equal(fields.get("输出方式"), "流式输出");
  assert.equal(fields.get("首个事件耗时"), "200 毫秒");
  assert.equal(fields.get("读取等待上限"), "300.0 秒");
  assert.equal(fields.get("最大输出 Token"), "16,000");
  assert.equal(fields.get("Token 用量（输入 / 输出）"), "600 / 1,200");
  assert.equal(fields.get("停止原因"), "length");
  const detailText = h.text(h.$("#model-request-detail"));
  assert.match(detailText, /传输完成.*length.*模型输出上限/);
  assert.match(detailText, /unexpected EOF/);
  assert.match(detailText, /预览已截断/);
  assert.equal(h.find("model-request-limit").className.includes("hidden"), false);
  assert.doesNotMatch(detailText, /private-value|private-key|private-nested|secret-value|user:password/);
  const requestPreview = h.json()[0].children.find((node) => node.tag === "pre");
  assert.match(requestPreview.textContent, /<img src=x onerror=alert\(1\)>/);
  assert.equal(requestPreview.children.length, 0);
});

test("zero Go timestamps remain missing and cannot produce historical dates or enormous idle times", async () => {
  const h = harness();
  const zero = "0001-01-01T00:00:00Z";
  h.setItems([record("waiting", { startedAt: zero, firstChunkAt: zero, lastChunkAt: zero, finishedAt: zero, status: "running" })]);
  await h.panel.load();
  const fields = new Map(h.fields());
  assert.equal(fields.get("开始时间"), "—");
  assert.equal(fields.get("结束时间"), "等待完成");
  assert.equal(fields.get("首个事件耗时"), "尚未收到首个事件");
  assert.equal(fields.get("最后接收后等待"), "—");
});

test("waiting streams update total duration on polling before the first event without persisted heartbeats", async () => {
  const h = harness();
  const waiting = record("waiting", {
    durationMs: 0, chunks: 0, firstChunkAt: "0001-01-01T00:00:00Z",
    lastChunkAt: "0001-01-01T00:00:00Z", finishedAt: "0001-01-01T00:00:00Z", status: "running",
  });
  h.setItems([waiting]);
  await h.panel.load();
  assert.equal(new Map(h.fields()).get("总耗时"), "10.0 秒");
  assert.equal(new Map(h.fields()).get("首个事件耗时"), "尚未收到首个事件");
  assert.match(h.text(h.$("#model-request-list")), /10\.0 秒.*0 个流式事件/);
  h.advanceTime(3500);
  await h.panel.load({ quiet: true });
  assert.equal(new Map(h.fields()).get("总耗时"), "13.5 秒");
  assert.match(h.text(h.$("#model-request-list")), /13\.5 秒.*0 个流式事件/);
  h.setItems([{ ...waiting, durationMs: 20000 }]);
  await h.panel.load({ quiet: true });
  assert.equal(new Map(h.fields()).get("总耗时"), "20.0 秒");
  h.setItems([{ ...waiting, status: "error", durationMs: 9000 }]);
  h.advanceTime(5000);
  await h.panel.load({ quiet: true });
  assert.equal(new Map(h.fields()).get("总耗时"), "9.0 秒");
  assert.match(h.text(h.$("#model-request-list")), /9\.0 秒.*0 个流式事件/);
});

test("polling keeps selection, JSON disclosure nodes, expansion and scroll while updating the response", async () => {
  const h = harness();
  h.setItems([record("selected")]);
  await h.panel.load();
  const disclosure = h.json()[1];
  const preview = disclosure.children[1];
  disclosure.open = true;
  preview.scrollTop = 160;
  preview.scrollLeft = 30;
  h.$("#model-request-list").scrollTop = 90;
  h.setItems([record("newer"), record("selected", { response: { content: "已更新响应" }, durationMs: 3000 })]);
  await h.panel.load({ quiet: true });
  assert.equal(h.json()[1], disclosure);
  assert.equal(disclosure.open, true);
  assert.equal(disclosure.children[1], preview);
  assert.equal(preview.scrollTop, 160);
  assert.equal(preview.scrollLeft, 30);
  assert.match(preview.textContent, /已更新响应/);
  assert.equal(h.$("#model-request-list").scrollTop, 90);
  assert.match(h.text(h.$("#model-request-detail")), /请求 ID：selected/);
  assert.equal(h.$("#model-request-list").children[1].attributes["aria-pressed"], "true");
});

test("status filters are sent to the server without replacing the selected request", async () => {
  const h = harness();
  h.setItems([record("selected")]);
  await h.panel.load();
  h.$("#model-request-status").value = "error";
  h.setItems([record("failed", { status: "error" })]);
  await h.panel.load();
  assert.equal(h.calls.includes("/api/model-requests?limit=100&status=error"), true);
  assert.match(h.text(h.$("#model-request-detail")), /请求 ID：selected/);
  assert.match(h.$("#model-request-detail-message").textContent, /不在当前筛选/);
});

test("a late detail response cannot overwrite a newly selected request", async () => {
  const h = harness();
  h.setItems([record("first"), record("second"), record("third")]);
  await h.panel.load();
  let finish;
  h.respond((route) => route.endsWith("/second")
    ? new Promise((resolve) => { finish = resolve; }) : record("third"));
  const pending = h.panel.select("second");
  await h.panel.select("third");
  finish(record("second", { error: "late stale detail" }));
  await pending;
  assert.match(h.text(h.$("#model-request-detail")), /请求 ID：third/);
  assert.doesNotMatch(h.text(h.$("#model-request-detail")), /late stale detail/);
});

test("project changes clear old details immediately and reject late detail responses", async () => {
  const h = harness();
  h.setItems([record("old")]);
  await h.panel.load();
  let finish;
  h.respond(() => new Promise((resolve) => { finish = resolve; }));
  const oldDetail = h.panel.select("old");
  h.setContext({ directory: "/books/two", epoch: 2 });
  h.panel.reset();
  assert.doesNotMatch(h.text(h.$("#model-request-detail")), /请求 ID：old/);
  h.respond((route) => route.includes("?")
    ? { directory: "/books/two", requests: [record("new")] } : record("new"));
  await h.panel.load();
  finish(record("old", { error: "another project secret" }));
  await oldDetail;
  assert.match(h.text(h.$("#model-request-detail")), /请求 ID：new/);
  assert.doesNotMatch(h.text(h.$("#model-request-detail")), /another project secret/);
});

test("late list responses and responses for a different directory never cross project boundaries", async () => {
  const h = harness();
  let finish;
  h.respond(() => new Promise((resolve) => { finish = resolve; }));
  const oldList = h.panel.load();
  h.setContext({ directory: "/books/two", epoch: 2 });
  h.panel.reset();
  h.respond((route) => route.includes("?")
    ? { directory: "/books/two", requests: [record("new")] } : record("new"));
  await h.panel.load();
  finish({ directory: "/books/one", requests: [record("old")] });
  await oldList;
  assert.doesNotMatch(h.text(h.$("#model-request-list")), /old/);
  assert.match(h.text(h.$("#model-request-detail")), /请求 ID：new/);
  h.respond(() => ({ directory: "/books/one", requests: [record("wrong-project")] }));
  await h.panel.load();
  assert.match(h.$("#model-requests-message").textContent, /项目正在切换/);
  assert.doesNotMatch(h.text(h.$("#model-request-list")), /wrong-project/);
});

test("detail read failures remain visible and the selected record can be refreshed", async () => {
  const h = harness();
  h.setItems([record("first")]);
  h.respond((route) => {
    if (route.includes("?")) return { directory: "/books/one", requests: [record("first")] };
    throw new Error("disk unavailable");
  });
  await h.panel.load();
  assert.match(h.$("#model-request-detail-message").textContent, /详情读取失败.*disk unavailable/);
  h.respond((route) => route.includes("?") ? { directory: "/books/one", requests: [record("first")] } : record("first"));
  await h.panel.load();
  assert.equal(h.$("#model-request-detail-message").textContent, "");
  assert.match(h.text(h.$("#model-request-detail")), /请求 ID：first/);
});
