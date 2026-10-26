"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { uploadDraft, fileIdentity, createGhRunner } = require("../scripts/upload-release-draft");

function fixture(t, platform = "linux") {
  const desktopDir = fs.mkdtempSync(path.join(os.tmpdir(), "release draft test "));
  t.after(() => fs.rmSync(desktopDir, { recursive: true, force: true }));
  const env = { GITHUB_ACTIONS: "true", GITHUB_EVENT_NAME: "workflow_dispatch", DRAFT_RELEASE_ID: "123", GITHUB_REPOSITORY: "owner/project", GITHUB_SHA: "a".repeat(40), GITHUB_RUN_ID: "456", GH_TOKEN: "secret-token-never-print" };
  const version = "0.1.3", tag = `v${version}`;
  const dist = path.join(desktopDir, "dist"), reportsDir = path.join(dist, "reports");
  fs.mkdirSync(reportsDir, { recursive: true });
  const write = (name, value) => fs.writeFileSync(path.join(reportsDir, name), JSON.stringify(value));
  fs.writeFileSync(path.join(desktopDir, "package.json"), JSON.stringify({ name: "ainovel-desktop", version }));
  const names = platform === "win32"
    ? [`ainovel-desktop-${version}-win-x64-setup.exe`, `ainovel-desktop-${version}-win-x64-setup.exe.blockmap`]
    : [`ainovel-desktop-${version}-linux-x86_64.AppImage`, `ainovel-desktop-${version}-linux-amd64.deb`];
  for (const name of names) fs.writeFileSync(path.join(dist, name), `build:${name}`);
  // Old packages and upgrade fixtures must never be selected by a broad glob.
  fs.writeFileSync(path.join(dist, "ainovel-desktop-0.0.0-win-x64-setup.exe"), "old fixture");
  const phase = name => ({ phase: name, ok: true, versions: { app: version }, steps: [{ name: "real-window", ok: true }], sidecarStopped: true, sidecarShutdown: { graceful: true, forced: false } });
  for (const name of ["smoke", "regression", "smoke-packaged"]) write(`${name}.json`, {
    schemaVersion: 1, suite: `desktop-${name}`, ok: true, failures: 0, packaged: name === "smoke-packaged",
    phases: (name === "regression" ? ["initial", "restart", "empty-prepare", "empty-restart"] : ["initial"]).map(phase),
  });
  write("creative-workflow.json", { schemaVersion: 1, suite: "creative-desktop-workflow", ok: true, failures: 0, errors: null, steps: [{ name: "write-and-commit-one-chapter", ok: true }] });
  if (platform === "win32") for (const name of ["windows-installer-upgrade.json", "windows-public-installer-upgrade.json"]) write(name, {
    schemaVersion: 1, suite: "windows-installer-upgrade", ok: true, cleanup: true, expectedVersion: version, installer: names[0],
    steps: [{ name: "installed-new-version-smoke", ok: true, result: phase("initial") }],
  });
  let release = { id: 123, draft: true, published_at: null, tag_name: tag, target_commitish: env.GITHUB_SHA };
  let tagId = 123, assets = [], afterRead = null;
  const calls = [];
  const gh = args => {
    calls.push(args);
    if (args[0] === "api" && args[1].includes("/assets?")) return JSON.stringify([assets]);
    if (args[0] === "api") { const response = JSON.stringify(release); afterRead?.(calls); return response; }
    if (args[1] === "view") return JSON.stringify({ databaseId: tagId, isDraft: release.draft, tagName: release.tag_name, targetCommitish: release.target_commitish });
    assert.equal(args[1], "upload", "Only upload is a permitted release mutation");
    assert.equal(args.includes("--clobber"), false);
    for (const filename of args.slice(5)) assets.push({ ...fileIdentity(filename), state: "uploaded" });
    return "";
  };
  return {
    env, desktopDir, dist, reportsDir, names, calls, write, phase,
    run: () => uploadDraft({ env, platform, arch: "x64", desktopDir, gh }),
    setRelease: patch => { release = { ...release, ...patch }; }, setTagId: value => { tagId = value; },
    setAssets: value => { assets = value; }, assets: () => assets,
    afterRead: callback => { afterRead = callback; },
  };
}

test("only an explicit manual Actions run can start a draft upload", t => {
  for (const patch of [{ GITHUB_ACTIONS: "false" }, { GITHUB_EVENT_NAME: "push" }, { DRAFT_RELEASE_ID: "" }, { DRAFT_RELEASE_ID: "12;publish" }, { GITHUB_SHA: "main" }, { GITHUB_REPOSITORY: "owner/project/other" }, { GH_TOKEN: "" }]) {
    const f = fixture(t); Object.assign(f.env, patch);
    assert.throws(f.run);
    assert.equal(f.calls.length, 0, "Invalid authorization must not call GitHub");
  }
  for (const platform of ["darwin", "freebsd"]) {
    const f = fixture(t);
    assert.throws(() => uploadDraft({ ...f, platform, arch: "x64", gh: () => assert.fail("GitHub must not be called") }), /Windows and Linux/);
  }
});

test("Linux and Windows upload exactly their current installers and a provenance report, never publish", t => {
  for (const platform of ["linux", "win32"]) {
    const f = fixture(t, platform), result = f.run();
    assert.deepEqual(result.uploaded.slice(0, 2), f.names);
    assert.equal(result.uploaded.length, 3);
    const upload = f.calls.find(args => args[1] === "upload");
    assert.deepEqual(upload.slice(0, 5), ["release", "upload", "v0.1.3", "--repo", "owner/project"]);
    assert.ok(upload.slice(5).every(name => name.startsWith(f.dist + path.sep)));
    const report = JSON.parse(fs.readFileSync(result.reportPath, "utf8"));
    assert.equal(report.commit, f.env.GITHUB_SHA); assert.equal(report.runId, "456"); assert.equal(report.version, "0.1.3");
    assert.equal(Object.keys(report.reports).length, platform === "win32" ? 6 : 4);
    assert.equal(report.installers.length, 2);
    assert.doesNotMatch(JSON.stringify(report), /secret-token-never-print/);
    assert.ok(f.calls.every(args => args[0] === "api" || ["view", "upload"].includes(args[1])));
  }
});

test("published, wrong-ID, wrong-version, wrong-commit and differently resolved tags never receive files", t => {
  for (const patch of [{ draft: false }, { published_at: "2026-01-01T00:00:00Z" }, { id: 99 }, { tag_name: "v0.1.2" }, { target_commitish: "main" }, { target_commitish: "b".repeat(40) }]) {
    const f = fixture(t); f.setRelease(patch);
    assert.throws(f.run, /specified unpublished draft/);
    assert.equal(f.calls.some(args => args[1] === "upload"), false);
  }
  const f = fixture(t); f.setTagId(999);
  assert.throws(f.run, /different or published release/);
  assert.equal(f.calls.some(args => args[1] === "upload"), false);
});

test("failed, stale, missing and incomplete acceptance evidence fail before network mutation", t => {
  const mutations = [
    f => fs.rmSync(path.join(f.reportsDir, "creative-workflow.json")),
    f => f.write("creative-workflow.json", { schemaVersion: 1, suite: "creative-desktop-workflow", ok: false, failures: 1 }),
    f => f.write("smoke.json", { schemaVersion: 1, suite: "desktop-smoke", ok: true, failures: 0, packaged: false, phases: [{ ...f.phase("initial"), versions: { app: "0.1.2" } }] }),
    f => f.write("regression.json", { schemaVersion: 1, suite: "desktop-regression", ok: true, failures: 0, packaged: false, phases: [f.phase("initial")] }),
    f => fs.rmSync(path.join(f.dist, f.names[1])),
  ];
  for (const mutate of mutations) { const f = fixture(t); mutate(f); assert.throws(f.run); assert.equal(f.calls.length, 0); }
  const f = fixture(t, "win32");
  const report = JSON.parse(fs.readFileSync(path.join(f.reportsDir, "windows-public-installer-upgrade.json"), "utf8"));
  report.expectedVersion = "0.1.2"; f.write("windows-public-installer-upgrade.json", report);
  assert.throws(f.run, /installer upgrade did not pass/); assert.equal(f.calls.length, 0);
});

test("retries skip only exact size and digest matches and refuse mismatches before any upload", t => {
  const f = fixture(t); f.run(); f.calls.length = 0;
  const result = f.run();
  assert.equal(result.skipped.length, 3); assert.equal(result.uploaded.length, 0);
  assert.equal(f.calls.some(args => args[1] === "upload"), false);
  for (const patch of [{ digest: null }, { digest: "sha256:" + "0".repeat(64) }, { size: 1 }, { state: "new" }]) {
    const other = fixture(t), asset = { ...fileIdentity(path.join(other.dist, other.names[0])), state: "uploaded", ...patch };
    other.setAssets([asset]);
    assert.throws(other.run, /Refusing to replace/);
    assert.equal(other.calls.some(args => args[1] === "upload"), false);
  }
  f.env.GITHUB_RUN_ID = "457";
  assert.throws(f.run, /different or unverifiable existing attachment: desktop-acceptance/);
});

test("target is rechecked before mutation and child execution is argv-only with secrets confined to env", t => {
  const f = fixture(t);
  f.afterRead(calls => { if (calls.length > 1) f.setRelease({ draft: false }); });
  assert.throws(f.run, /different or published release/);
  assert.equal(f.calls.some(args => args[1] === "upload"), false);
  let invocation;
  const gh = createGhRunner(f.env, f.desktopDir, (...args) => { invocation = args; return { status: 0, stdout: "{}" }; });
  gh(["release", "upload", "v0.1.3", "--repo", "owner/project", path.join(f.dist, "file with spaces.exe")]);
  assert.equal(invocation[0], "gh"); assert.equal(invocation[2].shell, false);
  assert.equal(invocation[2].env.GH_TOKEN, "secret-token-never-print");
  assert.doesNotMatch(JSON.stringify(invocation[1]), /secret-token/);
  const failing = createGhRunner(f.env, f.desktopDir, () => ({ status: 1, stderr: f.env.GH_TOKEN, stdout: f.env.GH_TOKEN }));
  assert.throws(() => failing(["api", "example"]), error => !error.message.includes(f.env.GH_TOKEN));
});
