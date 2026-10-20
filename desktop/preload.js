"use strict";

const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("ainovelDesktop", {
  getInfo: () => ipcRenderer.invoke("desktop:info"),
  saveExport: (payload) =>
    ipcRenderer.invoke("desktop:save-export", payload),
});
