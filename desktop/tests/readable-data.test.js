"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

class Element {
  constructor(tag = "div") {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.listeners = new Map();
    this.className = "";
    this.textContent = "";
    this.classList = { contains: (name) => this.className.split(" ").includes(name) };
  }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.textContent = ""; this.children = children; }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  set innerHTML(_value) { throw new Error("Readable data must never interpolate HTML"); }
}
const all = node => [node, ...node.children.flatMap(all)];
const text = node => [node.textContent, ...node.children.map(text)].filter(Boolean).join(" ");
const tagged = (node, tag) => all(node).filter(child => child.tagName === tag.toUpperCase());
const styled = (node, style) => all(node).filter(child => child.className.split(" ").includes(style));
const field = (node, name) => all(node).find(child => child.dataset.field === name);

function harness(value, empty) {
  const browser = { window: {}, document: { createElement: tag => new Element(tag) } };
  vm.runInNewContext(fs.readFileSync(path.resolve(__dirname, "../../internal/entry/desktopui/static/readable-data.js"), "utf8"), browser);
  const root = new Element(); root.className = "readable-view";
  const api = browser.window.NovelReadableData;
  api.render(root, value, empty);
  return { root, api };
}

test("reviews have Chinese labels, meaningful statuses and intact evidence without changing source records", () => {
  const review = {
    chapter: 2, verdict: "rewrite", summary: "主角动机需要补充。", contract_status: "partial",
    issues: [{ artifact: "world_rules", severity: "critical", description: "无代价复活违反设定", evidence: "第 2 章：他毫发无伤地重生", suggestion: "增加明确代价", requires_change: true }],
    dimensions: [{ dimension: "人物动机", score: 0, comment: "行动缺乏铺垫" }], fingerprint: "version-digest",
  };
  const before = JSON.stringify(review);
  const { root } = harness(review);
  assert.match(text(field(root, "verdict")), /评审结论.*需要重写/);
  assert.match(text(field(root, "contract_status")), /章节约定完成情况.*部分达成/);
  assert.match(text(field(root, "issues")), /严重问题.*无代价复活违反设定.*依据.*第 2 章.*修改建议.*增加明确代价/);
  assert.match(text(field(root, "artifact")), /相关资料.*世界规则/);
  assert.match(text(field(root, "dimensions")), /人物动机.*评分.*0.*行动缺乏铺垫/);
  assert.equal(tagged(root, "pre").length, 0);
  const metadata = styled(root, "readable-metadata");
  assert.equal(metadata.length, 1);
  assert.equal(metadata[0].open, undefined, "Technical metadata remains folded by default");
  assert.match(text(metadata[0]), /记录信息.*版本指纹.*version-digest/);
  assert.equal(root.attributes.tabindex, "0", "Scrollable cards are reachable with a keyboard");
  assert.equal(JSON.stringify(review), before);
});

test("story prose and Markdown are paragraphs, headings and lists while markup is always inert text", () => {
  const payload = "## 规划摘要\n第一段 **关键选择**。\n\n第二段保留 `道具`。\n- 线索一\n- 线索二\n\n1. 第一步\n2. 第二步\n\n<script>window.pwned=true</script><img src=x onerror=alert(1)>";
  const { root } = harness({ summary: payload, unknown_key: "**<svg onload=alert(1)>**" });
  assert.equal(tagged(root, "h5")[0].textContent, "");
  assert.equal(text(tagged(root, "h5")[0]), "规划摘要");
  assert.equal(tagged(root, "ul").length, 1);
  assert.equal(tagged(root, "ol").length, 1);
  assert.equal(tagged(root, "li").length, 4);
  assert.ok(tagged(root, "p").length >= 3);
  assert.match(text(tagged(root, "strong")[0]), /关键选择/);
  assert.equal(text(tagged(root, "code")[0]), "道具");
  assert.match(text(root), /<script>window.pwned=true<\/script><img src=x onerror=alert\(1\)>/);
  assert.match(text(root), /<svg onload=alert\(1\)>/);
  for (const tag of ["script", "img", "svg", "iframe"]) assert.equal(tagged(root, tag).length, 0);
});

test("nested JSON strings render as records and unknown keys, false and zero remain visible", () => {
  const value = JSON.parse('{"ready":false,"score":0,"custom_field":{"details":"[{\\"name\\":\\"林舟\\",\\"tier\\":\\"core\\",\\"description\\":\\"守护故乡\\"}]"},"empty":null,"__proto__":"用户字段","verdict":"__proto__"}');
  const { root, api } = harness(value);
  assert.match(text(field(root, "ready")), /就绪状态.*未就绪/);
  assert.match(text(field(root, "score")), /评分.*0/);
  assert.match(text(field(root, "custom_field")), /custom field.*林舟.*核心人物.*守护故乡/);
  assert.match(text(field(root, "empty")), /暂无记录/);
  assert.match(text(field(root, "__proto__")), /用户字段/);
  assert.equal(api.translated("verdict", "__proto__"), "__proto__");
  assert.equal(typeof api.label("__proto__"), "string");
  assert.doesNotMatch(text(root), /\[object Object\]|"name"|"tier"/);
  for (const empty of [null, undefined, "", [], {}]) {
    api.render(root, empty, "等待资料");
    assert.equal(text(root), "等待资料");
  }
  api.render(root, "{尚未完成的模型输出");
  assert.equal(text(root), "{尚未完成的模型输出");
});

test("long records are incrementally displayed without dropping any item or mutating the collection", () => {
  const rows = Array.from({ length: 53 }, (_, i) => ({ name: `人物 ${i + 1}`, description: `介绍 ${i + 1}` }));
  const { root } = harness(rows);
  const more = styled(root, "readable-more")[0];
  assert.equal(styled(root, "readable-record").length, 24);
  assert.equal(more.hidden, false);
  assert.match(text(more), /剩余 29 项/);
  more.listeners.get("click")();
  assert.equal(styled(root, "readable-record").length, 48);
  more.listeners.get("click")();
  assert.equal(styled(root, "readable-record").length, 53);
  assert.equal(more.hidden, true);
  assert.match(text(root), /人物 53.*介绍 53/);
  assert.equal(rows[52].name, "人物 53");
  const scalar = harness(["关键事件一", "关键事件二"]).root;
  assert.equal(styled(scalar, "readable-record").length, 0);
  assert.equal(tagged(scalar, "li").length, 2);
});

test("deep values can be expanded and accidental cycles stop at a readable boundary", () => {
  let value = { summary: "最深层内容" };
  for (let i = 0; i < 17; i++) value = { next: value };
  const { root } = harness(value);
  assert.doesNotMatch(text(root), /最深层内容/);
  for (let i = 0; i < 2; i++) {
    const button = styled(root, "readable-more").find(node => node.textContent === "展开更多层级");
    assert.ok(button);
    button.listeners.get("click")();
  }
  assert.match(text(root), /最深层内容/);
  const cyclic = { name: "循环数据" }; cyclic.self = cyclic;
  assert.match(text(harness(cyclic).root), /循环引用，无法展开/);
});
