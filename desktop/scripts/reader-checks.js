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
async function runReaderChecks({ window, options, paths, phase, api, step, waitForUI }) {
  const run = source => window.webContents.executeJavaScript(source);
  const fixture = path.join(options.root, "novel-reader-scenario.json");
  const reload = async () => { await new Promise(resolve => { window.webContents.once("did-finish-load", resolve); window.webContents.reload(); }); await waitForUI('Boolean(document.getElementById("nr-file"))', "Reader did not initialize"); };
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
  const savedAppearance = await run("window.ainovelDesktop.getAppearance()");
  const savedBounds = window.getBounds(), savedZoom = window.webContents.getZoomFactor();
  const wasVisible = window.isVisible(), wasThrottled = window.webContents.getBackgroundThrottling();
  const capture = async name => {
    const folder = process.env.AINOVEL_APPEARANCE_SCREENSHOTS; if (!folder) return;
    fs.mkdirSync(folder, { recursive: true });
    window.setBounds({ width: 1480, height: 1000 }); window.webContents.setZoomFactor(1); window.show(); window.moveTop();
    await run("document.getElementById('nr-reading').scrollIntoView({block:'start'})");
    await window.webContents.capturePage({}, { stayAwake: true }); await new Promise(resolve => setTimeout(resolve, 200));
    fs.writeFileSync(path.join(folder, name), (await window.webContents.capturePage({}, { stayAwake: true })).toPNG());
  };
  let directory;
  try {
    if (phase === "restart") {
      await step("reader-bookmark-settings-and-original-pdf-survive-restart", async () => {
        const saved = JSON.parse(fs.readFileSync(fixture, "utf8"));
        const book = await api(`/api/reader/book?${new URLSearchParams({ directory: saved.directory })}`);
        assert.equal(book.progress.chapter, 2); assert.equal(book.progress.theme, "sepia"); assert.equal(book.progress.fontSize, 24); assert.equal(book.progress.bookmarks.length, 1);
        await api("/api/workspaces", { path: saved.directory }); await reload(); await openReader();
        await waitForUI('document.getElementById("nr-theme").value === "sepia" && document.getElementById("nr-font-size").value === "24" && document.querySelectorAll(".nr-bookmark-row").length === 1', "Persisted reader settings did not reach UI");
        const response = await run(`fetch(${JSON.stringify(`/api/reader/source?${new URLSearchParams({ directory: saved.pdf })}`)}, {headers:{Range:'bytes=0-7'}}).then(async response => ({status:response.status, text:await response.text()}))`);
        assert.equal(response.status, 206); assert.equal(response.text, "%PDF-1.4");
      });
      return;
    }
    await openReader();
    await step("reader-imports-txt-epub-docx-markdown-html-and-opens-manual-projects", async () => {
      const paragraph = "灯塔照亮夜空，读者沿着文字走进故事。";
      const text = `第一章 夜航\n${Array.from({ length: 150 }, (_, index) => `第${index + 1}段：${paragraph}`).join("\n\n")}\n第二章 来信\n读者在窗边发现了一封来信。`;
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
      pdfDirectory = await importFile("reader.pdf", fs.readFileSync(path.join(__dirname, "fixtures/reader.pdf")), "阅读器PDF验收");
      assert.match(await run('document.querySelector(".nr-prose").textContent'), /First chapter/);
      // PDF.js display rendering yields to native animation frames. An initially
      // hidden CI window has no compositor frames, even after extraction works.
      // Test the actual displayed canvas, just as the dynamic-skin checks do.
      window.webContents.setBackgroundThrottling(false); window.show(); window.moveTop();
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
    await step("reader-selected-prose-opens-speech-illustration-video-editor-and-style-library", async () => {
      window.setBounds(savedBounds); window.webContents.setZoomFactor(savedZoom);
      for (const kind of ["audio", "image", "video"]) {
        await openReader(); await waitForUI('!document.getElementById("nr-audio").disabled', "Reader text controls did not load");
        await run(`document.getElementById('nr-${kind}').click()`);
        await waitForUI('!document.getElementById("view-illustrations").classList.contains("hidden") && document.getElementById("media-chapter").value === "2" && document.getElementById("media-source-text").textContent.includes("窗边")', `${kind} did not use the imported selected chapter`);
      }
      assert.equal((await api(`/api/chapters/2/media/generate?${new URLSearchParams({ directory })}`)).jobs.length, 0, "Opening reader tools must not submit paid jobs");
      await openReader(); await waitForUI('!document.getElementById("nr-edit").disabled', "Reader edit did not load"); await run('document.getElementById("nr-edit").click()');
      await waitForUI('!document.getElementById("view-chapters").classList.contains("hidden") && document.getElementById("edit-chapter-content").value.includes("窗边")', "Reader edit did not open current manual chapter");
      await openReader(); await waitForUI('!document.getElementById("nr-distill-chapter").disabled', "Reader style did not load"); await run('document.getElementById("nr-distill-chapter").click()');
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
    window.setBounds(savedBounds); window.webContents.setZoomFactor(savedZoom);
    await run(`window.ainovelDesktop.setAppearance(${JSON.stringify(savedAppearance)})`);
    await api("/api/workspaces", { path: paths.workspaceDir }); await reload();
    await run('document.querySelector("[data-view=overview]").click()');
    if (!wasVisible) window.hide();
    window.webContents.setBackgroundThrottling(wasThrottled);
  }
}
module.exports = { runReaderChecks, waitForPDFCanvas };
