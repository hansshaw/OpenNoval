"use strict";

// Keep the PDF reader available offline, including when Go is built without npm.
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const version = "6.4.299";
const source = path.resolve(__dirname, "../node_modules/pdfjs-dist");
const target = path.resolve(__dirname, "../../internal/entry/desktopui/static/vendor/pdfjs");
const hash = (bytes) => crypto.createHash("sha256").update(bytes).digest("hex");
function sourceFiles() {
  if (JSON.parse(fs.readFileSync(path.join(source, "package.json"), "utf8")).version !== version) throw new Error(`PDF.js must be exactly ${version}`);
  const files = new Map();
  const add = (name, relative = name) => files.set(relative, fs.readFileSync(path.join(source, name)));
  add("LICENSE"); add("build/pdf.mjs", "pdf.mjs"); add("build/pdf.worker.mjs", "pdf.worker.mjs");
  function walk(folder) {
    for (const entry of fs.readdirSync(path.join(source, folder), { withFileTypes: true })) {
      const name = `${folder}/${entry.name}`;
      if (entry.isDirectory()) walk(name); else add(name);
    }
  }
  for (const folder of ["cmaps", "standard_fonts", "wasm", "iccs"]) walk(folder);
  // Directory enumeration order differs across Windows and POSIX filesystems.
  // Use codepoint order so the byte-for-byte manifest check is reproducible.
  return new Map([...files].sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0));
}
function sync(check = false) {
  const files = sourceFiles();
  for (const [name, bytes] of files) {
    const destination = path.join(target, name);
    if (check) {
      if (!fs.existsSync(destination) || hash(fs.readFileSync(destination)) !== hash(bytes)) throw new Error(`PDF.js vendor differs: ${name}. Run npm run vendor:reader.`);
    } else { fs.mkdirSync(path.dirname(destination), { recursive: true }); fs.writeFileSync(destination, bytes); }
  }
  const manifest = { package: "pdfjs-dist", version, license: "Apache-2.0", source: "https://github.com/mozilla/pdf.js", files: Object.fromEntries([...files].map(([name, bytes]) => [name, hash(bytes)])) };
  const content = `${JSON.stringify(manifest, null, 2)}\n`;
  if (check) {
    if (fs.readFileSync(path.join(target, "manifest.json"), "utf8") !== content) throw new Error("PDF.js vendor manifest differs");
  } else fs.writeFileSync(path.join(target, "manifest.json"), content);
  return files.size;
}
if (require.main === module) {
  try { console.log(`PDF.js ${version}: ${sync(process.argv.includes("--check"))} offline files verified.`); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
module.exports = { sync, version, target };
