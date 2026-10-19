"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const { openDatabase } = require("../database");

test("desktop database persists projects, cocreate sessions and snapshots", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-desktop-test-"));
  const filePath = path.join(root, "ainovel-desktop.sqlite");
  const database = openDatabase(filePath);
  try {
    database.upsertProjects([
      {
        path: "C:\\books\\moon-library",
        title: "月球图书馆",
        status: "writing",
        stage: "writing",
        completed: 3,
        words: 12000,
        genre: "科幻",
        tone: "克制",
        archived: false,
      },
    ]);
    assert.equal(database.listProjects()[0].title, "月球图书馆");
    assert.equal(database.listProjects()[0].completed, 1);

    database.saveClarificationSession("C:\\books\\moon-library", {
      transcript: [{ role: "user", content: "写月球图书馆" }],
      draftPrompt: "## 作品定位\n近未来科幻",
      ready: true,
      confirmed: false,
    });
    const session = database.getClarificationSession(
      "C:\\books\\moon-library",
    );
    assert.equal(session.draftPrompt, "## 作品定位\n近未来科幻");
    assert.equal(session.transcript[0].content, "写月球图书馆");
    assert.equal(session.ready, true);
    assert.equal(session.confirmed, false);

    database.setSetting("theme", "dark");
    assert.equal(database.getSetting("theme"), "dark");

    database.addEventSnapshot("C:\\books\\moon-library", "state", {
      stage: "writing",
    });
    const snapshots = database.listEventSnapshots(
      "C:\\books\\moon-library",
    );
    assert.equal(snapshots.length, 1);
    assert.equal(JSON.parse(snapshots[0].payloadJson).stage, "writing");
  } finally {
    database.close();
    fs.rmSync(root, { recursive: true, force: true });
  }
});
