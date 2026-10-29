"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

async function waitForPDFCanvas({ readState, captureFrame, timeoutMs = 15000, now = Date.now, sleep = ms => new Promise(resolve => setTimeout(resolve, ms)) }) {
  const deadline = now() + timeoutMs;
  let diagnostic, captureFailures = 0, lastCaptureError = "", captureAfter = -Infinity;
  while (now() < deadline) {
    // A completed real canvas needs no screenshot to prove it rendered. Read
    // its state first, before asking a just-shown compositor to copy a surface.
    diagnostic = await readState();
    if (/失败/.test(diagnostic.note)) throw new Error(`PDF original page failed: ${JSON.stringify(diagnostic)}`);
    if (diagnostic.canvas?.width > 100 && diagnostic.canvas?.height > 100 && !diagnostic.note) return diagnostic;
    if (captureFailures < 3 && now() >= captureAfter) {
      try {
        await captureFrame(); captureAfter = now() + 100;
      } catch (error) {
        if (error.message !== "UnknownVizError") throw new Error(`PDF compositor capture failed: ${error.message}; ${JSON.stringify(diagnostic)}`, { cause: error });
        // Xvfb may not yet have a shared image mailbox immediately after show.
        // This is only a wakeup hint; readiness and actual text pixels remain
        // mandatory. Three bounded retries cannot turn a broken PDF into a pass.
        captureFailures++; lastCaptureError = error.message; captureAfter = now() + 100 * (2 ** (captureFailures - 1));
      }
    }
    await sleep(50);
  }
  throw new Error(`PDF original page did not render: ${JSON.stringify({ ...diagnostic, captureFailures, lastCaptureError })}`);
}

// Uses real file inputs, Chromium PDF rendering, sidecar parsing, workspace
// switching and disk persistence. Fixtures contain only original test prose.
async function runReaderChecks({ window, options, phase, api, step, waitForUI }) {
  const originalDirectory = (await api("/api/state")).directory;
  const run = source => window.webContents.executeJavaScript(source);
  const fixture = path.join(options.root, "novel-reader-scenario.json");
  const reload = async () => { await new Promise(resolve => { window.webContents.once("did-finish-load", resolve); window.webContents.reload(); }); await waitForUI('Boolean(document.getElementById("nr-file") && document.getElementById("current-project-path").title)', "Reader project state did not initialize"); };
  const openReader = async () => {
    await run('document.querySelector("[data-view=reader]").click()');
    await waitForUI('!document.getElementById("view-reader").classList.contains("hidden") && document.getElementById("novel-reader-root").getAttribute("aria-busy") === "false"', "Reader view did not finish opening");
  };
  const chapter = async number => {
    await run(`document.querySelector('#nr-toc [data-chapter="${number}"]').click()`);
    await waitForUI(`document.querySelector('#nr-toc [data-chapter="${number}"]').getAttribute('aria-current') === 'page' && !document.getElementById('nr-edit').disabled`, "Selected reader chapter did not load");
  };
  const importFile = async (name, content, title) => {
    const base64 = Buffer.from(content).toString("base64");
    await run(`(() => {
      document.getElementById('nr-import-form').parentElement.open = true;
      const transfer = new DataTransfer(); transfer.items.add(new File([Uint8Array.from(atob(${JSON.stringify(base64)}), char => char.charCodeAt(0))], ${JSON.stringify(name)}));
      document.getElementById('nr-file').files = transfer.files;
      document.getElementById('nr-import-title').value = ${JSON.stringify(title)};
      document.getElementById('nr-import-submit').click();
    })()`);
    try {
      await waitForUI(`document.getElementById('nr-book-title').textContent === ${JSON.stringify(title)} && !document.getElementById('nr-import-submit').disabled && !document.getElementById('nr-edit').disabled`, `${name} failed to import and open`, 20000);
    } catch (error) {
      const diagnostic = await run(`({importStatus:document.getElementById('nr-import-status').textContent,
        bookTitle:document.getElementById('nr-book-title').textContent, busy:document.getElementById('novel-reader-root').getAttribute('aria-busy'),
        importDisabled:document.getElementById('nr-import-submit').disabled, editDisabled:document.getElementById('nr-edit').disabled,
        status:document.getElementById('nr-status').textContent, project:document.getElementById('current-project-name').textContent})`);
      throw new Error(`${error.message}; ${JSON.stringify({ file: name, expectedTitle: title, ...diagnostic })}`, { cause: error });
    }
    const state = await api("/api/state"); assert.equal(state.project.mode, "manual"); assert.equal(state.project.name, title); return state.directory;
  };
  const change = async (id, value) => run(`document.getElementById(${JSON.stringify(id)}).value = ${JSON.stringify(String(value))}; document.getElementById(${JSON.stringify(id)}).dispatchEvent(new Event('change'))`);
  const settle = async () => run('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(true))))');
  const press = async (keyCode, modifiers = [], text) => {
    window.webContents.sendInputEvent({ type: "keyDown", keyCode, modifiers });
    if (text) window.webContents.sendInputEvent({ type: "char", keyCode: text, modifiers });
    window.webContents.sendInputEvent({ type: "keyUp", keyCode, modifiers });
    await settle();
  };
  const showReaderTools = async kind => {
    const id = kind === "creation" ? "nr-creation-tools" : "nr-reading-settings";
    await run(`document.querySelectorAll('.nr-reading-tools > details').forEach(panel=>{panel.open=panel.id===${JSON.stringify(id)}})`);
    await settle();
    await waitForUI(`document.getElementById(${JSON.stringify(id)}).open && Array.from(document.querySelectorAll('.nr-reading-tools > details')).filter(panel=>panel.open).length===1`, "Reader tool panels did not switch exclusively");
  };
  const enterFullscreen = async () => {
    window.show(); window.focus();
    await window.webContents.executeJavaScript('document.getElementById("nr-fullscreen").click()', true);
    await waitForUI('document.fullscreenElement === document.getElementById("novel-reader-root") && document.getElementById("novel-reader-root").dataset.fullscreenLayout === "ready"', "Reader did not enter native fullscreen", 10000);
  };
  const exitFullscreen = async () => {
    await run('document.getElementById("nr-fullscreen").click()');
    await waitForUI('!document.fullscreenElement && document.getElementById("nr-fullscreen").getAttribute("aria-pressed") === "false" && document.getElementById("novel-reader-root").dataset.fullscreenLayout === "ready"', "Reader did not leave native fullscreen", 10000);
  };
  const savedAppearance = await run("window.ainovelDesktop.getAppearance()");
  const savedBounds = window.getBounds(), savedZoom = window.webContents.getZoomFactor();
  const wasVisible = window.isVisible(), wasThrottled = window.webContents.getBackgroundThrottling();
  const screenshotFolder = process.env.AINOVEL_READER_SCREENSHOTS || process.env.AINOVEL_APPEARANCE_SCREENSHOTS;
  const capture = async (name, targetId = "nr-reading") => {
    if (!screenshotFolder) return;
    fs.mkdirSync(screenshotFolder, { recursive: true });
    const bounds = window.getBounds(), zoom = window.webContents.getZoomFactor();
    const visible = window.isVisible(), throttled = window.webContents.getBackgroundThrottling();
    const fullscreen = await run("Boolean(document.fullscreenElement)");
    try {
      if (!fullscreen) window.setBounds({ width: 1480, height: 1000 });
      window.webContents.setZoomFactor(1); window.webContents.setBackgroundThrottling(false); window.show(); window.moveTop();
      await run(`document.getElementById(${JSON.stringify(targetId)}).scrollIntoView({block:'start'})`);
      await settle();
      let screenshot;
      for (let attempt = 0; attempt < 3; attempt++) {
        try {
          screenshot = await window.webContents.capturePage({}, { stayAwake: true });
          break;
        } catch (error) {
          if (error.message !== "UnknownVizError" || attempt === 2) throw new Error(`Reader screenshot ${name} failed: ${error.message}`, { cause: error });
          await new Promise(resolve => setTimeout(resolve, 100 * (attempt + 1))); await settle();
        }
      }
      const size = screenshot.getSize(), png = screenshot.toPNG();
      assert.ok(!screenshot.isEmpty() && size.width > 0 && size.height > 0 && png.length > 0, `Reader screenshot ${name} is empty`);
      fs.writeFileSync(path.join(screenshotFolder, name), png);
    } finally {
      if (!fullscreen) window.setBounds(bounds);
      window.webContents.setZoomFactor(zoom); await settle();
      window.webContents.setBackgroundThrottling(throttled); if (!visible) window.hide();
    }
  };
  let directory;
  try {
    if (phase === "restart") {
      await step("reader-bookmark-settings-and-original-pdf-survive-restart", async () => {
        const saved = JSON.parse(fs.readFileSync(fixture, "utf8"));
        const book = await api(`/api/reader/book?${new URLSearchParams({ directory: saved.directory })}`);
        assert.equal(book.progress.chapter, 2); assert.equal(book.progress.theme, "sepia"); assert.equal(book.progress.fontSize, 24); assert.equal(book.progress.bookmarks.length, 1);
        assert.equal(book.progress.fontFamily, "sans"); assert.equal(book.progress.pageWidth, "compact");
        assert.equal(book.progress.paragraphSpacing, 1); assert.equal(book.progress.sidebarCollapsed, true);
        await api("/api/workspaces", { path: saved.directory }); await reload(); await openReader();
        await waitForUI('document.getElementById("nr-theme").value === "sepia" && document.getElementById("nr-font-size").value === "24" && document.querySelectorAll(".nr-bookmark-row").length === 1', "Persisted reader settings did not reach UI");
        await waitForUI('document.getElementById("nr-font-family").value === "sans" && document.getElementById("nr-page-width").value === "compact" && document.getElementById("nr-paragraph-spacing").value === "1" && document.getElementById("novel-reader-root").dataset.sidebarCollapsed === "true"', "Desktop typography and directory preference did not survive restart");
        const response = await run(`fetch(${JSON.stringify(`/api/reader/source?${new URLSearchParams({ directory: saved.pdf })}`)}, {headers:{Range:'bytes=0-7'}}).then(async response => ({status:response.status, text:await response.text()}))`);
        assert.equal(response.status, 206); assert.equal(response.text, "%PDF-1.4");
      });
      return;
    }
    await openReader();
    await step("reader-imports-txt-epub-docx-markdown-html-and-opens-manual-projects", async () => {
      const paragraph = "灯塔照亮夜空，读者沿着文字走进故事。";
      const text = `第一章 夜航\n${Array.from({ length: 150 }, (_, index) => `第${index + 1}段：${paragraph}${index === 124 ? "😀🌌Unicode 定位：𠀀星河灯塔🚀唯一命中。" : ""}`).join("\n\n")}\n第二章 来信\n读者在窗边发现了一封来信。`;
      directory = await importFile("reader.txt", text, "阅读器TXT验收");
      assert.match(await run('document.querySelector(".nr-prose").textContent'), /第150段/);
      for (const format of ["epub", "docx"]) {
        await importFile(`reader.${format}`, fs.readFileSync(path.join(__dirname, `fixtures/reader.${format}`)), `阅读器${format.toUpperCase()}验收`);
        assert.match(await run('document.querySelector(".nr-prose").textContent'), /灯塔照亮夜空/);
        assert.equal(await run('Boolean(window.untrustedEPUB)'), false);
      }
      await importFile("reader.md", "# 第一章 夜航\nMarkdown 灯塔照亮夜空。\n# 第二章 来信\n另一段正文。", "阅读器Markdown验收");
      await importFile("reader.html", '<html><body><h1>第一章 夜航</h1><p>HTML 灯塔照亮夜空。</p><script>window.untrustedHTML=true</script><iframe src="https://example.invalid"></iframe></body></html>', "阅读器HTML验收");
      assert.equal(await run('Boolean(window.untrustedHTML)'), false);
      assert.equal(await run('document.getElementById("nr-manuscript").querySelectorAll("script,iframe").length'), 0);
    });
    let pdfDirectory;
    await step("reader-offline-pdf-text-and-original-canvas-render-under-csp", async () => {
      for (const source of ["/vendor/pdfjs/pdf.mjs", "/vendor/pdfjs/pdf.worker.mjs"]) {
        const response = await run(`fetch(${JSON.stringify(source)}).then(async response => {
          const result={status:response.status,type:response.headers.get('content-type')};
          await response.body?.cancel(); return result;
        })`);
        assert.equal(response.status, 200, `${source}: ${JSON.stringify(response)}`);
        assert.match(response.type || "", /^(application|text)\/javascript(?:;|$)/i, `${source}: module MIME type must work without host registry mappings`);
      }
      pdfDirectory = await importFile("reader.pdf", fs.readFileSync(path.join(__dirname, "fixtures/reader.pdf")), "阅读器PDF验收");
      assert.match(await run('document.querySelector(".nr-prose").textContent'), /First chapter/);
      // PDF.js display rendering yields to native animation frames. An initially
      // hidden CI window has no compositor frames, even after extraction works.
      // Test the actual displayed canvas, just as the dynamic-skin checks do.
      window.webContents.setBackgroundThrottling(false); window.show(); window.moveTop();
      await showReaderTools("settings");
      await change("nr-mode", "original");
      const diagnostic = await waitForPDFCanvas({
        captureFrame: () => window.webContents.capturePage({ x: 0, y: 0, width: 1, height: 1 }, { stayAwake: true }),
        readState: async () => ({ ...(await run(`(() => {
          const canvas=document.querySelector('.nr-pdf-canvas'), manuscript=document.getElementById('nr-manuscript');
          return {canvas:canvas ? {width:canvas.width,height:canvas.height,rect:canvas.getBoundingClientRect().toJSON()} : null,
            note:manuscript.querySelector('.nr-muted')?.textContent || '', manuscript:manuscript.textContent.slice(0,1000),
            hidden:document.hidden, visibility:document.visibilityState, mode:document.getElementById('nr-mode').value};
        })()`)), windowVisible: window.isVisible(), backgroundThrottling: window.webContents.getBackgroundThrottling() }),
      });
      const pixels = await run(`(() => { const canvas = document.querySelector('.nr-pdf-canvas'), data = canvas.getContext('2d').getImageData(0, 0, canvas.width, canvas.height).data; let dark=0; for(let i=0;i<data.length;i+=4) if(data[i]<150 && data[i+3]>0) dark++; return dark; })()`);
      assert.ok(pixels > 300, `PDF canvas contains no text pixels (${pixels}): ${JSON.stringify(diagnostic)}`);
      assert.ok(!/失败/.test(await run('document.getElementById("nr-manuscript").textContent')));
      await capture("reader-pdf.png");
      await enterFullscreen();
      const fullPDF = await run(`(() => { const canvas=document.querySelector('.nr-pdf-canvas'), page=document.getElementById('nr-page'); return {mode:document.getElementById('nr-mode').value, drawn:canvas?.width>100 && !document.querySelector('#nr-manuscript .nr-muted'), fits:canvas?.getBoundingClientRect().width<=page.clientWidth}; })()`);
      assert.deepEqual(fullPDF, {mode:"original",drawn:true,fits:true});
      await exitFullscreen();
      await chapter(2); await change("nr-mode", "text"); assert.match(await run('document.querySelector(".nr-prose").textContent'), /Second chapter/);
      const source = await api(`/api/reader/book?${new URLSearchParams({ directory: pdfDirectory })}`); assert.equal(source.source.pages, 2); assert.equal(source.source.hasText, true);
    });
    await step("reader-independent-scroll-search-bookmarks-and-eight-skin-layouts", async () => {
      await run(`Array.from(document.querySelectorAll('#nr-shelf [data-directory]')).find(node => node.dataset.directory === ${JSON.stringify(directory)}).click()`);
      await waitForUI('document.getElementById("nr-book-title").textContent === "阅读器TXT验收" && !document.getElementById("nr-edit").disabled', "Text book did not reopen");
      const scroll = await run(`(() => { const page=document.getElementById('nr-page'), sidebar=document.getElementById('nr-sidebar'), outer=document.scrollingElement.scrollTop; page.scrollTop=300; return {moved:page.scrollTop>0, sidebar:sidebar.scrollTop, unchanged:document.scrollingElement.scrollTop===outer}; })()`);
      assert.deepEqual(scroll, { moved: true, sidebar: 0, unchanged: true });
      await run('document.getElementById("nr-search-input").value = "来信"; document.getElementById("nr-search-submit").click()');
      await waitForUI('document.querySelectorAll(".nr-search-result").length > 0', "Whole-book search returned no results");
      await run('document.querySelector(".nr-search-result").click()'); await waitForUI('document.querySelector(".nr-prose")?.textContent.includes("窗边")', "Search did not open matching chapter");
      await run('document.getElementById("nr-add-bookmark").click()'); await change("nr-theme", "sepia"); await change("nr-font-size", 24);
      await waitForUI(`fetch('/api/reader/book?directory='+encodeURIComponent(${JSON.stringify(directory)})).then(r=>r.json()).then(book=>book.progress.theme==='sepia' && book.progress.fontSize===24 && book.progress.bookmarks.length===1)`, "Reader preferences were not saved");
      fs.writeFileSync(fixture, JSON.stringify({ directory, pdf: pdfDirectory }));
      await showReaderTools("creation");
      for (const skin of ["original", "ink", "fresh", "romance", "genshin", "starry", "aurora", "silk"]) {
        window.setBounds({ width: 1080, height: 760 }); window.webContents.setZoomFactor(1.25);
        await run(`document.querySelector('input[name="appearance-theme"][value="${skin}"]').click(); document.getElementById('nr-audio').scrollIntoView({block:'center'})`);
        await waitForUI(`document.documentElement.dataset.theme === '${skin}'`, `Reader skin ${skin} did not apply`);
        const layout = await run(`(() => { const page=document.getElementById('nr-page'), button=document.getElementById('nr-audio'), rect=button.getBoundingClientRect(), x=rect.x+rect.width/2, y=rect.y+rect.height/2; return {pageWidth:page.clientWidth, pageHeight:page.clientHeight, horizontal:document.documentElement.scrollWidth<=innerWidth+1, button:button.contains(document.elementFromPoint(x,y)), text:getComputedStyle(page).color, background:getComputedStyle(page).backgroundColor}; })()`);
        assert.ok(layout.pageWidth > 250 && layout.pageHeight > 200 && layout.horizontal && layout.button, `${skin}: ${JSON.stringify(layout)}`);
        assert.notEqual(layout.text, layout.background);
        if (skin === "starry") await capture("reader-starry.png");
      }
      await change("nr-theme", "paper"); await capture("reader-paper.png"); await change("nr-theme", "sepia");
      await waitForUI(`fetch('/api/reader/book?directory='+encodeURIComponent(${JSON.stringify(directory)})).then(r=>r.json()).then(book=>book.progress.theme==='sepia')`, "Reader theme did not restore after visual QA");
    });
    await step("reader-desktop-typography-progress-and-continue-reading", async () => {
      window.setBounds({ width: 1600, height: 1000 }); window.webContents.setZoomFactor(1);
      window.webContents.setBackgroundThrottling(false); window.show(); window.moveTop(); window.focus();
      await chapter(1); await showReaderTools("settings"); await settle();
      await run('document.getElementById("nr-progress").value="370"; document.getElementById("nr-progress").dispatchEvent(new Event("input")); document.getElementById("nr-progress").dispatchEvent(new Event("change"))');
      await waitForUI('(() => { const p=document.getElementById("nr-page"); return Math.abs(p.scrollTop/(p.scrollHeight-p.clientHeight)-.37)<.01; })()', "Chapter progress slider did not move to the requested text");
      await change("nr-page-width", "wide"); await settle();
      const wide = await run('document.getElementById("nr-manuscript").getBoundingClientRect().width');
      await change("nr-font-family", "sans"); await change("nr-page-width", "compact"); await change("nr-paragraph-spacing", 1);
      await settle();
      const compact = await run('document.getElementById("nr-manuscript").getBoundingClientRect().width');
      assert.ok(wide > compact + 30, `Page width setting did not change the reading column: ${JSON.stringify({ wide, compact })}`);
      await waitForUI('(() => { const p=document.getElementById("nr-page"); return Math.abs(p.scrollTop/(p.scrollHeight-p.clientHeight)-.37)<.01; })()', "Changing typography moved the reading position");
      await waitForUI(`fetch('/api/reader/book?directory='+encodeURIComponent(${JSON.stringify(directory)})).then(r=>r.json()).then(book=>book.progress.fontFamily==='sans' && book.progress.pageWidth==='compact' && book.progress.paragraphSpacing===1 && Math.abs(book.progress.offset-.37)<.01)`, "Desktop typography and progress did not persist");
      await capture("reader-desktop-settings.png");
      await run('document.getElementById("nr-refresh").click()');
      await waitForUI('document.getElementById("novel-reader-root").getAttribute("aria-busy") === "false"', "Refreshed bookshelf did not settle");
      await change("nr-shelf-sort", "recent");
      await waitForUI(`document.querySelector('#nr-shelf [data-directory]')?.dataset.directory === ${JSON.stringify(directory)}`, "Recent reading order did not place the current book first");
      if (screenshotFolder) {
        const shelfOpen = await run('document.getElementById("nr-shelf-panel").open');
        try {
          await run('document.getElementById("nr-shelf-panel").open=true');
          await capture("reader-library.png", "nr-shelf-panel");
        } finally { await run(`document.getElementById("nr-shelf-panel").open=${shelfOpen}`); }
      }
      await run('document.getElementById("nr-continue").click()');
      await waitForUI('document.getElementById("nr-book-title").textContent === "阅读器TXT验收" && !document.getElementById("nr-edit").disabled', "Continue reading did not reopen the most recent book");
      await change("nr-chapter-jump", 2);
      await waitForUI('document.querySelector(".nr-prose")?.textContent.includes("窗边")', "Chapter selector did not jump to the requested chapter");
      await change("nr-chapter-jump", 1);
      await waitForUI('document.querySelector(".nr-prose")?.textContent.includes("第150段")', "Chapter selector did not return to the long chapter");
      await change("nr-progress", 1000);
      await run('document.getElementById("nr-page-next").click()');
      await waitForUI('document.getElementById("nr-chapter-jump").value === "2"', "Next-screen control did not cross the chapter boundary");
      await run('document.getElementById("nr-page-prev").click()');
      await waitForUI('document.getElementById("nr-chapter-jump").value === "1" && Number(document.getElementById("nr-progress").value)>990', "Previous-screen control did not return to the preceding chapter end");
    });
    await step("reader-unicode-search-jumps-to-exact-visible-match", async () => {
      const needle = "𠀀星河灯塔🚀";
      const response = await api(`/api/reader/search?${new URLSearchParams({ directory, q: needle })}`);
      assert.equal(response.matches.length, 1);
      const match = response.matches[0], source = await api(`/api/reader/chapter/${match.chapter}?${new URLSearchParams({ directory })}`);
      assert.equal(Array.from(source.content).slice(match.start, match.start + match.length).join(""), needle, "Search locations must use Unicode code points");
      await run(`document.getElementById('nr-search-input').value=${JSON.stringify(needle)}; document.getElementById('nr-search-submit').click()`);
      await waitForUI('document.querySelectorAll(".nr-search-result").length === 1', "Unicode query did not reach the results");
      await run('document.querySelector(".nr-search-result").click()');
      await waitForUI(`Array.from(document.querySelectorAll('mark.nr-search-hit')).some(mark=>mark.textContent===${JSON.stringify(needle)})`, "Unicode result did not highlight the exact text");
      const location = await run(`(() => {const page=document.getElementById('nr-page'), mark=Array.from(page.querySelectorAll('mark.nr-search-hit')).find(node=>node.textContent===${JSON.stringify(needle)}), p=page.getBoundingClientRect(), m=mark.getBoundingClientRect();return {top:page.scrollTop,visible:m.bottom>p.top && m.top<p.bottom,content:mark.textContent};})()`);
      assert.ok(location.top > 0 && location.visible, `Search opened the chapter without locating its result: ${JSON.stringify(location)}`);
      assert.equal(location.content, needle);
    });
    await step("reader-desktop-keys-ignore-editors-selects-and-dialogs", async () => {
      await chapter(1); await settle();
      await run('document.getElementById("nr-progress").value="250"; document.getElementById("nr-progress").dispatchEvent(new Event("change")); document.getElementById("nr-page").focus({preventScroll:true})');
      await settle();
      const start = await run('document.getElementById("nr-page").scrollTop');
      await press("PageDown");
      await waitForUI(`document.getElementById('nr-page').scrollTop > ${start + 20}`, "PageDown did not advance one reading screen");
      await press("PageUp");
      await waitForUI(`Math.abs(document.getElementById('nr-page').scrollTop-${start}) < 3`, "PageUp did not return to the previous screen");
      await press("Space");
      await waitForUI(`document.getElementById('nr-page').scrollTop > ${start + 20}`, "Space did not advance one reading screen");
      await press("Space", ["shift"]);
      await waitForUI(`Math.abs(document.getElementById('nr-page').scrollTop-${start}) < 3`, "Shift+Space did not return to the previous screen");
      await press("Right", ["control"]);
      await waitForUI('document.getElementById("nr-chapter-jump").value === "2"', "Ctrl+Right did not move to the next chapter");
      await run('document.getElementById("nr-page").focus({preventScroll:true})'); await press("Left", ["control"]);
      await waitForUI('document.getElementById("nr-chapter-jump").value === "1"', "Ctrl+Left did not move to the previous chapter");
      await run('document.getElementById("nr-page").focus({preventScroll:true})'); await press("f", ["control"]);
      assert.equal(await run('document.activeElement.id'), "nr-search-input");
      await run('document.getElementById("nr-search-input").value=""'); await press("f", [], "f"); await press("Right", ["control"]);
      assert.equal(await run('document.getElementById("nr-chapter-jump").value'), "1", "Typing in search must not trigger chapter navigation");
      assert.equal(await run('document.getElementById("novel-reader-root").dataset.focusMode'), "false", "Typing F in a search field must not enter focus mode");
      assert.match(await run('document.getElementById("nr-search-input").value'), /f/i);
      await showReaderTools("settings"); await run('document.getElementById("nr-font-family").focus()'); await press("Right", ["control"]);
      assert.equal(await run('document.getElementById("nr-chapter-jump").value'), "1", "Reader shortcuts must not intercept select controls");
      await change("nr-font-family", "sans");
      await run('document.getElementById("open-appearance").click()'); await press("Right", ["control"]); await press("f");
      assert.equal(await run('document.getElementById("nr-chapter-jump").value'), "1", "Reader shortcuts must not navigate under a modal dialog");
      assert.equal(await run('document.getElementById("novel-reader-root").dataset.focusMode'), "false");
      await run('document.getElementById("close-appearance").click(); document.getElementById("nr-page").focus({preventScroll:true})');
      const bookmarksBefore = await run('document.querySelectorAll(".nr-bookmark-row").length');
      await press("b", ["control"]);
      await waitForUI(`document.querySelectorAll('.nr-bookmark-row').length === ${bookmarksBefore + 1}`, "Ctrl+B did not add a reading bookmark");
      await run('Array.from(document.querySelectorAll(".nr-bookmark-row")).at(-1).querySelector(".nr-small").click()');
      await waitForUI(`document.querySelectorAll('.nr-bookmark-row').length === ${bookmarksBefore}`, "Temporary keyboard bookmark did not remove");
      await run('(() => {document.getElementById("nr-page").focus({preventScroll:true}); const selection=getSelection(),range=document.createRange();range.selectNodeContents(document.querySelector(".nr-prose"));selection.removeAllRanges();selection.addRange(range)})()');
      await press("f");
      assert.equal(await run('document.getElementById("novel-reader-root").dataset.focusMode'), "false", "F must not change the reading layout while text is selected");
      await run('getSelection().removeAllRanges()');
      await run('document.getElementById("nr-page").focus({preventScroll:true})'); await press("f");
      await waitForUI('document.getElementById("novel-reader-root").dataset.focusMode === "true"', "F did not enter focused reading");
      await press("Escape");
      await waitForUI('document.getElementById("novel-reader-root").dataset.focusMode === "false"', "Escape did not leave focused reading");
    });
    await step("reader-desktop-focus-sidebar-and-scaled-skin-controls", async () => {
      await run('document.getElementById("nr-focus").click()');
      await waitForUI('document.getElementById("novel-reader-root").dataset.focusMode === "true"', "Focus reading control did not open");
      if (await run('document.getElementById("novel-reader-root").dataset.sidebarCollapsed === "true"')) await run('document.getElementById("nr-toggle-sidebar").click()');
      await settle();
      const before = await run('document.getElementById("nr-page").clientWidth');
      await run('document.getElementById("nr-toggle-sidebar").click()'); await settle();
      const collapsed = await run('({state:document.getElementById("novel-reader-root").dataset.sidebarCollapsed,width:document.getElementById("nr-page").clientWidth,display:getComputedStyle(document.getElementById("nr-sidebar")).display})');
      assert.equal(collapsed.state, "true"); assert.equal(collapsed.display, "none"); assert.ok(collapsed.width > before + 50, JSON.stringify({ before, collapsed }));
      for (const skin of ["original", "ink", "fresh", "romance", "genshin", "starry", "aurora", "silk"]) {
        window.setBounds({ width: 1080, height: 760 }); window.webContents.setZoomFactor(1.5);
        await run(`document.querySelector('input[name="appearance-theme"][value="${skin}"]').click()`); await settle();
        const layout = await run(`(() => {const root=document.getElementById('novel-reader-root'),page=document.getElementById('nr-page');return {width:page.clientWidth,height:page.clientHeight,overflow:root.scrollWidth>root.clientWidth+1,controls:['nr-focus','nr-toggle-sidebar','nr-page-prev','nr-page-next'].map(id=>{const control=document.getElementById(id),r=control.getBoundingClientRect();return {id,visible:r.width>0 && r.height>0 && control.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2))};}),legible:getComputedStyle(page).color!==getComputedStyle(page).backgroundColor};})()`);
        assert.ok(layout.width > 200 && layout.height > 80 && !layout.overflow && layout.legible && layout.controls.every(control => control.visible), `${skin} focused reader: ${JSON.stringify(layout)}`);
        if (skin === "starry") await capture("reader-desktop-focus.png");
      }
      window.setBounds(savedBounds); window.webContents.setZoomFactor(1);
      await run('document.getElementById("nr-focus").click()'); await settle();
      assert.equal(await run('document.getElementById("novel-reader-root").dataset.focusMode'), "false");
      await chapter(2);
    });
    await step("reader-native-fullscreen-position-escape-and-navigation", async () => {
      window.webContents.setZoomFactor(1); window.setBounds(savedBounds);
      await chapter(1);
      await run('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(true))))');
      const before = await run(`(() => { const page=document.getElementById('nr-page'); page.scrollTop=(page.scrollHeight-page.clientHeight)*.45; page.dispatchEvent(new Event('scroll')); return {offset:page.scrollTop/(page.scrollHeight-page.clientHeight),top:page.scrollTop,width:page.clientWidth,height:page.clientHeight,scrollHeight:page.scrollHeight}; })()`);
      const position = before.offset;
      assert.ok(Math.abs(position-.45)<.01, `Reader did not reach the test reading position: ${JSON.stringify(before)}`);
      await enterFullscreen();
      const expanded = await run(`(() => { const root=document.getElementById('novel-reader-root'), page=document.getElementById('nr-page'), rect=root.getBoundingClientRect(); return {width:rect.width,height:rect.height,screenWidth:innerWidth,screenHeight:innerHeight,offset:page.scrollTop/(page.scrollHeight-page.clientHeight),background:getComputedStyle(root).backgroundColor,hidden:getComputedStyle(document.getElementById('nr-shelf-panel')).display}; })()`);
      assert.ok(Math.abs(expanded.width-expanded.screenWidth)<=1 && Math.abs(expanded.height-expanded.screenHeight)<=1, JSON.stringify(expanded));
      assert.ok(Math.abs(expanded.offset-position)<.01, `Fullscreen entry moved the reading position: ${JSON.stringify({before,expanded})}`);
      assert.equal(expanded.hidden, "none"); assert.notEqual(expanded.background, "rgba(0, 0, 0, 0)");
      await capture("reader-fullscreen.png");
      window.webContents.sendInputEvent({type:"keyDown",keyCode:"Escape"}); window.webContents.sendInputEvent({type:"keyUp",keyCode:"Escape"});
      await waitForUI('!document.fullscreenElement && document.getElementById("novel-reader-root").dataset.fullscreenLayout === "ready"', "Escape did not exit fullscreen", 10000);
      const exited = await run('(() => {const p=document.getElementById("nr-page");return {offset:p.scrollTop/(p.scrollHeight-p.clientHeight),top:p.scrollTop,width:p.clientWidth,height:p.clientHeight,scrollHeight:p.scrollHeight}})()');
      assert.ok(Math.abs(exited.offset-position)<.01, `Escape moved the reading position: ${JSON.stringify({before,expanded,exited})}`);
      await chapter(2);
      await enterFullscreen();
      for (const skin of ["original", "ink", "fresh", "romance", "genshin", "starry", "aurora", "silk"]) {
        window.webContents.setZoomFactor(1.5);
        await run(`document.querySelector('input[name="appearance-theme"][value="${skin}"]').click()`);
        await waitForUI(`document.documentElement.dataset.theme === '${skin}'`, `Fullscreen skin ${skin} did not apply`);
        const layout = await run(`(() => {const root=document.getElementById('novel-reader-root'),page=document.getElementById('nr-page'),button=document.getElementById('nr-fullscreen'),rect=button.getBoundingClientRect();return {width:page.clientWidth,height:page.clientHeight,overflow:root.scrollWidth>innerWidth+1,exit:button.contains(document.elementFromPoint(rect.x+rect.width/2,rect.y+rect.height/2)),visible:getComputedStyle(page).color!==getComputedStyle(page).backgroundColor}})()`);
        assert.ok(layout.width>200 && layout.height>80 && !layout.overflow && layout.exit && layout.visible, `${skin} fullscreen: ${JSON.stringify(layout)}`);
      }
      window.webContents.setZoomFactor(1);
      await showReaderTools("creation");
      await run('document.getElementById("nr-audio").click()');
      await waitForUI('!document.fullscreenElement && !document.getElementById("view-illustrations").classList.contains("hidden")', "Reader media action did not leave fullscreen");
      await openReader(); await waitForUI('!document.getElementById("nr-fullscreen").disabled', "Reader did not return from media navigation");
      await enterFullscreen(); await exitFullscreen();
      assert.equal(await run('document.getElementById("nr-theme").value'), "sepia");
    });
    await step("reader-selected-prose-opens-speech-illustration-video-editor-and-style-library", async () => {
      window.setBounds(savedBounds); window.webContents.setZoomFactor(savedZoom);
      for (const kind of ["audio", "image", "video"]) {
        await openReader(); await waitForUI('!document.getElementById("nr-audio").disabled', "Reader text controls did not load");
        await showReaderTools("creation");
        await run(`document.getElementById('nr-${kind}').click()`);
        await waitForUI('!document.getElementById("view-illustrations").classList.contains("hidden") && document.getElementById("media-chapter").value === "2" && document.getElementById("media-source-text").textContent.includes("窗边")', `${kind} did not use the imported selected chapter`);
      }
      assert.equal((await api(`/api/chapters/2/media/generate?${new URLSearchParams({ directory })}`)).jobs.length, 0, "Opening reader tools must not submit paid jobs");
      await openReader(); await waitForUI('!document.getElementById("nr-edit").disabled', "Reader edit did not load"); await showReaderTools("creation"); await run('document.getElementById("nr-edit").click()');
      await waitForUI('!document.getElementById("view-chapters").classList.contains("hidden") && document.getElementById("edit-chapter-content").value.includes("窗边")', "Reader edit did not open current manual chapter");
      await openReader(); await waitForUI('!document.getElementById("nr-distill-chapter").disabled', "Reader style did not load"); await showReaderTools("creation"); await run('document.getElementById("nr-distill-chapter").click()');
      await waitForUI('!document.getElementById("view-style").classList.contains("hidden")', "Reader style text did not open shared style library");
      const entries = await api("/api/style"); assert.ok(entries.entries?.length > 0); assert.equal(entries.analysisRunning, false, "Opening imported style material must not start paid analysis");
    });
    await step("manual-project-dialog-empty-chapter-save-clear-and-restore-version", async () => {
      await run(`document.querySelector('[data-view=projects]').click(); document.getElementById('create-project').click();
        document.getElementById('new-project-mode').value='manual'; document.getElementById('new-project-name').value='纯手动创作验收'; document.getElementById('create-project-form').requestSubmit()`);
      await waitForUI('!document.getElementById("project-dialog").open && !document.getElementById("manual-add-chapter").classList.contains("hidden") && !document.getElementById("manual-add-chapter").disabled', "Manual project dialog did not create a writable project");
      const manual = await api("/api/state"); assert.equal(manual.project.mode, "manual"); assert.equal(manual.project.name, "纯手动创作验收");
      await run(`document.getElementById('manual-add-chapter').click(); document.getElementById('manual-chapter-title').value='第一章 亲手写下的故事'; document.getElementById('manual-chapter-form').requestSubmit()`);
      await waitForUI('!document.getElementById("manual-chapter-dialog").open && !document.getElementById("chapter-editor").classList.contains("hidden") && !document.getElementById("edit-chapter-content").disabled', "New empty manual chapter is not editable");
      assert.equal(await run('document.getElementById("edit-chapter-content").value'), "");
      const prose = "这是纯手动保存的正文。灯塔点亮，作者开始亲手写下自己的故事。";
      const save = async content => {
        await run(`document.getElementById('edit-chapter-content').value=${JSON.stringify(content)}; document.getElementById('edit-chapter-content').dispatchEvent(new Event('input'))`);
        try {
          await waitForUI('!document.getElementById("save-chapter-content").disabled', "Manual save control did not accept the edited body");
          await run('document.getElementById("save-chapter-content").click()');
          await waitForUI(`(async () => {
            const editor=document.getElementById('edit-chapter-content');
            if(editor.disabled || editor.value!==${JSON.stringify(content)} || !document.getElementById('save-chapter-content').disabled) return false;
            const response=await fetch('/api/reader/chapter/1?directory='+encodeURIComponent(${JSON.stringify(manual.directory)}));
            return response.ok && (await response.json()).content===${JSON.stringify(content)};
          })()`, "Manual chapter body did not persist or its editor remained busy");
        } catch (error) {
          const diagnostic = await run(`(async () => ({status:document.getElementById('chapter-editor-status').textContent,
            body:document.getElementById('edit-chapter-content').value, editorDisabled:document.getElementById('edit-chapter-content').disabled,
            saveDisabled:document.getElementById('save-chapter-content').disabled,
            stored:await fetch('/api/reader/chapter/1?directory='+encodeURIComponent(${JSON.stringify(manual.directory)})).then(async response=>({status:response.status,body:await response.text()}))}))()`);
          throw new Error(`${error.message}; ${JSON.stringify(diagnostic)}`);
        }
      };
      await save(prose);
      const versions = await api(`/api/chapters/1/versions?${new URLSearchParams({ directory: manual.directory })}`);
      const written = versions.versions.find(version => version.content === prose); assert.ok(written, "Manual saved prose did not create a restorable version");
      await save("");
      assert.equal((await api(`/api/reader/chapter/1?${new URLSearchParams({ directory: manual.directory })}`)).content, "");
      await run('document.getElementById("reload-chapter-versions").click()');
      await waitForUI(`Array.from(document.querySelectorAll('#chapter-version-list .chapter-version-button')).some(button=>button.textContent==='版本 ${written.number}')`, "Previous nonempty version did not remain after clearing prose");
      await run(`Array.from(document.querySelectorAll('#chapter-version-list .chapter-version-button')).find(button=>button.textContent==='版本 ${written.number}').click()`);
      await waitForUI('!document.getElementById("apply-chapter-version").disabled', "Manual historical version cannot be restored"); await run('document.getElementById("apply-chapter-version").click()');
      await waitForUI(`fetch('/api/reader/chapter/1?directory='+encodeURIComponent(${JSON.stringify(manual.directory)})).then(r=>r.json()).then(chapter=>chapter.content===${JSON.stringify(prose)})`, "Manual version restore did not recover saved prose");
      assert.equal(await run('document.getElementById("chapter-sync-required").classList.contains("hidden")'), true, "Manual saving should not require an AI synchronization task");
      await openReader(); await waitForUI(`document.getElementById('nr-book-title').textContent==='纯手动创作验收' && document.querySelector('.nr-prose')?.textContent===${JSON.stringify(prose)}`, "Manually created project is not readable as a whole book");
      // Reload immediately, before the 500 ms debounce, to exercise the actual
      // unload keepalive plus local recovery path in Chromium.
      await run('document.getElementById("nr-add-bookmark").click()'); await reload(); await openReader();
      await waitForUI('document.getElementById("nr-book-title").textContent === "纯手动创作验收" && document.querySelectorAll(".nr-bookmark-row").length === 1', "An immediately reloaded reader lost its new bookmark");
    });
  } finally {
    await run('document.fullscreenElement ? document.exitFullscreen() : undefined');
    window.setBounds(savedBounds); window.webContents.setZoomFactor(savedZoom);
    await run(`window.ainovelDesktop.setAppearance(${JSON.stringify(savedAppearance)})`);
    await api("/api/workspaces", { path: originalDirectory }); await reload();
    await run('document.querySelector("[data-view=overview]").click()');
    if (!wasVisible) window.hide();
    window.webContents.setBackgroundThrottling(wasThrottled);
  }
}
module.exports = { runReaderChecks, waitForPDFCanvas };
