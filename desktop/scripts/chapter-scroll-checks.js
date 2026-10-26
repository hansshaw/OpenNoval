"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

// Exercise actual Chromium scrolling against saved chapter files. No model
// requests or user projects are involved in this scenario.
async function runChapterScrollChecks({ window, options, api, step, waitForUI }) {
  const originalDirectory = (await api("/api/state")).directory;
  const run = source => window.webContents.executeJavaScript(source);
  const savedBounds = window.getBounds(), savedZoom = window.webContents.getZoomFactor();
  const wasVisible = window.isVisible(), wasThrottled = window.webContents.getBackgroundThrottling();
  const appearance = await run("window.ainovelDesktop.getAppearance()");
  const settle = async () => {
    await run("document.getElementById('chapter-workspace').getBoundingClientRect().height");
    await new Promise(resolve => setTimeout(resolve, 50));
  };
  const capture = async name => {
    const folder = process.env.AINOVEL_APPEARANCE_SCREENSHOTS;
    if (!folder) return;
    fs.mkdirSync(folder, { recursive: true });
    window.show();
    window.moveTop();
    // The first capture may contain the compositor's old occluded surface.
    await window.webContents.capturePage({}, { stayAwake: true });
    await new Promise(resolve => setTimeout(resolve, 200));
    fs.writeFileSync(path.join(folder, name), (await window.webContents.capturePage({}, { stayAwake: true })).toPNG());
  };
  const settleViewport = async () => {
    // An occluded X11/Windows window can postpone ResizeObserver even with
    // background throttling disabled. A real compositor frame settles the
    // zoom-dependent sticky headers before testing a user navigation action.
    await window.webContents.capturePage({ x: 0, y: 0, width: 1, height: 1 }, { stayAwake: true });
    const [width, height] = window.getContentSize(), zoom = window.webContents.getZoomFactor();
    await waitForUI(`(() => {
      const measured = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--sticky-header-height'));
      const expected = document.querySelector('.topbar').getBoundingClientRect().height +
        (matchMedia('(max-width: 860px)').matches ? document.querySelector('.sidebar').getBoundingClientRect().height : 0);
      return Math.abs(innerWidth - ${width / zoom}) <= 1 && Math.abs(innerHeight - ${height / zoom}) <= 1 && Math.abs(measured - expected) < .5;
    })()`, "Resized chapter viewport and sticky header measurements did not settle");
    await settle();
  };
  const reachable = async id => {
    const result = await run(`(() => {
      const node = document.getElementById(${JSON.stringify(id)}), rect = node.getBoundingClientRect();
      const x = rect.x + rect.width / 2, y = rect.y + Math.min(rect.height / 2, 16), hit = document.elementFromPoint(x, y);
      return { reachable: rect.width > 0 && rect.height > 0 && x > 0 && x < innerWidth && y > 0 && y < innerHeight && node.contains(hit),
        viewport: [innerWidth, innerHeight], rect: rect.toJSON(), hit: hit?.id || hit?.className,
        workspace: document.getElementById('chapter-workspace').getBoundingClientRect().toJSON(),
        toolbar: document.getElementById('chapter-workspace-toolbar').getBoundingClientRect().toJSON(),
        header: document.querySelector('.topbar').getBoundingClientRect().toJSON() };
    })()`);
    if (!result.reachable) process.stderr.write(`Chapter target ${id} is covered: ${JSON.stringify(result)}\n`);
    return result.reachable;
  };
  const jump = async region => {
    await run(`document.getElementById('chapter-jump-${region}').click()`);
    await settle();
    for (const target of ["directory", "reader", "editor", "feedback"]) {
      assert.equal(await reachable(`chapter-jump-${target}`), true, `The ${target} shortcut is covered after navigating to ${region}`);
    }
  };
  let directory, primaryError;
  window.webContents.setBackgroundThrottling(false);
  window.showInactive();
  window.webContents.debugger.attach("1.3");
  try {
    await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "no-preference" }] });
    await step("chapter-independent-scroll-and-draft-retention", async () => {
      const project = await api("/api/workspaces", { create: true, name: "章节独立滚动验收" });
      directory = project.directory;
      assert.ok(directory.startsWith(options.root + path.sep), "Scroll fixture must remain in the isolated test workspace");
      await api("/api/workspaces", { path: originalDirectory });
      for (const folder of ["chapters", "meta"]) fs.mkdirSync(path.join(directory, folder), { recursive: true });
      const longBody = Array.from({ length: 320 }, (_, index) => `第${index + 1}段：林舟走过月球书库，循着旧信上的文字寻找故事里的答案。窗外的星光照亮长廊，他翻开书页，把未曾说出口的思念写进新的篇章。`).join("\n\n");
      const outline = Array.from({ length: 48 }, (_, index) => ({ chapter: index + 1, title: `滚动验收第${index + 1}章`, volume_id: Math.floor(index / 12) + 1, volume_title: `测试卷${Math.floor(index / 12) + 1}`, core_event: "目录和正文独立滚动", hook: "长章结尾", scenes: ["书库", "长廊"] }));
      fs.writeFileSync(path.join(directory, "outline.json"), JSON.stringify(outline));
      fs.writeFileSync(path.join(directory, "chapters/01.md"), longBody);
      fs.writeFileSync(path.join(directory, "chapters/02.md"), "第二章：这段正文用于检查切章后的局部滚动与草稿保留。");
      fs.writeFileSync(path.join(directory, "meta/progress.json"), JSON.stringify({ phase: "writing", total_chapters: 48, completed_chapters: [1, 2], current_chapter: 2, chapter_word_counts: { 1: longBody.length, 2: 30 } }));
      await api("/api/workspaces", { path: directory });
      await new Promise(resolve => { window.webContents.once("did-finish-load", resolve); window.webContents.reload(); });
      await waitForUI(`document.getElementById('current-project-path').title === ${JSON.stringify(directory)}`, "Scroll project state did not load");
      await run("document.querySelector('[data-view=chapters]').click()");
      await waitForUI('document.querySelectorAll("#chapter-list .chapter-item").length === 48', "Scroll fixture chapters did not load");
      window.webContents.setZoomFactor(1);
      window.setBounds({ width: 1600, height: 1000 });
      await settleViewport();
      await run(`document.querySelector('[data-view=chapters]').click(); document.querySelector('#chapter-list .chapter-item').click()`);
      await waitForUI('document.getElementById("reader-content").textContent.includes("第320段") && !document.getElementById("edit-chapter-content").disabled', "Long chapter did not load in reader and editor");
      // The editor and version history load independently. The version reader
      // hides the ordinary reader's summary (about 66px tall), which naturally
      // clamps a scrollbar at its bottom. Measure only after both have rendered.
      await waitForUI(`(() => {
        const selected = document.querySelector('#chapter-version-list .chapter-version-button.selected');
        return selected?.textContent.includes('当前正式版') &&
          document.getElementById('reader-meta').textContent.startsWith('第 1 章 · 版本 1') &&
          !document.getElementById('reload-chapter-versions').disabled &&
          document.getElementById('reader-notes-wrap').classList.contains('hidden');
      })()`, "Long chapter version rendering did not settle");
      await settle();
      await jump("reader");
      const before = await run(`(() => {
        const ids = ['chapter-reader-scroll', 'chapter-workspace', 'chapter-list', 'chapter-actions'];
        const nodes = ids.map(id => document.getElementById(id));
        nodes.forEach(node => node.scrollTop = 0);
        return Object.fromEntries(nodes.map((node, index) => [ids[index], { height: node.clientHeight, total: node.scrollHeight }]));
      })()`);
      assert.ok(before["chapter-reader-scroll"].height > 120 && before["chapter-reader-scroll"].total > before["chapter-reader-scroll"].height * 8, JSON.stringify(before));
      assert.ok(before["chapter-list"].total > before["chapter-list"].height, "Directory must have its own scrollbar");
      const independent = await run(`(() => {
        const reader = document.getElementById('chapter-reader-scroll'), list = document.getElementById('chapter-list');
        const work = document.getElementById('chapter-workspace'), actions = document.getElementById('chapter-actions');
        const page = document.scrollingElement.scrollTop;
        reader.scrollTop = reader.scrollHeight;
        const afterReader = { work: work.scrollTop, list: list.scrollTop, actions: actions.scrollTop, page: document.scrollingElement.scrollTop };
        const body = reader.scrollTop;
        list.scrollTop = list.scrollHeight;
        return { afterReader, page, body, readerStayed: reader.scrollTop === body, directoryMoved: list.scrollTop > 0, bodyAtEnd: reader.scrollHeight - reader.clientHeight - reader.scrollTop <= 1 };
      })()`);
      assert.deepEqual(independent.afterReader, { work: 0, list: 0, actions: 0, page: independent.page });
      assert.ok(independent.body > 0 && independent.bodyAtEnd && independent.directoryMoved && independent.readerStayed);
      await jump("feedback");
      assert.equal(await reachable("chapter-feedback"), true, "Instruction input is covered after its direct shortcut");
      assert.equal(await run('document.activeElement.id'), "chapter-feedback");
      assert.equal(await run('document.getElementById("chapter-reader-scroll").scrollTop'), independent.body, "Instructions must not require resetting or traversing the manuscript");
      await jump("editor");
      await waitForUI('!document.getElementById("chapter-editor").classList.contains("hidden")', "Editor shortcut did not open editor");
      const draft = `${longBody}\n\n本地未保存草稿标记`;
      await run(`(() => { const field = document.getElementById('edit-chapter-content'); field.value = ${JSON.stringify(draft)}; field.dispatchEvent(new Event('input')); field.scrollTop = field.scrollHeight; })()`);
      assert.equal(await reachable("save-chapter-content"), true, "Save action must remain visible independently of long editor text");
      await run("document.getElementById('chapter-read-tab').click(); document.getElementById('chapter-edit-tab').click()");
      assert.equal(await run('document.getElementById("edit-chapter-content").value'), draft, "Read/edit switching lost draft text");
      await run("document.querySelectorAll('#chapter-list .chapter-item')[1].click()");
      await waitForUI('document.getElementById("edit-chapter-content").value.startsWith("第二章：")', "Second chapter did not load");
      await run("document.querySelector('#chapter-list .chapter-item').click()");
      await waitForUI('document.getElementById("edit-chapter-content").value.endsWith("本地未保存草稿标记")', "Switching chapters lost draft text");
      assert.equal(fs.readFileSync(path.join(directory, "chapters/01.md"), "utf8"), longBody, "An unsaved draft changed disk content");
      await run("document.getElementById('chapter-read-tab').click()");
    });

    await step("chapter-scroll-controls-at-windows-scaling", async () => {
      const scenarios = [
        { width: 1600, height: 1000, zoom: 1, theme: "original" },
        { width: 1480, height: 900, zoom: 1, theme: "genshin" },
        { width: 1280, height: 900, zoom: 1.25, theme: "ink" },
        { width: 1080, height: 760, zoom: 1.5, theme: "fresh" },
        { width: 1080, height: 760, zoom: 1.5, theme: "romance" },
        { width: 1080, height: 760, zoom: 1.5, theme: "starry" },
        { width: 1280, height: 900, zoom: 1.25, theme: "aurora" },
        { width: 1080, height: 760, zoom: 1.5, theme: "silk" },
      ];
      let outerDidScroll = false;
      for (const scenario of scenarios) {
        const label = JSON.stringify(scenario);
        window.setBounds({ width: scenario.width, height: scenario.height });
        window.webContents.setZoomFactor(scenario.zoom);
        await run(`document.querySelector('input[name="appearance-theme"][value="${scenario.theme}"]').click()`);
        await waitForUI(`document.documentElement.dataset.theme === '${scenario.theme}'`, `Skin did not apply: ${label}`);
        await settleViewport();
        await jump("feedback");
        assert.equal(await reachable("chapter-feedback"), true, `Instruction input is covered: ${label}`);
        if (scenario.theme === "starry" && process.env.AINOVEL_APPEARANCE_SCREENSHOTS) {
          await capture("chapter-workspace-starry-150.png");
        }
        const sizes = await run(`(() => {
          const work = document.getElementById('chapter-workspace'), reader = document.getElementById('chapter-reader-scroll');
          return { documentOverflow: document.documentElement.scrollWidth - innerWidth, workOverflow: work.scrollWidth - work.clientWidth,
            readerHeight: reader.clientHeight, readerTotal: reader.scrollHeight, workHeight: work.clientHeight, workTotal: work.scrollHeight,
            scrollbar: getComputedStyle(reader).overflowY, gutter: getComputedStyle(reader).scrollbarGutter };
        })()`);
        assert.ok(sizes.documentOverflow <= 1 && sizes.workOverflow <= 1, `Unexpected horizontal clipping: ${label} ${JSON.stringify(sizes)}`);
        assert.equal(sizes.scrollbar, "auto");
        assert.match(sizes.gutter, /stable/);
        assert.ok(sizes.readerHeight >= 80 && sizes.readerTotal > sizes.readerHeight, `Reader is not bounded: ${label} ${JSON.stringify(sizes)}`);
        const scroll = await run(`(() => {
          const work = document.getElementById('chapter-workspace'), reader = document.getElementById('chapter-reader-scroll'), list = document.getElementById('chapter-list');
          reader.scrollTop = 120; list.scrollTop = 80;
          const body = reader.scrollTop, directory = list.scrollTop;
          work.scrollTop = work.scrollHeight;
          return { moved: work.scrollTop > 0, body: reader.scrollTop === body, directory: list.scrollTop === directory, atEnd: work.scrollHeight - work.clientHeight - work.scrollTop <= 1 };
        })()`);
        outerDidScroll ||= scroll.moved;
        assert.ok(scroll.body && scroll.directory && scroll.atEnd, `Outer scrolling moved inner regions: ${label}`);
        await jump("feedback");
        assert.equal(await reachable("chapter-feedback"), true, `Instruction shortcut cannot escape bottom of workbench: ${label}`);
        await run("document.getElementById('chapter-actions').scrollTop = document.getElementById('chapter-actions').scrollHeight");
        await settle();
        // On short windows the instruction pane is taller than the outer
        // workspace. Reach its bottom using the independent outer scrollbar,
        // without traversing or moving the manuscript.
        const bodyStayed = await run(`(() => {
          const reader = document.getElementById('chapter-reader-scroll'), body = reader.scrollTop;
          const work = document.getElementById('chapter-workspace');
          const control = document.getElementById('apply-chapter-version').getBoundingClientRect();
          work.scrollTop += Math.max(0, control.bottom - work.getBoundingClientRect().bottom + 8);
          return reader.scrollTop === body;
        })()`);
        await settle();
        assert.equal(bodyStayed, true, `Reaching a bottom action moved the manuscript: ${label}`);
        assert.equal(await reachable("apply-chapter-version"), true, `Bottom version action is unreachable: ${label}`);
        await run("document.getElementById('chapter-plan-editor').open = true; document.getElementById('chapter-editor-fields').scrollTop = 0");
        await jump("editor");
        assert.equal(await reachable("edit-chapter-content"), true, `Expanded planning left the focused manuscript offscreen: ${label}`);
        assert.equal(await reachable("save-chapter-content"), true, `Fixed editor save bar is unreachable: ${label}`);
        assert.equal(await reachable("chapter-jump-feedback"), true, `Workspace toolbar is hidden under global navigation: ${label}`);
        await run("document.getElementById('chapter-read-tab').click()");
        await jump("reader");
        assert.equal(await reachable("chapter-reader-scroll"), true, `Reader shortcut is covered: ${label}`);
        await jump("directory");
        assert.equal(await reachable("chapter-search"), true, `Directory shortcut is covered: ${label}`);
      }
      assert.ok(outerDidScroll, "Narrow workbench did not have an independent outer scrollbar");
      window.webContents.setZoomFactor(1);
      window.setBounds({ width: 1600, height: 1000 });
      await settleViewport();
      await run("document.querySelector('input[name=appearance-theme][value=genshin]').click(); document.getElementById('chapter-reader-scroll').scrollTop = 0; document.getElementById('chapter-actions').scrollTop = 0; document.getElementById('chapter-list').scrollTop = 0; document.getElementById('chapter-workspace').scrollTop = 0");
      await settleViewport();
      await jump("reader");
      const screenshots = process.env.AINOVEL_APPEARANCE_SCREENSHOTS;
      if (screenshots) {
        await capture("chapter-workspace.png");
      }
    });
  } catch (error) {
    primaryError = error;
    process.stderr.write(`Chapter scroll check failed: ${error.stack || error}\n`);
    if (process.env.AINOVEL_APPEARANCE_SCREENSHOTS) {
      fs.mkdirSync(process.env.AINOVEL_APPEARANCE_SCREENSHOTS, { recursive: true });
      fs.writeFileSync(path.join(process.env.AINOVEL_APPEARANCE_SCREENSHOTS, "chapter-workspace-failure.png"), (await window.webContents.capturePage({}, { stayAwake: true })).toPNG());
    }
    throw error;
  } finally {
    try {
      await run(`document.querySelector('input[name="appearance-theme"][value="${appearance.theme === "original" ? "ink" : "original"}"]').click()`);
      await run(`document.querySelector('input[name="appearance-theme"][value="${appearance.theme}"]').click()`);
      if (appearance.theme !== "original") await waitForUI("!document.getElementById('appearance-motion').disabled", "Motion toggle did not become available for preference restoration");
      await run(`if (document.getElementById('appearance-motion').checked !== ${appearance.motion}) document.getElementById('appearance-motion').click()`);
      await waitForUI("document.getElementById('appearance-status').textContent === '外观已保存'", "Appearance restoration was not saved");
      assert.deepEqual(await run("window.ainovelDesktop.getAppearance()"), appearance);
    } catch (error) {
      if (!primaryError) throw error;
      process.stderr.write(`Could not restore appearance after failed chapter check: ${error.message}\n`);
    } finally {
      await api("/api/workspaces", { path: originalDirectory });
      await run("document.querySelector('[data-view=overview]').click()");
      await window.webContents.debugger.sendCommand("Emulation.setEmulatedMedia", { features: [] });
      window.webContents.debugger.detach();
      window.webContents.setZoomFactor(savedZoom);
      window.setBounds(savedBounds);
      if (!wasVisible) window.hide();
      window.webContents.setBackgroundThrottling(wasThrottled);
    }
  }
}

module.exports = { runChapterScrollChecks };
