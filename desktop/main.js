"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawn } = require("node:child_process");
const {
  app,
  BrowserWindow,
  dialog,
  ipcMain,
  shell,
} = require("electron");
const { openDatabase } = require("./database");

const isSmokeTest = process.argv.includes("--smoke-test");

let mainWindow = null;
let sidecar = null;
let baseURL = "";
let database = null;
let syncTimer = null;
let stdoutLog = null;
let stderrLog = null;
let lastSnapshotJSON = "";
let stopPromise = null;
let quitReady = false;

function desktopPaths() {
  const userData = app.getPath("userData");
  return {
    userData,
    workspaceDir: path.join(userData, "output", "web-books"),
    logDir: path.join(userData, "logs"),
    sqlitePath: path.join(userData, "ainovel-desktop.sqlite"),
  };
}

function sidecarPath() {
  const executable =
    process.platform === "win32"
      ? "ainovel-sidecar.exe"
      : "ainovel-sidecar";
  if (app.isPackaged) {
    return path.join(process.resourcesPath, "bin", executable);
  }
  return path.join(__dirname, "bin", executable);
}

function appendLog(stream, message) {
  if (!stream) return;
  stream.write(`[${new Date().toISOString()}] ${message}\n`);
}

function parseSidecarURL(text) {
  const matches = String(text || "").match(
    /https?:\/\/(?:127\.0\.0\.1|localhost):\d+/i,
  );
  return matches ? matches[0] : "";
}

async function waitForBackend(url, child, timeoutMs = 30000) {
  const started = Date.now();
  let exited = false;
  let exitMessage = "";
  child.once("exit", (code, signal) => {
    exited = true;
    exitMessage = `sidecar 提前退出（code=${code}, signal=${signal || ""}）`;
  });
  while (Date.now() - started < timeoutMs) {
    if (exited) throw new Error(exitMessage);
    try {
      const response = await fetch(`${url}/api/state`, {
        signal: AbortSignal.timeout(2500),
      });
      if (response.ok) return;
    } catch (_) {
      // The HTTP listener may not be accepting requests during host startup.
    }
    await new Promise((resolve) => setTimeout(resolve, 300));
  }
  throw new Error("等待 Go sidecar 就绪超时");
}

async function startSidecar() {
  if (sidecar) return baseURL;
  const paths = desktopPaths();
  fs.mkdirSync(paths.workspaceDir, { recursive: true });
  fs.mkdirSync(paths.logDir, { recursive: true });
  stdoutLog = fs.createWriteStream(path.join(paths.logDir, "sidecar.log"), {
    flags: "a",
  });
  stderrLog = fs.createWriteStream(path.join(paths.logDir, "sidecar-error.log"), {
    flags: "a",
  });

  const executable = sidecarPath();
  if (!fs.existsSync(executable)) {
    throw new Error(
      `找不到 Go sidecar：${executable}\n请先在 desktop 目录运行 npm run build:web。`,
    );
  }

  const args = [
    "--web",
    "--listen",
    "127.0.0.1:0",
    "--output",
    paths.workspaceDir,
  ];
  appendLog(stdoutLog, `启动 ${executable} ${args.join(" ")}`);
  sidecar = spawn(executable, args, {
    cwd: paths.userData,
    env: {
      ...process.env,
      ELECTRON_RUN_AS_NODE: "",
    },
    windowsHide: true,
    stdio: ["ignore", "pipe", "pipe"],
  });

  sidecar.stdout.setEncoding("utf8");
  sidecar.stderr.setEncoding("utf8");
  let stdoutBuffer = "";
  baseURL = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("等待 Go sidecar 输出监听地址超时")),
      30000,
    );
    sidecar.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    sidecar.once("exit", (code, signal) => {
      clearTimeout(timer);
      reject(
        new Error(
          `Go sidecar 启动失败（code=${code}, signal=${signal || ""}）`,
        ),
      );
    });
    sidecar.stdout.on("data", (chunk) => {
      appendLog(stdoutLog, chunk.trimEnd());
      stdoutBuffer += chunk;
      const url = parseSidecarURL(stdoutBuffer);
      if (url) {
        clearTimeout(timer);
        resolve(url);
      }
    });
    sidecar.stderr.on("data", (chunk) => {
      appendLog(stderrLog, chunk.trimEnd());
    });
  });

  sidecar.once("exit", (code, signal) => {
    appendLog(
      stdoutLog,
      `Go sidecar 已退出（code=${code}, signal=${signal || ""}）`,
    );
  });
  await waitForBackend(baseURL, sidecar);
  return baseURL;
}

function snapshotPayload(state, workspaces, cocreate) {
  return {
    directory: state?.directory || "",
    ready: Boolean(state?.ready),
    project: state?.project || null,
    workflow: state?.workflow || null,
    runtime: state?.snapshot || null,
    workspaces,
    cocreate: cocreate
      ? {
          ready: Boolean(cocreate.ready),
          confirmed: Boolean(cocreate.confirmed),
          started: Boolean(cocreate.started),
          transcriptCount: (cocreate.transcript || []).length,
          updatedAt: cocreate.updatedAt || "",
        }
      : null,
  };
}

async function syncDesktopData() {
  if (!database || !baseURL) return;
  try {
    const [stateResponse, workspaceResponse] = await Promise.all([
      fetch(`${baseURL}/api/state`, { signal: AbortSignal.timeout(5000) }),
      fetch(`${baseURL}/api/workspaces`, { signal: AbortSignal.timeout(5000) }),
    ]);
    if (!stateResponse.ok || !workspaceResponse.ok) return;
    const state = await stateResponse.json();
    const workspacePayload = await workspaceResponse.json();
    if (!Array.isArray(workspacePayload)) return;
    const workspaces = workspacePayload;
    database.upsertProjects(workspaces);
    database.pruneProjects(workspaces.map((project) => project.path));

    let cocreate = null;
    if (state.directory) {
      const query = new URLSearchParams({ directory: state.directory });
      const response = await fetch(`${baseURL}/api/cocreate?${query}`, {
        signal: AbortSignal.timeout(5000),
      });
      if (response.ok) {
        cocreate = await response.json();
        database.saveClarificationSession(state.directory, cocreate);
      }
    }

    const payload = snapshotPayload(state, workspaces, cocreate);
    const serialized = JSON.stringify(payload);
    if (serialized !== lastSnapshotJSON) {
      database.addEventSnapshot(state.directory || "", "state", payload);
      lastSnapshotJSON = serialized;
    }
    database.setSetting("lastSyncAt", new Date().toISOString());
  } catch (error) {
    database.setSetting("lastSyncError", String(error.message || error));
  }
}

function scheduleDesktopSync(immediate = false) {
  clearInterval(syncTimer);
  if (immediate) void syncDesktopData();
  syncTimer = setInterval(() => void syncDesktopData(), 10000);
}

function hardenWindow(window) {
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith("https://") || url.startsWith("http://")) {
      void shell.openExternal(url);
    }
    return { action: "deny" };
  });
  window.webContents.on("will-navigate", (event, url) => {
    if (!baseURL || !url.startsWith(baseURL)) {
      event.preventDefault();
    }
  });
}

async function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1480,
    height: 980,
    minWidth: 1080,
    minHeight: 720,
    show: false,
    backgroundColor: "#091018",
    title: "AI Novel Studio",
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, "preload.js"),
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      devTools: !isSmokeTest,
    },
  });
  hardenWindow(mainWindow);
  mainWindow.once("ready-to-show", () => {
    if (!isSmokeTest) mainWindow.show();
  });
  mainWindow.on("closed", () => {
    mainWindow = null;
  });
  await mainWindow.loadURL(baseURL);
}

async function boot() {
  const paths = desktopPaths();
  database = openDatabase(paths.sqlitePath);
  database.setSetting("workspaceDir", paths.workspaceDir);
  ipcMain.handle("desktop:info", () => ({
    sqlitePath: paths.sqlitePath,
    workspaceDir: paths.workspaceDir,
    baseURL,
    version: app.getVersion(),
  }));
  ipcMain.handle("desktop:save-export", async (_event, payload) => {
    if (isSmokeTest) return { saved: false, smokeTest: true };
    const format = String(payload?.format || "").toLowerCase();
    if (format !== "txt" && format !== "epub") {
      throw new Error("不支持的导出格式");
    }
    let suggested = path.basename(String(payload?.filename || ""));
    if (!suggested || !suggested.toLowerCase().endsWith(`.${format}`)) {
      suggested = `novel.${format}`;
    }
    const extension = format === "epub" ? "EPUB" : "TXT";
    const options = {
      title: "导出小说",
      defaultPath: path.join(app.getPath("documents"), suggested),
      filters: [{ name: extension, extensions: [format] }],
      properties: ["createDirectory", "showOverwriteConfirmation"],
    };
    const result = mainWindow
      ? await dialog.showSaveDialog(mainWindow, options)
      : await dialog.showSaveDialog(options);
    if (result.canceled || !result.filePath) return { saved: false };
    let data;
    if (payload?.data instanceof Uint8Array) {
      data = Buffer.from(payload.data);
    } else if (payload?.data instanceof ArrayBuffer) {
      data = Buffer.from(new Uint8Array(payload.data));
    } else if (Array.isArray(payload?.data)) {
      data = Buffer.from(payload.data);
    } else {
      throw new Error("导出内容无效");
    }
    await fs.promises.writeFile(result.filePath, data);
    return { saved: true, path: result.filePath };
  });

  await startSidecar();
  await createWindow();
  await syncDesktopData();
  scheduleDesktopSync();

  if (isSmokeTest) {
    database.setSetting("smokeTest", "passed");
    const projectCount = database.listProjects().length;
    const snapshots = database.listEventSnapshots("", 1).length;
    process.stdout.write(
      `${JSON.stringify({
        ok: true,
        url: baseURL,
        sqlitePath: paths.sqlitePath,
        workspaceDir: paths.workspaceDir,
        projectCount,
        snapshots,
      })}\n`,
    );
    await shutdown(0);
  }
}

async function stopSidecar() {
  if (stopPromise) return stopPromise;
  stopPromise = (async () => {
    clearInterval(syncTimer);
    syncTimer = null;
    const child = sidecar;
    if (child && child.exitCode === null && child.signalCode === null) {
      child.kill();
      await Promise.race([
        new Promise((resolve) => child.once("exit", resolve)),
        new Promise((resolve) => setTimeout(resolve, 5000)),
      ]);
      if (child.exitCode === null && child.signalCode === null) {
        child.kill("SIGKILL");
        await Promise.race([
          new Promise((resolve) => child.once("exit", resolve)),
          new Promise((resolve) => setTimeout(resolve, 2000)),
        ]);
      }
    }
    sidecar = null;
    stdoutLog?.end();
    stderrLog?.end();
    stdoutLog = null;
    stderrLog = null;
  })();
  return stopPromise;
}

async function shutdown(exitCode = 0) {
  await stopSidecar();
  if (database) {
    database.setSetting("lastCleanExitAt", new Date().toISOString());
    database.close();
    database = null;
  }
  quitReady = true;
  if (isSmokeTest || exitCode !== 0) app.exit(exitCode);
  else app.quit();
}

const hasLock = app.requestSingleInstanceLock();
if (!hasLock) {
  app.quit();
} else {
  app.on("second-instance", () => {
    if (!mainWindow) return;
    if (mainWindow.isMinimized()) mainWindow.restore();
    mainWindow.show();
    mainWindow.focus();
  });

  app.whenReady().then(boot).catch(async (error) => {
    appendLog(stderrLog, error.stack || String(error));
    if (!isSmokeTest) {
      await dialog.showMessageBox({
        type: "error",
        title: "AI Novel Studio 启动失败",
        message: "桌面工作台未能启动。",
        detail: String(error.message || error),
      });
    }
    await shutdown(1);
  });
}

app.on("window-all-closed", () => {
  if (!quitReady) app.quit();
});

app.on("before-quit", (event) => {
  if (quitReady) return;
  event.preventDefault();
  void shutdown(0);
});
