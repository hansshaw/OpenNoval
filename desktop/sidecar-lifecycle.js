"use strict";

function isRunning(child) {
  return child && child.exitCode === null && child.signalCode === null;
}

function waitForExit(child, timeoutMs) {
  if (!isRunning(child)) return Promise.resolve(true);
  return new Promise((resolve) => {
    const finish = (exited) => {
      clearTimeout(timer);
      child.removeListener("exit", exitedHandler);
      resolve(exited);
    };
    const exitedHandler = () => finish(true);
    const timer = setTimeout(() => finish(!isRunning(child)), timeoutMs);
    child.once("exit", exitedHandler);
    if (!isRunning(child)) finish(true);
  });
}

async function stopSidecarProcess({ child, baseURL, token, sessionToken = "", timeoutMs = 30000, forceTimeoutMs = 3000, fetchImpl = fetch }) {
  let accepted = false;
  let forced = false;
  if (isRunning(child) && baseURL && token) {
    try {
      const response = await fetchImpl(`${baseURL}/api/desktop/shutdown`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-AInovel-Desktop-Token": token, "X-AInovel-Desktop-Session": sessionToken },
        body: "{}",
        signal: AbortSignal.timeout(Math.min(3000, timeoutMs)),
      });
      accepted = response.status === 202;
      await response.arrayBuffer();
    } catch (_) {
      // Startup failures may leave no HTTP listener. The process deadline is
      // still enforced, and a failed handshake is never reported as graceful.
    }
  }
  if (isRunning(child)) {
    const exited = await waitForExit(child, baseURL ? timeoutMs : 0);
    if (!exited) {
      forced = true;
      child.kill("SIGKILL");
      await waitForExit(child, forceTimeoutMs);
    }
  }
  const stopped = !isRunning(child);
  if (!stopped) throw new Error("Go sidecar 未能在退出期限内停止");
  return {
    method: accepted ? "http" : "fallback",
    graceful: accepted && !forced && child?.exitCode === 0 && child?.signalCode === null,
    forced, stopped, exitCode: child?.exitCode ?? null, signal: child?.signalCode ?? null,
  };
}

module.exports = { stopSidecarProcess };
