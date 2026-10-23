/* Appearance is independent of project state and never reloads the workbench. */
(() => {
  "use strict";
  const names = { original: "原始静态", ink: "水墨 · 远山", fresh: "清爽 · 晨间", romance: "暧昧 · 微醺", genshin: "原神风 · 星旅" };
  const cacheKey = "ainovel.appearance.v1";
  const root = document.documentElement;
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
  const desktop = window.ainovelDesktop;
  let revision = 0;
  let saveQueue = Promise.resolve();
  let status = "";
  let settings = normalize(null);

  function normalize(value) {
    const theme = value && Object.hasOwn(names, value.theme) ? value.theme : "original";
    return { theme, motion: theme !== "original" && value?.motion === true };
  }

  function cache() {
    try { window.localStorage.setItem(cacheKey, JSON.stringify(settings)); }
    catch (_) { /* Desktop SQLite remains the durable source of preferences. */ }
  }

  function apply() {
    root.dataset.theme = settings.theme;
    root.dataset.motion = settings.motion && !reducedMotion.matches ? "on" : "off";
    root.dataset.appearancePaused = document.hidden ? "true" : "false";
    const meta = document.querySelector('meta[name="color-scheme"]');
    if (meta) meta.content = ["ink", "fresh", "genshin"].includes(settings.theme) ? "light" : "dark";
    const label = document.getElementById("appearance-label");
    if (!label) return;
    label.textContent = names[settings.theme];
    for (const radio of document.querySelectorAll('input[name="appearance-theme"]')) radio.checked = radio.value === settings.theme;
    const toggle = document.getElementById("appearance-motion");
    toggle.checked = settings.motion;
    toggle.disabled = settings.theme === "original" || reducedMotion.matches;
    document.getElementById("appearance-motion-help").textContent = reducedMotion.matches
      ? "已遵循系统的“减少动态效果”设置，所有流光保持静止。"
      : settings.theme === "original" ? "原始界面保持静态。选择其他皮肤后可开启流光。"
        : "轻缓的光沿边缘流转，让创作空间呼吸。";
    document.getElementById("appearance-status").textContent = status;
  }

  function choose(value) {
    settings = normalize(value);
    const currentRevision = ++revision;
    const snapshot = { ...settings };
    status = "";
    cache();
    apply();
    // Serialize IPC writes, including rapid theme changes, without blocking preview.
    if (typeof desktop?.setAppearance === "function") {
      saveQueue = saveQueue.then(() => desktop.setAppearance(snapshot)).then(() => {
        if (currentRevision === revision) { status = "外观已保存"; apply(); }
      }).catch(() => {
        if (currentRevision === revision) { status = "外观已应用，但未能保存。请重新选择以重试。"; apply(); }
      });
    }
  }

  try { settings = normalize(JSON.parse(window.localStorage.getItem(cacheKey))); }
  catch (_) { /* A missing or corrupt cache must not prevent app startup. */ }
  apply();
  if (typeof desktop?.getAppearance === "function") {
    const initialRevision = revision;
    Promise.resolve().then(() => desktop.getAppearance()).then((saved) => {
      if (revision !== initialRevision) return;
      settings = normalize(saved);
      cache();
      apply();
    }).catch(() => {
      if (revision === initialRevision) { status = "未能读取已保存的外观，可以重新选择皮肤。"; apply(); }
    });
  }

  reducedMotion.addEventListener("change", apply);
  document.addEventListener("visibilitychange", apply);
  document.addEventListener("DOMContentLoaded", () => {
    const dialog = document.getElementById("appearance-dialog");
    document.getElementById("open-appearance").addEventListener("click", () => {
      if (!dialog.open) dialog.showModal();
      dialog.querySelector('input[name="appearance-theme"]:checked').focus();
    });
    document.getElementById("close-appearance").addEventListener("click", () => dialog.close());
    for (const radio of document.querySelectorAll('input[name="appearance-theme"]')) {
      radio.addEventListener("change", () => { if (radio.checked) choose({ ...settings, theme: radio.value }); });
    }
    document.getElementById("appearance-motion").addEventListener("change", (event) => choose({ ...settings, motion: event.target.checked }));
    document.getElementById("reset-appearance").addEventListener("click", () => choose(null));
    apply();
  }, { once: true });
})();
