"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(value = "") { Object.assign(this, { value, textContent: "", placeholder: "", disabled: false, dataset: {}, children: [], listeners: new Map(), open: false }); }
  get options() { return this.children; }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = [...nodes]; }
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  showModal() { this.open = true; }
  focus() { this.focused = true; }
}
const fixture = () => ({ provider: "local", model: "writer", providers: [{ name: "local", type: "openai", api: "chat", base_url: "https://provider.example/v1", has_api_key: true, models: ["writer", "planner", "automatic"], model_details: [
  { name: "writer", context_window: 64000, max_output_tokens: 8192, configured_context_window: 64000, configured_max_output_tokens: 8192, context_source: "model_config", output_source: "model_config" },
  { name: "planner", context_window: 96000, max_output_tokens: 4096, configured_context_window: 96000, configured_max_output_tokens: 4096, context_source: "model_config", output_source: "model_config" },
  { name: "automatic", context_window: 128000, max_output_tokens: 16384, configured_context_window: 0, configured_max_output_tokens: 0, context_source: "upstream", output_source: "upstream" },
] }] });
function harness() {
  const nodes = new Map(), calls = [], notices = [];
  const $ = (selector) => { if (!nodes.has(selector)) nodes.set(selector, new Element()); return nodes.get(selector); };
  const form = $("#config-form");
  form.elements = Object.fromEntries(["name", "model", "models", "type", "api", "base_url", "api_key", "context_window", "max_output_tokens"].map((key) => [key, new Element()]));
  const state = { config: fixture(), modelDiscovery: { provider: "", details: {} }, settingsRevision: 0, settingsBusy: false };
  let respond = async () => ({ ok: true });
  class FormData { constructor(target) { this.target = target; } *[Symbol.iterator]() { for (const [key, node] of Object.entries(this.target.elements)) if (!node.disabled) yield [key, node.value]; } }
  const browser = { window: {}, FormData };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/models.js"), "utf8"), browser);
  const option = (value, label) => Object.assign(new Element(value), { textContent: label });
  const module = browser.window.NovelModels.create({ state, $, $$: () => [...Object.values(form.elements), $("#saved-model"), $("#saved-provider"), $("#fetch-models")], api: async (url, options) => { calls.push({ url, options }); return respond(url, options); }, option, setBusy: () => {}, refreshAll: async () => {}, syncDisabledControls: () => {}, toast: (message) => notices.push(message) });
  module.bindEvents();
  const fire = (node, name) => node.listeners.get(name)({ target: node, preventDefault() {} });
  fire($("#open-settings"), "click");
  return { $, form, state, calls, notices, fire, respond: (fn) => { respond = fn; }, select: async (name) => { $("#saved-model").value = name; await fire($("#saved-model"), "change"); } };
}

test("editing a saved model loads its own overrides and saves only those limits", async () => {
  const h = harness();
  assert.equal(h.form.elements.model.value, "writer");
  assert.equal(h.form.elements.context_window.value, "64000");
  await h.select("planner");
  assert.equal(h.form.elements.model.value, "planner");
  assert.equal(h.form.elements.context_window.value, "96000");
  assert.equal(h.form.elements.max_output_tokens.value, "4096");
  h.form.elements.context_window.value = "120000";
  h.form.elements.max_output_tokens.value = "12000";
  await h.fire(h.form, "submit");
  const body = h.calls.at(-1).options.body;
  assert.equal(body.model, "planner");
  assert.equal(body.context_window, 120000);
  assert.equal(body.max_output_tokens, 12000);
  assert.deepEqual(Array.from(body.models), ["writer", "planner", "automatic"]);
  assert.equal(body.activate, false);
  assert.equal(body.api_key, "");
  assert.equal(h.form.elements.model.value, "planner", "keep editing the saved model instead of jumping to the default");
});

test("choosing another model or adding a new one never copies previous model limits", async () => {
  const h = harness();
  h.form.elements.context_window.value = "77777";
  await h.select("automatic");
  assert.equal(h.form.elements.context_window.value, "");
  assert.equal(h.form.elements.max_output_tokens.value, "");
  assert.match(h.$("#model-limit-hint").textContent, /上游声明/);
  await h.select("");
  h.form.elements.model.value = "new-model";
  await h.fire(h.form.elements.model, "input");
  await h.fire(h.form, "submit");
  const body = h.calls.at(-1).options.body;
  assert.equal(body.context_window, 0);
  assert.equal(body.max_output_tokens, 0);
  assert.ok(body.models.includes("new-model"));
});

test("clearing saved limits restores automatic parameters through explicit zero values", async () => {
  const h = harness();
  await h.fire(h.$("#reset-model-limits"), "click");
  assert.equal(h.form.elements.context_window.value, "");
  assert.equal(h.form.elements.max_output_tokens.value, "");
  await h.fire(h.form, "submit");
  assert.equal(h.calls.at(-1).options.body.context_window, 0);
  assert.equal(h.calls.at(-1).options.body.max_output_tokens, 0);
});

test("upstream discovery shows separate sources without turning defaults into overrides", async () => {
  const h = harness(); await h.select("automatic");
  h.respond(async () => ({ models: ["automatic", "new"], details: [{ id: "automatic", context_window: 128000, max_output_tokens: 8192, context_source: "upstream", output_source: "registry", source: "mixed" }] }));
  await h.fire(h.$("#fetch-models"), "click");
  assert.equal(h.form.elements.context_window.value, "");
  assert.equal(h.form.elements.max_output_tokens.value, "");
  assert.match(h.$("#model-limit-hint").textContent, /上游声明/);
  assert.match(h.$("#model-limit-hint").textContent, /内置注册表/);
  assert.equal(h.form.elements.model.value, "automatic");
  h.form.elements.base_url.value = "https://different.example/v1";
  await h.fire(h.form.elements.base_url, "input");
  assert.doesNotMatch(h.$("#model-limit-hint").textContent, /检测结果/);
});

test("fetching models preserves a saved explicit override and rejects late dialog results", async () => {
  const h = harness(); let complete;
  h.respond(async () => new Promise((resolve) => { complete = resolve; }));
  const pending = h.fire(h.$("#fetch-models"), "click");
  assert.equal(h.form.elements.context_window.disabled, true);
  await h.fire(h.$("#open-settings"), "click");
  complete({ models: ["stale"], details: [] }); await pending;
  assert.equal(h.form.elements.context_window.value, "64000");
  assert.deepEqual(h.$("#model-options").children.map((row) => row.value), ["writer", "planner", "automatic"]);
  assert.equal(h.form.elements.context_window.disabled, false);
});


test("successful saves display a metadata fallback warning without reporting save failure", async () => {
  const h = harness();
  h.respond(async () => ({ ok: true, warning: "上游参数暂不可用，已使用有效缓存。" }));
  await h.fire(h.form, "submit");
  assert.match(h.$("#config-status").textContent, /模型配置已保存/);
  assert.match(h.$("#config-status").textContent, /上游参数暂不可用/);
  assert.match(h.notices.at(-1), /已使用有效缓存/);
  assert.doesNotMatch(h.$("#config-status").textContent, /保存失败/);
});
