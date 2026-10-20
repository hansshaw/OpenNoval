"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div") {
    this.tag = tag;
    this.children = [];
    this.listeners = new Map();
    this.dataset = {};
    this.textContent = "";
    this.value = "";
    this.disabled = false;
    this.open = false;
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren() { this.children = []; }
  querySelectorAll(selector) {
    return this.children.flatMap((child) => [
      ...(selector === "details[open]" && child.tag === "details" && child.open ? [child] : []),
      ...child.querySelectorAll(selector),
    ]);
  }
  addEventListener(name, listener) {
    this.listeners.set(name, [...(this.listeners.get(name) || []), listener]);
  }
  async fire(name) {
    for (const listener of this.listeners.get(name) || []) await listener({ preventDefault() {} });
  }
}

function harness() {
  const nodes = new Map();
  const $ = (id) => {
    if (!nodes.has(id)) nodes.set(id, new Element());
    return nodes.get(id);
  };
  const browser = { window: {}, document: { getElementById: $ } };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/entry/web/static/jobs.js"), "utf8"), browser);
  const calls = [], toasts = [];
  let limits = { maxConcurrent: 2, providerLimit: 1, providerLimits: { alpha: 3 }, editable: true, maxSupported: 4096 };
  let providers = [{ name: "alpha" }, { name: "beta" }];
  let getFailure = null, postFailure = null, delayedRead = null;
  const jobs = [{ id: "active", project_id: "/books/one", status: "running", provider_id: "alpha", kind: "batch" }];
  const el = (tag, className, text) => Object.assign(new Element(tag), { className, textContent: text ?? "" });
  const panel = browser.window.NovelJobs.create({
    api: async (route, options) => {
      calls.push({ route, options: options ? JSON.parse(JSON.stringify(options)) : undefined });
      if (options?.method === "POST") {
        if (postFailure) throw postFailure;
        limits = { ...options.body, editable: true, maxSupported: 4096 };
        return limits;
      }
      if (getFailure) throw getFailure;
      if (delayedRead) return delayedRead;
      return route === "/api/jobs/limits" ? limits : { jobs, events: {}, limits };
    },
    el, button: (text, className) => el("button", className, text),
    getProjects: () => [], getConfig: () => ({ providers }),
    toast: (message) => toasts.push(message), refresh: async () => {},
  });
  return {
    $, panel, calls, toasts,
    inputs: () => $("job-provider-limits").children.filter((node) => node.tag === "label").map((label) => label.children[0]),
    setLimits: (value) => { limits = { ...limits, ...value }; },
    setProviders: (value) => { providers = value; },
    failGet: (error) => { getFailure = error; }, failPost: (error) => { postFailure = error; },
    delayRead: (promise) => { delayedRead = promise; },
    edit: async (total, perProvider) => {
      $("job-max-concurrent").value = String(total);
      $("job-provider-limit").value = String(perProvider);
      await $("job-limits-form").fire("input");
    },
  };
}

test("concurrency form saves high limits and omits blank per-connection overrides while jobs are running", async () => {
  const h = harness();
  await h.panel.load();
  assert.equal(h.$("job-max-concurrent").value, 2);
  assert.equal(h.$("job-provider-limit").value, 1);
  assert.equal(h.$("job-max-concurrent").max, 4096);
  assert.deepEqual(h.inputs().map((node) => [node.dataset.limitProvider, node.value]), [["alpha", 3], ["beta", ""]]);
  await h.edit(4096, 1024);
  h.inputs()[0].value = "2048";
  h.inputs()[1].value = "";
  await h.$("job-limits-form").fire("submit");
  const post = h.calls.find((call) => call.options?.method === "POST");
  assert.deepEqual(post, { route: "/api/jobs/limits", options: { method: "POST", body: { maxConcurrent: 4096, providerLimit: 1024, providerLimits: { alpha: 2048 } } } });
  assert.match(h.$("job-limits-status").textContent, /已保存/);
  assert.equal(h.toasts.length, 1);
  assert.equal(h.$("save-job-limits").disabled, false);
});

test("task polling preserves unsaved fields, provider inputs and expanded settings; reset uses latest server values", async () => {
  const h = harness();
  await h.panel.load();
  h.$("job-limits-settings").open = true;
  await h.edit(500, 200);
  const providerInput = h.inputs()[0];
  providerInput.value = "99";
  h.setLimits({ maxConcurrent: 32, providerLimit: 8, providerLimits: { beta: 4 } });
  await h.panel.load({ quiet: true });
  assert.equal(h.$("job-max-concurrent").value, "500");
  assert.equal(h.$("job-provider-limit").value, "200");
  assert.equal(h.inputs()[0], providerInput);
  assert.equal(providerInput.value, "99");
  assert.equal(h.$("job-limits-settings").open, true);
  assert.match(h.$("job-limits").textContent, /32/);
  await h.$("reset-job-limits").fire("click");
  assert.equal(h.$("job-max-concurrent").value, 32);
  assert.deepEqual(h.inputs().map((node) => node.value), ["", 4]);
});

test("unchanged polling keeps clean provider inputs connected; changed saved connections refresh the form", async () => {
  const h = harness();
  await h.panel.load();
  const before = h.inputs();
  h.$("job-limits-settings").open = true;
  h.setLimits({ providerLimits: { alpha: 3 } });
  await h.panel.load({ quiet: true });
  assert.equal(h.inputs()[0], before[0]);
  assert.equal(h.inputs()[1], before[1]);
  assert.equal(h.$("job-limits-settings").open, true);
  h.setProviders([{ name: "alpha" }, { name: "beta" }, { name: "gamma" }]);
  await h.panel.load({ quiet: true });
  assert.equal(h.inputs().length, 3);
  assert.equal(h.inputs()[2].dataset.limitProvider, "gamma");
  h.setLimits({ providerLimit: 8 });
  await h.panel.load({ quiet: true });
  assert.equal(h.$("job-provider-limit").value, 8);
});

test("load and save failures are visible, retain the draft, and never emit success", async () => {
  const h = harness();
  h.failGet(new Error("offline"));
  await h.panel.load({ quiet: true });
  assert.match(h.$("job-status").textContent, /offline/);
  assert.equal(h.$("save-job-limits").disabled, true);
  assert.equal(h.$("job-max-concurrent").value, "");
  h.failGet(null);
  await h.panel.load();
  await h.edit(500, 200);
  h.inputs()[0].value = "55";
  h.failPost(new Error("disk full"));
  await h.$("job-limits-form").fire("submit");
  assert.match(h.$("job-limits-status").textContent, /保存失败.*disk full/);
  assert.equal(h.$("job-max-concurrent").value, "500");
  assert.equal(h.inputs()[0].value, "55");
  assert.equal(h.$("save-job-limits").disabled, false);
  assert.equal(h.toasts.length, 0);
  await h.panel.load();
  assert.equal(h.$("job-max-concurrent").value, "500");
});

test("a polling response started before save cannot roll back successfully saved settings", async () => {
  const h = harness();
  await h.panel.load();
  let finishRead;
  h.delayRead(new Promise((resolve) => { finishRead = resolve; }));
  const oldRead = h.panel.load({ quiet: true });
  await h.edit(128, 64);
  h.inputs()[0].value = "8";
  await h.$("job-limits-form").fire("submit");
  finishRead({ jobs: [], events: {}, limits: { maxConcurrent: 2, providerLimit: 1, providerLimits: {}, editable: true } });
  await oldRead;
  assert.equal(h.$("job-max-concurrent").value, 128);
  assert.match(h.$("job-limits").textContent, /128/);
  assert.match(h.$("job-limits-status").textContent, /已保存/);
});

test("invalid total, default and individual limits never submit a request", async () => {
  for (const [total, perProvider, override] of [[0, 1, ""], [1, 4097, ""], [1.5, 1, ""], [1, 1, "4097"]]) {
    const h = harness();
    await h.panel.load();
    await h.edit(total, perProvider);
    h.inputs()[0].value = override;
    await h.$("job-limits-form").fire("submit");
    assert.equal(h.calls.some((call) => call.options?.method === "POST"), false);
    assert.equal(h.toasts.length, 0);
    assert.match(h.$("job-limits-status").textContent, /整数/);
  }
});
