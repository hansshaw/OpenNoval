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

test("deleteProject removes the project index, session and snapshots", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-delete-test-"));
  const database = openDatabase(path.join(root, "ainovel-desktop.sqlite"));
  const projectPath = "C:\\books\\deleted";
  const keptPath = "C:\\books\\kept";
  try {
    database.upsertProjects([
      { path: projectPath, title: "待删除" },
      { path: keptPath, title: "保留" },
    ]);
    database.saveClarificationSession(projectPath, {
      transcript: [{ role: "user", content: "删除项目" }],
      ready: true,
    });
    database.saveClarificationSession(keptPath, {
      transcript: [{ role: "user", content: "保留项目" }],
      ready: true,
    });
    database.addEventSnapshot(projectPath, "state", { chapter: 1 });
    database.addEventSnapshot(keptPath, "state", { chapter: 2 });
    database.addEventSnapshot("", "global", { theme: "dark" });

    assert.equal(database.deleteProject(projectPath), true);
    assert.deepEqual(
      database.listProjects().map((project) => project.path),
      [keptPath],
    );
    assert.equal(database.getClarificationSession(projectPath), null);
    assert.ok(database.getClarificationSession(keptPath));
    assert.equal(database.listEventSnapshots(projectPath).length, 0);
    assert.equal(database.listEventSnapshots(keptPath).length, 1);
    assert.equal(
      database
        .listEventSnapshots("", 10)
        .filter((snapshot) => snapshot.projectPath === "").length,
      1,
    );
    assert.equal(database.deleteProject(projectPath), false);
  } finally {
    database.close();
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test("pruneProjects removes stale projects and preserves active and global data", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-prune-test-"));
  const database = openDatabase(path.join(root, "ainovel-desktop.sqlite"));
  const staleA = "C:\\books\\stale-a";
  const active = "C:\\books\\active";
  const staleB = "C:\\books\\stale-b";
  try {
    database.upsertProjects([
      { path: staleA, title: "过期 A" },
      { path: active, title: "活动项目" },
      { path: staleB, title: "过期 B" },
    ]);
    for (const projectPath of [staleA, active, staleB]) {
      database.saveClarificationSession(projectPath, {
        transcript: [{ role: "user", content: projectPath }],
      });
      database.addEventSnapshot(projectPath, "state", { projectPath });
    }
    database.addEventSnapshot("", "global", { persisted: true });

    assert.equal(database.pruneProjects([active]), 2);
    assert.deepEqual(
      database.listProjects().map((project) => project.path),
      [active],
    );
    assert.equal(database.getClarificationSession(staleA), null);
    assert.equal(database.getClarificationSession(staleB), null);
    assert.ok(database.getClarificationSession(active));
    assert.equal(database.listEventSnapshots(staleA).length, 0);
    assert.equal(database.listEventSnapshots(staleB).length, 0);
    assert.equal(database.listEventSnapshots(active).length, 1);
    assert.equal(
      database
        .listEventSnapshots("", 10)
        .filter((snapshot) => snapshot.projectPath === "").length,
      1,
    );
    assert.equal(database.pruneProjects([active]), 0);
  } finally {
    database.close();
    fs.rmSync(root, { recursive: true, force: true });
  }
});
