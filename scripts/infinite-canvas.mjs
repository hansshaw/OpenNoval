#!/usr/bin/env node
// Runs the separately licensed upstream service without sharing workbench keys.
import { createHash, randomBytes } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const source = path.join(root, "third_party", "infinite-canvas");
const command = process.argv[2] || "help";

function verifySource() {
  const manifest = JSON.parse(readFileSync(path.join(source, "AINOVEL_UPSTREAM.json"), "utf8"));
  for (const [name, expected] of Object.entries(manifest.filesSHA256)) {
    const actual = createHash("sha256").update(readFileSync(path.join(source, name))).digest("hex");
    if (actual !== expected) throw new Error(`上游源码已变化：${name}`);
  }
  for (const name of Object.keys(manifest.excludedFiles)) {
    if (existsSync(path.join(source, name))) throw new Error(`出现未纳入分发的第三方资产：${name}`);
  }
  console.log(`源码校验通过：${manifest.commit}，${Object.keys(manifest.filesSHA256).length} 个文件。`);
}

try {
  if (command === "verify-source") {
    verifySource();
  } else if (["help", "--help", "-h"].includes(command)) {
    console.log("独立无限画布服务（需 Docker Engine 和 Compose）：\n  node scripts/infinite-canvas.mjs start          构建并启动\n  node scripts/infinite-canvas.mjs stop           停止服务，保留数据\n  node scripts/infinite-canvas.mjs logs           查看日志\n  node scripts/infinite-canvas.mjs verify-source  校验固定上游源码\n默认仅绑定 http://127.0.0.1:3000，可用 AINOVEL_CANVAS_PORT 修改端口。\n登录配置和服务数据保存在本仓库 .ainovel/ 内；本脚本不安装 Docker。");
  } else if (["start", "stop", "logs"].includes(command)) {
    const port = process.env.AINOVEL_CANVAS_PORT || "3000";
    if (!/^\d+$/.test(port) || Number(port) < 1024 || Number(port) > 65535) throw new Error("AINOVEL_CANVAS_PORT 须为 1024–65535 的端口。");
    const privateDir = path.join(root, ".ainovel");
    const config = path.join(privateDir, "infinite-canvas.env");
    const data = path.join(privateDir, "infinite-canvas-data");
    if (command === "start") {
      verifySource();
      mkdirSync(data, { recursive: true, mode: 0o700 });
      if (!existsSync(config)) {
        writeFileSync(config, `ADMIN_USERNAME=admin\nADMIN_PASSWORD=${randomBytes(24).toString("base64url")}\nJWT_SECRET=${randomBytes(48).toString("base64url")}\nSTORAGE_DRIVER=sqlite\nDATABASE_DSN=/app/data/infinite-canvas.db\n`, { mode: 0o600, flag: "wx" });
      }
    } else if (!existsSync(config)) {
      throw new Error("独立画布尚未初始化，请先执行 start。");
    }
    const action = command === "start" ? ["up", "--build", "--detach"] : command === "stop" ? ["stop"] : ["logs", "--tail", "100", "--follow"];
    const result = spawnSync("docker", ["compose", "--project-name", "ainovel-canvas", "--file", path.join(root, "scripts", "infinite-canvas-compose.yml"), ...action], {
      cwd: root,
      env: { ...process.env, AINOVEL_CANVAS_ENV: config, AINOVEL_CANVAS_DATA: data, AINOVEL_CANVAS_PORT: port },
      stdio: "inherit",
    });
    if (result.error) throw new Error(`无法运行 Docker Compose：${result.error.message}`);
    if (result.signal || result.status !== 0) process.exitCode = result.status || 1;
    else if (command === "start") console.log(`画布地址：http://127.0.0.1:${port}/canvas\n管理员登录配置：${config}\n未登录也可在此浏览器保存画布；工作台的模型密钥不会传入此服务。`);
  } else {
    throw new Error(`未知命令 ${command}，使用 --help 查看说明。`);
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
