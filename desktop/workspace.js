"use strict";

const fs = require("node:fs");
const path = require("node:path");

const LAST_PROJECT_KEY = "lastProjectPath";

function selectWorkspace(userData) {
  const books = path.join(userData, "output", "web-books");
  const legacyFiles = [
    "meta/web-project.json", "meta/book.json", "meta/run.json",
    "meta/progress.json", "meta/web-cocreate.json", "premise.md",
    "outline.json", "chapters",
  ];
  const legacy = legacyFiles.some((name) => fs.existsSync(path.join(books, name)));
  return {
    workspaceDir: legacy || fs.existsSync(path.join(books, ".ainovel-deleting")) ? books : path.join(userData, "output", "novel"),
    legacy,
  };
}

// Persist only the selected workspace reported by the authoritative state API.
// A background project's updated index/snapshot must never change the selection.
function rememberLastProject(database, state, workspaces) {
  const directory = state?.directory;
  if (directory === "" && state.hasProject === false && state.project === null && Array.isArray(workspaces)) {
    database.setSetting(LAST_PROJECT_KEY, "");
    return true;
  }
  if (typeof directory !== "string" || !path.isAbsolute(directory) || !Array.isArray(workspaces)) return false;
  const project = workspaces.find(item => item?.path === directory);
  if (!project) return false;
  if (project.archived || state.project?.archived) {
    database.setSetting(LAST_PROJECT_KEY, "");
    return true;
  }
  database.setSetting(LAST_PROJECT_KEY, directory);
  return true;
}

async function captureLastProject({ database, baseURL, fetchBackend }) {
  const [stateResponse, projectsResponse] = await Promise.all([
    fetchBackend(`${baseURL}/api/state`, { signal: AbortSignal.timeout(5000) }),
    fetchBackend(`${baseURL}/api/workspaces`, { signal: AbortSignal.timeout(5000) }),
  ]);
  if (!stateResponse.ok || !projectsResponse.ok) return false;
  const [state, projects] = await Promise.all([stateResponse.json(), projectsResponse.json()]);
  return rememberLastProject(database, state, projects);
}

// The default output directory remains the sidecar's stable home. Restoring by
// the normal switch API keeps the project catalogue and copy roots unchanged,
// and lets the sidecar enforce missing, archived and linked-directory checks.
async function restoreLastProject({ database, baseURL, fetchBackend, workspaceDir }) {
  const selected = database.getSetting(LAST_PROJECT_KEY);
  // A missing preference is a first launch/upgrade; an empty string is an
  // explicit empty selection and must not reopen a legacy home story.
  if (selected == null) return false;
  const deselect = async () => {
    const response = await fetchBackend(`${baseURL}/api/workspaces`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ deselect: true }), signal: AbortSignal.timeout(10000),
    });
    return response.ok;
  };
  try {
    if (typeof selected !== "string" || !path.isAbsolute(selected)) {
      await deselect();
      return false;
    }
    const response = await fetchBackend(`${baseURL}/api/workspaces`, { signal: AbortSignal.timeout(5000) });
    if (!response.ok) { await deselect(); return false; }
    const projects = await response.json();
    if (!Array.isArray(projects) || !projects.some(project => project?.path === selected && !project.archived)) {
      await deselect();
      return false;
    }
    const switched = await fetchBackend(`${baseURL}/api/workspaces`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: selected }), signal: AbortSignal.timeout(10000),
    });
    if (!switched.ok) { await deselect(); return false; }
    const state = await switched.json();
    if (state?.directory === selected) return true;
    await deselect();
    return false;
  } catch (error) {
    // Network/JSON failures must not accidentally display an unrelated home.
    // Preserve the saved preference so a later successful startup can retry.
    try { await deselect(); } catch { /* The sidecar may already be gone. */ }
    throw error;
  }
}

module.exports = { selectWorkspace, rememberLastProject, captureLastProject, restoreLastProject };
