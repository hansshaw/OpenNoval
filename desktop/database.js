"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { DatabaseSync } = require("node:sqlite");

const SCHEMA = `
  PRAGMA journal_mode = WAL;
  PRAGMA foreign_keys = ON;

  CREATE TABLE IF NOT EXISTS project_index (
    path TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    stage TEXT NOT NULL DEFAULT '',
    completed INTEGER NOT NULL DEFAULT 0,
    words INTEGER NOT NULL DEFAULT 0,
    genre TEXT NOT NULL DEFAULT '',
    tone TEXT NOT NULL DEFAULT '',
    archived INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
  );

  CREATE TABLE IF NOT EXISTS clarification_sessions (
    project_path TEXT PRIMARY KEY,
    transcript_json TEXT NOT NULL DEFAULT '[]',
    draft_prompt TEXT NOT NULL DEFAULT '',
    ready INTEGER NOT NULL DEFAULT 0,
    confirmed INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
  );

  CREATE TABLE IF NOT EXISTS desktop_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
  );

  CREATE TABLE IF NOT EXISTS event_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_path TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL
  );

  CREATE INDEX IF NOT EXISTS event_snapshots_project_created
    ON event_snapshots(project_path, created_at DESC);
`;

function integer(value) {
  return value ? 1 : 0;
}

function json(value, fallback) {
  try {
    return JSON.stringify(value ?? fallback);
  } catch (_) {
    return JSON.stringify(fallback);
  }
}

class DesktopDatabase {
  constructor(filePath) {
    this.filePath = filePath;
    fs.mkdirSync(path.dirname(filePath), { recursive: true });
    this.db = new DatabaseSync(filePath);
    this.db.exec(SCHEMA);
  }

  close() {
    this.db.close();
  }

  upsertProjects(projects) {
    const statement = this.db.prepare(`
      INSERT INTO project_index (
        path, title, status, stage, completed, words, genre, tone,
        archived, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(path) DO UPDATE SET
        title = excluded.title,
        status = excluded.status,
        stage = excluded.stage,
        completed = excluded.completed,
        words = excluded.words,
        genre = excluded.genre,
        tone = excluded.tone,
        archived = excluded.archived,
        updated_at = excluded.updated_at
    `);
    const now = new Date().toISOString();
    for (const project of projects || []) {
      if (!project?.path) continue;
      statement.run(
        String(project.path),
        String(project.title || ""),
        String(project.status || ""),
        String(project.stage || ""),
        integer(project.completed),
        Number(project.words || 0),
        String(project.genre || ""),
        String(project.tone || ""),
        integer(project.archived),
        String(project.updatedAt || project.updated_at || now),
      );
    }
    return this.listProjects();
  }

  listProjects() {
    return this.db
      .prepare(
        `SELECT path, title, status, stage, completed, words, genre, tone,
                archived, updated_at AS updatedAt
           FROM project_index
          ORDER BY archived ASC, updated_at DESC`,
      )
      .all();
  }

  saveClarificationSession(projectPath, session) {
    if (!projectPath) return;
    const now = new Date().toISOString();
    this.db
      .prepare(`
        INSERT INTO clarification_sessions (
          project_path, transcript_json, draft_prompt, ready, confirmed,
          updated_at
        ) VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(project_path) DO UPDATE SET
          transcript_json = excluded.transcript_json,
          draft_prompt = excluded.draft_prompt,
          ready = excluded.ready,
          confirmed = excluded.confirmed,
          updated_at = excluded.updated_at
      `)
      .run(
        String(projectPath),
        json(session?.transcript, []),
        String(session?.draftPrompt || ""),
        integer(session?.ready),
        integer(session?.confirmed),
        String(session?.updatedAt || now),
      );
  }

  getClarificationSession(projectPath) {
    const row = this.db
      .prepare(`
        SELECT project_path AS projectPath, transcript_json AS transcriptJson,
               draft_prompt AS draftPrompt, ready, confirmed,
               updated_at AS updatedAt
          FROM clarification_sessions
         WHERE project_path = ?
      `)
      .get(String(projectPath));
    if (!row) return null;
    return {
      ...row,
      transcript: JSON.parse(row.transcriptJson || "[]"),
      ready: Boolean(row.ready),
      confirmed: Boolean(row.confirmed),
    };
  }

  setSetting(key, value) {
    this.db
      .prepare(`
        INSERT INTO desktop_settings (key, value)
        VALUES (?, ?)
        ON CONFLICT(key) DO UPDATE SET value = excluded.value
      `)
      .run(String(key), String(value));
  }

  getSetting(key, fallback = null) {
    const row = this.db
      .prepare("SELECT value FROM desktop_settings WHERE key = ?")
      .get(String(key));
    return row ? row.value : fallback;
  }

  addEventSnapshot(projectPath, kind, payload) {
    this.db
      .prepare(`
        INSERT INTO event_snapshots (
          project_path, kind, payload_json, created_at
        ) VALUES (?, ?, ?, ?)
      `)
      .run(
        String(projectPath || ""),
        String(kind || "state"),
        json(payload, {}),
        new Date().toISOString(),
      );
    this.db.exec(`
      DELETE FROM event_snapshots
       WHERE id NOT IN (
         SELECT id FROM event_snapshots ORDER BY id DESC LIMIT 5000
       )
    `);
  }

  listEventSnapshots(projectPath = "", limit = 100) {
    const safeLimit = Math.max(1, Math.min(1000, Number(limit) || 100));
    if (projectPath) {
      return this.db
        .prepare(`
          SELECT id, project_path AS projectPath, kind,
                 payload_json AS payloadJson, created_at AS createdAt
            FROM event_snapshots
           WHERE project_path = ?
           ORDER BY id DESC
           LIMIT ?
        `)
        .all(String(projectPath), safeLimit);
    }
    return this.db
      .prepare(`
        SELECT id, project_path AS projectPath, kind,
               payload_json AS payloadJson, created_at AS createdAt
          FROM event_snapshots
         ORDER BY id DESC
         LIMIT ?
      `)
      .all(safeLimit);
  }
}

function openDatabase(filePath) {
  return new DesktopDatabase(filePath);
}

module.exports = {
  DesktopDatabase,
  openDatabase,
};
