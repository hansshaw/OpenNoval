"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");

// Runs through the real renderer, sidecar and disk storage with an isolated
// local provider. No user credentials or paid generation service are used.
async function runMediaChecks({ window, options, paths, phase, api, step, waitForUI }) {
  const run = (source) => window.webContents.executeJavaScript(source);
  const fixturePath = path.join(options.root, "chapter-media-scenario.json");
  const reload = async () => {
    await new Promise((resolve, reject) => {
      window.webContents.once("did-finish-load", resolve);
      window.webContents.once("did-fail-load", (_event, code, description) => reject(new Error(`Media reload failed ${code}: ${description}`)));
      window.webContents.reload();
    });
    await waitForUI('document.getElementById("media-chapter").options.length >= 2', "Media chapter list did not load");
  };
  const openMedia = async (chapter) => {
    await run('document.querySelector("[data-view=illustrations]").click()');
    await waitForUI('!document.getElementById("view-illustrations").classList.contains("hidden")', "Media workspace did not open");
    await run(`document.getElementById("media-chapter").value = "${chapter}"; document.getElementById("media-chapter").dispatchEvent(new Event("change"))`);
  };
  const jobs = (directory, chapter = 2) => api(`/api/chapters/${chapter}/media/generate?${new URLSearchParams({ directory })}`);
  if (phase === "restart") {
    await step("chapter-media-restart-persistence", async () => {
      const fixture = JSON.parse(fs.readFileSync(fixturePath, "utf8"));
      await api("/api/workspaces", { path: fixture.directory });
      await reload();
      await openMedia(2);
      const saved = await jobs(fixture.directory);
      assert.deepEqual(saved.jobs.filter(job => job.status === "completed").map(job => job.kind).sort(), ["audio", "image", "video"]);
      await waitForUI('document.querySelectorAll("#media-generation-results audio, #media-generation-results video").length === 2', "Saved audio and video did not reappear after restart");
      const models = await api("/api/media-models");
      for (const kind of ["image", "video", "audio"]) {
        assert.equal(models.connections.find(item => item.id === models.selected[kind]).kind, kind);
      }
      await api("/api/workspaces", { path: paths.workspaceDir });
      await run('document.querySelector("[data-view=overview]").click()');
    });
    return;
  }

  const fixtures = path.join(__dirname, "fixtures");
  const png = fs.readFileSync(path.join(fixtures, "media.png"));
  const audio = fs.readFileSync(path.join(fixtures, "media.mp3"));
  const video = fs.readFileSync(path.join(fixtures, "media.mp4"));
  const requests = [];
  const provider = http.createServer(async (request, response) => {
    try {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const raw = Buffer.concat(chunks).toString("utf8");
      requests.push({ method: request.method, url: request.url, raw, authorization: request.headers.authorization });
      const json = (value, status = 200) => { response.writeHead(status, { "Content-Type": "application/json" }); response.end(JSON.stringify(value)); };
      if (request.url === "/v1/images/generations") return json({ data: [{ b64_json: png.toString("base64") }] });
      if (request.url === "/v1/audio/speech") { response.writeHead(200, { "Content-Type": "audio/mpeg" }); response.end(audio); return; }
      if (request.url === "/v1/videos" && request.method === "POST") return json({ id: "video_fixture", status: "queued", progress: 0 });
      if (request.url === "/v1/videos/video_fixture") return json({ id: "video_fixture", status: "completed", progress: 100 });
      if (request.url === "/v1/videos/video_fixture/content") { response.writeHead(200, { "Content-Type": "video/mp4" }); response.end(video); return; }
      json({ error: { message: "Unexpected local media fixture endpoint" } }, 404);
    } catch (error) { response.writeHead(500); response.end(String(error)); }
  });
  await new Promise(resolve => provider.listen(0, "127.0.0.1", resolve));
  const providerURL = `http://127.0.0.1:${provider.address().port}/v1`;
  let directory;
  try {
    await step("independent-image-video-and-speech-settings", async () => {
      await run('document.getElementById("open-settings").click()');
      for (const kind of ["image", "video", "audio"]) {
        await run(`document.getElementById("settings-${kind}-tab").click()`);
        await waitForUI(`!document.getElementById("media-${kind}-name").disabled`, `${kind} settings did not load`);
        const visible = await run('Array.from(document.querySelectorAll("#settings-dialog > .dialog-card > [id$=panel]")).filter(node => !node.classList.contains("hidden")).map(node => node.id)');
        assert.deepEqual(visible, [`settings-${kind}-panel`]);
        await run(`(() => {
          const field = (name, value) => { document.getElementById("media-${kind}-" + name).value = value; };
          field("name", "本地${kind}验收"); field("id", "fixture-${kind}"); field("url", ${JSON.stringify(providerURL)});
          field("key-action", "clear");
          ${kind === "audio" ? 'field("voice", "alloy");' : ""}
          document.getElementById("media-${kind}-activate").checked = true;
          document.getElementById("media-${kind}-form").requestSubmit();
        })()`);
        await waitForUI(`document.getElementById("media-${kind}-status").textContent.includes("已保存")`, `${kind} settings did not save`);
      }
      assert.equal(requests.length, 0, "Saving a model must not send a generation request");
      const config = await api("/api/media-models");
      for (const kind of ["image", "video", "audio"]) {
        assert.equal(config.connections.find(item => item.id === config.selected[kind]).model, `fixture-${kind}`);
      }
      await run('document.getElementById("close-settings").click()');
    });

    await step("selected-chapter-image-video-and-narration", async () => {
      const project = await api("/api/workspaces", { create: true, name: "章节媒体验收" });
      directory = project.directory;
      assert.ok(directory.startsWith(options.root + path.sep), "Fixture must remain in the test workspace");
      // The creative pipeline has separate integration coverage. Seed two
      // different saved chapters here to detect accidental chapter cross-talk.
      await api("/api/workspaces", { path: paths.workspaceDir });
      for (const folder of ["chapters", "meta"]) fs.mkdirSync(path.join(directory, folder), { recursive: true });
      const first = "第一章的旧城钟声，不应出现在第二章的生成请求中。";
      const second = "第二章：月球书库里，林舟打开蓝色信封，看见远方亲人的问候。";
      fs.writeFileSync(path.join(directory, "outline.json"), JSON.stringify([1, 2].map(chapter => ({ chapter, title: `媒体第${chapter}章`, core_event: "规划不是正文", hook: "", scenes: [] }))));
      fs.writeFileSync(path.join(directory, "chapters/01.md"), first);
      fs.writeFileSync(path.join(directory, "chapters/02.md"), second);
      fs.writeFileSync(path.join(directory, "meta/progress.json"), JSON.stringify({ phase: "writing", total_chapters: 2, completed_chapters: [1, 2], current_chapter: 2, chapter_word_counts: { 1: first.length, 2: second.length } }));
      await api("/api/workspaces", { path: directory });
      await reload();
      await openMedia(2);
      await waitForUI(`document.getElementById("media-source-text").textContent === ${JSON.stringify(second)} && !document.getElementById("media-generate-image").disabled`, "Selected chapter prose or media model did not load");
      for (const kind of ["image", "video", "audio"]) {
        await run(`document.getElementById("media-generate-${kind}").click()`);
        const deadline = Date.now() + 20000;
        let result;
        while (Date.now() < deadline) {
          result = (await jobs(directory)).jobs.find(job => job.kind === kind);
          if (result && ["completed", "failed", "cancelled"].includes(result.status)) break;
          await new Promise(resolve => setTimeout(resolve, 100));
        }
        assert.equal(result?.status, "completed", `${kind} did not complete: ${JSON.stringify(result)}`);
        assert.equal(result.model, `fixture-${kind}`);
      }
      await run('document.getElementById("media-refresh").click()');
      await waitForUI('document.querySelectorAll("#media-generation-results audio, #media-generation-results video").length === 2', "Generated audio and video controls did not appear");
      await run('document.querySelectorAll("#media-generation-results audio, #media-generation-results video").forEach(node => node.load())');
      await waitForUI('Array.from(document.querySelectorAll("#media-generation-results audio, #media-generation-results video")).every(node => node.readyState >= 1 && !node.error && Number.isFinite(node.duration) && node.duration > 0)', "Generated media did not decode in Chromium");
      const completed = (await jobs(directory)).jobs;
      for (const job of completed) {
        const content = await run(`fetch(${JSON.stringify(job.url)}, { headers: { Range: "bytes=0-15" } }).then(async response => ({status:response.status,bytes:(await response.arrayBuffer()).byteLength,type:response.headers.get("Content-Type")}))`);
        assert.equal(content.status, 206, `${job.kind} must support media seeking`);
        assert.equal(content.bytes, 16);
        const saved = await run(`(async () => {
          const response = await fetch(${JSON.stringify(job.url)});
          const format = ${JSON.stringify({ image: "png", audio: "mp3", video: "mp4" }[job.kind])};
          return window.ainovelDesktop.saveExport({ format, filename: "chapter-2." + format, data: new Uint8Array(await response.arrayBuffer()) });
        })()`);
        assert.deepEqual(saved, { saved: false, smokeTest: true }, `${job.kind} did not reach the trusted desktop save handler`);
      }
      const generatedImages = await api(`/api/chapters/2/media?${new URLSearchParams({ directory })}`);
      assert.equal(generatedImages.items.length, 1);
      assert.equal((await jobs(directory, 1)).jobs.length, 0, "Generated media leaked to another chapter");
      const generationRequests = requests.filter(request => request.method === "POST");
      assert.equal(generationRequests.length, 3);
      for (const request of generationRequests) {
        assert.ok(request.raw.includes(second), `Chapter prose missing from ${request.url}`);
        assert.ok(!request.raw.includes(first));
        assert.ok(!request.raw.includes("规划不是正文"));
      }
      const speech = JSON.parse(generationRequests.find(request => request.url.endsWith("/speech")).raw);
      assert.equal(speech.input, second); assert.equal(speech.voice, "alloy");
      assert.equal(fs.readFileSync(path.join(directory, "chapters/02.md"), "utf8"), second, "Generating media modified chapter prose");
      fs.writeFileSync(fixturePath, JSON.stringify({ directory }));
    });
  } finally {
    provider.closeAllConnections();
    await new Promise(resolve => provider.close(resolve));
    await api("/api/workspaces", { path: paths.workspaceDir });
    await run('document.querySelector("[data-view=overview]").click()');
  }
}

module.exports = { runMediaChecks };
