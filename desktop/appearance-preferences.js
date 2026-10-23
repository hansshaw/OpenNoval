"use strict";

const { assertTrustedSender } = require("./security");

const SETTING_KEY = "appearance";
const THEMES = new Set(["original", "ink", "fresh", "romance", "genshin", "starry", "aurora", "silk"]);
const defaults = () => ({ theme: "original", motion: false });

function validateAppearance(value) {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
      !THEMES.has(value.theme) || typeof value.motion !== "boolean") {
    throw new Error("界面偏好无效：请选择支持的皮肤与动效设置");
  }
  // Store only this narrow preference. Renderer input can never address other
  // desktop_settings keys or introduce additional persisted fields.
  return { theme: value.theme, motion: value.motion };
}

function readAppearance(database) {
  const stored = database.getSetting(SETTING_KEY);
  if (stored === null) return defaults();
  try { return validateAppearance(JSON.parse(stored)); }
  catch (_) { return defaults(); }
}

function registerAppearanceIPC({ ipcMain, getWindow, getBaseURL, database }) {
  ipcMain.handle("desktop:get-appearance", (event) => {
    assertTrustedSender(event, getWindow(), getBaseURL());
    return readAppearance(database);
  });
  ipcMain.handle("desktop:set-appearance", (event, value) => {
    assertTrustedSender(event, getWindow(), getBaseURL());
    const preference = validateAppearance(value);
    // SQLite writes are synchronous, preserving IPC arrival order even during
    // rapid theme changes. No user project or model setting is touched.
    database.setSetting(SETTING_KEY, JSON.stringify(preference));
    return preference;
  });
}

module.exports = { validateAppearance, readAppearance, registerAppearanceIPC };
