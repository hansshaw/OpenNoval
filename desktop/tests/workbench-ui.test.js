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
    this.dataset = {};
    this.style = {};
    this.attributes = {};
    this.listeners = new Map();
    this.className = "";
    this.value = "";
    this.textContent = "";
    this.open = false;
    this.classList = {
      add: (...names) => { this.className = [...new Set([...this.className.split(" "), ...names])].join(" "); },
      remove: (...names) => { this.className = this.className.split(" ").filter((name) => !names.includes(name)).join(" "); },
      toggle: (name, force) => { if (force) this.classList.add(name); else this.classList.remove(name); },
    };
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  setAttribute(name, value) { this.attributes[name] = value; }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  focus() {}
  querySelector() { return new Element(); }
  querySelectorAll(selector) {
    const match = (node) => selector.startsWith(".") && node.className.split(" ").includes(selector.slice(1).replace("[open]", "")) && (!selector.endsWith("[open]") || node.open);
    return this.children.flatMap((child) => [...(match(child) ? [child] : []), ...child.querySelectorAll(selector)]);
  }
}

function harness(chapters = []) {
  const nodes = new Map();
  const $ = (id) => { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); };
  const requests = [];
  let response = async (url) => ({ number: Number(url.match(/chapters\/(\d+)/)?.[1]), content: "正文内容" });
  const browser = {
    window: {}, URLSearchParams, console,
    document: { querySelector: $, querySelectorAll: () => [], createElement: (tag) => new Element(tag), getElementById: (id) => $("#" + id) },
    location: { hash: "" }, history: { replaceState() {} },
    setTimeout: () => 1, clearTimeout() {},
    fetch: async (url, options) => {
      requests.push({ url, options });
      const data = await response(url, options);
      return { ok: true, headers: { get: () => "application/json" }, json: async () => data };
    },
  };
  const source = fs.readFileSync(path.resolve(__dirname, "../../internal/entry/web/static/app.js"), "utf8");
  vm.runInNewContext(source.replace("  init();", "  window.test = { state, renderChapters, renderPlanOutline, chapterEntry, readChapter, syncDisabledControls, hasExportableChapters, sendChapterFeedback, runAction, renderCoCreate };"), browser);
  const api = browser.window.test;
  api.state.data = { directory: "/books/test", ready: true, chapters, workflow: { stage: "ready", approved: true, canRevise: true } };
  return { $, api, requests, respond: (fn) => { response = fn; }, text: (node) => [node.textContent, ...node.children.map((child) => [child.textContent, ...child.children.map((grandchild) => grandchild.textContent)].join(" "))].join(" ") };
}

const planned = (number, volume = 1) => ({ number, title: `计划章${number}`, source: "planned", status: "planned", has_content: false,
  volume_id: volume, volume_title: `卷名${volume}`, arc_title: "故事弧", core_event: "主角发现线索", hook: "门后是谁", scenes: ["抵达书店", "发现旧信"] });

test("confirmed unwritten chapters appear immediately under their volume, with their plan and no body request", () => {
  const h = harness([planned(1), planned(2), planned(3, 2)]);
  h.api.renderChapters();
  assert.equal(h.$("#chapter-count").textContent, "2 卷 · 3 章");
  assert.equal(h.$("#chapter-list").children.length, 2);
  assert.equal(h.$("#chapter-list").children[0].children.length, 3);
  assert.equal(h.$("#reader-title").textContent, "计划章1");
  assert.match(h.text(h.$("#reader-content")), /主角发现线索.*门后是谁.*抵达书店/);
  assert.equal(h.requests.length, 0);
  assert.equal(h.$("#send-chapter-feedback").disabled, true);
  assert.equal(h.api.hasExportableChapters("/books/test"), false);
});

test("search and status filters retain volume groups and selected chapter", async () => {
  const h = harness([planned(1), { ...planned(2), source: "final", status: "completed", has_content: true, words: 560 }, planned(3, 2)]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "planned";
  h.api.renderChapters();
  h.$("#chapter-search").value = "卷名2";
  h.api.renderChapters();
  assert.equal(h.$("#chapter-list").children.length, 1);
  assert.equal(h.$("#chapter-list").children[0].dataset.volume, "2");
  assert.equal(h.api.state.selectedChapter, 1);
  h.$("#chapter-search").value = "";
  h.api.state.chapterFilter = "written";
  h.api.renderChapters();
  assert.equal(h.$("#chapter-list").children[0].children.length, 2);
  assert.match(h.$("#chapter-progress").textContent, /已完成 1 \/ 3/);
  assert.equal(h.api.hasExportableChapters("/books/test"), true);
});

test("completed chapter feedback is enabled outside review only with approved planning", () => {
  const h = harness([{ ...planned(1), source: "final", status: "completed", has_content: true }]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "final";
  h.api.syncDisabledControls();
  assert.equal(h.$("#send-chapter-feedback").disabled, false);
  assert.equal(h.$("#revise-plan").disabled, false);
  h.api.state.data.workflow.approved = false;
  h.api.syncDisabledControls();
  assert.equal(h.$("#send-chapter-feedback").disabled, true);
  assert.equal(h.$("#start-batch").disabled, true);
});

test("a late body response cannot replace a newly selected planned chapter", async () => {
  const h = harness([{ ...planned(1), source: "final", status: "completed", has_content: true }, planned(2)]);
  let finish;
  h.respond(() => new Promise((resolve) => { finish = resolve; }));
  const pending = h.api.readChapter(1, "final");
  await h.api.readChapter(2, "planned");
  finish({ number: 1, content: "旧章正文" });
  await pending;
  assert.equal(h.api.state.selectedChapter, 2);
  assert.equal(h.$("#reader-title").textContent, "计划章2");
  assert.equal(h.api.state.loading.chapter, false);
});

test("planning review displays snake_case volume, event and scene fields", () => {
  const h = harness();
  h.api.renderPlanOutline([planned(1), planned(2, 2)].map(h.api.chapterEntry));
  assert.equal(h.$("#plan-outline").children.length, 2);
  const first = h.$("#plan-outline").children[0];
  assert.match(h.text(first.children[0]), /第 1 卷 · 卷名1/);
  assert.match(h.text(first.children[1]), /主角发现线索.*章节悬念：门后是谁.*抵达书店/);
});


test("an unfinished draft is readable but cannot receive completed-chapter rewrite instructions", async () => {
  const h = harness([{ ...planned(1), source: "draft", status: "draft", has_content: true }]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "draft";
  h.api.syncDisabledControls();
  assert.equal(h.$("#send-chapter-feedback").disabled, true);
  h.$("#chapter-feedback").value = "重写开头";
  await h.api.sendChapterFeedback();
  assert.equal(h.requests.length, 0);
  assert.match(h.$("#toast").textContent, /请选择已完成/);
});


test("approval refreshes state and opens the chapter workbench with the confirmed plan", async () => {
  const h = harness();
  h.api.state.data.workflow.approved = false;
  h.respond(async (url) => url === "/api/state"
    ? { ...h.api.state.data, chapters: [planned(1)], workflow: { approved: true, stage: "ready" } }
    : {});
  await h.api.runAction({ action: "approve-plan", fingerprint: "reviewed-version" });
  assert.equal(h.api.state.view, "chapters");
  assert.equal(h.$("#reader-title").textContent, "计划章1");
  assert.equal(h.$("#workbench-plan-state").textContent, "当前规划已确认");
  assert.deepEqual(JSON.parse(h.requests[0].options.body), { action: "approve-plan", fingerprint: "reviewed-version", directory: "/books/test" });
});

test("completed chapter rewrite sends only the selected chapter and explicit user instructions", async () => {
  const h = harness([{ ...planned(1), source: "final", status: "completed", has_content: true }, { ...planned(2), source: "final", status: "completed", has_content: true }]);
  h.api.state.selectedChapter = 1;
  h.api.state.selectedSource = "final";
  h.$("#chapter-feedback").value = "只改第一章结尾的对话";
  h.respond(async (url) => url === "/api/state" ? h.api.state.data : {});
  await h.api.sendChapterFeedback();
  assert.deepEqual(JSON.parse(h.requests[0].options.body), {
    action: "chapter-feedback", chapter: 1, text: "只改第一章结尾的对话", directory: "/books/test",
  });
});

test("the original cocreation record remains accessible after formal planning starts", () => {
  const h = harness();
  h.api.state.cocreate = { started: true, confirmed: true, draftPrompt: "保留温暖结局和第三人称原则", transcript: [{ role: "user", content: "第三人称，温暖结局" }] };
  h.api.renderCoCreate();
  assert.equal(h.$("#cocreate-workspace").className.includes("hidden"), false);
  assert.match(h.$("#cocreate-history-title").textContent, /第一阶段共创记录.*已保留/);
  assert.equal(h.$("#cocreate-draft").value, "保留温暖结局和第三人称原则");
  assert.equal(h.$("#cocreate-draft").disabled, true);
  assert.equal(h.$("#cocreate-history").open, false);
});
