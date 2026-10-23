"use strict";

const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("ainovelDesktop", {
  getInfo: () => ipcRenderer.invoke("desktop:info"),
  getAppearance: () => ipcRenderer.invoke("desktop:get-appearance"),
  setAppearance: (preference) => ipcRenderer.invoke("desktop:set-appearance", preference),
  saveExport: (payload) =>
    ipcRenderer.invoke("desktop:save-export", payload),
});
