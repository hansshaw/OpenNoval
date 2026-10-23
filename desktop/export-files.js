"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { assertTrustedSender } = require("./security");

const formats = new Map([
  ["txt", "导出小说"], ["epub", "导出小说"], ["zip", "导出章节素材包"],
  ["mp3", "保存章节朗读"], ["mp4", "保存章节视频"],
  ...["png", "jpg", "jpeg", "webp", "gif"].map(format => [format, "保存章节配图"]),
]);

function registerExportIPC({ ipcMain, getWindow, getBaseURL, getDocumentsPath, dialog, isSmokeTest = false }) {
  ipcMain.handle("desktop:save-export", async (event, payload) => {
    const window = getWindow();
    assertTrustedSender(event, window, getBaseURL());
    const format = String(payload?.format || "").toLowerCase();
    if (!formats.has(format)) throw new Error("不支持的导出格式");
    let data;
    if (payload?.data instanceof Uint8Array) data = Buffer.from(payload.data);
    else if (payload?.data instanceof ArrayBuffer) data = Buffer.from(new Uint8Array(payload.data));
    else if (Array.isArray(payload?.data)) data = Buffer.from(payload.data);
    else throw new Error("导出内容无效");
    // Test mode still validates the real format and data contract, while never
    // opening a dialog or writing into the user's Documents folder.
    if (isSmokeTest) return { saved: false, smokeTest: true };
    let suggested = path.basename(String(payload?.filename || ""));
    if (!suggested || !suggested.toLowerCase().endsWith(`.${format}`)) suggested = `chapter.${format}`;
    const options = {
      title: formats.get(format),
      defaultPath: path.join(getDocumentsPath(), suggested),
      filters: [{ name: format.toUpperCase(), extensions: [format] }],
      properties: ["createDirectory", "showOverwriteConfirmation"],
    };
    const result = await dialog.showSaveDialog(window, options);
    if (result.canceled || !result.filePath) return { saved: false };
    await fs.promises.writeFile(result.filePath, data);
    return { saved: true, path: result.filePath };
  });
}

module.exports = { registerExportIPC };
