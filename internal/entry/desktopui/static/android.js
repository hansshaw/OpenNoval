/* Android capabilities are available only in the app's trusted local WebView. */
(() => {
  "use strict";
  const native = window.ainovelAndroid;
  if (!native) return;
  document.documentElement.classList.add("android-app");

  // Keep Android's system policy controls in a user-initiated settings action.
  // Older APKs and the desktop browser leave this panel hidden.
  if (typeof native.openBackgroundSettings === "function") {
    const bindBackgroundSettings = () => {
      const panel = document.getElementById("android-background-settings");
      const button = document.getElementById("open-android-background-settings");
      if (!panel || !button) return;
      panel.hidden = false;
      button.addEventListener("click", () => native.openBackgroundSettings());
    };
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", bindBackgroundSettings, { once: true });
    else bindBackgroundSettings();
  }

  // The backend uses a fresh loopback origin. Keep drafts and reader preferences
  // in app-private storage so a changed port or process restart cannot hide them.
  const storage = {
    getItem: key => native.storageGet(String(key)),
    setItem(key, value) {
      const error = native.storageSet(String(key), String(value));
      if (error) throw new Error(error);
    },
    removeItem: key => native.storageRemove(String(key)),
    clear: () => native.storageClear(),
    key: index => JSON.parse(native.storageKeys())[Number(index)] ?? null,
    get length() { return JSON.parse(native.storageKeys()).length; },
  };
  Object.defineProperty(window, "localStorage", { value: storage, configurable: false });

  const pending = new Map();
  let serial = 0;
  window.ainovelAndroidExportComplete = (id, saved, error) => {
    const request = pending.get(id);
    if (!request) return;
    pending.delete(id);
    if (error) request.reject(new Error(error));
    else request.resolve({ saved: Boolean(saved) });
  };
  const check = error => { if (error) throw new Error(error); };
  window.ainovelDesktop = Object.freeze({
    getAppearance: async () => JSON.parse(storage.getItem("ainovel.appearance.v1") || '{"theme":"original","motion":false}'),
    setAppearance: async value => { storage.setItem("ainovel.appearance.v1", JSON.stringify(value)); return value; },
    saveExport: async ({ filename, format, data }) => {
      const id = `export-${Date.now()}-${++serial}`;
      const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
      check(native.beginExport(id));
      try {
        // Bound every JNI argument; avoid expanding a whole novel into a JS
        // argument list or a single huge JSON/base64 allocation.
        for (let offset = 0; offset < bytes.length; offset += 48 * 1024) {
          const part = bytes.subarray(offset, offset + 48 * 1024);
          let binary = "";
          for (let i = 0; i < part.length; i++) binary += String.fromCharCode(part[i]);
          check(native.appendExport(id, btoa(binary)));
          if (offset % (480 * 1024) === 0) await new Promise(resolve => setTimeout(resolve, 0));
        }
        return await new Promise((resolve, reject) => {
          pending.set(id, { resolve, reject });
          try { check(native.finishExport(id, String(filename || "novel.txt"), String(format || "").toLowerCase())); }
          catch (error) { pending.delete(id); reject(error); }
        });
      } catch (error) { native.cancelExport(id); throw error; }
    },
  });
})();
