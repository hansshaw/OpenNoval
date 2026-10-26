"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { configureTestMode, createTestEnvironment } = require("../test-environment");

test("test processes use independent homes, config, Chromium and temp paths without inherited credentials", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-env-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const inherited = { HOME: "/real/home", USERPROFILE: "C:\\Real", OPENAI_API_KEY: "real-secret", CUSTOM_ACCESS_TOKEN: "another-secret", DISPLAY: ":0", PATH: process.env.PATH, HTTPS_PROXY: "http://external-proxy", ELECTRON_RUN_AS_NODE: "1", Electron_Run_As_Node: "1" };
  const isolated = createTestEnvironment(root, inherited);
  for (const name of ["HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP"]) {
    assert.ok(isolated.env[name].startsWith(root + path.sep), `${name} must remain under the test root`);
    assert.equal(fs.statSync(isolated.env[name]).isDirectory(), true);
  }
  assert.equal(isolated.env.OPENAI_API_KEY, undefined);
  assert.equal(isolated.env.CUSTOM_ACCESS_TOKEN, undefined);
  assert.equal(isolated.env.ELECTRON_RUN_AS_NODE, undefined);
  assert.equal(isolated.env.Electron_Run_As_Node, undefined);
  assert.equal(isolated.env.HTTPS_PROXY, "");
  assert.equal(isolated.env.DISPLAY, ":0");
  assert.equal(inherited.HOME, "/real/home");
  const calls = [];
  const app = { setPath: (...args) => calls.push(args) };
  const configured = configureTestMode(app, ["electron", "--smoke-test"], { AINOVEL_DESKTOP_TEST_ROOT: root });
  assert.equal(configured.mode, "smoke");
  assert.deepEqual(calls, [["userData", isolated.userData], ["sessionData", isolated.sessionData]]);
});

test("normal startup never overrides user data or configuration", () => {
  const app = { setPath() { assert.fail("normal startup must not change Electron data paths"); } };
  assert.equal(configureTestMode(app, ["electron", "."], { AINOVEL_DESKTOP_TEST_ROOT: "/ignored" }), null);
});

test("desktop checks use the supplied X11 display independently of screenshot output", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-display-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const inherited = { DISPLAY: ":99", WAYLAND_DISPLAY: "wayland-0" };
  const linux = createTestEnvironment(root, inherited, "linux");
  assert.deepEqual(linux.displayArgs, ["--ozone-platform=x11"]);
  assert.equal(linux.env.DISPLAY, ":99"); assert.equal(linux.env.WAYLAND_DISPLAY, undefined);
  assert.equal(inherited.WAYLAND_DISPLAY, "wayland-0");
  for (const platform of ["win32", "darwin"]) {
    const other = createTestEnvironment(root, inherited, platform);
    assert.deepEqual(other.displayArgs, []); assert.equal(other.env.WAYLAND_DISPLAY, "wayland-0");
  }
  const waylandOnly = createTestEnvironment(root, { WAYLAND_DISPLAY: "wayland-0" }, "linux");
  assert.deepEqual(waylandOnly.displayArgs, []);
  assert.equal(waylandOnly.env.WAYLAND_DISPLAY, "wayland-0");
});
