"use strict";

const fs = require("node:fs");
const path = require("node:path");

function selectWorkspace(userData) {
  const books = path.join(userData, "output", "web-books");
  const legacyFiles = [
    "meta/web-project.json", "meta/book.json", "meta/run.json",
    "meta/progress.json", "meta/web-cocreate.json", "premise.md",
    "outline.json", "chapters",
  ];
  const legacy = legacyFiles.some((name) => fs.existsSync(path.join(books, name)));
  return {
    workspaceDir: legacy ? books : path.join(userData, "output", "novel"),
    legacy,
  };
}

module.exports = { selectWorkspace };
