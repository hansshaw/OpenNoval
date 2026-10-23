"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawn } = require("node:child_process");
const { randomBytes } = require("node:crypto");
const {
  app,
  BrowserWindow,
  dialog,
  ipcMain,
  shell,
} = require("electron");
const { openDatabase } = require("./database");
const { isTrustedURL, assertTrustedSender } = require("./security");
const { configureTestMode } = require("./test-environment");
const { selectWorkspace } = require("./workspace");
const { stopSidecarProcess } = require("./sidecar-lifecycle");

// Set these before requesting a single-instance lock or creating Chromium data.
// The sidecar receives the same isolated home, including ~/.ainovel/config.json.
const testOptions = configureTestMode(app);
const isSmokeTest = Boolean(testOptions);
if (testOptions?.owned) {
  process.once("exit", () => {
    fs.rmSync(testOptions.root, { recursive: true, force: true, maxRetries: 4, retryDelay: 200 });
  });
}

let mainWindow = null;
let sidecar = null;
let baseURL = "";
let database = null;
let syncTimer = null;
let syncPromise = null;
let stdoutLog = null;
let stderrLog = null;
let lastSnapshotJSON = "";
let stopPromise = null;
let quitReady = false;
let sidecarShutdownToken = "";
let sidecarShutdown = null;

function desktopPaths() {
  const userData = app.getPath("userData");
  return {
    userData,
    ...selectWorkspace(userData),
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
  sidecarShutdownToken = randomBytes(32).toString("hex");
  sidecar = spawn(executable, args, {
    cwd: paths.userData,
    env: {
      ...(testOptions?.env || process.env),
      ELECTRON_RUN_AS_NODE: "",
      AINOVEL_DESKTOP_SHUTDOWN_TOKEN: sidecarShutdownToken,
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
  if (testOptions) {
    fs.writeFileSync(path.join(testOptions.root, "sidecar.json"), JSON.stringify({ pid: sidecar.pid, url: baseURL }));
  }
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

function syncDesktopData() {
  if (!syncPromise) {
    syncPromise = performDesktopSync().finally(() => { syncPromise = null; });
  }
  return syncPromise;
}

async function performDesktopSync() {
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
  const preventUntrustedNavigation = (event, url) => {
    if (!isTrustedURL(url, baseURL)) {
      event.preventDefault();
    }
  };
  window.webContents.on("will-navigate", preventUntrustedNavigation);
  window.webContents.on("will-redirect", preventUntrustedNavigation);
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
  ipcMain.handle("desktop:info", (event) => {
    assertTrustedSender(event, mainWindow, baseURL);
    return {
      sqlitePath: paths.sqlitePath,
      workspaceDir: paths.workspaceDir,
      baseURL,
      version: app.getVersion(),
    };
  });
  ipcMain.handle("desktop:save-export", async (event, payload) => {
    assertTrustedSender(event, mainWindow, baseURL);
    if (isSmokeTest) return { saved: false, smokeTest: true };
    const format = String(payload?.format || "").toLowerCase();
    if (!["txt", "epub", "zip"].includes(format)) {
      throw new Error("不支持的导出格式");
    }
    let suggested = path.basename(String(payload?.filename || ""));
    if (!suggested || !suggested.toLowerCase().endsWith(`.${format}`)) {
      suggested = `novel.${format}`;
    }
    const extension = format.toUpperCase();
    const options = {
      title: format === "zip" ? "导出章节画布包" : "导出小说",
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
  if (paths.legacy && !isSmokeTest && !database.getSetting("legacyWorkspaceNotice")) {
    await dialog.showMessageBox(mainWindow, {
      type: "info",
      title: "保留旧版默认项目",
      message: "已继续打开旧版默认项目，原有作品保持在原目录。",
      detail: "旧版默认项目和项目中心共用根目录。现在可直接在项目中心创建独立副本；根目录下的其他项目不会被递归复制。原作品会继续保留在原目录。",
    });
    database.setSetting("legacyWorkspaceNotice", new Date().toISOString());
  }
  await syncDesktopData();
  scheduleDesktopSync();

  if (testOptions) {
    const { runDesktopChecks } = require("./scripts/desktop-checks");
    const result = await runDesktopChecks({
      app, window: mainWindow, database, paths, baseURL,
      syncDesktopData, options: testOptions,
    });
    const child = sidecar;
    await shutdown(0, false);
    process.stdout.write(
      `AINOVEL_DESKTOP_TEST_RESULT ${JSON.stringify({
        ...result,
        sidecarPID: child?.pid,
        sidecarStopped: !child || child.exitCode !== null || child.signalCode !== null,
        sidecarShutdown,
      })}\n`,
    );
    app.exit(0);
  }
}

async function stopSidecar() {
  if (stopPromise) return stopPromise;
  stopPromise = (async () => {
    clearInterval(syncTimer);
    syncTimer = null;
    const child = sidecar;
    sidecarShutdown = await stopSidecarProcess({ child, baseURL, token: sidecarShutdownToken });
    if (sidecarShutdown.forced) appendLog(stderrLog, "Go sidecar 优雅退出超时，已执行强制停止");
    sidecarShutdownToken = "";
    sidecar = null;
    stdoutLog?.end();
    stderrLog?.end();
    stdoutLog = null;
    stderrLog = null;
  })();
  return stopPromise;
}

async function shutdown(exitCode = 0, exit = true) {
  await stopSidecar();
  // A timer may already be fetching state when quit begins. Let it finish
  // before closing SQLite so its result/error cannot write to a closed DB.
  await syncPromise;
  if (database) {
    database.setSetting("lastExitMode", sidecarShutdown?.graceful ? "graceful" : "unclean");
    database.setSetting(sidecarShutdown?.graceful ? "lastCleanExitAt" : "lastUncleanExitAt", new Date().toISOString());
    database.close();
    database = null;
  }
  quitReady = true;
  if (!exit) return;
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
    if (testOptions) process.stderr.write(`${error.stack || error}\n`);
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
