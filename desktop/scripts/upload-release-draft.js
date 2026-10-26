"use strict";

// Attach a verified CI build to an explicitly chosen, existing draft. This
// script has no release creation, editing, publication or asset deletion path.
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const { spawnSync } = require("node:child_process");

function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

function validateEnvironment(env, platform, arch) {
  requireValue(env.GITHUB_ACTIONS === "true" && env.GITHUB_EVENT_NAME === "workflow_dispatch", "Draft uploads require an explicit GitHub Actions workflow_dispatch run");
  requireValue(/^[1-9]\d*$/.test(env.DRAFT_RELEASE_ID || "") && Number.isSafeInteger(Number(env.DRAFT_RELEASE_ID)), "DRAFT_RELEASE_ID must be an explicit positive integer");
  requireValue(/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(env.GITHUB_REPOSITORY || ""), "GITHUB_REPOSITORY is invalid");
  requireValue(/^[a-f0-9]{40}$/.test(env.GITHUB_SHA || ""), "GITHUB_SHA must be the exact 40-character commit SHA");
  requireValue(/^[1-9]\d*$/.test(env.GITHUB_RUN_ID || ""), "GITHUB_RUN_ID is required");
  requireValue(typeof env.GH_TOKEN === "string" && env.GH_TOKEN.trim().length > 0, "GH_TOKEN must be supplied through the environment");
  requireValue(["win32", "linux"].includes(platform) && arch === "x64", "Draft uploads support the Windows and Linux x64 acceptance jobs only");
  return { repository: env.GITHUB_REPOSITORY, releaseId: env.DRAFT_RELEASE_ID, commit: env.GITHUB_SHA, runId: env.GITHUB_RUN_ID, platform: platform === "win32" ? "windows" : "linux" };
}

function regularFile(filename) {
  const stat = fs.lstatSync(filename);
  requireValue(stat.isFile() && !stat.isSymbolicLink() && stat.size > 0, `Expected a nonempty regular file: ${path.basename(filename)}`);
  return stat;
}

function readJSON(filename) {
  regularFile(filename);
  return JSON.parse(fs.readFileSync(filename, "utf8").replace(/^\uFEFF/, ""));
}

function fileIdentity(filename) {
  const before = regularFile(filename);
  const hash = crypto.createHash("sha256"), buffer = Buffer.alloc(1024 * 1024);
  const fd = fs.openSync(filename, "r");
  try {
    let count;
    while ((count = fs.readSync(fd, buffer, 0, buffer.length, null)) > 0) hash.update(buffer.subarray(0, count));
  } finally { fs.closeSync(fd); }
  const after = regularFile(filename);
  requireValue(before.size === after.size && before.mtimeMs === after.mtimeMs && before.ctimeMs === after.ctimeMs, `Build file changed while hashing: ${path.basename(filename)}`);
  return { name: path.basename(filename), size: after.size, digest: `sha256:${hash.digest("hex")}` };
}

function passedSteps(steps, context) {
  requireValue(Array.isArray(steps) && steps.length > 0 && steps.every(step => step?.ok === true && typeof step.name === "string"), `${context}: missing or failed acceptance steps`);
}

function validateReports(reportsDir, platform, version, installerName) {
  const reports = {};
  for (const name of ["smoke", "regression", "smoke-packaged"]) {
    const file = `${name}.json`, report = readJSON(path.join(reportsDir, file));
    requireValue(report.schemaVersion === 1 && report.suite === `desktop-${name}` && report.ok === true && report.failures === 0 && report.packaged === (name === "smoke-packaged"), `${file}: acceptance did not pass with the expected schema`);
    const expected = name === "regression" ? ["initial", "restart", "empty-prepare", "empty-restart"] : ["initial"];
    requireValue(Array.isArray(report.phases) && report.phases.length === expected.length && expected.every(phase => report.phases.filter(item => item.phase === phase).length === 1), `${file}: required acceptance phases are missing`);
    for (const phase of report.phases) {
      requireValue(phase.ok === true && phase.versions?.app === version && phase.sidecarStopped === true && phase.sidecarShutdown?.graceful === true && phase.sidecarShutdown?.forced === false, `${file}: phase failed, has a stale app version, or did not shut down cleanly`);
      passedSteps(phase.steps, `${file}/${phase.phase}`);
    }
    reports[file] = report;
  }
  const creative = readJSON(path.join(reportsDir, "creative-workflow.json"));
  requireValue(creative.schemaVersion === 1 && creative.suite === "creative-desktop-workflow" && creative.ok === true && creative.failures === 0 && !creative.errors?.length, "creative-workflow.json: creative workflow acceptance did not pass");
  passedSteps(creative.steps, "creative-workflow.json");
  reports["creative-workflow.json"] = creative;
  if (platform === "windows") {
    for (const file of ["windows-installer-upgrade.json", "windows-public-installer-upgrade.json"]) {
      const report = readJSON(path.join(reportsDir, file));
      requireValue(report.schemaVersion === 1 && report.suite === "windows-installer-upgrade" && report.ok === true && report.cleanup === true && report.expectedVersion === version && report.installer === installerName, `${file}: installer upgrade did not pass for this version`);
      passedSteps(report.steps, file);
      const installed = report.steps.find(step => step.name === "installed-new-version-smoke")?.result;
      requireValue(installed?.ok === true && installed.versions?.app === version && installed.sidecarStopped === true && installed.sidecarShutdown?.graceful === true && installed.sidecarShutdown?.forced === false, `${file}: installed application was not verified`);
      passedSteps(installed.steps, `${file}/installed-new-version-smoke`);
      reports[file] = report;
    }
  }
  return reports;
}

function prepareBuild(desktopDir, identity) {
  const metadata = readJSON(path.join(desktopDir, "package.json"));
  requireValue(metadata.name === "ainovel-desktop" && /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(metadata.version), "Unexpected package identity or version");
  const dist = path.join(desktopDir, "dist"), reportDir = path.join(dist, "reports");
  for (const directory of [dist, reportDir]) {
    const stat = fs.lstatSync(directory);
    requireValue(stat.isDirectory() && !stat.isSymbolicLink(), "Build and report directories must be real directories");
  }
  const base = `${metadata.name}-${metadata.version}`;
  const names = identity.platform === "windows"
    ? [`${base}-win-x64-setup.exe`, `${base}-win-x64-setup.exe.blockmap`]
    : [`${base}-linux-x86_64.AppImage`, `${base}-linux-amd64.deb`];
  const files = names.map(name => ({ filename: path.join(dist, name), ...fileIdentity(path.join(dist, name)) }));
  const reports = validateReports(reportDir, identity.platform, metadata.version, names[0]);
  const acceptance = { schemaVersion: 1, ok: true, ...identity, version: metadata.version, installers: files.map(({ filename, ...file }) => file), reports };
  const reportPath = path.join(dist, `desktop-acceptance-${identity.platform}.json`);
  // The generated aggregate is deterministic for identical reports and run ID,
  // so a retry can recognize identical attachments without replacing anything.
  if (fs.existsSync(reportPath)) requireValue(fs.lstatSync(reportPath).isFile() && !fs.lstatSync(reportPath).isSymbolicLink(), "Acceptance aggregate cannot be a link or directory");
  fs.writeFileSync(reportPath, `${JSON.stringify(acceptance, null, 2)}\n`);
  files.push({ filename: reportPath, ...fileIdentity(reportPath) });
  requireValue(files.every(file => !file.filename.includes("#")), "Upload paths cannot contain gh asset label separators");
  return { version: metadata.version, tag: `v${metadata.version}`, files, reportPath };
}

function createGhRunner(env, cwd, spawn = spawnSync) {
  return args => {
    const result = spawn("gh", args, { cwd, env: { ...env, GH_PROMPT_DISABLED: "1" }, shell: false, windowsHide: true, encoding: "utf8", maxBuffer: 16 * 1024 * 1024, timeout: 30 * 60 * 1000 });
    // Never echo child output or environment on failure: diagnostics must not
    // accidentally include an authentication token supplied by the runner.
    requireValue(!result.error && result.status === 0, `gh ${args[0]} failed (exit ${result.status ?? "unavailable"})`);
    return result.stdout || "";
  };
}

function verifyDraft(gh, identity, build) {
  const release = JSON.parse(gh(["api", `repos/${identity.repository}/releases/${identity.releaseId}`]));
  requireValue(String(release.id) === identity.releaseId && release.draft === true && release.published_at == null && release.tag_name === build.tag && release.target_commitish === identity.commit, "Target must be the specified unpublished draft with the exact package tag and workflow commit");
  // `release upload` addresses a tag. Verify that gh resolves that tag to the
  // exact same draft ID rather than a similarly named or historical release.
  const byTag = JSON.parse(gh(["release", "view", build.tag, "--repo", identity.repository, "--json", "databaseId,isDraft,tagName,targetCommitish"]));
  requireValue(String(byTag.databaseId) === identity.releaseId && byTag.isDraft === true && byTag.tagName === build.tag && byTag.targetCommitish === identity.commit, "Release tag resolves to a different or published release");
}

function releaseAssets(gh, identity) {
  const pages = JSON.parse(gh(["api", `repos/${identity.repository}/releases/${identity.releaseId}/assets?per_page=100`, "--paginate", "--slurp"]));
  requireValue(Array.isArray(pages) && pages.every(Array.isArray), "Invalid release asset listing");
  return pages.flat();
}

function matchingAsset(assets, file) {
  const matches = assets.filter(asset => asset.name === file.name);
  requireValue(matches.length <= 1, `Duplicate existing attachment: ${file.name}`);
  if (!matches.length) return false;
  const asset = matches[0];
  requireValue(asset.state === "uploaded" && asset.size === file.size && asset.digest === file.digest, `Refusing to replace a different or unverifiable existing attachment: ${file.name}`);
  return true;
}

function uploadDraft({ env = process.env, platform = process.platform, arch = process.arch, desktopDir = path.resolve(__dirname, ".."), gh } = {}) {
  const identity = validateEnvironment(env, platform, arch);
  const build = prepareBuild(desktopDir, identity);
  gh ||= createGhRunner(env, desktopDir);
  verifyDraft(gh, identity, build);
  const assets = releaseAssets(gh, identity), pending = [], skipped = [];
  for (const file of build.files) (matchingAsset(assets, file) ? skipped : pending).push(file);
  if (pending.length) {
    verifyDraft(gh, identity, build);
    for (const file of pending) {
      const current = fileIdentity(file.filename);
      requireValue(current.size === file.size && current.digest === file.digest, `Build changed before upload: ${file.name}`);
    }
    gh(["release", "upload", build.tag, "--repo", identity.repository, ...pending.map(file => file.filename)]);
  }
  verifyDraft(gh, identity, build);
  const uploadedAssets = releaseAssets(gh, identity);
  for (const file of build.files) requireValue(matchingAsset(uploadedAssets, file), `Attachment missing after upload: ${file.name}`);
  return { releaseId: identity.releaseId, reportPath: build.reportPath, uploaded: pending.map(file => file.name), skipped: skipped.map(file => file.name) };
}

module.exports = { validateEnvironment, validateReports, fileIdentity, prepareBuild, createGhRunner, verifyDraft, matchingAsset, uploadDraft };
if (require.main === module) {
  try {
    const result = uploadDraft();
    process.stdout.write(`Draft ${result.releaseId}: uploaded ${result.uploaded.length}, verified unchanged ${result.skipped.length}. Release remains unpublished.\n`);
  } catch (error) {
    process.stderr.write(`Draft attachment upload refused: ${error.message}\n`);
    process.exitCode = 1;
  }
}
