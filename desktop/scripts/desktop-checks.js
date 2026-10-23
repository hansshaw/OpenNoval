"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

async function runDesktopChecks(context) {
  const { app, window, database, paths, baseURL, syncDesktopData, options } = context;
  const steps = [];
  const started = Date.now();
  const phase = process.env.AINOVEL_DESKTOP_TEST_PHASE || "initial";
  async function step(name, action) {
    const start = Date.now();
    await action();
    steps.push({ name, ok: true, durationMs: Date.now() - start });
  }
  async function api(route, body, method = body === undefined ? "GET" : "POST", status = 200) {
    const result = await window.webContents.executeJavaScript(`(async () => {
      const response = await fetch(${JSON.stringify(route)}, {
        method: ${JSON.stringify(method)},
        headers: { "Content-Type": "application/json" },
        ${body === undefined ? "" : `body: ${JSON.stringify(JSON.stringify(body))},`}
      });
      return { status: response.status, text: await response.text() };
    })()`);
    assert.equal(result.status, status, `${method} ${route}: ${result.text}`);
    return result.text ? JSON.parse(result.text) : null;
  }
  async function waitForUI(expression, message) {
    const deadline = Date.now() + 5000;
    while (Date.now() < deadline) {
      if (await window.webContents.executeJavaScript(expression)) return;
      await new Promise((resolve) => setTimeout(resolve, 30));
    }
    throw new Error(message);
  }
  await step("real-window-preload-and-api", async () => {
    assert.equal(window.webContents.getURL(), `${baseURL}/`);
    const info = await window.webContents.executeJavaScript(`(async () => ({
      desktop: await window.ainovelDesktop.getInfo(),
      hasUI: Boolean(document.querySelector("#current-project-button") && document.querySelector("#settings-dialog #runtime-model") && !document.querySelector("#view-overview #runtime-model") && !document.querySelector("#config-form #runtime-model")),
      nodeDisabled: typeof require === "undefined" && typeof process === "undefined",
    }))()`);
    assert.equal(info.desktop.sqlitePath, paths.sqlitePath);
    assert.equal(info.hasUI, true);
    assert.equal(info.nodeDisabled, true);
    const state = await api("/api/state");
    assert.equal(state.directory, paths.workspaceDir);
    const config = await api("/api/config");
    assert.ok(config.config_path.startsWith(options.home + path.sep));
    if (phase === "initial") assert.equal(config.providers.length, 0);
    await api("/api/desktop/shutdown", {}, "POST", 403);
  });

  await step("standalone-browser-access-is-disabled", async () => {
    for (const route of ["/", "/app.js", "/api/state", "/api/events"]) {
      const response = await fetch(`${baseURL}${route}`, { signal: AbortSignal.timeout(5000) });
      assert.equal(response.status, 403, `Unauthenticated route was accessible: ${route}`);
      await response.arrayBuffer();
    }
  });

  await step("appearance-themes-contrast-motion-and-persistence", async () => {
    const run = async (source) => {
      let timer;
      try {
        return await Promise.race([window.webContents.executeJavaScript(source), new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error("Appearance UI timed out: " + source.slice(0, 240))), 7000);
        })]);
      } finally { clearTimeout(timer); }
    };
    const expected = phase === "restart" ? "genshin" : "original";
    await waitForUI(`document.documentElement.dataset.theme === ${JSON.stringify(expected)}`, "Appearance did not restore from SQLite");
    if (phase === "initial") assert.equal(await run('document.documentElement.dataset.motion'), "off");
    else assert.deepEqual(await run('window.ainovelDesktop.getAppearance()'), { theme: "genshin", motion: true });
    await run('document.getElementById("prompt").value = "换肤不得丢失的创作草稿"; document.getElementById("open-appearance").click()');
    assert.equal(await run('document.getElementById("appearance-dialog").open'), true);
    const screenshots = process.env.AINOVEL_APPEARANCE_SCREENSHOTS;
    if (screenshots) {
      fs.mkdirSync(screenshots, { recursive: true });
      window.webContents.setBackgroundThrottling(false);
      // Wayland can stop producing compositor frames after capturing a hidden
      // window. Opt-in visual QA uses an unfocused window for reliable captures.
      window.showInactive();
    }
    for (const theme of ["original", "ink", "fresh", "romance", "genshin"]) {
      await run(`document.querySelector('input[name="appearance-theme"][value="${theme}"]').click()`);
      await waitForUI(`document.documentElement.dataset.theme === "${theme}"`, `Theme ${theme} did not apply`);
      const result = await run(`(() => {
        const css = getComputedStyle(document.documentElement);
        const luminance = (hex) => {
          const rgb = hex.trim().slice(1).match(/../g).map(v => parseInt(v, 16) / 255);
          const linear = rgb.map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4);
          return linear[0] * .2126 + linear[1] * .7152 + linear[2] * .0722;
        };
        const contrast = (a, b) => (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
        const failures = [];
        for (const foreground of ["ink", "ink-soft", "muted", "faint", "accent", "amber", "red", "blue"]) {
          for (const background of ["canvas", "sidebar", "panel", "panel-raised", "panel-hover", "input"]) {
            const ratio = contrast(luminance(css.getPropertyValue("--" + foreground)), luminance(css.getPropertyValue("--" + background)));
            if (ratio < 4.5) failures.push({ foreground, background, ratio });
          }
        }
        return { failures, scheme: css.colorScheme, draft: document.getElementById("prompt").value,
          checked: document.querySelector('input[name="appearance-theme"]:checked').value,
          dialogBackground: getComputedStyle(document.getElementById("appearance-dialog")).backgroundColor,
          panelBackground: getComputedStyle(document.querySelector(".prompt-panel")).backgroundColor,
          inputBackground: getComputedStyle(document.getElementById("prompt")).backgroundColor,
          loaded: [...document.styleSheets].some(sheet => sheet.href?.endsWith("/appearance.css") && sheet.cssRules.length > 0) };
      })()`);
      assert.equal(result.loaded, true);
      assert.equal(result.checked, theme);
      assert.equal(result.draft, "换肤不得丢失的创作草稿");
      assert.equal(result.scheme, ["original", "romance"].includes(theme) ? "dark" : "light");
      if (theme !== "original") {
        assert.deepEqual(result.failures, [], `${theme}: text contrast below 4.5:1`);
        assert.equal(result.dialogBackground, result.panelBackground, `${theme}: dialog must use an opaque readable surface`);
        const artwork = await run(`fetch("/themes/${theme}.svg").then(response => ({ ok: response.ok, type: response.headers.get("content-type") }))`);
        assert.equal(artwork.ok, true);
        assert.match(artwork.type, /image\/svg\+xml/);
      }
      await run('document.getElementById("close-appearance").click()');
      if (screenshots && phase === "initial") {
        assert.equal(await run('document.getElementById("appearance-dialog").open'), false);
        await run('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(true))))');
        await new Promise(resolve => setTimeout(resolve, 200));
        fs.writeFileSync(path.join(screenshots, `${theme}.png`), (await window.webContents.capturePage({}, { stayHidden: true, stayAwake: true })).toPNG());
      }
      await run('document.getElementById("open-appearance").click()');
    }
    if (screenshots && phase === "initial") {
      await run('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(true))))');
      await new Promise(resolve => setTimeout(resolve, 200));
      fs.writeFileSync(path.join(screenshots, "picker.png"), (await window.webContents.capturePage({}, { stayHidden: true, stayAwake: true })).toPNG());
    }
    // Force a known media preference, independent of the CI host's desktop settings.
    window.webContents.debugger.attach("1.3");
    try {
      await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "no-preference" }] });
      await waitForUI('!document.getElementById("appearance-motion").disabled', "Motion switch remained disabled");
      await run('if (!document.getElementById("appearance-motion").checked) document.getElementById("appearance-motion").click()');
      await waitForUI('document.documentElement.dataset.motion === "on"', "Motion did not enable");
      assert.equal(await run('getComputedStyle(document.querySelector(".workflow-banner"), "::before").pointerEvents'), "none");
      assert.equal(await run('getComputedStyle(document.querySelector(".workflow-banner"), "::before").animationName'), "ambient-flow");
      await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
      await waitForUI('document.documentElement.dataset.motion === "off"', "Reduced motion was not respected");
      assert.equal(await run('getComputedStyle(document.querySelector(".workflow-banner"), "::before").animationName'), "none");
      assert.equal(await run('document.getElementById("appearance-motion").disabled'), true);
    } finally {
      await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [] });
      window.webContents.debugger.detach();
    }
    if (options.mode !== "regression") {
      await run('document.getElementById("reset-appearance").click()');
      await waitForUI('document.documentElement.dataset.theme === "original" && document.documentElement.dataset.motion === "off"', "Reset did not restore static original");
    }
    await waitForUI('document.getElementById("appearance-status").textContent === "外观已保存"', "Appearance did not finish saving");
    await run('document.getElementById("close-appearance").click(); document.getElementById("prompt").value = ""');
    assert.deepEqual(await run('window.ainovelDesktop.getAppearance()'), options.mode === "regression" ? { theme: "genshin", motion: true } : { theme: "original", motion: false });
  });

  await step("replaceable-media-module-in-desktop", async () => {
    await window.webContents.executeJavaScript('document.querySelector("[data-view=illustrations]").click()');
    await waitForUI('!document.getElementById("view-illustrations").classList.contains("hidden") && typeof window.NovelMedia?.create === "function"', "Chapter media module did not open");
    const media = await window.webContents.executeJavaScript(`({
      cssLoaded: [...document.styleSheets].some(sheet => sheet.href?.endsWith("/modules/media/media.css") && sheet.cssRules.length > 0),
      hasImport: Boolean(document.getElementById("media-upload-form")),
      hasBundle: Boolean(document.getElementById("media-export-bundle")),
      externalCanvas: Boolean(document.querySelector("#media-canvas-form, #media-canvas-link, #media-canvas-url")),
    })`);
    assert.deepEqual(media, { cssLoaded: true, hasImport: true, hasBundle: true, externalCanvas: false });
    await window.webContents.executeJavaScript('document.querySelector("[data-view=overview]").click()');
  });

  const fixturePath = path.join(options.root, "scenario.json");
  if (options.mode === "regression" && phase === "initial") {
    const original = paths.workspaceDir;
    let copied;
    const mockURL = process.env.AINOVEL_DESKTOP_MOCK_URL;
    assert.match(mockURL || "", /^http:\/\/127\.0\.0\.1:\d+\/v1$/);
    const draft = {
      name: "desktop-mock", model: "mock-fast", type: "openai", api: "chat",
      base_url: mockURL, api_key: "", models: ["mock-fast", "mock-careful"],
      context_window: 64000, max_output_tokens: 8000, activate: true,
    };
    await step("local-model-connection-and-library", async () => {
      await api("/api/config/test", draft);
      await api("/api/config", draft);
      await api("/api/config", { ...draft, name: "desktop-secondary", activate: false });
      const config = await api("/api/config");
      assert.equal(config.providers.length, 2);
      assert.equal(config.provider, "desktop-mock");
      assert.equal(config.model, "mock-fast");
    });
    await step("editable-concurrency-ui-and-polling-preserves-draft", async () => {
      await new Promise((resolve, reject) => {
        window.webContents.once("did-finish-load", resolve);
        window.webContents.once("did-fail-load", (_event, code, description) => reject(new Error(`Reload failed ${code}: ${description}`)));
        window.webContents.reload();
      });
      await waitForUI('!document.querySelector("#job-max-concurrent").disabled', "Concurrency form did not load");
      await window.webContents.executeJavaScript('document.querySelector("[data-view=jobs]").click(); document.querySelector("#job-limits-settings > summary").click()');
      await waitForUI('document.querySelectorAll("#job-provider-limits input").length === 2', "Saved connections were not shown in concurrency form");
      const draftState = await window.webContents.executeJavaScript(`(async () => {
        const input = (id, value) => {
          const node = document.getElementById(id);
          node.value = value;
          node.dispatchEvent(new Event("input", { bubbles: true }));
        };
        input("job-max-concurrent", "4096");
        input("job-provider-limit", "128");
        for (const node of document.querySelectorAll("#job-provider-limits input")) {
          node.value = node.dataset.limitProvider === "desktop-mock" ? "64" : "";
          node.dispatchEvent(new Event("input", { bubbles: true }));
        }
        await new Promise((resolve, reject) => {
          const observer = new MutationObserver(() => { clearTimeout(timer); observer.disconnect(); resolve(); });
          const timer = setTimeout(() => { observer.disconnect(); reject(new Error("Task refresh did not complete")); }, 5000);
          observer.observe(document.getElementById("job-list"), { childList: true });
          document.getElementById("refresh-jobs").click();
        });
        return {
          total: document.getElementById("job-max-concurrent").value,
          perProvider: document.getElementById("job-provider-limit").value,
          open: document.getElementById("job-limits-settings").open,
          overrides: Array.from(document.querySelectorAll("#job-provider-limits input"), (node) => [node.dataset.limitProvider, node.value]),
        };
      })()`);
      assert.equal(draftState.total, "4096");
      assert.equal(draftState.perProvider, "128");
      assert.equal(draftState.open, true);
      assert.deepEqual(Object.fromEntries(draftState.overrides), { "desktop-mock": "64", "desktop-secondary": "" });
      await window.webContents.executeJavaScript('document.getElementById("job-limits-form").requestSubmit()');
      await waitForUI('document.getElementById("job-limits-status").textContent.startsWith("并发设置已保存")', "Concurrency UI save did not succeed");
      const limits = await api("/api/jobs/limits");
      assert.equal(limits.editable, true);
      assert.equal(limits.maxSupported, 4096);
      assert.equal(limits.maxConcurrent, 4096);
      assert.equal(limits.providerLimit, 128);
      assert.deepEqual(limits.providerLimits, { "desktop-mock": 64 });
    });
    await step("streaming-cocreate-and-confirm", async () => {
      await api("/api/workspaces", {
        directory: original, name: "桌面回归原项目", genre: "科幻", targetChapters: 1,
      }, "PATCH");
      const session = await api("/api/cocreate", {
        directory: original, text: "写一章月球图书馆短篇，林舟找回旧信，第三人称，温暖结局。",
      });
      assert.equal(session.ready, true);
      assert.ok(session.draftPrompt.includes("月球图书馆"));
      assert.equal(session.transcript.length, 2);
      await api("/api/cocreate/confirm", { directory: original }, "POST", 202);
      assert.equal((await api("/api/cocreate")).confirmed, true);
      const deadline = Date.now() + 5000;
      let pending = false;
      while (Date.now() < deadline) {
        const status = await fetch(mockURL.replace(/\/v1$/, "/__test/status")).then((response) => response.json());
        if (status.pendingStartupRequests > 0) { pending = true; break; }
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      assert.equal(pending, true, "confirmation did not reach startup model generation");
      await api("/api/action", { action: "pause" }, "POST", 202);
      let paused = false;
      while (Date.now() < deadline) {
        const state = await api("/api/state");
        if (!state.busy && !state.stopping && !state.ready) { paused = true; break; }
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      assert.equal(paused, true, "pending startup did not cancel and release the project");
    });
    await step("default-project-copy-and-independent-development", async () => {
      const result = await api("/api/workspaces/copy", { directory: original, name: "桌面独立副本" }, "POST", 201);
      copied = result.directory;
      assert.notEqual(copied, original);
      await api("/api/workspaces", { path: copied });
      await api("/api/workspaces", { directory: copied, name: "桌面副本已修改", genre: "悬疑" }, "PATCH");
      await api("/api/cocreate/reset", { directory: copied });
      assert.equal((await api("/api/cocreate")).confirmed, false);
      await api("/api/workspaces", { path: original });
      const state = await api("/api/state");
      assert.equal(state.project.name, "桌面回归原项目");
      assert.equal(state.project.genre, "科幻");
      assert.equal((await api("/api/cocreate")).confirmed, true);
    });
    await step("create-new-project-and-select-model", async () => {
      const created = await api("/api/workspaces", { create: true, name: "桌面新项目" });
      assert.notEqual(created.directory, copied);
      await api("/api/config/select", { provider: "desktop-secondary", model: "mock-careful" });
      const config = await api("/api/config");
      assert.equal(config.provider, "desktop-secondary");
      assert.equal(config.model, "mock-careful");
      await api("/api/config/test", { ...draft, name: "desktop-secondary", model: "mock-careful" });
      await api("/api/workspaces", { path: original });
      fs.writeFileSync(fixturePath, JSON.stringify({ original, copied, created: created.directory }));
    });
  } else if (options.mode === "regression") {
    const fixture = JSON.parse(fs.readFileSync(fixturePath, "utf8"));
    await step("restart-model-and-project-persistence", async () => {
      const config = await api("/api/config");
      assert.equal(config.provider, "desktop-secondary");
      assert.equal(config.model, "mock-careful");
      assert.equal(config.providers.length, 2);
      assert.equal((await api("/api/cocreate")).confirmed, true);
      await api("/api/workspaces", { path: fixture.copied });
      assert.equal((await api("/api/state")).project.name, "桌面副本已修改");
      assert.equal((await api("/api/cocreate")).confirmed, false);
      await api("/api/workspaces", { path: fixture.original });
    });
    await step("restart-concurrency-settings-persistence", async () => {
      const limits = await api("/api/jobs/limits");
      assert.equal(limits.maxConcurrent, 4096);
      assert.equal(limits.providerLimit, 128);
      assert.deepEqual(limits.providerLimits, { "desktop-mock": 64 });
      await window.webContents.executeJavaScript('document.querySelector("[data-view=jobs]").click(); document.querySelector("#job-limits-settings > summary").click()');
      await waitForUI('document.getElementById("job-max-concurrent").value === "4096" && document.querySelectorAll("#job-provider-limits input").length === 2', "Saved concurrency settings did not appear after restart");
      const form = await window.webContents.executeJavaScript(`({
        providerLimit: document.getElementById("job-provider-limit").value,
        overrides: Object.fromEntries(Array.from(document.querySelectorAll("#job-provider-limits input"), (node) => [node.dataset.limitProvider, node.value])),
      })`);
      assert.equal(form.providerLimit, "128");
      assert.deepEqual(form.overrides, { "desktop-mock": "64", "desktop-secondary": "" });
    });
  }
  await step("sqlite-project-session-and-snapshot-sync", async () => {
    await syncDesktopData();
    assert.ok(database.listProjects().some((project) => project.path === paths.workspaceDir));
    assert.ok(database.listEventSnapshots("", 1).length > 0);
    assert.ok(database.getSetting("lastSyncAt"));
    if (options.mode === "regression") {
      assert.ok(database.listProjects().length >= 3);
      assert.equal(database.getClarificationSession(paths.workspaceDir).confirmed, true);
    }
    database.setSetting("smokeTest", "passed");
  });
  if (options.mode === "regression" && phase === "restart") {
    await step("prepare-active-startup-for-graceful-exit", async () => {
      const fixture = JSON.parse(fs.readFileSync(fixturePath, "utf8"));
      await api("/api/workspaces", { path: fixture.copied });
      await api("/api/cocreate", { directory: fixture.copied, text: "为副本整理月球图书馆单章故事，准备规划。" });
      await api("/api/cocreate/confirm", { directory: fixture.copied }, "POST", 202);
      const deadline = Date.now() + 5000;
      let pending = false;
      while (Date.now() < deadline) {
        const status = await fetch(process.env.AINOVEL_DESKTOP_MOCK_URL.replace(/\/v1$/, "/__test/status")).then((response) => response.json());
        if (status.pendingStartupRequests > 0) { pending = true; break; }
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      assert.equal(pending, true, "shutdown scenario never reached an active model request");
      // Leave it active. The private HTTP shutdown must cancel this request
      // and wait for Host cleanup before the sidecar reports a normal exit.
    });
  }
  return {
    ok: true, mode: options.mode, phase, steps, durationMs: Date.now() - started,
    versions: { app: app.getVersion(), electron: process.versions.electron, node: process.versions.node, chrome: process.versions.chrome },
    url: baseURL, sqlitePath: paths.sqlitePath, workspaceDir: paths.workspaceDir,
    projectCount: database.listProjects().length,
  };
}

module.exports = { runDesktopChecks };
