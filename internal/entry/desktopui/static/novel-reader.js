(function (scope) {
  "use strict";

  const THEMES = ["auto", "paper", "sepia", "dark"];
  const FONTS = { serif: '"Noto Serif SC", "Songti SC", SimSun, serif', sans: '"Noto Sans SC", "Microsoft YaHei", sans-serif', system: 'system-ui, -apple-system, "Segoe UI", sans-serif' };
  const WIDTHS = { compact: 640, comfortable: 800, wide: 1040 };
  const MAX_FILE = 64 * 1024 * 1024, MAX_PAGES = 2000, MAX_TEXT = 32 * 1024 * 1024;
  const clamp = (value, low, high, fallback) => Number.isFinite(Number(value)) ? Math.min(high, Math.max(low, Number(value))) : fallback;
  function normalizeProgress(value = {}, chapters = []) {
    const numbers = new Set(chapters.map((chapter) => Number(chapter.number)));
    const valid = (number) => numbers.has(Number(number));
    return {
      chapter: valid(value.chapter) ? Number(value.chapter) : Number(chapters[0]?.number || 0),
      offset: clamp(value.offset, 0, 1, 0), fontSize: clamp(value.fontSize ?? 20, 16, 30, 20),
      lineHeight: clamp(value.lineHeight ?? 1.8, 1.4, 2.2, 1.8), theme: THEMES.includes(value.theme) ? value.theme : "auto",
      mode: value.mode === "original" ? "original" : "text",
      fontFamily: Object.hasOwn(FONTS, value.fontFamily) ? value.fontFamily : "serif",
      pageWidth: Object.hasOwn(WIDTHS, value.pageWidth) ? value.pageWidth : "comfortable",
      paragraphSpacing: [0, 0.5, 1].includes(value.paragraphSpacing) ? value.paragraphSpacing : 0.5,
      sidebarCollapsed: value.sidebarCollapsed === true,
      bookmarks: Array.isArray(value.bookmarks) ? value.bookmarks.filter((item) => item && valid(item.chapter)).slice(0, 200).map((item) => ({ chapter: Number(item.chapter), offset: clamp(item.offset, 0, 1, 0), label: String(item.label || "书签").slice(0, 80) })) : [],
    };
  }
  function bookRatio(chapters, chapter, offset) {
    const index = (chapters || []).findIndex(item => item.number === chapter);
    if (index < 0) return 0;
    const weights = chapters.map(item => Math.max(1, Number(item.words) || 0));
    return (weights.slice(0, index).reduce((sum, n) => sum + n, 0) + weights[index] * clamp(offset, 0, 1, 0)) / weights.reduce((sum, n) => sum + n, 0);
  }
  function textFromPDFItems(items) {
    let text = "", previous = null;
    for (const item of items || []) {
      if (typeof item.str !== "string") continue;
      if (previous && text && !text.endsWith("\n")) {
        const lineChanged = Math.abs((previous.transform?.[5] || 0) - (item.transform?.[5] || 0)) > Math.max(2, (item.height || previous.height || 10) * 0.45);
        if (lineChanged) text += "\n";
        else if (!/\s$/.test(text) && !/^\s/.test(item.str) && /[A-Za-z0-9]$/.test(text) && /^[A-Za-z0-9]/.test(item.str)) {
          const previousEnd = (previous.transform?.[4] || 0) + (previous.width || 0);
          const gap = (item.transform?.[4] || 0) - previousEnd;
          if (gap > Math.max(1, (item.height || previous.height || 10) * 0.12)) text += " ";
        }
      }
      text += item.str;
      if (item.hasEOL) text += "\n";
      previous = item;
    }
    return text.replace(/\r\n?/g, "\n").replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
  }
  function pdfOptions(data, base = "/vendor/pdfjs/") {
    // Path-rendered fonts and JS image decoders retain the application's strict
    // CSP. Never enable the generic viewer's scripting, forms, or annotations.
    return { data, cMapUrl: `${base}cmaps/`, cMapPacked: true, standardFontDataUrl: `${base}standard_fonts/`,
      wasmUrl: `${base}wasm/`, iccUrl: `${base}iccs/`, useWasm: false, disableFontFace: true, useSystemFonts: true, enableXfa: false };
  }
  const aborted = () => Object.assign(new Error("已取消"), { name: "AbortError" });
  function abortable(promise, signal) {
    if (signal.aborted) return Promise.reject(aborted());
    return new Promise((resolve, reject) => {
      const stop = () => reject(aborted()); signal.addEventListener("abort", stop, { once: true });
      Promise.resolve(promise).then(resolve, reject).finally(() => signal.removeEventListener("abort", stop));
    });
  }
  function create(options) {
    const { api, getContext = () => ({}), onSelectProject = async () => {}, onOpenMedia = async () => {}, onDistill = async () => {}, onEditChapter = async () => {}, toast = () => {} } = options;
    const doc = options.document || scope.document;
    const view = doc.defaultView || scope;
    const root = options.root || doc.getElementById("novel-reader-root");
    if (!root) throw new Error("缺少 novel-reader-root 阅读器容器");
    const fetchBytes = options.fetch || scope.fetch.bind(scope);
    const loadPDF = options.loadPDF || (() => import("/vendor/pdfjs/pdf.mjs"));
    let storage = options.storage;
    if (storage === undefined) { try { storage = scope.document ? scope.localStorage : null; } catch (_) { storage = null; } }
    let pdfLibrary;
    let books = [], book = null, current = null, progress = normalizeProgress(), sequence = 0, chapterSequence = 0, shelfSequence = 0, searchSequence = 0;
    let saveTimer = null, retryTimer = null, saveChain = Promise.resolve(), importJob = null, pdfSession = null, renderTask = null, destroyed = false, restoring = false;
    let lastSaveError = "", lifecycleSignature = "", lifecycleTime = 0;
    let actionBusy = false, distillBusy = false, shelfLoading = false, bookLoading = false, fullscreenBusy = false, fullscreenWanted = false, fullscreenRevision = 0;
    let fullscreenTransition = null, layoutMetrics = null, cancelLayoutWait = null;
    let focusMode = false, lastLocation = null, searchMatches = [], searchTerm = "", searchIndex = -1, activeHit = null;
    const mobileShell = options.mobile || view.ainovelMobile;
    let mobile = false, readerVisible = true, readingWanted = false, mobileReading = false, mobilePanel = "", pointer = null, pendingTap = false;
    const isLoading = () => shelfLoading || bookLoading;
    const pendingProgress = new Map();
    const forgottenBooks = new Set();
    const refs = {};
    function node(tag, className, text, id) {
      const result = doc.createElement(tag);
      if (className) result.className = className;
      if (text !== undefined) result.textContent = text;
      if (id) { result.id = id; refs[id] = result; }
      return result;
    }
    function button(text, callback, className = "nr-button", id) {
      const result = node("button", className, text, id); result.type = "button";
      result.addEventListener("click", () => Promise.resolve(callback()).catch(report)); return result;
    }
    const status = (message) => { refs["nr-status"].textContent = message; };
    function report(error) { if (error?.name !== "AbortError" && !destroyed) { status(error.message || String(error)); toast(error.message || String(error)); } }
    const query = (directory = book?.directory) => new URLSearchParams({ directory });
    function field(label, control) { const result = node("label", "nr-field"); result.append(node("span", "", label), control); return result; }
    function select(id, values, callback) {
      const control = node("select", "nr-select", undefined, id);
      for (const [value, text] of values) { const option = node("option", "", text); option.value = String(value); control.append(option); }
      control.addEventListener("change", () => Promise.resolve(callback(control.value)).catch(report)); return control;
    }
    function input(id, placeholder, type = "text") { const control = node("input", "nr-input", undefined, id); control.type = type; control.placeholder = placeholder; return control; }
    const header = node("header", "nr-header");
    const heading = node("div"); heading.append(node("p", "nr-eyebrow", "READ · CREATE · COLLECT"), node("h2", "", "整本阅读"), node("p", "nr-muted", "阅读生成的作品，也让导入的书成为可继续创作的章节。"));
    const refresh = button("刷新书架", () => load(), "nr-button", "nr-refresh"); header.append(heading, refresh);
    const importPanel = node("details", "nr-import"); importPanel.append(node("summary", "", "导入书籍 · TXT / EPUB / PDF / DOCX / Markdown / HTML"));
    const importForm = node("form", "nr-import-form", undefined, "nr-import-form");
    const file = input("nr-file", "选择文档", "file"); file.accept = ".txt,.epub,.pdf,.docx,.md,.markdown,.html,.htm";
    const title = input("nr-import-title", "可选，留空使用文档标题"); title.maxLength = 200;
    const importSubmit = button("导入并阅读", () => importFile(file.files?.[0], title.value), "nr-button nr-primary", "nr-import-submit");
    const importCancel = button("取消导入", () => cancelImport(), "nr-button", "nr-import-cancel"); importCancel.hidden = true;
    const importStatus = node("p", "nr-muted", "最大 64 MB。PDF 保留原页并提取文字；扫描件可阅读原页，暂不提供 OCR。", "nr-import-status"); importStatus.setAttribute("role", "status");
    importForm.addEventListener("submit", (event) => { event.preventDefault(); importFile(file.files?.[0], title.value).catch(report); });
    importForm.append(field("文件", file), field("书名", title), importSubmit, importCancel, importStatus); importPanel.append(importForm);
    const shelfPanel = node("details", "nr-shelf-panel", undefined, "nr-shelf-panel"); shelfPanel.open = true;
    const shelfSummary = node("summary", "", "我的书架"); shelfPanel.append(shelfSummary);
    const shelfFilter = input("nr-shelf-filter", "筛选书名、作者或格式", "search"); shelfFilter.setAttribute("aria-label", "筛选书架"); shelfFilter.addEventListener("input", renderShelf);
    const shelfSort = select("nr-shelf-sort", [["recent", "最近阅读"], ["title", "书名顺序"], ["progress", "阅读进度"]], renderShelf); shelfSort.value = "recent"; shelfSort.setAttribute("aria-label", "书架排序");
    const shelfTools = node("div", "nr-shelf-tools"); shelfTools.append(shelfFilter, shelfSort);
    const continueReading = button("", () => openBook(continueReading.dataset.directory), "nr-continue", "nr-continue"); continueReading.hidden = true;
    const shelf = node("div", "nr-shelf", undefined, "nr-shelf"); shelfPanel.append(shelfTools, continueReading, shelf);
    const message = node("p", "nr-status", "", "nr-status"); message.setAttribute("role", "status"); message.setAttribute("aria-live", "polite");
    const reading = node("section", "nr-reading", undefined, "nr-reading"); reading.hidden = true; reading.setAttribute("aria-label", "小说阅读器");
    const bookHeader = node("div", "nr-book-header");
    const bookInfo = node("div"); bookInfo.append(node("h3", "", "", "nr-book-title"), node("p", "nr-muted", "", "nr-book-meta"));
    const actions = node("div", "nr-actions");
    const fullscreenButton = button("全屏阅读", () => toggleFullscreen(), "nr-button", "nr-fullscreen");
    fullscreenButton.setAttribute("aria-pressed", "false"); fullscreenButton.setAttribute("aria-label", "展开全屏阅读");
    fullscreenButton.title = "全屏阅读，按 Esc 或再次点击退出";
    actions.append(fullscreenButton, button("朗读本章", () => media("audio"), "nr-button", "nr-audio"), button("本章配图", () => media("image"), "nr-button", "nr-image"), button("本章视频", () => media("video"), "nr-button", "nr-video"), button("编辑本章", () => edit(), "nr-button", "nr-edit")); bookHeader.append(bookInfo, actions);
    const toolbar = node("div", "nr-toolbar");
    const font = select("nr-font-size", [16, 18, 20, 22, 24, 26, 28, 30].map((n) => [n, `${n} px`]), (value) => settings({ fontSize: Number(value) }));
    const height = select("nr-line-height", [[1.4, "紧凑"], [1.6, "舒适"], [1.8, "宽松"], [2, "疏朗"], [2.2, "更宽"]], (value) => settings({ lineHeight: Number(value) }));
    const fontFamily = select("nr-font-family", [["serif", "宋体 · 衬线"], ["sans", "黑体 · 无衬线"], ["system", "系统字体"]], value => settings({ fontFamily: value }));
    const pageWidth = select("nr-page-width", [["compact", "窄版心"], ["comfortable", "舒适版心"], ["wide", "宽版心"]], value => settings({ pageWidth: value }));
    const paragraphSpacing = select("nr-paragraph-spacing", [[0, "紧凑"], [0.5, "适中"], [1, "宽松"]], value => settings({ paragraphSpacing: Number(value) }));
    const theme = select("nr-theme", [["auto", "跟随界面"], ["paper", "明亮纸张"], ["sepia", "暖色护眼"], ["dark", "深夜阅读"]], (value) => settings({ theme: value }));
    const mode = select("nr-mode", [["text", "文字阅读"], ["original", "PDF 原页"]], (value) => settings({ mode: value }));
    const modeField = field("显示", mode); modeField.id = "nr-mode-field"; refs[modeField.id] = modeField;
    toolbar.append(field("字号", font), field("行距", height), field("纸张", theme), modeField,
      button("本章风格素材", () => distill(false), "nr-button", "nr-distill-chapter"), button("整本风格素材", () => distill(true), "nr-button", "nr-distill-book"));
    toolbar.append(field("字体", fontFamily), field("版心", pageWidth), field("段距", paragraphSpacing));
    const readingTools = node("div", "nr-reading-tools nr-desktop-only");
    const focusButton = button("专注阅读", () => setFocusMode(!focusMode), "nr-button", "nr-focus"); focusButton.setAttribute("aria-pressed", "false");
    const sidebarButton = button("收起目录", () => settings({ sidebarCollapsed: !progress.sidebarCollapsed }), "nr-button", "nr-toggle-sidebar"); sidebarButton.setAttribute("aria-controls", "nr-sidebar");
    const preferences = node("details", "nr-preferences", undefined, "nr-reading-settings"); preferences.append(node("summary", "", "阅读设置"), toolbar);
    const creationTools = node("details", "nr-creation-tools", undefined, "nr-creation-tools"), creationActions = node("div", "nr-creation-actions");
    creationTools.append(node("summary", "", "朗读与创作"), creationActions);
    const creationIDs = ["nr-audio", "nr-image", "nr-video", "nr-edit", "nr-distill-chapter", "nr-distill-book"];
    creationActions.append(...creationIDs.map(id => refs[id]));
    const shortcuts = node("details", "nr-shortcuts"), shortcutHelp = node("div", "nr-shortcut-help");
    shortcuts.append(node("summary", "", "快捷键"), shortcutHelp);
    shortcutHelp.append(node("p", "", "PageUp / PageDown、← / →：上一屏 / 下一屏；空格向后，Shift + 空格向前。"), node("p", "", "Ctrl / ⌘ + ← / →：上一章 / 下一章；Ctrl / ⌘ + F：搜索；Ctrl / ⌘ + B：书签；F3 / Shift + F3：下一处 / 上一处命中。"), node("p", "", "F：专注阅读；Esc：退出专注。输入、选择文字或打开弹窗时不会抢占翻阅按键。"));
    readingTools.append(sidebarButton, focusButton, preferences, creationTools, shortcuts);
    for (const details of [preferences, creationTools, shortcuts]) details.addEventListener("toggle", () => {
      if (details.open) for (const other of [preferences, creationTools, shortcuts]) if (other !== details) other.open = false;
    });
    const backToShelf = button("返回书架", () => showDesktopShelf(), "nr-button nr-desktop-only", "nr-back-shelf"); actions.append(backToShelf);
    const layout = node("div", "nr-layout");
    const sidebar = node("details", "nr-sidebar", undefined, "nr-sidebar"); sidebar.open = true;
    sidebar.append(node("summary", "", "目录 · 搜索 · 书签"));
    const asideBody = node("div", "nr-sidebar-body");
    const searchForm = node("form", "nr-search"); const searchInput = input("nr-search-input", "搜索整本正文", "search"); searchInput.maxLength = 100; searchInput.setAttribute("aria-label", "搜索整本正文");
    const searchButton = button("搜索", () => search(), "nr-button", "nr-search-submit");
    searchForm.addEventListener("submit", (event) => { event.preventDefault(); search().catch(report); }); searchForm.append(searchInput, searchButton);
    const results = node("div", "nr-search-results", undefined, "nr-search-results"); results.setAttribute("aria-live", "polite");
    const searchNavigation = node("div", "nr-search-navigation");
    searchNavigation.append(button("上一处", () => nextSearch(-1), "nr-button nr-small", "nr-search-prev"), button("下一处", () => nextSearch(1), "nr-button nr-small", "nr-search-next"));
    const toc = node("nav", "nr-toc", undefined, "nr-toc"); toc.setAttribute("aria-label", "章节目录");
    const bookmarks = node("div", "nr-bookmarks", undefined, "nr-bookmarks");
    asideBody.append(searchForm, searchNavigation, results, node("h4", "", "章节目录"), toc, node("h4", "", "我的书签"), bookmarks); sidebar.append(asideBody);
    const page = node("div", "nr-page", undefined, "nr-page"); page.tabIndex = 0; page.setAttribute("aria-label", "正文，使用滚轮或方向键阅读");
    const manuscript = node("article", "nr-manuscript", undefined, "nr-manuscript");
    page.append(manuscript); page.addEventListener("scroll", () => { if (book && current && !restoring) { captureOffset(); scheduleSave(); } });
    layout.append(sidebar, page);
    const footer = node("div", "nr-footer"); footer.append(button("上一章", () => adjacent(-1), "nr-button", "nr-prev"), node("span", "nr-position", "", "nr-position"), button("添加书签", () => addBookmark(), "nr-button", "nr-add-bookmark"), button("下一章", () => adjacent(1), "nr-button", "nr-next"));
    const chapterJump = select("nr-chapter-jump", [], value => navigateChapter(Number(value))); chapterJump.setAttribute("aria-label", "跳转章节");
    const progressSlider = input("nr-progress", "", "range"); progressSlider.min = "0"; progressSlider.max = "1000"; progressSlider.step = "1"; progressSlider.value = "0"; progressSlider.setAttribute("aria-label", "本章阅读进度");
    const progressLabel = node("span", "nr-progress-label", "本章 0%", "nr-progress-label");
    progressSlider.addEventListener("input", () => { progressLabel.textContent = `本章 ${Math.round(Number(progressSlider.value) / 10)}%`; });
    progressSlider.addEventListener("change", () => seek(Number(progressSlider.value) / 1000));
    const progressControls = node("div", "nr-progress-controls nr-desktop-only"); progressControls.append(chapterJump, progressSlider, progressLabel, button("返回跳转前", () => returnLocation(), "nr-button nr-small", "nr-return-location"));
    footer.append(progressControls, button("上一屏", () => turnPage(-1), "nr-button nr-desktop-only", "nr-page-prev"), button("下一屏", () => turnPage(1), "nr-button nr-desktop-only", "nr-page-next"));
    const mobileStatus = node("div", "nr-mobile-status");
    const compactPosition = node("span", "nr-mobile-position", "", "nr-mobile-position");
    const menuButton = button("菜单", () => showMobilePanel("controls"), "nr-button nr-mobile-menu", "nr-open-controls");
    menuButton.setAttribute("aria-label", "显示阅读菜单，或轻点正文中央"); menuButton.setAttribute("aria-haspopup", "dialog"); menuButton.setAttribute("aria-controls", "nr-mobile-controls");
    mobileStatus.append(compactPosition, menuButton);
    const controlsDialog = node("dialog", "nr-mobile-dialog nr-mobile-controls", undefined, "nr-mobile-controls");
    controlsDialog.setAttribute("aria-label", "阅读菜单");
    const controlsHeading = node("div", "nr-mobile-dialog-heading");
    const mobileTitle = node("strong", "nr-mobile-book-title", "阅读菜单");
    controlsHeading.append(mobileTitle, button("收起", () => closeMobilePanel(), "nr-button", "nr-close-controls"));
    const mobileQuick = node("div", "nr-mobile-quick");
    mobileQuick.append(button("返回书架", () => showMobileShelf(), "nr-button", "nr-mobile-shelf"), button("目录 · 搜索 · 书签", () => showMobilePanel("directory"), "nr-button", "nr-open-directory"));
    const moreTools = node("details", "nr-mobile-more"); moreTools.append(node("summary", "", "朗读、配图与创作工具"));
    controlsDialog.append(controlsHeading, mobileQuick, moreTools);
    const directoryDialog = node("dialog", "nr-mobile-dialog nr-mobile-directory", undefined, "nr-mobile-directory");
    directoryDialog.setAttribute("aria-label", "目录、搜索与书签");
    const directoryHeading = node("div", "nr-mobile-dialog-heading");
    directoryHeading.append(node("strong", "", "目录 · 搜索 · 书签"), button("关闭", () => closeMobilePanel(), "nr-button", "nr-close-directory"));
    directoryDialog.append(directoryHeading);
    reading.append(bookHeader, readingTools, layout, footer, mobileStatus); root.classList.add("novel-reader"); root.dataset.focusMode = "false"; root.replaceChildren(header, importPanel, shelfPanel, message, reading, controlsDialog, directoryDialog);

    function closeMobilePanel(restoreFocus = true) {
      mobilePanel = ""; root.dataset.mobilePanel = ""; menuButton.setAttribute("aria-expanded", "false");
      if (controlsDialog.open) controlsDialog.close();
      if (directoryDialog.open) directoryDialog.close();
      if (restoreFocus && mobileReading) page.focus({ preventScroll: true });
    }
    function showMobilePanel(which) {
      if (!mobileReading || !["controls", "directory"].includes(which)) return;
      if (mobilePanel === which) { closeMobilePanel(); return; }
      closeMobilePanel(false);
      const dialog = which === "directory" ? directoryDialog : controlsDialog;
      mobilePanel = which; root.dataset.mobilePanel = which; menuButton.setAttribute("aria-expanded", "true");
      try { dialog.showModal(); }
      catch (error) { closeMobilePanel(false); throw error; }
      refs[which === "directory" ? "nr-close-directory" : "nr-close-controls"].focus({ preventScroll: true });
    }
    for (const [which, dialog] of [["controls", controlsDialog], ["directory", directoryDialog]]) {
      dialog.addEventListener("cancel", event => { event.preventDefault(); closeMobilePanel(); });
      dialog.addEventListener("close", () => { if (mobilePanel === which) closeMobilePanel(); });
      dialog.addEventListener("click", event => {
        if (event.target !== dialog) return;
        const rect = dialog.getBoundingClientRect();
        if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) closeMobilePanel();
      });
    }
    function updateMobileReading() {
      const next = mobile && readerVisible && readingWanted && Boolean(book);
      if (next !== mobileReading) { captureOffset(); closeMobilePanel(false); pointer = null; pendingTap = false; }
      mobileReading = next;
      root.dataset.mobileReading = String(next); root.dataset.mobileMode = mobile ? next ? "reading" : "shelf" : "desktop";
      mobileShell?.setReaderImmersive?.(next);
      if (current) restoreOffset(chapterSequence);
    }
    function setMobileEnvironment(nextMobile, visible = true) {
      if (destroyed) return;
      const moveFocus = Boolean(mobilePanel) && Boolean(visible);
      captureOffset();
      if (mobile !== Boolean(nextMobile)) {
        closeMobilePanel(false); mobile = Boolean(nextMobile);
        page.setAttribute("aria-label", mobile ? "正文，轻点中央或按 Enter 显示阅读菜单，使用方向键阅读" : "正文，使用滚轮或方向键阅读");
        if (mobile) {
          setFocusMode(false);
          sidebar.open = true; directoryDialog.append(sidebar);
          actions.append(...creationIDs.map(id => refs[id]));
          moreTools.append(bookHeader);
          controlsDialog.replaceChildren(controlsHeading, mobileQuick, toolbar, footer, moreTools);
        } else {
          creationActions.append(...creationIDs.map(id => refs[id])); preferences.append(toolbar);
          layout.replaceChildren(sidebar, page);
          reading.replaceChildren(bookHeader, readingTools, layout, footer, mobileStatus);
        }
      }
      readerVisible = Boolean(visible); if (!readerVisible) setFocusMode(false); updateMobileReading();
      if (moveFocus && !mobilePanel) page.focus({ preventScroll: true });
    }
    function showMobileShelf() {
      flushProgress(); readingWanted = false; updateMobileReading(); shelfPanel.open = true;
      void leaveFullscreen().catch(report); shelfSummary.focus({ preventScroll: true });
    }
    function handleMobileBack() {
      if (!mobileReading) return false;
      if (mobilePanel) closeMobilePanel(); else showMobileShelf();
      return true;
    }
    const selectionActive = () => Boolean(view.getSelection?.()?.toString().trim());
    page.addEventListener("pointerdown", event => {
      pointer = null; pendingTap = false;
      if (!mobileReading || mobilePanel || event.isPrimary === false || event.button > 0 || selectionActive()) return;
      if (event.target?.closest?.("a, button, input, select, textarea, summary")) return;
      pointer = { id: event.pointerId, x: event.clientX, y: event.clientY, top: page.scrollTop, time: Date.now(), moved: false };
    });
    page.addEventListener("pointermove", event => {
      if (pointer && (pointer.id !== event.pointerId || Math.hypot(event.clientX - pointer.x, event.clientY - pointer.y) > 10)) pointer.moved = true;
    });
    page.addEventListener("pointercancel", () => { pointer = null; pendingTap = false; });
    page.addEventListener("pointerup", event => {
      const gesture = pointer; pointer = null;
      if (!gesture || gesture.id !== event.pointerId || gesture.moved || Math.hypot(event.clientX - gesture.x, event.clientY - gesture.y) > 10 || Date.now() - gesture.time > 450 || Math.abs(page.scrollTop - gesture.top) > 2 || selectionActive()) return;
      const rect = page.getBoundingClientRect(), x = (event.clientX - rect.left) / rect.width, y = (event.clientY - rect.top) / rect.height;
      pendingTap = x >= .25 && x <= .75 && y >= .3 && y <= .7;
    });
    page.addEventListener("click", () => {
      const accepted = pendingTap; pendingTap = false;
      // Android synthesizes a click after pointerup. Opening a modal before
      // that click retargets it onto the new backdrop and immediately closes it.
      if (accepted && !selectionActive()) showMobilePanel("controls");
    });
    page.addEventListener("keydown", event => {
      if (!mobileReading || event.target !== page || event.altKey || event.ctrlKey || event.metaKey) return;
      if (event.key === "Enter" || event.key === "m") { event.preventDefault(); showMobilePanel("controls"); }
      else if (event.key === "Escape") { event.preventDefault(); handleMobileBack(); }
    });

    function setFocusMode(value) {
      value = Boolean(value) && !mobile && Boolean(book) && !isFullscreen();
      if (focusMode === value) return;
      captureOffset(); focusMode = value; root.dataset.focusMode = String(value);
      focusButton.textContent = value ? "退出专注 · Esc" : "专注阅读"; focusButton.setAttribute("aria-pressed", String(value));
      if (value) { preferences.open = creationTools.open = shortcuts.open = false; }
      restoreOffset(chapterSequence);
      if (value) page.focus({ preventScroll: true });
    }
    async function showDesktopShelf() {
      setFocusMode(false); await leaveFullscreen(); shelfPanel.open = true;
      shelfSummary.scrollIntoView?.({ block: "start" }); shelfSummary.focus({ preventScroll: true });
    }
    function rememberLocation() {
      if (!book || !current) return; captureOffset();
      lastLocation = { directory: book.directory, chapter: current.number, offset: progress.offset };
      refs["nr-return-location"].disabled = false;
    }
    async function navigateChapter(number) { rememberLocation(); await openChapter(number); page.focus({ preventScroll: true }); }
    function seek(offset) {
      if (!current || isLoading() || fullscreenTransition) return;
      rememberLocation(); progress.offset = clamp(offset, 0, 1, 0); restoreOffset(chapterSequence); scheduleSave(); page.focus({ preventScroll: true });
    }
    async function returnLocation() {
      const target = lastLocation; if (!target || target.directory !== book?.directory || isLoading()) return;
      rememberLocation(); await openChapter(target.chapter, target.offset); page.focus({ preventScroll: true });
    }
    async function turnPage(direction) {
      if (!current || isLoading() || fullscreenTransition || page.clientHeight <= 0) return;
      const range = Math.max(0, page.scrollHeight - page.clientHeight), top = page.scrollTop;
      const atEdge = direction > 0 ? top >= range - 2 : top <= 2;
      if (atEdge) {
        const index = book.chapters.findIndex(chapter => chapter.number === current.number), next = book.chapters[index + direction];
        if (next) await openChapter(next.number, direction < 0 ? 1 : 0);
      } else {
        const overlap = Math.min(page.clientHeight * .2, progress.fontSize * progress.lineHeight * 2);
        page.scrollTop = Math.min(range, Math.max(0, top + direction * Math.max(1, page.clientHeight - overlap)));
        captureOffset(); scheduleSave();
      }
      page.focus({ preventScroll: true }); renderControls();
    }
    const onReaderKeydown = event => {
      if (destroyed || mobile || !readerVisible || reading.hidden || !current || isLoading() || event.defaultPrevented || event.isComposing || event.altKey) return;
      if (root.closest?.(".view")?.classList.contains("hidden") || doc.querySelector?.("dialog[open]")) return;
      const target = event.target || doc.activeElement;
      if (target?.isContentEditable || target?.closest?.('input, textarea, select, button, summary, [contenteditable], [role="textbox"]') || ["INPUT", "TEXTAREA", "SELECT", "BUTTON", "SUMMARY"].includes(target?.tagName?.toUpperCase())) return;
      if (selectionActive()) return;
      const modifier = event.ctrlKey || event.metaKey, key = String(event.key || "").toLowerCase(); let action;
      if (event.shiftKey && key !== " " && key !== "f3") return;
      if (modifier && key === "f") action = async () => { await settings({ sidebarCollapsed: false }); sidebar.open = true; searchInput.focus({ preventScroll: true }); };
      else if (modifier && key === "b") action = addBookmark;
      else if (modifier && ["arrowleft", "arrowright"].includes(key)) action = () => adjacent(key === "arrowleft" ? -1 : 1);
      else if (!modifier && key === "f3") action = () => nextSearch(event.shiftKey ? -1 : 1);
      else if (!modifier && key === "escape" && focusMode) action = () => setFocusMode(false);
      else if (!modifier && !event.shiftKey && key === "f") action = () => setFocusMode(!focusMode);
      else if (!modifier && ["pagedown", "pageup", "arrowright", "arrowleft", " "].includes(key)) action = () => turnPage(["pageup", "arrowleft"].includes(key) || (key === " " && event.shiftKey) ? -1 : 1);
      if (action) { event.preventDefault(); return Promise.resolve(action()).catch(report); }
    };
    root.addEventListener("keydown", onReaderKeydown);

    const isFullscreen = () => doc.fullscreenElement === root;
    function updateFullscreenControl() {
      const active = isFullscreen(); root.dataset.fullscreen = String(active);
      fullscreenButton.textContent = active ? "退出全屏 · Esc" : "全屏阅读";
      fullscreenButton.setAttribute("aria-pressed", String(active));
      fullscreenButton.setAttribute("aria-label", active ? "退出全屏阅读" : "展开全屏阅读");
      fullscreenButton.disabled = fullscreenBusy || (!active && (!book || isLoading()));
      focusButton.disabled = active || fullscreenBusy || !book || isLoading();
    }
    const readLayout = () => [page.clientWidth, page.clientHeight, page.scrollHeight, isFullscreen()];
    const sameLayout = (left, right) => left && right && left.every((value, index) => value === right[index]);
    function beginFullscreenTransition() {
      if (!fullscreenTransition || fullscreenTransition.chapter !== chapterSequence || fullscreenTransition.book !== sequence) {
        fullscreenTransition = { revision: ++fullscreenRevision, chapter: chapterSequence, book: sequence, offset: progress.offset };
      }
      root.dataset.fullscreenLayout = "pending";
      return fullscreenTransition;
    }
    function preserveLayout() {
      if (destroyed || !current || sameLayout(layoutMetrics, readLayout())) return;
      // Resize/scroll may precede fullscreenchange on Windows. Preserve the
      // last reading position before accepting any scroll from the new range.
      if (layoutMetrics && layoutMetrics[3] !== isFullscreen()) beginFullscreenTransition();
      restoreOffset(chapterSequence);
    }
    const layoutObserver = typeof view.ResizeObserver === "function" ? new view.ResizeObserver(preserveLayout) : null;
    layoutObserver?.observe(page); layoutObserver?.observe(manuscript);
    view.addEventListener?.("resize", preserveLayout);
    function afterLayout(transition) {
      cancelLayoutWait?.();
      return new Promise(resolve => {
        let timer, previous = readLayout(), stable = 0, elapsed = 0;
        const finish = () => { clearTimeout(timer); if (cancelLayoutWait === finish) cancelLayoutWait = null; resolve(); };
        cancelLayoutWait = finish;
        const sample = () => {
          if (destroyed || fullscreenTransition !== transition || transition.chapter !== chapterSequence || transition.book !== sequence) { finish(); return; }
          const next = readLayout(); stable = sameLayout(previous, next) ? stable + 40 : 0; previous = next; elapsed += 40;
          restoreOffset(chapterSequence);
          // Native window bounds can settle after fullscreenchange and several
          // animation frames. Keep the target authoritative until sizes settle.
          if (stable >= 120 || elapsed >= 2000) finish();
          else timer = setTimeout(sample, 40);
        };
        timer = setTimeout(sample, 40);
      });
    }
    async function fullscreenChanged() {
      if (destroyed) return;
      if (!isFullscreen()) fullscreenWanted = false;
      const transition = beginFullscreenTransition(), token = chapterSequence;
      transition.revision = ++fullscreenRevision;
      const revision = transition.revision;
      updateFullscreenControl(); restoreOffset(token);
      try {
        await afterLayout(transition);
        if (destroyed || fullscreenTransition !== transition || revision !== fullscreenRevision || token !== chapterSequence || transition.book !== sequence) return;
        progress.offset = transition.offset;
        if (current && progress.mode === "original") {
          renderTask?.cancel(); renderTask = null;
          transition.chapter = ++chapterSequence;
          await renderChapter(transition.chapter);
        } else restoreOffset(token);
        if (current) scheduleSave();
      } finally { if (fullscreenTransition === transition && revision === fullscreenRevision) { fullscreenTransition = null; root.dataset.fullscreenLayout = "ready"; } }
    }
    async function leaveFullscreen() {
      closeMobilePanel(false);
      fullscreenWanted = false;
      if (!isFullscreen()) return;
      captureOffset(); const transition = beginFullscreenTransition(), requestRevision = transition.revision;
      try { await doc.exitFullscreen(); }
      catch (error) { if (fullscreenTransition === transition && transition.revision === requestRevision) { fullscreenTransition = null; root.dataset.fullscreenLayout = "ready"; } throw error; }
      finally { if (!destroyed) updateFullscreenControl(); }
    }
    async function toggleFullscreen() {
      if (fullscreenBusy || destroyed) return;
      if (isFullscreen()) { await leaveFullscreen(); return; }
      if (!book || isLoading()) return;
      if (typeof root.requestFullscreen !== "function" || doc.fullscreenEnabled === false) throw new Error("当前环境无法进入全屏阅读。");
      setFocusMode(false);
      captureOffset(); const transition = beginFullscreenTransition(), requestRevision = transition.revision; fullscreenWanted = true; fullscreenBusy = true; updateFullscreenControl();
      try {
        await root.requestFullscreen();
        // Switching view or destroying the reader can overtake Chromium's
        // asynchronous fullscreen request. Never reopen a now-hidden reader.
        if ((!fullscreenWanted || destroyed) && isFullscreen()) await doc.exitFullscreen();
      }
      catch (error) { fullscreenWanted = false; if (fullscreenTransition === transition && transition.revision === requestRevision) { fullscreenTransition = null; root.dataset.fullscreenLayout = "ready"; } throw new Error(`无法进入全屏阅读：${error.message || error}`); }
      finally { fullscreenBusy = false; if (!destroyed) updateFullscreenControl(); }
    }
    const onFullscreenChanged = () => { void fullscreenChanged().catch(report); };
    doc.addEventListener?.("fullscreenchange", onFullscreenChanged);

    function renderShelf() {
      const filter = shelfFilter.value.trim().toLocaleLowerCase();
      const selected = books.filter((entry) => `${entry.title} ${entry.author || ""} ${entry.format}`.toLocaleLowerCase().includes(filter));
      const recent = entry => Date.parse(entry.lastReadAt || "") || 0;
      selected.sort((left, right) => (shelfSort.value === "title" ? 0 : shelfSort.value === "progress" ? (Number(right.progress?.ratio) || 0) - (Number(left.progress?.ratio) || 0) : recent(right) - recent(left)) || String(left.title || "").localeCompare(String(right.title || ""), "zh-CN"));
      const latest = selected.filter(entry => recent(entry) > 0).sort((left, right) => recent(right) - recent(left))[0];
      continueReading.hidden = !latest;
      if (latest) {
        continueReading.dataset.directory = latest.directory;
        continueReading.replaceChildren(node("span", "nr-eyebrow", "CONTINUE READING"), node("strong", "", `继续阅读 · ${latest.title || "未命名作品"}`), node("span", "nr-muted", `上次读到第 ${latest.progress?.chapter || 1} 章 · 整本 ${Math.round(clamp(latest.progress?.ratio, 0, 1, 0) * 100)}%`));
      }
      shelf.replaceChildren();
      if (!selected.length) { shelf.append(node("p", "nr-empty", books.length ? "没有匹配的书籍。" : "还没有可阅读的作品。导入一本书，或在项目中心创建手动项目。")); return; }
      for (const entry of selected) {
        const card = button("", () => openBook(entry.directory), "nr-book"); card.dataset.directory = entry.directory;
        card.setAttribute("aria-pressed", String(entry.directory === book?.directory));
        const cover = node("span", "nr-book-cover", Array.from(entry.title || "书").slice(0, 4).join("")); cover.setAttribute("aria-hidden", "true");
        const details = node("span", "nr-book-details"), ratio = clamp(entry.progress?.ratio, 0, 1, 0);
        const meter = node("progress", "nr-book-progress"); meter.max = 1; meter.value = ratio; meter.setAttribute("aria-label", `整本阅读进度 ${Math.round(ratio * 100)}%`);
        details.append(node("span", "nr-book-format", String(entry.format || "小说").toUpperCase()), node("strong", "", entry.title || "未命名作品"), node("span", "nr-muted", `${entry.author ? `${entry.author} · ` : ""}${entry.chapters || 0} 章 · ${Number(entry.words || 0).toLocaleString()} 字`), meter, node("span", "nr-muted", recent(entry) ? `已读 ${Math.round(ratio * 100)}% · ${new Date(recent(entry)).toLocaleDateString("zh-CN")}` : "尚未阅读"));
        card.append(cover, details); shelf.append(card);
      }
    }
    async function load({ refreshBook = true } = {}) {
      const token = ++shelfSequence, selectedBookToken = sequence;
      shelfLoading = true; renderControls(); status("正在加载书架…");
      try {
        const result = await api("/api/reader/books"); if (destroyed || token !== shelfSequence) return;
        books = Array.isArray(result.books) ? result.books : []; renderShelf(); status("");
        const selectedDirectory = getContext().directory;
        // A book explicitly chosen during this shelf request is newer intent.
        if (refreshBook && selectedBookToken === sequence) {
          if (selectedDirectory && books.some((entry) => entry.directory === selectedDirectory)) await openBook(selectedDirectory);
          else if (book && !books.some((entry) => entry.directory === book.directory)) forgetBook(book.directory);
        }
      } finally { if (token === shelfSequence) { shelfLoading = false; renderControls(); } }
    }
    function captureOffset() {
      if (restoring || fullscreenTransition) return;
      // A hidden view has no layout metrics. Keep its last observed position.
      if (!current || page.clientHeight <= 0 || page.scrollHeight <= 0) return;
      if (!sameLayout(layoutMetrics, readLayout())) { preserveLayout(); return; }
      const range = page.scrollHeight - page.clientHeight;
      progress.offset = range > 0 ? clamp(page.scrollTop / range, 0, 1, 0) : 0; renderPosition();
    }
    const cacheKey = (directory) => `ainovel.reader.pending.v1:${encodeURIComponent(directory)}`;
    function cache(payload) { try { storage?.setItem(cacheKey(payload.directory), JSON.stringify(payload)); } catch (_) { /* Disk progress remains authoritative when local storage is full. */ } }
    function rememberProgress() {
      if (!book || !progress.chapter) return null;
      const payload = { directory: book.directory, ...progress, bookmarks: progress.bookmarks.map((item) => ({ ...item })) };
      pendingProgress.set(payload.directory, payload); cache(payload); return payload;
    }
    function scheduleSave() { rememberProgress(); clearTimeout(saveTimer); saveTimer = setTimeout(() => saveProgress(), 500); }
    function retryLater() {
      if (destroyed || retryTimer || !pendingProgress.size) return;
      retryTimer = setTimeout(() => {
        retryTimer = null;
        for (const payload of pendingProgress.values()) saveChain = saveChain.catch(() => {}).then(() => transmit(payload));
      }, 15000);
    }
    async function transmit(payload, keepalive = false) {
      if (forgottenBooks.has(payload.directory)) return;
      try {
        const saved = await api("/api/reader/progress", { method: "PUT", body: payload, ...(keepalive ? { keepalive: true } : {}) });
        const entry = books.find(item => item.directory === payload.directory);
        if (entry && saved?.lastReadAt) {
          entry.lastReadAt = saved.lastReadAt;
          entry.progress = { chapter: payload.chapter, offset: payload.offset, ratio: book?.directory === payload.directory ? bookRatio(book.chapters, payload.chapter, payload.offset) : entry.progress?.ratio || 0 };
          if (!destroyed) renderShelf();
        }
        // A closing renderer may not finish callbacks. Keep its recovery cache
        // until the next ordinary save, so an older in-flight write cannot win.
        if (!keepalive && pendingProgress.get(payload.directory) === payload) {
          pendingProgress.delete(payload.directory); try { storage?.removeItem(cacheKey(payload.directory)); } catch (_) { /* Optional cache. */ }
        }
        lastSaveError = "";
      } catch (error) {
        if (!destroyed && book?.directory === payload.directory && lastSaveError !== error.message) {
          lastSaveError = error.message; status(`进度已暂存本机，稍后自动重试：${error.message}`);
        }
      } finally { retryLater(); }
    }
    function saveProgress() {
      clearTimeout(saveTimer); saveTimer = null;
      if (!book || !progress.chapter || destroyed) return saveChain;
      const payload = rememberProgress();
      // Serialize snapshots so a slow earlier request cannot overwrite a later one.
      saveChain = saveChain.catch(() => {}).then(() => transmit(payload));
      return saveChain;
    }
    function flushProgress() {
      if (destroyed) return;
      captureOffset(); const payload = rememberProgress(); if (!payload) return;
      clearTimeout(saveTimer); saveTimer = null;
      const signature = JSON.stringify(payload), now = Date.now();
      if (signature === lifecycleSignature && now - lifecycleTime < 1000) return;
      lifecycleSignature = signature; lifecycleTime = now;
      // Start while the renderer is alive. Pending local storage also covers
      // network failure, a project lock and the browser keepalive size budget.
      void transmit(payload, true);
    }
    const visibilityChanged = () => { if (doc.hidden) flushProgress(); else if (pendingProgress.size) retryLater(); };
    doc.addEventListener?.("visibilitychange", visibilityChanged);
    scope.addEventListener?.("pagehide", flushProgress); scope.addEventListener?.("beforeunload", flushProgress);
    function stopPDF() {
      renderTask?.cancel(); renderTask = null;
      if (pdfSession) { const old = pdfSession; pdfSession = null; old.controller.abort(); Promise.resolve(old.task?.destroy()).catch(() => {}); }
    }
    function forgetBook(directory) {
      forgottenBooks.add(directory);
      pendingProgress.delete(directory);
      try { storage?.removeItem(cacheKey(directory)); } catch (_) { /* Optional cache. */ }
      books = books.filter((entry) => entry.directory !== directory);
      if (book?.directory === directory) {
        setFocusMode(false); lastLocation = null; searchMatches = []; activeHit = null;
        void leaveFullscreen().catch(report);
        sequence++; chapterSequence++; searchSequence++;
        clearTimeout(saveTimer); saveTimer = null;
        stopPDF(); book = null; current = null; bookLoading = false; progress = normalizeProgress();
        reading.hidden = true; shelfPanel.open = true; readingWanted = false; updateMobileReading();
        manuscript.replaceChildren(); results.replaceChildren(); toc.replaceChildren(); bookmarks.replaceChildren();
        status(""); renderControls();
      }
      renderShelf();
    }
    async function openBook(directory) {
      if (!directory || destroyed) return;
      const token = ++sequence; ++chapterSequence; ++searchSequence;
      bookLoading = true; renderControls(); status("正在打开作品…"); captureOffset(); stopPDF();
      try {
        await saveProgress(); if (token !== sequence || destroyed) return;
        current = null; renderControls();
        const next = await api(`/api/reader/book?${query(directory)}`); if (token !== sequence || destroyed) return;
        if (next.directory !== directory || !Array.isArray(next.chapters)) throw new Error("返回的书籍与请求不匹配");
        forgottenBooks.delete(directory);
        if (getContext().directory !== directory) await onSelectProject(directory);
        if (token !== sequence || destroyed) return;
        let unsaved = pendingProgress.get(directory);
        if (!unsaved) { try { const stored = JSON.parse(storage?.getItem(cacheKey(directory)) || "null"); if (stored?.directory === directory) unsaved = stored; } catch (_) { /* Ignore a damaged optional cache. */ } }
        book = next; progress = normalizeProgress({ ...next.progress, ...unsaved }, next.chapters);
        lastLocation = null; searchMatches = []; searchTerm = ""; searchIndex = -1; activeHit = null;
        if (next.source?.format !== "pdf") progress.mode = "text";
        if (next.source?.format === "pdf" && next.source.hasText === false) progress.mode = "original";
        refs["nr-book-title"].textContent = book.title; refs["nr-book-meta"].textContent = `${book.author ? `${book.author} · ` : ""}${book.chapters.length} 章 · ${String(book.format || "小说").toUpperCase()}`;
        reading.hidden = false; shelfPanel.open = false; results.replaceChildren(); searchInput.value = "";
        mobileTitle.textContent = book.title || "阅读菜单"; readingWanted = true; updateMobileReading();
        renderShelf(); renderToc(); renderBookmarks(); applySettings();
        if (progress.chapter) await openChapter(progress.chapter, progress.offset, false);
        else { manuscript.replaceChildren(node("p", "nr-empty", "这本作品还没有章节。可在章节工作台手动添加。")); status("还没有可阅读的章节。"); renderControls(); }
      } finally { if (token === sequence) { bookLoading = false; renderControls(); } }
    }
    function renderToc() {
      toc.replaceChildren(...(book?.chapters || []).map((chapter) => { const item = button(chapter.title || `第 ${chapter.number} 章`, () => openChapter(chapter.number), "nr-toc-item"); item.dataset.chapter = String(chapter.number); item.setAttribute("aria-current", chapter.number === progress.chapter ? "page" : "false"); return item; }));
      chapterJump.replaceChildren(...(book?.chapters || []).map(chapter => { const option = node("option", "", chapter.title || `第 ${chapter.number} 章`); option.value = String(chapter.number); return option; })); chapterJump.value = String(progress.chapter);
    }
    function renderBookmarks() {
      bookmarks.replaceChildren();
      if (!progress.bookmarks.length) { bookmarks.append(node("p", "nr-muted", "读到喜欢的地方，添加一个书签。")); return; }
      progress.bookmarks.forEach((mark, index) => { const row = node("div", "nr-bookmark-row"); const jump = button(mark.label, () => openChapter(mark.chapter, mark.offset), "nr-bookmark"); const remove = button("删除", () => { progress.bookmarks.splice(index, 1); renderBookmarks(); scheduleSave(); }, "nr-button nr-small"); remove.setAttribute("aria-label", `删除书签：${mark.label}`); row.append(jump, remove); bookmarks.append(row); });
    }
    function renderPosition() {
      const index = book?.chapters.findIndex((chapter) => chapter.number === progress.chapter) ?? -1;
      refs["nr-position"].textContent = index < 0 ? "暂无章节" : `${index + 1} / ${book.chapters.length} 章 · 整本 ${Math.round(bookRatio(book.chapters, progress.chapter, progress.offset) * 100)}% · 本章 ${Math.round(progress.offset * 100)}%`;
      compactPosition.textContent = refs["nr-position"].textContent;
      if (doc.activeElement !== progressSlider) progressSlider.value = String(Math.round(progress.offset * 1000));
      progressSlider.setAttribute("aria-valuetext", `本章 ${Math.round(progress.offset * 100)}%`);
      progressLabel.textContent = `本章 ${Math.round(progress.offset * 100)}%`;
      chapterJump.value = String(progress.chapter);
    }
    function renderControls() {
      const loading = isLoading(); root.setAttribute("aria-busy", String(loading));
      updateFullscreenControl();
      const hasText = Boolean(current?.content?.trim());
      for (const id of ["nr-audio", "nr-image", "nr-video"]) refs[id].disabled = loading || !hasText || actionBusy;
      refs["nr-distill-chapter"].disabled = loading || !hasText || distillBusy;
      refs["nr-distill-book"].disabled = loading || distillBusy || !book || !book.chapters.some((chapter) => chapter.hasText !== false);
      refs["nr-edit"].disabled = loading || !current || actionBusy; refs["nr-add-bookmark"].disabled = loading || !current;
      font.disabled = height.disabled = fontFamily.disabled = pageWidth.disabled = paragraphSpacing.disabled = loading || !book || progress.mode === "original"; theme.disabled = mode.disabled = loading || !book;
      searchInput.disabled = searchButton.disabled = loading || !book;
      sidebarButton.disabled = loading || !book;
      chapterJump.disabled = progressSlider.disabled = loading || !current;
      refs["nr-page-prev"].disabled = refs["nr-page-next"].disabled = loading || !current;
      refs["nr-search-prev"].disabled = refs["nr-search-next"].disabled = loading || !searchMatches.length;
      refs["nr-return-location"].disabled = loading || !lastLocation || lastLocation.directory !== book?.directory;
      for (const item of toc.children) item.disabled = loading;
      for (const row of bookmarks.children) for (const item of row.children) item.disabled = loading;
      const index = book?.chapters.findIndex((chapter) => chapter.number === current?.number) ?? -1;
      refs["nr-prev"].disabled = loading || index <= 0; refs["nr-next"].disabled = loading || index < 0 || index >= book.chapters.length - 1; renderPosition();
    }
    async function openChapter(number, offset = 0, save = true) {
      if (!book || (isLoading() && save) || !book.chapters.some((chapter) => chapter.number === Number(number))) return;
      closeMobilePanel();
      if (save && current) { captureOffset(); saveProgress(); }
      const token = ++chapterSequence, bookToken = sequence, directory = book.directory;
      activeHit = null;
      renderTask?.cancel(); renderTask = null; current = null; renderControls(); manuscript.replaceChildren(node("p", "nr-empty", "正在加载章节…"));
      const chapter = await api(`/api/reader/chapter/${Number(number)}?${query(directory)}`);
      if (token !== chapterSequence || bookToken !== sequence || destroyed) return;
      if (Number(chapter.number) !== Number(number)) throw new Error("返回的章节与请求不匹配");
      current = chapter; progress.chapter = Number(number); progress.offset = clamp(offset, 0, 1, 0); renderToc(); renderControls();
      await renderChapter(token); if (token === chapterSequence && bookToken === sequence) { scheduleSave(); status(chapter.content?.trim() ? "阅读进度自动保存。朗读、配图和视频会打开本章的创作工具。" : book.source?.format === "pdf" ? "本页没有可提取文字。可阅读 PDF 原页；扫描文字暂不支持 OCR，编辑补充正文后可使用朗读等功能。" : "本章暂无正文，可手动编辑。 "); }
    }
    function safeLocalURL(value) {
      try { const url = new URL(value, scope.location?.href || "http://localhost/"); return url.origin === new URL(scope.location?.href || "http://localhost/").origin && url.pathname.startsWith("/api/") ? url.href : ""; } catch (_) { return ""; }
    }
    function appendImages(target, placement) {
      for (const item of current?.images || []) {
        if ((item.placement || "chapter-end") !== placement) continue;
        const src = safeLocalURL(item.url); if (!src) continue;
        const figure = node("figure", "nr-illustration"), image = node("img"); image.src = src; image.alt = item.caption || item.name || "章节配图"; image.loading = "lazy";
        if (item.width > 0 && item.height > 0) { image.width = item.width; image.height = item.height; }
        figure.append(image); if (item.caption) figure.append(node("figcaption", "", item.caption)); target.append(figure);
      }
    }
    function restoreOffset(token) {
      if (token !== chapterSequence || destroyed) return;
      const wasRestoring = restoring; restoring = true;
      if (fullscreenTransition?.chapter === token && fullscreenTransition.book === sequence) progress.offset = fullscreenTransition.offset;
      page.scrollTop = Math.max(0, page.scrollHeight - page.clientHeight) * progress.offset;
      layoutMetrics = readLayout(); restoring = wasRestoring; renderPosition();
    }
    function renderProse(text) {
      const prose = node("div", "nr-prose"); let start = 0, hitElement = null;
      // Keep source offsets in Unicode code points, matching the search API.
      // All book text stays in textContent; highlights never parse book HTML.
      const pieces = text.split(/(\n[ \t]*\n+)/);
      for (let index = 0; index < pieces.length; index++) {
        const piece = pieces[index], points = Array.from(piece), end = start + points.length;
        if (index % 2 === 0) {
          const paragraph = node("p", "nr-paragraph");
          const hitStart = Math.max(start, activeHit?.start ?? Infinity), hitEnd = Math.min(end, (activeHit?.start ?? 0) + (activeHit?.length || 0));
          if (hitStart < hitEnd) {
            paragraph.append(node("span", "", points.slice(0, hitStart - start).join("")));
            const mark = node("mark", "nr-search-hit", points.slice(hitStart - start, hitEnd - start).join(""));
            paragraph.append(mark, node("span", "", points.slice(hitEnd - start).join(""))); hitElement ||= mark;
          } else paragraph.textContent = piece;
          prose.append(paragraph);
        }
        start = end;
      }
      return { prose, hitElement };
    }
    async function renderChapter(token = chapterSequence) {
      if (!current) return;
      manuscript.replaceChildren(node("h2", "nr-chapter-title", current.title || `第 ${current.number} 章`));
      if (progress.mode === "original" && book.source?.format === "pdf" && Number(current.pdfPage) > 0) {
        const note = node("p", "nr-muted", "正在绘制 PDF 原页…"); const canvas = node("canvas", "nr-pdf-canvas"); canvas.setAttribute("role", "img"); canvas.setAttribute("aria-label", `${current.title || "PDF"} 原页，文字内容可切换至文字阅读模式`); manuscript.append(note, canvas);
        try {
          const pdf = await getBookPDF(); if (token !== chapterSequence || destroyed) return;
          const pdfPage = await pdf.getPage(Number(current.pdfPage)); if (token !== chapterSequence || destroyed) return;
          const base = pdfPage.getViewport({ scale: 1 });
          const scale = Math.min(Math.max(320, page.clientWidth - 72) / base.width, 2) * Math.min(scope.devicePixelRatio || 1, 2);
          const bounded = Math.min(scale, Math.sqrt(8_000_000 / (base.width * base.height))); const viewport = pdfPage.getViewport({ scale: bounded });
          canvas.width = Math.ceil(viewport.width); canvas.height = Math.ceil(viewport.height);
          const task = pdfPage.render({ canvasContext: canvas.getContext("2d"), viewport, annotationMode: 0, background: "rgb(255,255,255)" }); renderTask = task;
          await task.promise; if (token !== chapterSequence || destroyed) return; renderTask = null; note.remove(); pdfPage.cleanup();
        } catch (error) { if (token !== chapterSequence || destroyed || error?.name === "RenderingCancelledException" || error?.name === "AbortError") return; note.textContent = `原页读取失败：${error.message}。可切换至文字阅读。`; }
      } else {
        if (progress.mode === "original" && book.source?.format === "pdf") manuscript.append(node("p", "nr-muted", "本章为手动新增，没有对应的 PDF 原页，以下显示已保存正文。"));
        appendImages(manuscript, "chapter-start");
        manuscript.append(renderProse(current.content || (book.source?.format === "pdf" ? "本页没有可提取文字，请切换到 PDF 原页阅读。" : "本章暂无正文。")).prose);
        appendImages(manuscript, "chapter-end");
      }
      restoreOffset(token);
    }
    async function library() {
      if (!pdfLibrary) pdfLibrary = loadPDF().then((value) => { value.GlobalWorkerOptions.workerSrc = "/vendor/pdfjs/pdf.worker.mjs"; return value; }).catch((error) => { pdfLibrary = null; throw error; });
      return pdfLibrary;
    }
    function password(task, controller) {
      const signal = controller.signal;
      let dialog = null, settle = null;
      const close = () => { dialog?.remove(); dialog = null; settle = null; };
      const abort = () => { close(); Promise.resolve(task.destroy()).catch(() => {}); };
      signal.addEventListener("abort", abort, { once: true });
      task.onPassword = (update, reason) => {
        close(); if (signal.aborted) { abort(); return; }
        dialog = node("dialog", "nr-password"); const form = node("form"), fieldInput = node("input", "nr-input"); fieldInput.type = "password"; fieldInput.autocomplete = "off"; fieldInput.required = true;
        const description = node("p", "", reason === 2 ? "密码不正确，请重新输入。密码只用于本次打开，不会保存。" : "此 PDF 已加密，请输入密码。密码不会保存。");
        const submit = node("button", "nr-button nr-primary", "打开 PDF"); submit.type = "submit";
        const cancel = button("取消", () => { close(); controller.abort(); });
        settle = update; form.addEventListener("submit", (event) => { event.preventDefault(); const value = fieldInput.value; fieldInput.value = ""; const callback = settle; close(); callback?.(value); });
        dialog.addEventListener("cancel", (event) => { event.preventDefault(); close(); controller.abort(); });
        form.append(description, field("PDF 密码", fieldInput), submit, cancel); dialog.append(form); root.append(dialog); dialog.showModal(); fieldInput.focus();
      };
      return () => { signal.removeEventListener("abort", abort); close(); };
    }
    async function getBookPDF() {
      if (pdfSession?.directory === book.directory) return pdfSession.promise;
      stopPDF(); const directory = book.directory, controller = new AbortController(); const session = { directory, controller, task: null, promise: null }; pdfSession = session;
      session.promise = (async () => {
        const lib = await library(); if (controller.signal.aborted) throw aborted();
        const source = safeLocalURL(book.source?.url || `/api/reader/source?${query(directory)}`); if (!source) throw new Error("PDF 源地址无效");
        const response = await fetchBytes(source, { signal: controller.signal, credentials: "same-origin" }); if (!response.ok) throw new Error(`PDF 文件读取失败（${response.status}）`);
        const bytes = new Uint8Array(await response.arrayBuffer()); if (controller.signal.aborted) throw aborted();
        const task = lib.getDocument(pdfOptions(bytes)); session.task = task; const cleanup = password(task, controller);
        try { return await abortable(task.promise, controller.signal); } finally { cleanup(); }
      })().catch((error) => { if (pdfSession === session) pdfSession = null; throw error; });
      return session.promise;
    }
    function applySettings() {
      page.dataset.theme = mobileStatus.dataset.theme = progress.theme; page.style.setProperty("--nr-font-size", `${progress.fontSize}px`); page.style.setProperty("--nr-line-height", String(progress.lineHeight));
      page.style.setProperty("--nr-font-family", FONTS[progress.fontFamily]); page.style.setProperty("--nr-content-width", `${WIDTHS[progress.pageWidth]}px`); page.style.setProperty("--nr-paragraph-gap", `${progress.paragraphSpacing}em`);
      font.value = String(progress.fontSize); height.value = String(progress.lineHeight); theme.value = progress.theme; mode.value = progress.mode;
      fontFamily.value = progress.fontFamily; pageWidth.value = progress.pageWidth; paragraphSpacing.value = String(progress.paragraphSpacing);
      root.dataset.sidebarCollapsed = String(progress.sidebarCollapsed); sidebarButton.setAttribute("aria-expanded", String(!progress.sidebarCollapsed)); sidebarButton.textContent = progress.sidebarCollapsed ? "展开目录" : "收起目录";
      modeField.hidden = book?.source?.format !== "pdf"; font.disabled = height.disabled = fontFamily.disabled = pageWidth.disabled = paragraphSpacing.disabled = progress.mode === "original";
    }
    async function settings(values) {
      captureOffset(); progress = normalizeProgress({ ...progress, ...values }, book?.chapters || []); applySettings();
      if (values.sidebarCollapsed === false) sidebar.open = true;
      if (values.mode) { renderTask?.cancel(); renderTask = null; await renderChapter(++chapterSequence); }
      else restoreOffset(chapterSequence);
      scheduleSave();
    }
    async function adjacent(direction) { const index = book?.chapters.findIndex((chapter) => chapter.number === progress.chapter); const next = book?.chapters[index + direction]; if (next) await openChapter(next.number); }
    function addBookmark() {
      if (!current || isLoading()) return; captureOffset();
      if (progress.bookmarks.length >= 200) { status("最多保存 200 个书签，请先删除不需要的书签。"); return; }
      if (progress.bookmarks.some((mark) => mark.chapter === current.number && Math.abs(mark.offset - progress.offset) < 0.005)) { status("此位置已有书签。"); return; }
      progress.bookmarks.push({ chapter: current.number, offset: progress.offset, label: `${current.title || `第 ${current.number} 章`} · ${Math.round(progress.offset * 100)}%`.slice(0, 80) }); renderBookmarks(); scheduleSave(); status("书签已添加。");
    }
    async function search() {
      if (!book || isLoading()) return; const term = searchInput.value.trim(); const token = ++searchSequence, directory = book.directory; results.replaceChildren();
      if (activeHit) {
        captureOffset(); activeHit = null;
        const old = manuscript.querySelector?.(".nr-prose") || [...manuscript.children].find(child => child.className === "nr-prose");
        if (old && current && progress.mode === "text") {
          const replacement = renderProse(current.content || "").prose;
          if (old.replaceWith) old.replaceWith(replacement); else { old.remove(); manuscript.append(replacement); }
          restoreOffset(chapterSequence);
        }
      }
      searchMatches = []; searchIndex = -1; searchTerm = term; renderControls();
      if (!term) return; results.append(node("p", "nr-muted", "正在搜索…"));
      const response = await api(`/api/reader/search?${query(directory)}&q=${encodeURIComponent(term)}`); if (token !== searchSequence || directory !== book?.directory || destroyed) return;
      searchMatches = Array.isArray(response.matches) ? response.matches : [];
      results.replaceChildren(node("p", "nr-muted", `找到 ${searchMatches.length} 处结果${response.truncated ? "（最多显示 100 处）" : ""}`));
      searchMatches.forEach((match, index) => { const item = button("", () => openSearchHit(index), "nr-search-result"); item.dataset.resultIndex = String(index); item.append(node("strong", "", match.title), node("span", "", match.excerpt)); results.append(item); });
      renderControls();
    }
    async function nextSearch(direction) {
      if (!searchMatches.length || isLoading()) return;
      const next = searchIndex < 0 ? direction > 0 ? 0 : searchMatches.length - 1 : (searchIndex + direction + searchMatches.length) % searchMatches.length;
      await openSearchHit(next);
    }
    async function openSearchHit(index) {
      const match = searchMatches[index]; if (!match || isLoading()) return;
      const directory = book.directory, token = searchSequence;
      rememberLocation(); const loading = openChapter(match.chapter), expectedChapter = chapterSequence; await loading;
      if (destroyed || book?.directory !== directory || token !== searchSequence || chapterSequence !== expectedChapter || current?.number !== match.chapter) return;
      if (match.contentHash && current.contentHash && match.contentHash !== current.contentHash) { status("正文已更新，请重新搜索以准确定位。"); return; }
      const size = Array.from(current.content || "").length;
      if (!Number.isInteger(match.start) || !Number.isInteger(match.length) || match.start < 0 || match.length <= 0 || match.start + match.length > size) { status("已打开对应章节，请重新搜索以获取准确位置。"); return; }
      if (progress.mode === "original") { const rendering = settings({ mode: "text" }), expectedRender = chapterSequence; await rendering; if (book?.directory !== directory || token !== searchSequence || chapterSequence !== expectedRender || current?.number !== match.chapter) return; }
      searchIndex = index; activeHit = { start: match.start, length: match.length };
      // Rebuild only the text block. Images and chapter content stay intact.
      const old = manuscript.querySelector?.(".nr-prose") || [...manuscript.children].find(child => child.className === "nr-prose");
      const rendered = renderProse(current.content || "");
      if (old?.replaceWith) old.replaceWith(rendered.prose);
      else { old?.remove(); manuscript.append(rendered.prose); }
      const mark = rendered.hitElement;
      if (mark) {
        restoring = true;
        const rect = mark.getBoundingClientRect(), pageRect = page.getBoundingClientRect();
        page.scrollTop = Math.max(0, page.scrollTop + rect.top - pageRect.top - page.clientHeight * .3);
        layoutMetrics = readLayout(); restoring = false; captureOffset(); scheduleSave();
      }
      for (const item of results.children) if (item.dataset.resultIndex !== undefined) item.setAttribute("aria-current", Number(item.dataset.resultIndex) === index ? "true" : "false");
      status(`第 ${index + 1} / ${searchMatches.length} 处匹配 · ${searchTerm}`); page.focus({ preventScroll: true });
    }
    async function chapterAction(callback) {
      if (!current || actionBusy || isLoading()) return;
      const directory = book.directory, number = current.number, token = sequence;
      const valid = () => !destroyed && sequence === token && book?.directory === directory && current?.number === number;
      actionBusy = true; renderControls();
      try { await saveProgress(); if (!valid()) return; setFocusMode(false); await leaveFullscreen(); if (!valid()) return; if (getContext().directory !== directory) await onSelectProject(directory); if (valid()) await callback(number); }
      finally { actionBusy = false; renderControls(); }
    }
    async function media(kind) { if (current?.content?.trim()) await chapterAction(number => onOpenMedia(kind, number)); }
    async function edit() { await chapterAction(number => onEditChapter(number)); }
    async function distill(whole) {
      if (isLoading() || distillBusy || !book || (!whole && !current?.content?.trim())) return;
      const directory = book.directory, token = sequence, number = current?.number;
      distillBusy = true; renderControls();
      try {
        const result = await api(`/api/reader/text?${query(directory)}${whole ? "" : `&chapter=${number}`}`); if (token !== sequence || directory !== book?.directory || destroyed || (!whole && current?.number !== number)) return;
        if (!result.content?.trim()) { status("没有可用于风格蒸馏的文字。扫描 PDF 可先手动补充正文。"); return; }
        setFocusMode(false); await leaveFullscreen(); if (token !== sequence || destroyed) return;
        await onDistill({ title: result.title || book.title, text: result.content, directory, chapters: whole ? book.chapters.map((chapter) => chapter.number) : [number] });
      } finally { distillBusy = false; renderControls(); }
    }
    function cancelImport() {
      if (!importJob) return;
      const job = importJob; importJob = null; job.controller.abort(); Promise.resolve(job.task?.destroy()).catch(() => {});
      file.value = ""; importSubmit.disabled = file.disabled = title.disabled = false; importCancel.hidden = true;
      importStatus.textContent = job.uploading ? "已取消等待。上传中的文档可能已完成导入，刷新书架可确认。" : "已取消导入，可重新选择文件。";
    }
    async function importFile(selectedFile, suppliedTitle = "") {
      if (importJob) return;
      if (!selectedFile) { importStatus.textContent = "请先选择需要导入的文档。"; return; }
      if (selectedFile.size > MAX_FILE) { importStatus.textContent = "文件超过 64 MB，请先拆分后再导入。"; file.value = ""; return; }
      if (!/\.(txt|epub|pdf|docx|md|markdown|html|htm)$/i.test(selectedFile.name)) { importStatus.textContent = "支持 TXT、EPUB、PDF、DOCX、Markdown 和 HTML 格式。"; file.value = ""; return; }
      const job = { controller: new AbortController(), task: null, uploading: false }; importJob = job; importSubmit.disabled = file.disabled = title.disabled = true; importCancel.hidden = false;
      importStatus.textContent = "正在读取文档…";
      let cleanup = () => {}, importedDirectory = "";
      try {
        const form = new FormData(); form.append("file", selectedFile); if (suppliedTitle.trim()) form.append("title", suppliedTitle.trim());
        if (/\.pdf$/i.test(selectedFile.name)) {
          const lib = await library(); if (job.controller.signal.aborted) throw aborted();
          const data = new Uint8Array(await selectedFile.arrayBuffer()); if (job.controller.signal.aborted) throw aborted();
          job.task = lib.getDocument(pdfOptions(data)); cleanup = password(job.task, job.controller); const pdf = await abortable(job.task.promise, job.controller.signal);
          if (pdf.numPages > MAX_PAGES) throw new Error("PDF 超过 2000 页，请拆分后导入。");
          const pages = []; let bytes = 0;
          for (let pageNumber = 1; pageNumber <= pdf.numPages; pageNumber++) {
            if (job.controller.signal.aborted) throw aborted(); importStatus.textContent = `正在提取 PDF 文字 ${pageNumber} / ${pdf.numPages} 页…`;
            const pdfPage = await pdf.getPage(pageNumber), content = await pdfPage.getTextContent(); const text = textFromPDFItems(content.items);
            bytes += new TextEncoder().encode(text).byteLength; if (bytes > MAX_TEXT) throw new Error("提取文字超过 32 MB，请拆分后导入。");
            pages.push({ page: pageNumber, title: `第 ${pageNumber} 页`, text }); pdfPage.cleanup();
          }
          form.append("pdfPages", JSON.stringify(pages)); cleanup(); cleanup = () => {}; await job.task.destroy(); job.task = null;
        }
        if (job.controller.signal.aborted) throw aborted();
        job.uploading = true; importStatus.textContent = "正在保存文档与章节…";
        const result = await api("/api/reader/import", { method: "POST", body: form, signal: job.controller.signal });
        if (job !== importJob || destroyed) return;
        importedDirectory = result.directory;
        file.value = ""; title.value = ""; importStatus.textContent = "导入成功，已创建可手动编辑的项目。";
        await load({ refreshBook: false }); if (job !== importJob || destroyed) return; await openBook(result.directory);
      } catch (error) {
        if (job === importJob) { file.value = ""; importStatus.textContent = importedDirectory ? `文档已导入，但打开失败：${error.message}。请刷新书架后打开，无需重复导入。` : error?.name === "AbortError" ? "已取消导入，可重新选择文件。" : `导入失败：${error.message}。可重新选择文件重试。`; }
      } finally {
        cleanup(); await Promise.resolve(job.task?.destroy()).catch(() => {});
        if (job === importJob) { importJob = null; importSubmit.disabled = file.disabled = title.disabled = false; importCancel.hidden = true; }
      }
    }
    function sync() { return book && getContext().directory === book.directory ? openBook(book.directory) : Promise.resolve(); }
    function destroy() {
      setFocusMode(false); root.removeEventListener?.("keydown", onReaderKeydown);
      flushProgress(); closeMobilePanel(false); mobileShell?.setReaderImmersive?.(false); unregisterMobile?.(); void leaveFullscreen().catch(() => {}); destroyed = true; sequence++; chapterSequence++; searchSequence++; shelfSequence++; fullscreenRevision++; clearTimeout(saveTimer); clearTimeout(retryTimer);
      doc.removeEventListener?.("fullscreenchange", onFullscreenChanged);
      cancelLayoutWait?.(); layoutObserver?.disconnect(); view.removeEventListener?.("resize", preserveLayout);
      doc.removeEventListener?.("visibilitychange", visibilityChanged); scope.removeEventListener?.("pagehide", flushProgress); scope.removeEventListener?.("beforeunload", flushProgress);
      cancelImport(); stopPDF(); root.replaceChildren();
    }
    const unregisterMobile = mobileShell?.registerReader?.({ setEnvironment: setMobileEnvironment, handleBack: handleMobileBack });
    renderControls();
    return { load, openBook, sync, destroy, openChapter, importFile, cancelImport, addBookmark, saveProgress, flushProgress, leaveFullscreen, forgetBook, getState: () => ({ directory: book?.directory || "", chapter: current?.number || 0, loading: isLoading(), importing: Boolean(importJob), fullscreen: isFullscreen(), focusMode, mobileReading, mobilePanel, progress: { ...progress, bookmarks: progress.bookmarks.map((mark) => ({ ...mark })) } }) };
  }
  const exported = { create, normalizeProgress, bookRatio, textFromPDFItems, pdfOptions };
  if (typeof module === "object" && module.exports) module.exports = exported;
  else scope.NovelReader = exported;
})(typeof window === "undefined" ? globalThis : window);
