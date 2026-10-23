"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

test("offline PDF manifest verifies regardless of filesystem enumeration order", () => {
  // Isolate the filesystem override from the parallel test runner. Reverse each
  // directory listing to exercise a different order without modifying assets.
  const result = spawnSync(process.execPath, ["-e", `
    const fs = require("node:fs");
    const original = fs.readdirSync;
    let directories = 0;
    fs.readdirSync = function (...args) {
      directories++;
      return original.apply(this, args).reverse();
    };
    const { sync } = require("./scripts/sync-reader-vendor.js");
    const count = sync(true);
    if (directories < 4 || count < 200) throw new Error("Expected the complete PDF asset tree");
  `], { cwd: path.resolve(__dirname, ".."), encoding: "utf8" });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stderr || result.stdout);
});
