"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

async function runDesktopChecks(context) {
  const { app, window, database, paths, baseURL, syncDesktopData, options } = context;
  const steps = [];
  const started = Date.now();
  const phase = process.env.AINOVEL_DESKTOP_TEST_PHASE || "initial";
  const fixturePath = path.join(options.root, "scenario.json");
  let primaryDirectory = phase === "initial" ? "" : JSON.parse(fs.readFileSync(fixturePath, "utf8")).original;
  const result = () => ({
    ok: true, mode: options.mode, phase, steps, durationMs: Date.now() - started,
    versions: { app: app.getVersion(), electron: process.versions.electron, node: process.versions.node, chrome: process.versions.chrome },
    url: baseURL, sqlitePath: paths.sqlitePath, workspaceDir: paths.workspaceDir,
    projectCount: database.listProjects().length,
  });
  async function step(name, action) {
    const start = Date.now();
    if (process.env.AINOVEL_DESKTOP_TEST_TRACE) process.stdout.write(`AINOVEL_DESKTOP_CHECK ${phase} ${name}\n`);
    try { await action(); }
    catch (error) { throw new Error(`${phase} / ${name}: ${error.message}`, { cause: error }); }
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
  async function waitForUI(expression, message, timeoutMs = 5000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      if (await window.webContents.executeJavaScript(expression)) return;
      await new Promise((resolve) => setTimeout(resolve, 30));
    }
    throw new Error(message);
  }
  async function captureReview(name) {
    const folder = process.env.AINOVEL_APPEARANCE_SCREENSHOTS;
    if (!folder) return;
    const visible = window.isVisible(), throttled = window.webContents.getBackgroundThrottling();
    try {
      fs.mkdirSync(folder, { recursive: true });
      window.showInactive(); window.webContents.setBackgroundThrottling(false);
      await window.webContents.capturePage({}, { stayAwake: true });
      await new Promise(resolve => setTimeout(resolve, 100));
      fs.writeFileSync(path.join(folder, name), (await window.webContents.capturePage({}, { stayAwake: true })).toPNG());
    } finally {
      if (!visible) window.hide();
      window.webContents.setBackgroundThrottling(throttled);
    }
  }
  await step("real-window-preload-and-api", async () => {
    const loadedURL = new URL(window.webContents.getURL()); loadedURL.hash = "";
    assert.equal(loadedURL.href, `${baseURL}/`);
    const info = await window.webContents.executeJavaScript(`(async () => ({
      desktop: await window.ainovelDesktop.getInfo(),
      hasUI: Boolean(document.querySelector("#current-project-button") && document.querySelector("#settings-dialog #runtime-model") && !document.querySelector("#view-overview #runtime-model") && !document.querySelector("#config-form #runtime-model")),
      nodeDisabled: typeof require === "undefined" && typeof process === "undefined",
    }))()`);
    assert.equal(info.desktop.sqlitePath, paths.sqlitePath);
    assert.equal(info.hasUI, true);
    assert.equal(info.nodeDisabled, true);
    const state = await api("/api/state");
    const fixture = phase === "initial" ? null : JSON.parse(fs.readFileSync(fixturePath, "utf8"));
    const expectedDirectory = phase === "restart" ? fixture.created : phase === "empty-prepare" ? fixture.copied : "";
    assert.equal(state.directory, expectedDirectory, "Startup must restore the project selected immediately before the last exit");
    assert.equal(state.hasProject, Boolean(expectedDirectory));
    assert.equal(info.desktop.workspaceDir, paths.workspaceDir, "Restoring a project must preserve the stable storage root");
    const config = await api("/api/config");
    assert.ok(config.config_path.startsWith(options.home + path.sep));
    if (phase === "initial") assert.equal(config.providers.length, 0);
    await api("/api/desktop/shutdown", {}, "POST", 403);
  });

  if (phase === "empty-prepare" || phase === "empty-restart") {
    await step(phase === "empty-prepare" ? "delete-all-projects-and-persist-empty-selection" : "restart-remains-empty-without-recreating-default-project", async () => {
      if (phase === "empty-prepare") {
        for (const project of await api("/api/workspaces")) {
          assert.ok(project.path.startsWith(options.root + path.sep), "Deletion fixture escaped its isolated workspace");
          if (!project.archived) await api("/api/workspaces/archive", { directory: project.path, archived: true });
          await api("/api/workspaces/delete", { directory: project.path, confirmed: true });
        }
      }
      const state = await api("/api/state");
      assert.equal(state.directory, ""); assert.equal(state.hasProject, false); assert.equal(state.project, null);
      assert.deepEqual(await api("/api/workspaces"), []);
      await window.webContents.executeJavaScript('document.getElementById("refresh-all").click()');
      await waitForUI('!document.getElementById("view-projects").classList.contains("hidden") && !document.getElementById("project-empty").classList.contains("hidden")', "Empty project center did not render");
      assert.equal(fs.existsSync(path.join(paths.workspaceDir, "meta", "web-project.json")), false);
      assert.equal((await api("/api/config")).providers.length, 2, "Deleting projects must preserve global model connections");
      await syncDesktopData(); assert.deepEqual(database.listProjects(), []);
      assert.equal(database.getSetting("lastProjectPath"), "");
    });
    return result();
  }

  if (phase === "initial") await step("empty-project-center-create-and-delete-the-only-project", async () => {
    const run = source => window.webContents.executeJavaScript(source);
    await waitForUI('!document.getElementById("view-projects").classList.contains("hidden") && !document.getElementById("project-empty").classList.contains("hidden")', "First launch must show an empty project center");
    await captureReview("empty-project-center.png");
    assert.deepEqual(await api("/api/workspaces"), []);
    for (const route of ["/api/cocreate", "/api/planning"]) await api(route, undefined, "GET", 409);
    assert.equal(await run('document.querySelector("[data-view=chapters]").disabled'), true);
    await run('document.getElementById("empty-import-project").click()');
    await waitForUI('!document.getElementById("view-reader").classList.contains("hidden") && !document.getElementById("nr-import-submit").disabled', "Import must remain available without a current project");
    await run('document.querySelector("[data-view=projects]").click(); document.getElementById("empty-create-project").click(); document.getElementById("new-project-mode").value="manual"; document.getElementById("new-project-name").value="可移除的唯一项目"; document.getElementById("create-project-form").requestSubmit()');
    await waitForUI('!document.getElementById("project-dialog").open && !document.getElementById("manual-add-chapter").disabled', "Manual creation from the empty center failed");
    const only = (await api("/api/state")).directory;
    assert.ok(only.startsWith(options.root + path.sep));
    await run('document.querySelector("[data-view=projects]").click(); document.querySelector("#active-project-list [data-project-action=archive]").click()');
    await waitForUI('Boolean(document.querySelector("#archived-project-list [data-project-action=delete]:not(:disabled)"))', "The only project could not be archived for deletion");
    try {
      await run('window.__savedConfirm=window.confirm; window.__deleteConfirms=0; window.confirm=()=>{window.__deleteConfirms++;return true}; document.querySelector("#archived-project-list [data-project-action=delete]").click()');
      await waitForUI('!document.getElementById("project-empty").classList.contains("hidden")', "Deleting the only project must return to the empty center");
      assert.equal(await run('window.__deleteConfirms'), 2);
    } finally { await run('window.confirm=window.__savedConfirm; delete window.__savedConfirm; delete window.__deleteConfirms'); }
    assert.equal(fs.existsSync(only), false);
    assert.equal((await api("/api/state")).directory, "");
    await syncDesktopData(); assert.equal(database.getSetting("lastProjectPath"), "");
    // Subsequent existing scenarios use an explicitly created project instead
    // of relying on an implicit, undeletable application-owned novel.
    primaryDirectory = (await api("/api/workspaces", { create: true, name: "桌面回归原项目" })).directory;
    await new Promise(resolve => { window.webContents.once("did-finish-load", resolve); window.webContents.reload(); });
    await waitForUI(`document.getElementById("current-project-path").title === ${JSON.stringify(primaryDirectory)} && !document.querySelector("[data-view=chapters]").disabled`, "Created project did not unlock its workbench");
    await run('document.querySelector("[data-view=overview]").click()');
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
    const expected = phase === "restart" ? "starry" : "original";
    await waitForUI(`document.documentElement.dataset.theme === ${JSON.stringify(expected)}`, "Appearance did not restore from SQLite");
    if (phase === "initial") assert.equal(await run('document.documentElement.dataset.motion'), "off");
    else {
      assert.deepEqual(await run('window.ainovelDesktop.getAppearance()'), { theme: "starry", motion: false });
      assert.equal(await run('document.documentElement.dataset.motion'), "off", "Restart must preserve a paused dynamic skin");
    }
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
    for (const theme of ["original", "ink", "fresh", "romance", "starry", "aurora", "silk", "genshin"]) {
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
      assert.equal(result.scheme, ["ink", "fresh", "genshin"].includes(theme) ? "light" : "dark");
      if (theme !== "original") {
        assert.deepEqual(result.failures, [], `${theme}: text contrast below 4.5:1`);
        assert.equal(result.dialogBackground, result.panelBackground, `${theme}: dialog must use an opaque readable surface`);
        if (["ink", "fresh", "romance", "genshin"].includes(theme)) {
          const artwork = await run(`fetch("/themes/${theme}.svg").then(response => ({ ok: response.ok, type: response.headers.get("content-type") }))`);
          assert.equal(artwork.ok, true);
          assert.match(artwork.type, /image\/svg\+xml/);
        }
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
    // Media-query change events are dispatched on rendering updates. A hidden
    // Xvfb window can defer them indefinitely even though CSS already sees the
    // emulated preference. Briefly map the window without taking focus, as a
    // user-visible window would be when changing the system motion setting.
    const wasVisible = window.isVisible();
    const wasThrottled = window.webContents.getBackgroundThrottling();
    window.webContents.setBackgroundThrottling(false);
    if (!wasVisible) window.showInactive();
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
      if (options.mode === "regression") {
        await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "no-preference" }] });
        await waitForUI('!document.getElementById("appearance-motion").disabled', "Could not save paused dynamic skin");
        await run('document.querySelector(\'input[name="appearance-theme"][value="starry"]\').click(); if (document.getElementById("appearance-motion").checked) document.getElementById("appearance-motion").click()');
      }
    } finally {
      await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [] });
      window.webContents.debugger.detach();
      if (!wasVisible) window.hide();
      window.webContents.setBackgroundThrottling(wasThrottled);
    }
    if (options.mode !== "regression") {
      await run('document.getElementById("reset-appearance").click()');
      await waitForUI('document.documentElement.dataset.theme === "original" && document.documentElement.dataset.motion === "off"', "Reset did not restore static original");
    }
    await waitForUI('document.getElementById("appearance-status").textContent === "外观已保存"', "Appearance did not finish saving");
    await run('document.getElementById("close-appearance").click(); document.getElementById("prompt").value = ""');
    assert.deepEqual(await run('window.ainovelDesktop.getAppearance()'), options.mode === "regression" ? { theme: "starry", motion: false } : { theme: "original", motion: false });
  });

  if (phase === "initial") await step("all-skins-scaled-layout-scroll-and-dialog-access", async () => {
    const run = source => window.webContents.executeJavaScript(source);
    const bounds = window.getBounds();
    const zoom = window.webContents.getZoomFactor();
    const visible = window.isVisible();
    const throttled = window.webContents.getBackgroundThrottling();
    const saved = await run("window.ainovelDesktop.getAppearance()");
    const systemReducedMotion = await run("matchMedia('(prefers-reduced-motion: reduce)').matches");
    let debuggerAttached = false;
    let requestTracking = false;
    const screenshots = process.env.AINOVEL_APPEARANCE_SCREENSHOTS;
    const capture = async (name) => {
      if (!screenshots) return;
      await run("new Promise(resolve => { requestAnimationFrame(() => requestAnimationFrame(() => resolve(true))); setTimeout(() => resolve(true), 1000); })");
      // Occluded Linux compositors can return their last surface on the first
      // capture; wake the surface before retaining the visual QA image.
      await window.webContents.capturePage({}, { stayHidden: true, stayAwake: true });
      await new Promise(resolve => setTimeout(resolve, 200));
      fs.writeFileSync(path.join(screenshots, name), (await window.webContents.capturePage({}, { stayHidden: true, stayAwake: true })).toPNG());
    };
    // Hidden/occluded Windows and Wayland windows may suppress compositor
    // frames. Bound the wait while still forcing layout before each assertion.
    const settle = async () => {
      await run("void document.body.offsetHeight");
      await new Promise(resolve => setTimeout(resolve, 50));
    };
    const waitForPageLoad = async (scene) => {
      try {
        // Allow transient packaged-CI latency while still waiting for actual
        // response consumption and rendering rather than an arbitrary delay.
        await waitForUI("window.__ainovelLayoutRequests.pending.size === 0", "Page requests did not finish before layout inspection", 15000);
      } catch (error) {
        const pending = await run("Array.from(window.__ainovelLayoutRequests.pending, request => ({ ...request, ageMs: Date.now() - request.startedAt }))");
        throw new Error(`${scene}: ${error.message}: ${JSON.stringify(pending)}`, { cause: error });
      }
      await run("document.fonts.ready.then(() => true)");
      await settle();
    };
    const inspect = () => run(`(() => {
      const topbar = document.querySelector('.topbar').getBoundingClientRect();
      const sidebar = document.querySelector('.sidebar').getBoundingClientRect();
      const failures = [];
      const view = getComputedStyle(document.querySelector('.view:not(.hidden)'));
      if (Number(view.opacity) !== 1 || view.transform !== 'none')
        failures.push('page content depends on an opacity/transform animation');
      if (matchMedia('(max-width: 860px)').matches && sidebar.bottom > topbar.top + 1)
        failures.push('navigation overlaps title bar by ' + (sidebar.bottom - topbar.top));
      if (document.body.scrollWidth > document.documentElement.clientWidth + 1)
        failures.push('content overflows horizontally');
      for (const id of ['open-appearance', 'refresh-all']) {
        const node = document.getElementById(id), rect = node.getBoundingClientRect();
        const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
        if (rect.left < 0 || rect.right > innerWidth + 1 || rect.top < 0 || rect.bottom > innerHeight + 1 || !node.contains(hit))
          failures.push(id + ' is clipped or covered');
      }
      return failures;
    })()`);
    try {
      window.webContents.setBackgroundThrottling(false);
      if (!visible) window.showInactive();
      // Exercise animated skins and restore the saved toggle through the UI
      // even when the Windows runner disables animations in its OS settings.
      // Restore that system preference only after the durable setting is saved.
      window.webContents.debugger.attach("1.3");
      debuggerAttached = true;
      await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "no-preference" }] });
      await waitForUI("!matchMedia('(prefers-reduced-motion: reduce)').matches", "Layout motion preference did not apply");
      // Navigation starts asynchronous API reads, especially loadPlanning.
      // Observe body consumption and its render continuation instead of
      // assuming a fixed delay means the page's final height is available.
      await run(`(() => {
        const originalFetch = window.fetch;
        const pending = new Set();
        window.__ainovelLayoutRequests = { pending, restore: () => { window.fetch = originalFetch; } };
        window.fetch = async (...args) => {
          const url = new URL(args[0] instanceof Request ? args[0].url : String(args[0]), location.href);
          if (url.origin !== location.origin || !url.pathname.startsWith('/api/')) return originalFetch(...args);
          const token = { path: url.pathname, stage: 'headers', startedAt: Date.now() };
          pending.add(token);
          const finished = () => {
            token.stage = 'consumed';
            token.finishedAt = Date.now();
            setTimeout(() => pending.delete(token), 0);
          };
          try {
            const response = await originalFetch(...args);
            token.stage = 'body';
            for (const method of ['json', 'text', 'arrayBuffer', 'blob', 'formData']) {
              const read = response[method].bind(response);
              response[method] = async (...parameters) => {
                token.stage = method;
                try { return await read(...parameters); }
                finally { finished(); }
              };
            }
            return response;
          } catch (error) { finished(); throw error; }
        };
      })()`);
      requestTracking = true;
      for (const scale of [1, 1.25, 1.5]) {
        window.setSize(1080, 720);
        window.webContents.setZoomFactor(scale);
        await settle();
        for (const theme of ["original", "ink", "fresh", "romance", "genshin", "starry", "aurora", "silk"]) {
          await run(`document.querySelector('input[name="appearance-theme"][value="${theme}"]').click()`);
          if (theme !== "original") await run("if (!document.getElementById('appearance-motion').disabled && !document.getElementById('appearance-motion').checked) document.getElementById('appearance-motion').click()");
          if (theme !== "original") assert.equal(await run("getComputedStyle(document.querySelector('.current-project')).backgroundColor === getComputedStyle(document.querySelector('.prompt-panel')).backgroundColor"), true, `${theme}: control backgrounds must switch with their text colors`);
          await settle();
          for (const view of ["overview", "profile", "planning", "chapters", "illustrations", "activity"]) {
            await run(`document.querySelector('[data-view="${view}"]').click(); window.scrollTo({ top: 0, left: 0, behavior: 'instant' })`);
            await waitForPageLoad(`${theme} ${scale * 100}% ${view}`);
            assert.deepEqual(await inspect(), [], `${theme} ${scale * 100}% ${view} top`);
            if (theme === "genshin" && scale === 1.25 && ["chapters", "illustrations"].includes(view)) await capture(`genshin-125-${view}.png`);
            await run("window.scrollTo({ top: document.scrollingElement.scrollHeight, behavior: 'instant' })");
            await settle();
            assert.deepEqual(await inspect(), [], `${theme} ${scale * 100}% ${view} bottom`);
            const bottom = await run("({ top: scrollY, viewport: innerHeight, height: document.scrollingElement.scrollHeight })");
            assert.ok(bottom.top + bottom.viewport >= bottom.height - 2, `${theme} ${scale * 100}% ${view}: page bottom is unreachable ${JSON.stringify(bottom)}`);
          }
          if (scale === 1.5) {
            window.hide();
            await run("document.querySelector('[data-view=chapters]').click()");
            await settle();
            assert.equal(await run("getComputedStyle(document.getElementById('view-chapters')).opacity"), "1", `${theme}: a hidden window froze page content transparent`);
            window.showInactive();
            await settle();
            assert.deepEqual(await inspect(), [], `${theme}: restored window content is covered`);
          }
          for (const [openID, dialogID, closeID] of [["open-appearance", "appearance-dialog", "close-appearance"], ["open-settings", "settings-dialog", "close-settings"]]) {
            await run(`document.getElementById('${openID}').click(); document.getElementById('${dialogID}').scrollTop = 0`);
            await settle();
            const dialog = await run(`(() => {
              const node = document.getElementById('${dialogID}');
              const rect = node.getBoundingClientRect();
              const close = document.getElementById('${closeID}');
              const button = close.getBoundingClientRect();
              const hit = document.elementFromPoint(button.x + button.width / 2, button.y + button.height / 2);
              return { open: node.open, inViewport: rect.left >= 0 && rect.right <= innerWidth + 1 && rect.top >= 0 && rect.bottom <= innerHeight + 1,
                closeReachable: close.contains(hit), fitsWidth: node.scrollWidth <= node.clientWidth + 1 };
            })()`);
            assert.deepEqual(dialog, { open: true, inViewport: true, closeReachable: true, fitsWidth: true }, `${theme} ${scale * 100}% ${dialogID}`);
            if (theme === "genshin" && scale === 1.25 && dialogID === "settings-dialog") await capture("genshin-125-settings.png");
            await run(`document.getElementById('${dialogID}').scrollTop = document.getElementById('${dialogID}').scrollHeight`);
            await settle();
            assert.equal(await run(`(() => { const node = document.getElementById('${dialogID}'); return node.scrollTop + node.clientHeight >= node.scrollHeight - 2; })()`), true, `${theme} ${dialogID}: bottom controls cannot be scrolled into view`);
            await run(`document.getElementById('${closeID}').click()`);
          }
        }
      }
    } finally {
      try {
        if (requestTracking) await run("window.__ainovelLayoutRequests.restore(); delete window.__ainovelLayoutRequests");
        await run(`for (const dialog of document.querySelectorAll('dialog[open]')) dialog.close(); document.querySelector('input[name="appearance-theme"][value="${saved.theme}"]').click()`);
        if (saved.theme !== "original") await waitForUI("!document.getElementById('appearance-motion').disabled", "Saved motion preference cannot be restored through the UI");
        await run(`if (document.getElementById('appearance-motion').checked !== ${saved.motion}) document.getElementById('appearance-motion').click(); document.querySelector('[data-view="overview"]').click(); window.scrollTo(0, 0)`);
        await waitForUI('document.getElementById("appearance-status").textContent === "外观已保存"', "Layout checks did not restore the saved theme");
        assert.deepEqual(await run("window.ainovelDesktop.getAppearance()"), saved, "Layout checks changed the durable appearance preference");
      } finally {
        try {
          if (debuggerAttached) {
            if (!window.isVisible()) window.showInactive();
            await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [] });
            await waitForUI(`matchMedia('(prefers-reduced-motion: reduce)').matches === ${systemReducedMotion} && document.getElementById('appearance-motion').disabled === ${saved.theme === "original" || systemReducedMotion}`, "Layout checks did not restore the system motion preference");
            assert.deepEqual(await run("window.ainovelDesktop.getAppearance()"), saved, "Restoring the system motion preference changed the saved setting");
          }
        } finally {
          if (debuggerAttached) window.webContents.debugger.detach();
          window.webContents.setZoomFactor(zoom);
          window.setBounds(bounds);
          await settle();
          if (!visible) window.hide();
          window.webContents.setBackgroundThrottling(throttled);
        }
      }
    }
  });

  if (phase === "initial") {
    const { runChapterScrollChecks } = require("./chapter-scroll-checks");
    await runChapterScrollChecks({ window, paths, options, api, step, waitForUI });
    const { runDynamicBackgroundChecks } = require("./dynamic-background-checks");
    await runDynamicBackgroundChecks({ window, step, waitForUI });
  }

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

  if (options.mode === "regression" && phase === "initial") {
    const original = primaryDirectory;
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
    await step("edit-existing-model-and-restore-upstream-defaults", async () => {
      const run = source => window.webContents.executeJavaScript(source);
      await run('document.getElementById("open-settings").click(); document.getElementById("settings-text-tab").click()');
      await waitForUI('document.getElementById("saved-model").value === "mock-fast"', "Saved model editor did not select the active model");
      assert.deepEqual(await run('(() => { const f = document.getElementById("config-form"); return [f.elements.context_window.value, f.elements.max_output_tokens.value]; })()'), ["64000", "8000"]);
      await run('(() => { const f = document.getElementById("config-form"); f.elements.context_window.value = "48000"; f.elements.max_output_tokens.value = "7000"; f.requestSubmit(); })()');
      await waitForUI('!document.getElementById("saved-model").disabled && document.getElementById("config-status").textContent.includes("模型配置已保存")', "Existing model edits did not save", 15000);
      let config = await api("/api/config");
      let detail = config.providers.find(provider => provider.name === "desktop-mock").model_details.find(model => model.name === "mock-fast");
      assert.equal(detail.context_window, 48000);
      assert.equal(detail.max_output_tokens, 7000);
      assert.equal(detail.context_source, "model_config");
      await run('document.getElementById("fetch-models").click()');
      await waitForUI('!document.getElementById("fetch-models").disabled && document.getElementById("config-status").textContent.includes("已获取")', "Upstream model list did not load", 15000);
      assert.deepEqual(await run('Array.from(document.getElementById("model-options").options, option => option.value).sort()'), ["mock-careful", "mock-fast"]);
      // Clearing both overrides must return to automatic values, not retain
      // the old explicit numbers or turn discovered limits into overrides.
      await run('document.getElementById("reset-model-limits").click(); document.getElementById("config-form").requestSubmit()');
      await waitForUI('!document.getElementById("saved-model").disabled && document.getElementById("config-status").textContent.includes("模型配置已保存")', "Automatic model limits did not save", 15000);
      config = await api("/api/config");
      detail = config.providers.find(provider => provider.name === "desktop-mock").model_details.find(model => model.name === "mock-fast");
      assert.equal(detail.configured_context_window, 0);
      assert.equal(detail.configured_max_output_tokens, 0);
      assert.equal(detail.context_window, 32000);
      assert.equal(detail.max_output_tokens, 12000);
      assert.equal(detail.context_source, "upstream");
      assert.equal(detail.output_source, "upstream");
      assert.equal(config.providers.find(provider => provider.name === "desktop-secondary").model_details.find(model => model.name === "mock-fast").configured_context_window, 64000);
      await run('document.getElementById("close-settings").click()');
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
      assert.equal((await api("/api/state")).directory, fixture.created);
      assert.equal((await api("/api/cocreate")).confirmed, false);
      await api("/api/workspaces", { path: fixture.original });
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
  if (options.mode === "regression") {
    if (phase === "initial") await step("readable-planning-rules-and-review-with-safe-prose", async () => {
      const run = source => window.webContents.executeJavaScript(source);
      const original = (await api("/api/state")).directory;
      const created = await api("/api/manual/projects", { name: "设定与审核阅读验收" }, "POST", 201);
      assert.ok(created.directory.startsWith(options.root + path.sep));
      const hostile = '<img src=x onerror="window.__reviewInjected=true">';
      const fixtures = {
        "premise.md": "## 灯塔来信\n在海边重建彼此的信任。",
        "meta/compass.json": { ending_direction: "灯塔重新亮起", open_threads: ["找回未寄出的信"], estimated_scale: "一卷" },
        "meta/foundation_audit.json": { fingerprint: "readable-test", ready: false, summary: "## 审查结论\n需统一灯塔开放时间。", issues: [{ artifact: "world_rules", description: "开放时间不一致", evidence: "人物档案与世界规则中的时间不同", suggestion: "将开放时间统一为日落之后" }] },
        "characters.json": [{ name: "林舟", role: "主角", description: "守护旧灯塔的年轻人", arc: "从逃避到直面过去", traits: ["谨慎", "重诺"], tier: "core" }],
        "world_rules.json": [{ category: "society", rule: `灯塔只在日落后开放。${hostile}`, boundary: "禁止擅自改变开放时间" }],
        "layered_outline.json": [{ index: 1, title: "灯塔之卷", theme: "重建信任", final: true, arcs: [{ index: 1, title: "旧信重现", goal: "找到信的主人", chapters: [{ chapter: 1, title: "海风与旧信", core_event: "林舟找到旧信", hook: "信件没有署名", scenes: ["日落前抵达灯塔"] }] }] }],
        "chapters/01.md": "日落时分，林舟推开灯塔的门，找到一封未寄出的信。",
        "meta/progress.json": { phase: "writing", total_chapters: 1, completed_chapters: [1], current_chapter: 1 },
        "reviews/01.json": { chapter: 1, scope: "chapter", verdict: "polish", summary: "## 章节评审\n人物动机明确，需要统一时间。", issues: [{ type: "连续性", severity: "warning", description: "日落前进入灯塔", evidence: "林舟提前抵达", suggestion: "调整到日落后", chapters: [1], requires_change: true }], dimensions: [{ dimension: "连续性", score: 80, comment: "需完善时间衔接" }] },
      };
      const contents = Object.fromEntries(Object.entries(fixtures).map(([name, value]) => [name, typeof value === "string" ? value : JSON.stringify(value)]));
      for (const [name, content] of Object.entries(contents)) {
        const file = path.join(created.directory, name); fs.mkdirSync(path.dirname(file), { recursive: true }); fs.writeFileSync(file, content);
      }
      try {
        await api("/api/workspaces", { path: created.directory });
        await new Promise(resolve => { window.webContents.once("did-finish-load", resolve); window.webContents.reload(); });
        await waitForUI(`document.getElementById("current-project-path").title === ${JSON.stringify(created.directory)} && typeof window.NovelReadableData?.render === "function"`, "Readable review project state did not load");
        await run('document.querySelector("[data-view=planning]").click()');
        await waitForUI('document.getElementById("plan-foundation-audit").textContent.includes("需统一灯塔开放时间")', "Foundation audit did not render from saved project");
        assert.equal(await run('document.querySelectorAll("#view-planning pre.json-pre").length'), 0);
        assert.ok(await run('document.querySelectorAll("#plan-characters .readable-record, #plan-world-rules .readable-record").length >= 2'));
        assert.equal(await run('document.getElementById("plan-world-rules").textContent.includes("<img src=x")'), true);
        assert.equal(await run('Boolean(document.querySelector("#plan-world-rules img") || window.__reviewInjected)'), false);
        assert.ok(await run('Boolean(document.querySelector("#plan-foundation-audit [data-field=ready]"))'), "False readiness must not disappear");
        assert.ok(await run('document.getElementById("plan-layered").textContent.includes("灯塔之卷")'));
        if (process.env.AINOVEL_APPEARANCE_SCREENSHOTS) {
          await run('document.getElementById("plan-foundation-audit").scrollIntoView({ block: "center" })');
          await captureReview("readable-planning.png");
        }
        await run('document.querySelector("[data-view=audit]").click()');
        await waitForUI('document.getElementById("audit-review").textContent.includes("人物动机明确")', "Chapter audit did not render readable review");
        assert.equal(await run('document.querySelectorAll("#view-audit pre.json-pre").length'), 0);
        assert.ok(await run('document.querySelectorAll("#audit-review .readable-record").length >= 2'));
        for (const [name, content] of Object.entries(contents)) assert.equal(fs.readFileSync(path.join(created.directory, name), "utf8"), content, "Reading reviews must not rewrite project data");
      } finally {
        await api("/api/workspaces", { path: original });
        await new Promise(resolve => { window.webContents.once("did-finish-load", resolve); window.webContents.reload(); });
        await waitForUI(`document.getElementById("current-project-path").title === ${JSON.stringify(original)}`, "Workspace did not return after review checks");
      }
    });
    const { runMediaChecks } = require("./media-checks");
    await runMediaChecks({ window, options, paths, phase, api, step, waitForUI });
    const { runReaderChecks } = require("./reader-checks");
    await runReaderChecks({ window, options, paths, phase, api, step, waitForUI });
  }
  await step("sqlite-project-session-and-snapshot-sync", async () => {
    await syncDesktopData();
    assert.ok(database.listProjects().some((project) => project.path === primaryDirectory));
    assert.ok(database.listEventSnapshots("", 1).length > 0);
    assert.ok(database.getSetting("lastSyncAt"));
    if (options.mode === "regression") {
      assert.ok(database.listProjects().length >= 3);
      assert.equal(database.getClarificationSession(primaryDirectory).confirmed, true);
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
  if (options.mode === "regression" && phase === "initial") {
    await step("select-project-immediately-before-exit", async () => {
      const fixture = JSON.parse(fs.readFileSync(fixturePath, "utf8"));
      await api("/api/workspaces", { path: fixture.created });
      // A stale poll may have persisted the previous selection. Quit without
      // another renderer refresh or explicit sync; shutdown must read it afresh.
      database.setSetting("lastProjectPath", fixture.original);
    });
  }
  return result();
}

module.exports = { runDesktopChecks };
