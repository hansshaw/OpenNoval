/* Phone navigation is shared by the Android WebView and small browser windows. */
(() => {
  "use strict";
  const root = document.documentElement;
  const $ = (id) => document.getElementById(id);
  const phone = window.matchMedia("(max-width: 600px), (max-width: 860px) and (pointer: coarse)");
  const android = Boolean(window.ainovelAndroid) || new URLSearchParams(location.search).get("mobile") === "1";
  const nativeCompact = window.matchMedia("(max-width: 860px)");
  const sidebar = $("main-navigation"), workspace = document.querySelector(".workspace");
  const bottomNav = document.querySelector(".mobile-bottom-nav");
  let mobile = false, menuOpen = false, view = "projects", returnFocus = null, readerController = null, readerImmersive = false;
  let viewportWidth = window.innerWidth, viewportHeight = window.visualViewport?.height || window.innerHeight;
  root.classList.toggle("android-app", android);

  function setMenu(open, restoreFocus = true) {
    menuOpen = mobile && Boolean(open);
    root.classList.toggle("mobile-menu-open", menuOpen);
    $("mobile-open-menu").setAttribute("aria-expanded", String(menuOpen));
    $("mobile-menu-backdrop").hidden = !menuOpen;
    sidebar.inert = mobile && !menuOpen;
    workspace.inert = menuOpen;
    bottomNav.inert = menuOpen || readerImmersive;
    if (menuOpen) {
      returnFocus = document.activeElement;
      sidebar.setAttribute("role", "dialog");
      sidebar.setAttribute("aria-modal", "true");
      $("mobile-close-menu").focus({ preventScroll: true });
    } else {
      sidebar.removeAttribute("role");
      sidebar.removeAttribute("aria-modal");
      if (restoreFocus && returnFocus && !document.querySelector("dialog[open]")) returnFocus.focus?.({ preventScroll: true });
      returnFocus = null;
    }
  }

  function showChapterRegion(region) {
    if (!["directory", "reader", "editor", "feedback"].includes(region)) return;
    $("chapter-workspace").dataset.mobileRegion = region;
    for (const name of ["directory", "reader", "editor", "feedback"]) {
      $("chapter-jump-" + name).setAttribute("aria-pressed", String(name === region));
    }
  }

  function onViewChange(next) {
    view = next;
    root.dataset.mobileView = next;
    for (const button of bottomNav.querySelectorAll("[data-go-view]")) {
      button.setAttribute("aria-current", button.dataset.goView === next ? "page" : "false");
    }
    if (menuOpen) setMenu(false, false);
    readerController?.setEnvironment(mobile, view === "reader");
  }

  function updateViewport() {
    const height = window.visualViewport?.height || window.innerHeight;
    if (viewportWidth !== window.innerWidth) {
      viewportWidth = window.innerWidth;
      viewportHeight = height;
    }
    viewportHeight = Math.max(viewportHeight, height);
    const editing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName || "");
    root.classList.toggle("mobile-keyboard-open", mobile && editing && viewportHeight - height > 120);
    root.style.setProperty("--mobile-viewport-height", `${height}px`);
  }

  function syncLayout() {
    const next = phone.matches || android && nativeCompact.matches;
    root.classList.toggle("mobile-layout", next);
    if (mobile !== next) {
      mobile = next;
      setMenu(false, false);
    }
    $("prompt-input-hint").textContent = mobile ? "可以换行补充灵感，准备好后点击开始" : "Enter 开始 · Shift + Enter 换行";
    updateViewport();
    readerController?.setEnvironment(mobile, view === "reader");
  }

  // Android calls this before leaving the activity. Closing an overlay or
  // returning to the project shelf must not reload the page or lose drafts.
  function handleBack() {
    const dialogs = [...document.querySelectorAll("dialog[open]")];
    if (dialogs.length) {
      const dialog = dialogs.at(-1);
      if (dialog.dispatchEvent(new Event("cancel", { cancelable: true }))) dialog.close();
      return true;
    }
    if (view === "reader" && readerController?.handleBack()) return true;
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
      return true;
    }
    if (menuOpen) { setMenu(false); return true; }
    if (view !== "projects") {
      document.querySelector('.nav-item[data-view="projects"]').click();
      return true;
    }
    return false;
  }

  $("mobile-open-menu").addEventListener("click", () => setMenu(true));
  $("mobile-close-menu").addEventListener("click", () => setMenu(false));
  $("mobile-menu-backdrop").addEventListener("click", () => setMenu(false));
  $("mobile-settings").addEventListener("click", () => $("open-settings").click());
  sidebar.addEventListener("click", (event) => {
    if (menuOpen && event.target.closest(".sidebar-action")) setMenu(false, false);
  });
  $("chapter-list").addEventListener("click", (event) => {
    if (!mobile || !event.target.closest(".chapter-item")) return;
    $("chapter-read-tab").click();
    $("chapter-workspace").scrollIntoView({ block: "start", behavior: "instant" });
  });
  document.addEventListener("keydown", (event) => {
    if (!menuOpen) return;
    if (event.key === "Escape") { event.preventDefault(); setMenu(false); return; }
    if (event.key !== "Tab") return;
    const controls = [...sidebar.querySelectorAll("button:not(:disabled), a[href], input:not(:disabled)")].filter((node) => node.getClientRects().length);
    const first = controls[0], last = controls.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
  });
  window.addEventListener("resize", syncLayout);
  window.visualViewport?.addEventListener("resize", updateViewport);
  document.addEventListener("focusin", updateViewport);
  document.addEventListener("focusout", () => requestAnimationFrame(updateViewport));
  phone.addEventListener("change", syncLayout);
  nativeCompact.addEventListener("change", syncLayout);
  function setReaderImmersive(active) {
    readerImmersive = mobile && view === "reader" && Boolean(active);
    root.classList.toggle("mobile-reader-active", readerImmersive);
    const topbar = document.querySelector(".topbar");
    if (topbar) topbar.inert = readerImmersive;
    bottomNav.inert = readerImmersive || menuOpen;
  }
  function registerReader(controller) {
    readerController = controller;
    controller.setEnvironment(mobile, view === "reader");
    return () => {
      if (readerController === controller) { readerController = null; setReaderImmersive(false); }
    };
  }
  window.ainovelMobile = { isMobile: () => mobile, onViewChange, showChapterRegion, handleBack, registerReader, setReaderImmersive };
  showChapterRegion("directory");
  syncLayout();
})();
