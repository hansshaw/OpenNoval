"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { selectWorkspace, rememberLastProject, captureLastProject, restoreLastProject } = require("../workspace");
const { openDatabase } = require("../database");

test("new desktop default is independent of the project catalogue and remains copyable", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-workspace-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const child = path.join(root, "output", "web-books", "existing-child", "meta");
  fs.mkdirSync(child, { recursive: true });
  fs.writeFileSync(path.join(child, "web-project.json"), '{"name":"child"}');
  assert.deepEqual(selectWorkspace(root), { workspaceDir: path.join(root, "output", "novel"), legacy: false });
});

test("legacy root stories keep their exact directory and files without unlocked migration", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-workspace-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const books = path.join(root, "output", "web-books");
  fs.mkdirSync(path.join(books, "meta"), { recursive: true });
  const filename = path.join(books, "meta", "run.json");
  fs.writeFileSync(filename, "legacy-story-must-stay");
  assert.deepEqual(selectWorkspace(root), { workspaceDir: books, legacy: true });
  assert.equal(fs.readFileSync(filename, "utf8"), "legacy-story-must-stay");
  assert.equal(fs.existsSync(path.join(root, "output", "novel")), false);
});

function testSelection() {
  const selected = path.resolve("selected-book");
  const background = path.resolve("background-book");
  const settings = new Map();
  const database = { getSetting: key => settings.get(key), setSetting: (key, value) => settings.set(key, value) };
  return { selected, background, settings, database };
}

test("only the selected existing unarchived project updates the last-project setting", () => {
  const { selected, background, settings, database } = testSelection();
  const projects = [{ path: background, status: "running" }, { path: selected }];
  assert.equal(rememberLastProject(database, { directory: selected }, projects), true);
  assert.equal(settings.get("lastProjectPath"), selected);
  for (const [state, catalogue] of [
    [{ directory: background }, [{ path: selected }]],
    [{ directory: "relative" }, [{ path: "relative" }]],
    [{ directory: background }, null],
  ]) {
    assert.equal(rememberLastProject(database, state, catalogue), false);
    assert.equal(settings.get("lastProjectPath"), selected, "Invalid/background state must preserve the last usable selection");
  }
});

test("last selected project survives reopening SQLite separately from the default workspace", async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-project-restore-"));
  let database;
  t.after(() => {
    database?.close();
    fs.rmSync(root, { recursive: true, force: true });
  });
  const file = path.join(root, "desktop.sqlite");
  const selected = path.join(root, "output", "web-books", "last-book");
  const workspaceDir = selectWorkspace(root).workspaceDir;
  database = openDatabase(file);
  database.setSetting("workspaceDir", workspaceDir);
  rememberLastProject(database, { directory: selected }, [{ path: selected }]);
  database.close();
  database = null;
  database = openDatabase(file);
  const requests = [];
  const restored = await restoreLastProject({ database, baseURL: "http://localhost:1234", workspaceDir,
    fetchBackend: async (url, options) => {
      requests.push({ url, options });
      return Response.json(options.method === "POST" ? { directory: selected } : [{ path: selected }]);
    },
  });
  assert.equal(restored, true);
  assert.equal(requests.length, 2);
  assert.deepEqual(JSON.parse(requests[1].options.body), { path: selected });
  assert.equal(database.getSetting("workspaceDir"), workspaceDir);
  assert.notEqual(database.getSetting("workspaceDir"), selected);
});

test("startup clears selection for missing, archived or invalid remembered projects", async () => {
  const { selected, settings, database } = testSelection();
  for (const catalogue of [[], [{ path: selected, archived: true }], { error: "invalid catalogue" }]) {
    settings.set("lastProjectPath", selected);
    const requests = [];
    assert.equal(await restoreLastProject({ database, baseURL: "http://localhost:1234",
      fetchBackend: async (_url, options) => {
        requests.push(options);
        return Response.json(options.method === "POST" ? { directory: "" } : catalogue);
      },
    }), false);
    assert.equal(requests.length, 2);
    assert.deepEqual(JSON.parse(requests[1].body), { deselect: true });
    assert.equal(settings.get("lastProjectPath"), selected);
  }
  settings.delete("lastProjectPath");
  assert.equal(await restoreLastProject({ database, baseURL: "http://localhost:1234",
    fetchBackend: () => assert.fail("First upgrade without a preference preserves existing legacy home"),
  }), false);
  for (const remembered of ["", "relative/path"]) {
    settings.set("lastProjectPath", remembered);
    assert.equal(await restoreLastProject({ database, baseURL: "http://localhost:1234",
      fetchBackend: async (_url, options) => {
        assert.deepEqual(JSON.parse(options.body), { deselect: true });
        return Response.json({ directory: "" });
      },
    }), false);
  }
});

test("a rejected restore deselects safely and keeps its record for a later retry", async () => {
  const { selected, settings, database } = testSelection();
  settings.set("lastProjectPath", selected);
  let deselections = 0;
  assert.equal(await restoreLastProject({ database, baseURL: "http://localhost:1234",
    fetchBackend: async (_url, options) => {
      if (options.method !== "POST") return Response.json([{ path: selected }]);
      if (JSON.parse(options.body).deselect) { deselections++; return Response.json({ directory: "" }); }
      return Response.json({ error: "Project was archived after listing" }, { status: 409 });
    },
  }), false);
  assert.equal(deselections, 1);
  assert.equal(settings.get("lastProjectPath"), selected);
});

test("a failing catalogue read clears active selection without replacing the saved preference", async () => {
  const { selected, settings, database } = testSelection();
  settings.set("lastProjectPath", selected);
  let deselections = 0;
  await assert.rejects(restoreLastProject({ database, baseURL: "http://localhost:1234",
    fetchBackend: async (_url, options) => {
      if (options.method !== "POST") throw new Error("temporary network failure");
      assert.deepEqual(JSON.parse(options.body), { deselect: true });
      deselections++;
      return Response.json({ directory: "" });
    },
  }), /temporary network failure/);
  assert.equal(deselections, 1);
  assert.equal(settings.get("lastProjectPath"), selected);
});

test("an explicit empty or archived selected project replaces a stale previous selection", () => {
  const { selected, background, settings, database } = testSelection();
  for (const [state, catalogue] of [
    [{ directory: "", project: null, hasProject: false }, []],
    [{ directory: selected }, [{ path: selected, archived: true }]],
    [{ directory: selected, project: { archived: true } }, [{ path: selected }]],
  ]) {
    settings.set("lastProjectPath", background);
    assert.equal(rememberLastProject(database, state, catalogue), true);
    assert.equal(settings.get("lastProjectPath"), "");
  }
});

test("deleted legacy root remains the stable home without displaying a migration notice", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "ainovel-deleted-root-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const books = path.join(root, "output", "web-books");
  fs.mkdirSync(books, { recursive: true });
  fs.writeFileSync(path.join(books, ".ainovel-deleting"), "deleted legacy novel");
  assert.deepEqual(selectWorkspace(root), { workspaceDir: books, legacy: false });
});

test("final capture reads the authoritative selection and preserves it when a later read fails", async () => {
  const { selected, background, settings, database } = testSelection();
  settings.set("lastProjectPath", background);
  const projects = [{ path: selected }, { path: background }];
  assert.equal(await captureLastProject({ database, baseURL: "http://localhost:1234",
    fetchBackend: async url => Response.json(url.endsWith("/state") ? { directory: selected } : projects),
  }), true);
  assert.equal(settings.get("lastProjectPath"), selected);
  assert.equal(await captureLastProject({ database, baseURL: "http://localhost:1234",
    fetchBackend: async () => new Response("", { status: 503 }),
  }), false);
  assert.equal(settings.get("lastProjectPath"), selected);
  await assert.rejects(captureLastProject({ database, baseURL: "http://localhost:1234",
    fetchBackend: async () => { throw new Error("sidecar unavailable"); },
  }), /sidecar unavailable/);
  assert.equal(settings.get("lastProjectPath"), selected);
});
