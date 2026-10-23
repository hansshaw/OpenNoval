"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const desktopDir = path.resolve(__dirname, "..");
const projectDir = path.resolve(desktopDir, "..");

function buildSidecar({
  platform = process.platform,
  arch = process.arch,
  outputDir = path.join(desktopDir, "bin"),
} = {}) {
  const goos = { win32: "windows", linux: "linux", darwin: "darwin" }[platform];
  const goarch = { x64: "amd64", arm64: "arm64" }[arch];
  if (!goos || !goarch) {
    throw new Error(`不支持的 sidecar 目标：${platform}/${arch}（支持 x64、arm64）`);
  }
  const outputName = platform === "win32" ? "ainovel-sidecar.exe" : "ainovel-sidecar";
  const outputPath = path.join(outputDir, outputName);
  fs.mkdirSync(outputDir, { recursive: true });

  const result = spawnSync(
    "go",
    [
      "build",
      "-buildvcs=false",
      "-trimpath",
      "-o",
      outputPath,
      "./cmd/ainovel-cli",
    ],
    {
      cwd: projectDir,
      env: { ...process.env, CGO_ENABLED: "0", GOOS: goos, GOARCH: goarch },
      stdio: "inherit",
      shell: false,
    },
  );

  if (result.error) {
    throw new Error(`无法启动 Go 编译器：${result.error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(`Go sidecar 编译失败：${platform}/${arch}（exit=${result.status}, signal=${result.signal || ""}）`);
  }
  console.log(`Sidecar 已构建（${platform}/${arch}）：${outputPath}`);
  return outputPath;
}

if (require.main === module) {
  try {
    buildSidecar();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}

module.exports = { buildSidecar };
