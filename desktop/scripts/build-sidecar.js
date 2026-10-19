"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const desktopDir = path.resolve(__dirname, "..");
const projectDir = path.resolve(desktopDir, "..");
const outputDir = path.join(desktopDir, "bin");
const outputName =
  process.platform === "win32" ? "ainovel-sidecar.exe" : "ainovel-sidecar";
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
    env: { ...process.env, CGO_ENABLED: "0" },
    stdio: "inherit",
    shell: false,
  },
);

if (result.error) {
  console.error(`无法启动 Go 编译器：${result.error.message}`);
  process.exit(1);
}
if (result.status !== 0) {
  process.exit(result.status || 1);
}

console.log(`Sidecar 已构建：${outputPath}`);
