"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { createHash } = require("node:crypto");
const metadata = require("../package.json");

test("Windows installer identity stays compatible with releases before an explicit GUID", () => {
  // These identifiers locate existing NSIS installation records and Electron
  // user data. A new release number must never create a separate installation.
  assert.equal(metadata.name, "ainovel-desktop");
  assert.equal(metadata.build.appId, "com.ainovel.desktop");
  assert.equal(metadata.build.productName, "AI Novel Studio");
  // RFC 4122 UUIDv5, kept dependency-free for the renderer unit-test CI job.
  const digest = createHash("sha1")
    .update(Buffer.from("50e065bc313411e69bab38c9862bdaf3", "hex"))
    .update("com.ainovel.desktop").digest().subarray(0, 16);
  digest[6] = (digest[6] & 0x0f) | 0x50;
  digest[8] = (digest[8] & 0x3f) | 0x80;
  const hex = digest.toString("hex");
  const originalGuid = `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  assert.equal(originalGuid, "e8db1337-b330-59d5-a41f-5283be9b2bcf");
  assert.equal(metadata.build.nsis.guid, originalGuid);
});

test("Windows setup preserves user data and supports the existing installation scope", () => {
  const nsis = metadata.build.nsis;
  assert.equal(nsis.oneClick, false);
  assert.equal(nsis.perMachine, false, "keep the per-user default and existing all-users detection");
  assert.equal(nsis.allowElevation, true, "existing all-users installations must remain upgradable");
  assert.equal(nsis.allowToChangeInstallationDirectory, true);
  assert.equal(nsis.deleteAppDataOnUninstall, false);
  assert.deepEqual(metadata.build.win.target, ["nsis"]);
});
