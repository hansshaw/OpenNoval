"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { selectWorkspace } = require("../workspace");

test("new desktop default is independent of the project catalogue and remains copyable", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-workspace-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const child = path.join(root, "output", "web-books", "existing-child", "meta");
  fs.mkdirSync(child, { recursive: true });
  fs.writeFileSync(path.join(child, "web-project.json"), '{"name":"child"}');
  assert.deepEqual(selectWorkspace(root), { workspaceDir: path.join(root, "output", "novel"), legacy: false });
});

test("legacy root stories keep their exact directory and files without unlocked migration", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-workspace-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const books = path.join(root, "output", "web-books");
  fs.mkdirSync(path.join(books, "meta"), { recursive: true });
  const filename = path.join(books, "meta", "run.json");
  fs.writeFileSync(filename, "legacy-story-must-stay");
  assert.deepEqual(selectWorkspace(root), { workspaceDir: books, legacy: true });
  assert.equal(fs.readFileSync(filename, "utf8"), "legacy-story-must-stay");
  assert.equal(fs.existsSync(path.join(root, "output", "novel")), false);
});
