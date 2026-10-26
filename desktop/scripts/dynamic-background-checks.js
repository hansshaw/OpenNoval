"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

async function runDynamicBackgroundChecks({ window, step, waitForUI, setMotionPreference }) {
  await step("dynamic-skins-render-pixels-and-stop-when-paused", async () => {
    const run = source => window.webContents.executeJavaScript(source);
    const saved = await run("window.ainovelDesktop.getAppearance()");
    const wasVisible = window.isVisible();
    const wasThrottled = window.webContents.getBackgroundThrottling();
    const state = () => run("window.NovelDynamicBackground.getStatus()");
    const pixels = () => run(`(() => {
      const source = document.getElementById('dynamic-background');
      const sample = document.createElement('canvas');
      sample.width = 160; sample.height = 90;
      const context = sample.getContext('2d');
      context.drawImage(source, 0, 0, sample.width, sample.height);
      const data = context.getImageData(0, 0, sample.width, sample.height).data;
      let hash = 2166136261, nonzero = 0;
      for (const value of data) { hash = Math.imul(hash ^ value, 16777619); nonzero += value !== 0; }
      return { hash: hash >>> 0, nonzero };
    })()`);
    const still = async (label) => {
      await waitForUI("!window.NovelDynamicBackground.getStatus().running", `${label}: animation did not stop`);
      const first = await pixels(), count = (await state()).frameCount;
      await new Promise(resolve => setTimeout(resolve, 180));
      assert.deepEqual(await pixels(), first, `${label}: paused canvas pixels changed`);
      assert.equal((await state()).frameCount, count, `${label}: paused engine still renders`);
    };
    window.webContents.setBackgroundThrottling(false);
    // Continuous animation needs a genuinely exposed compositor surface. A
    // visible but occluded X11 window can keep its last frame indefinitely.
    window.show();
    window.moveTop();
    window.focus();
    window.webContents.debugger.attach("1.3");
    try {
      await run("document.querySelector('[data-view=overview]').click()");
      await setMotionPreference("no-preference");
      await waitForUI("!matchMedia('(prefers-reduced-motion: reduce)').matches", "Motion preference did not apply");
      await waitForUI("Boolean(window.NovelDynamicBackground)", "Dynamic background script did not load");
      for (const theme of ["starry", "aurora", "silk"]) {
        // Move away first so this is an explicit new selection even if the
        // persisted preference already names the first skin with motion off.
        await run(`document.querySelector('input[name="appearance-theme"][value="original"]').click(); document.querySelector('input[name="appearance-theme"][value="${theme}"]').click()`);
        await waitForUI(`window.NovelDynamicBackground.getStatus().theme === '${theme}' && window.NovelDynamicBackground.getStatus().running`, `${theme}: choosing the dynamic skin did not start animation`);
        const layout = await run(`(() => {
          const canvas = document.getElementById('dynamic-background');
          const rect = canvas.getBoundingClientRect(), css = getComputedStyle(canvas);
          const button = document.getElementById('open-appearance'), r = button.getBoundingClientRect();
          return { count: document.querySelectorAll('#dynamic-background').length, decorative: canvas.getAttribute('aria-hidden'),
            fixed: css.position, pointerEvents: css.pointerEvents, fillsWindow: rect.width >= document.documentElement.clientWidth - 1 && rect.height >= document.documentElement.clientHeight - 1,
            buttonReachable: button.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)) };
        })()`);
        assert.deepEqual(layout, { count: 1, decorative: "true", fixed: "fixed", pointerEvents: "none", fillsWindow: true, buttonReachable: true });
        const first = await pixels(), count = (await state()).frameCount;
        assert.ok(first.nonzero > 0, `${theme}: background canvas is empty`);
        // Native compositors can throttle occluded windows to about one frame
        // per second even with timer throttling disabled. Verify two actual new
        // frames and changed pixels, not a machine-dependent minimum frame rate.
        // Captures wake the surface without advancing the scene clock ourselves.
        const deadline = Date.now() + 5000;
        while ((await state()).frameCount < count + 2 && Date.now() < deadline) {
          await window.webContents.capturePage({}, { stayAwake: true });
          await new Promise(resolve => setTimeout(resolve, 50));
        }
        assert.ok((await state()).frameCount >= count + 2, `${theme}: fewer than two new animation frames (initial ${count}) ${JSON.stringify(await state())}`);
        assert.notEqual((await pixels()).hash, first.hash, `${theme}: the animation did not change actual pixels`);
        const screenshots = process.env.AINOVEL_APPEARANCE_SCREENSHOTS;
        if (screenshots) {
          fs.mkdirSync(screenshots, { recursive: true });
          fs.writeFileSync(path.join(screenshots, `${theme}-animated.png`), (await window.webContents.capturePage({}, { stayAwake: true })).toPNG());
        }
        await run("document.getElementById('appearance-motion').click()");
        await still(`${theme} manual pause`);
        await run("document.getElementById('appearance-motion').click()");
        await waitForUI("window.NovelDynamicBackground.getStatus().running", `${theme}: manual resume failed`);
        await setMotionPreference("reduce");
        await still(`${theme} reduced motion`);
        assert.equal(await run("document.getElementById('appearance-motion').checked"), true, "System preference must preserve the user's setting");
        await setMotionPreference("no-preference");
        await waitForUI("window.NovelDynamicBackground.getStatus().running", `${theme}: system motion resume failed`);
        // Electron reports the page as visible while background throttling is
        // disabled. Restore normal visibility semantics for this lifecycle check.
        window.webContents.setBackgroundThrottling(true);
        window.hide();
        await waitForUI("document.hidden", "Hidden window did not update visibility");
        await still(`${theme} hidden window`);
        window.show();
        window.moveTop();
        await waitForUI("window.NovelDynamicBackground.getStatus().running", `${theme}: visible window did not resume`);
        window.webContents.setBackgroundThrottling(false);
      }
    } finally {
      try {
        window.showInactive();
        await setMotionPreference("no-preference");
        await run(`document.querySelector('input[name="appearance-theme"][value="${saved.theme}"]').click()`);
        if (saved.theme !== "original") await waitForUI("!document.getElementById('appearance-motion').disabled", "Could not restore motion toggle");
        await run(`if (document.getElementById('appearance-motion').checked !== ${saved.motion}) document.getElementById('appearance-motion').click()`);
        await waitForUI("document.getElementById('appearance-status').textContent === '外观已保存'", "Could not restore dynamic skin preference");
        assert.deepEqual(await run("window.ainovelDesktop.getAppearance()"), saved);
      } finally {
        await setMotionPreference();
        window.webContents.debugger.detach();
        if (!wasVisible) window.hide();
        window.webContents.setBackgroundThrottling(wasThrottled);
      }
    }
  });
}

module.exports = { runDynamicBackgroundChecks };
