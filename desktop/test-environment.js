"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

function createTestEnvironment(root, inherited = process.env, platform = process.platform) {
  const home = path.join(root, "home");
  const userData = path.join(root, "user-data");
  const sessionData = path.join(root, "session-data");
  const config = path.join(root, "config");
  const cache = path.join(root, "cache");
  const temp = path.join(root, "temp");
  for (const directory of [home, userData, sessionData, config, cache, temp]) {
    fs.mkdirSync(directory, { recursive: true });
  }
  const env = { ...inherited };
  // Desktop checks resize, position and focus real windows. Wayland does not
  // permit these operations reliably; use the supplied X11/Xvfb display even
  // when the launching desktop also exports a WAYLAND_DISPLAY.
  const displayArgs = platform === "linux" && env.DISPLAY ? ["--ozone-platform=x11"] : [];
  if (displayArgs.length) delete env.WAYLAND_DISPLAY;
  for (const name of Object.keys(env)) {
    if (/(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)/i.test(name) || name.toUpperCase() === "ELECTRON_RUN_AS_NODE") {
      delete env[name];
    }
  }
  Object.assign(env, {
    HOME: home,
    USERPROFILE: home,
    APPDATA: config,
    LOCALAPPDATA: cache,
    XDG_CONFIG_HOME: config,
    XDG_CACHE_HOME: cache,
    TMPDIR: temp,
    TMP: temp,
    TEMP: temp,
    AINOVEL_DESKTOP_TEST_ROOT: root,
    // Preserve the local display connection while excluding external proxies.
    HTTP_PROXY: "",
    HTTPS_PROXY: "",
    ALL_PROXY: "",
    http_proxy: "",
    https_proxy: "",
    all_proxy: "",
    NO_PROXY: "127.0.0.1,localhost,::1",
    no_proxy: "127.0.0.1,localhost,::1",
  });
  return { root, home, userData, sessionData, env, displayArgs };
}

function configureTestMode(app, argv = process.argv, inherited = process.env) {
  const mode = argv.includes("--desktop-regression")
    ? "regression"
    : argv.includes("--smoke-test") ? "smoke" : "";
  if (!mode) return null;
  const supplied = inherited.AINOVEL_DESKTOP_TEST_ROOT;
  const root = supplied
    ? path.resolve(supplied)
    : fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-desktop-test-"));
  const options = createTestEnvironment(root, inherited);
  app.setPath("userData", options.userData);
  app.setPath("sessionData", options.sessionData);
  return { ...options, mode, owned: !supplied };
}

module.exports = { createTestEnvironment, configureTestMode };
