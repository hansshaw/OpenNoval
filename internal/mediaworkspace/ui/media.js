(() => {
  "use strict";

  const KINDS = { image: "AI 生图", video: "视频生成", audio: "章节朗读" };
  const JOB_STATES = { running: "生成中", completed: "已完成", failed: "失败", cancelled: "已取消" };
  window.NovelMedia = {
    create({ api, el, button, getContext, getChapters, setView, toast, download }) {
      const $ = (id) => document.getElementById(id);
      let directory = "", chapter = 0, epoch = -1, sequence = 0, chaptersKey = "";
      let items = [], sourceText = "", sourceTitle = "", mediaBusy = false, mediaLoading = false;
      let config = { connections: [], selected: {} }, configBusy = false, configSequence = 0, configLoaded = false;
      let readerKey = "", readerSequence = 0, readerLoadedAt = 0, readerImagesKey = "";
      let sourceReady = false, sourceKind = "final", sourceChapterKey = "";
      const generationCards = new Map();
      let generationJobs = [], generationPending = false, generationTimer = null, generationSequence = 0, generationFailures = 0;
      const model = (kind, field) => $(`media-${kind}-${field}`);
      const generationPath = (context) => `${mediaPath(context)}/generate`;
      const capture = () => ({ directory, chapter, epoch, sequence });
      const valid = (context) => context.directory === directory && context.chapter === chapter && context.epoch === epoch && context.sequence === sequence && directory === getContext().directory && epoch === getContext().epoch;
      const query = (context) => new URLSearchParams({ directory: context.directory });
      const mediaPath = (context) => `/api/chapters/${context.chapter}/media`;
      const status = (text) => { $("media-status").textContent = text; };
      const option = (value, label) => { const node = el("option", "", label); node.value = value; return node; };
      function validateResult(result, context) {
        if (result.directory !== context.directory || Number(result.chapter) !== context.chapter) throw new Error("返回的配图与当前章节不匹配，请刷新重试。");
        return Array.isArray(result.items) ? result.items : [];
      }
      function setMediaBusy(value) {
        mediaBusy = value;
        for (const id of ["media-upload", "media-export-bundle"]) $(id).disabled = value || mediaLoading || !chapter;
        $("media-refresh").disabled = value;
        $("media-copy-text").disabled = !sourceText;
        renderGenerationControls();
      }
      function reset() {
        directory = getContext().directory || "";
        epoch = getContext().epoch;
        chapter = 0; sequence++; chaptersKey = ""; items = []; sourceText = ""; sourceTitle = ""; mediaLoading = false;
        sourceReady = false; sourceChapterKey = ""; generationJobs = []; generationFailures = 0; generationSequence++; clearTimeout(generationTimer); generationTimer = null;
        $("media-generation-status").textContent = ""; $("media-generation-instructions").value = ""; renderGenerationJobs();
        $("media-file").value = ""; $("media-caption").value = "";
        $("media-source-text").textContent = "选择章节后显示已保存的正文或规划。";
        $("media-source-title").textContent = "章节内容";
        $("media-source-meta").textContent = "";
        status(""); renderGallery();
        readerKey = ""; readerSequence++; readerLoadedAt = 0; readerImagesKey = "";
        $("reader-images-start").replaceChildren(); $("reader-images-end").replaceChildren();
      }
      function sync() {
        const context = getContext();
        if (directory !== (context.directory || "") || epoch !== context.epoch) reset();
        const chapters = getChapters();
        const nextKey = JSON.stringify(chapters.map((item) => [item.number, item.title, item.volumeTitle]));
        if (chaptersKey !== nextKey) {
          chaptersKey = nextKey;
          const previous = chapter;
          if (!chapters.some((item) => item.number === chapter)) chapter = chapters.find((item) => item.number === context.chapter)?.number || chapters[0]?.number || 0;
          const select = $("media-chapter"); select.replaceChildren();
          if (!chapters.length) select.append(option("", "先生成章节规划"));
          for (const item of chapters) select.append(option(String(item.number), `${item.volumeTitle || "正文"} · 第 ${item.number} 章 ${item.title || ""}`));
          select.value = String(chapter || "");
          if (previous !== chapter) { sequence++; items = []; sourceText = ""; sourceReady = false; renderGallery(); }
        }
        for (const id of ["illustrate-current-chapter", "video-current-chapter", "read-current-chapter"]) $(id).disabled = !context.chapter;
        setMediaBusy(mediaBusy);
      }
      function figure(item) {
        const node = el("figure", "chapter-illustration");
        const image = el("img"); image.src = item.url; image.alt = item.caption || item.name || "章节配图"; image.loading = "lazy";
        image.width = item.width; image.height = item.height;
        node.append(image);
        if (item.caption) node.append(el("figcaption", "", item.caption));
        return node;
      }
      async function syncReader({ force = false } = {}) {
        const context = { ...getContext() };
        const key = `${context.directory}\n${context.chapter}\n${context.epoch}`;
        if (key !== readerKey) {
          readerKey = key; readerLoadedAt = 0; readerSequence++; readerImagesKey = "";
          $("reader-images-start").replaceChildren(); $("reader-images-end").replaceChildren();
        }
        if (!context.directory || !context.chapter || (!force && Date.now() - readerLoadedAt < 5000)) return;
        readerLoadedAt = Date.now();
        const token = ++readerSequence;
        try {
          const result = await api(`${mediaPath(context)}?${query(context)}`);
          if (token !== readerSequence || context.directory !== getContext().directory || context.chapter !== getContext().chapter || context.epoch !== getContext().epoch) return;
          const images = validateResult(result, context);
          const imagesKey = JSON.stringify(images);
          if (imagesKey === readerImagesKey) return;
          readerImagesKey = imagesKey;
          for (const placement of ["chapter-start", "chapter-end"]) {
            const target = $(placement === "chapter-start" ? "reader-images-start" : "reader-images-end");
            target.replaceChildren(...images.filter((item) => item.placement === placement).map(figure));
          }
        } catch (_) { readerLoadedAt = 0; }
      }
      async function load() {
        sync();
        const context = capture(), entry = getChapters().find((item) => item.number === chapter);
        if (!chapter || !directory) { sourceReady = false; renderGenerationControls(); status("先生成章节规划，再添加章节媒体。"); return; }
        const token = ++sequence; context.sequence = token;
        sourceReady = false;
        const sourceKey = `${directory}\n${chapter}\n${epoch}`;
        if (sourceChapterKey !== sourceKey) {
          sourceChapterKey = sourceKey; sourceKind = entry?.source === "draft" ? "draft" : "final";
          $("media-generation-source").value = sourceKind;
          generationJobs = []; generationFailures = 0; generationSequence++; clearTimeout(generationTimer); renderGenerationJobs();
          $("media-generation-status").textContent = "";
        }
        mediaLoading = true; setMediaBusy(mediaBusy);
        items = []; renderGallery(); status("正在读取章节配图……");
        sourceTitle = entry?.title || `第 ${chapter} 章`;
        const plan = [entry?.coreEvent, entry?.hook ? `悬念：${entry.hook}` : "", ...(entry?.scenes || []).map((scene, index) => `场景 ${index + 1}：${typeof scene === "string" ? scene : JSON.stringify(scene)}`)].filter(Boolean).join("\n\n");
        sourceText = plan;
        $("media-source-title").textContent = sourceTitle;
        $("media-source-text").textContent = plan || "本章尚无正文或详细规划。";
        $("media-source-meta").textContent = "已保存的章节规划";
        const params = query(context); if (sourceKind === "draft") params.set("source", "draft");
        const jobs = [api(`${mediaPath(context)}?${query(context)}`), api(`/api/chapters/${chapter}?${params}`)];
        const results = await Promise.allSettled(jobs);
        if (!valid(context)) return;
        mediaLoading = false;
        if (results[0].status === "fulfilled") {
          try { items = validateResult(results[0].value, context); renderGallery(); status(""); }
          catch (error) { status(error.message); }
        } else status(`配图读取失败：${results[0].reason.message}`);
        if (results[1]?.status === "fulfilled") {
          sourceText = results[1].value.content || ""; sourceReady = Boolean(sourceText.trim());
          $("media-source-text").textContent = sourceText || "正文为空。";
          $("media-source-meta").textContent = sourceKind === "draft" ? "当前已保存草稿" : "当前正式正文";
        } else if (results[1]?.status === "rejected") $("media-source-meta").textContent = `正文读取失败，当前展示规划：${results[1].reason.message}`;
        setMediaBusy(mediaBusy);
        await Promise.all([loadConfig(), loadGeneration(context)]);
      }
      async function mutate(context, action) {
        if (mediaBusy || mediaLoading || !valid(context)) return;
        setMediaBusy(true);
        try {
          const result = await action();
          if (valid(context)) { items = validateResult(result, context); renderGallery(); status("配图已保存。"); }
          readerLoadedAt = 0; await syncReader({ force: true });
          return true;
        } catch (error) { if (valid(context)) status(error.message); else toast(error.message, "error"); return false; }
        finally { setMediaBusy(false); }
      }
      function renderGallery() {
        const gallery = $("media-gallery"); gallery.replaceChildren();
        $("media-count").textContent = `${items.length} 张配图`;
        if (!items.length) { gallery.append(el("div", "panel media-empty", chapter ? "本章还没有配图。导入图片后，可调整图注与展示位置。" : "选择章节，开始布置画面。")); return; }
        const context = capture();
        for (const item of items) {
          const card = el("article", "panel media-card"); card.append(figure(item));
          card.append(el("p", "hint", `${item.name} · ${item.width} × ${item.height} · ${(item.bytes / 1024 / 1024).toFixed(2)} MB`));
          const captionLabel = el("label", "", "图注"), caption = el("input"); caption.value = item.caption || ""; caption.maxLength = 1000; captionLabel.append(caption);
          const placementLabel = el("label", "", "展示位置"), placement = el("select");
          placement.append(option("chapter-start", "章节开头"), option("chapter-end", "章节结尾")); placement.value = item.placement; placementLabel.append(placement);
          const orderLabel = el("label", "", "顺序"), order = el("input"); order.type = "number"; order.min = "0"; order.max = "9999"; order.value = String(item.order); orderLabel.append(order);
          const fields = el("div", "media-card-fields"); fields.append(placementLabel, orderLabel);
          const controls = el("div", "button-row");
          controls.append(button("保存图注与位置", "secondary-button", () => mutate(context, () => api(`${mediaPath(context)}/${item.id}`, { method: "PATCH", body: { directory: context.directory, caption: caption.value, placement: placement.value, order: Number(order.value) } }))));
          controls.append(button("移除配图", "text-button danger-text", () => mutate(context, () => api(`${mediaPath(context)}/${item.id}?${query(context)}`, { method: "DELETE" }))));
          card.append(captionLabel, fields, controls); gallery.append(card);
        }
      }
      async function upload(event) {
        event?.preventDefault();
        if (mediaBusy || mediaLoading || !chapter) return;
        const file = $("media-file").files?.[0];
        if (!file) { status("请选择一张图片。"); return; }
        if (file.size > 20 * 1024 * 1024) { status("图片不能超过 20 MB。"); return; }
        const context = capture(), body = new FormData();
        body.append("directory", context.directory); body.append("file", file);
        body.append("caption", $("media-caption").value); body.append("placement", $("media-placement").value);
        status("正在导入图片……");
        const saved = await mutate(context, () => api(mediaPath(context), { method: "POST", body }));
        if (saved && valid(context)) { $("media-file").value = ""; $("media-caption").value = ""; status("图片已导入，可在章节工作台查看。"); }
      }
      function selectedConnection(kind) {
        return (config.connections || []).find((item) => item.id === config.selected?.[kind] && item.kind === kind);
      }
      function renderGenerationControls() {
        const blocked = mediaLoading || mediaBusy || generationPending || !chapter || !sourceReady;
        $("media-generation-source").disabled = mediaLoading || generationPending;
        $("media-generation-source-hint").textContent = sourceReady
          ? `将使用第 ${chapter} 章${sourceKind === "draft" ? "已保存草稿" : "正式正文"}（${[...sourceText].length.toLocaleString("zh-CN")} 字符）。未保存修改不会参与生成。`
          : mediaLoading ? "正在读取所选正文……" : "所选来源没有可用正文，暂不能生成。请先保存正文或切换来源；章节规划不会替代正文。";
        for (const kind of Object.keys(KINDS)) {
          const connection = selectedConnection(kind);
          const running = generationJobs.some((job) => job.kind === kind && job.status === "running");
          $("media-generate-" + kind).disabled = blocked || running || !connection || connection.protocol !== "openai-compatible";
          $("media-generation-" + kind + "-model").textContent = !connection ? `尚未配置${KINDS[kind]}模型`
            : `${connection.name} / ${connection.model}${connection.protocol === "custom" ? " · 自定义协议暂不能生成" : running ? " · 正在生成" : ""}`;
        }
      }
      function renderGenerationJobs() {
        const target = $("media-generation-results");
        const context = capture(), currentIDs = new Set(generationJobs.map((job) => job.id));
        for (const [id, entry] of generationCards) {
          if (!currentIDs.has(id)) { entry.node.remove(); generationCards.delete(id); }
        }
        for (const job of generationJobs) {
          // Updating a different running job must not recreate an audio/video player.
          const signature = JSON.stringify([context, job]);
          const previous = generationCards.get(job.id);
          if (previous?.signature === signature) continue;
          const card = el("article", "media-job"); card.setAttribute("data-media-job", job.id); card.setAttribute("data-media-kind", job.kind);
          const heading = el("div", "media-job-heading");
          heading.append(el("h4", "", `${KINDS[job.kind] || "媒体"} · 第 ${job.chapter} 章`), el("span", "soft-badge", JOB_STATES[job.status] || job.status)); card.append(heading);
          card.append(el("p", "hint", `${job.source === "draft" ? "已保存草稿" : "正式正文"} · ${job.model || ""}${job.createdAt ? " · " + new Date(job.createdAt).toLocaleString("zh-CN") : ""}`));
          if (job.status === "running") { const progress = el("progress"); progress.max = 100; if (Number.isFinite(job.progress) && job.progress > 0) progress.value = job.progress; progress.setAttribute("aria-label", "媒体生成进度"); card.append(progress); }
          if (job.error) card.append(el("p", "form-status danger-text", job.error));
          if (job.status === "completed" && job.url && ["audio", "video"].includes(job.kind)) {
            const player = el(job.kind); player.src = job.url; player.controls = true; player.preload = "metadata"; player.setAttribute("aria-label", `第 ${job.chapter} 章${job.kind === "audio" ? "朗读音频" : "视频短片"}`); card.append(player);
          }
          if (job.status === "completed" && job.kind === "image") card.append(el("p", "hint", "生成配图已加入下方章节画廊，可编辑图注与位置。"));
          const controls = el("div", "button-row");
          if (job.status === "completed" && job.url) controls.append(button("下载" + (job.kind === "audio" ? "朗读音频" : job.kind === "video" ? "视频短片" : "配图"), "secondary-button", async () => {
            try { await download(job.url, `chapter-${job.chapter}-${job.id}.${({ "audio/wav": "wav", "audio/x-wav": "wav", "audio/mpeg": "mp3", "audio/mp3": "mp3", "audio/aac": "aac", "audio/ogg": "ogg", "image/jpeg": "jpg", "image/webp": "webp", "image/png": "png", "video/mp4": "mp4" })[job.mime] || (job.kind === "audio" ? "mp3" : job.kind === "video" ? "mp4" : "png")}`); }
            catch (error) { if (valid(context)) $("media-generation-status").textContent = error.message; }
          }));
          controls.append(button(job.status === "running" ? "取消生成" : "删除任务记录" + (job.kind === "image" ? "" : "与文件"), "text-button danger-text", async () => {
            if (!valid(context) || generationPending) return;
            generationPending = true; renderGenerationControls();
            try { await api(`${generationPath(context)}/${encodeURIComponent(job.id)}?${query(context)}`, { method: "DELETE" }); if (valid(context)) await loadGeneration(context); }
            catch (error) { if (valid(context)) $("media-generation-status").textContent = error.message; }
            finally { generationPending = false; renderGenerationControls(); }
          }));
          card.append(controls);
          if (previous) previous.node.replaceWith(card); else target.append(card);
          generationCards.set(job.id, { signature, node: card });
        }
      }
      async function refreshImages(context) {
        const result = await api(`${mediaPath(context)}?${query(context)}`);
        if (valid(context)) { items = validateResult(result, context); renderGallery(); await syncReader({ force: true }); }
      }
      async function loadGeneration(context = capture()) {
        if (!valid(context) || !context.chapter) return;
        clearTimeout(generationTimer); generationTimer = null;
        const token = ++generationSequence;
        let retry = false;
        try {
          const result = await api(`${generationPath(context)}?${query(context)}`);
          if (!valid(context) || token !== generationSequence) return;
          if (result.directory !== context.directory || Number(result.chapter) !== context.chapter || !Array.isArray(result.jobs)) throw new Error("生成任务与当前章节不匹配，请刷新重试。");
          const old = new Map(generationJobs.map((job) => [job.id, job.status]));
          generationJobs = result.jobs; generationFailures = 0;
          if ($("media-generation-status").textContent.startsWith("生成任务读取失败：")) $("media-generation-status").textContent = "";
          renderGenerationJobs(); renderGenerationControls();
          if (generationJobs.some((job) => job.kind === "image" && job.status === "completed" && old.get(job.id) !== "completed")) await refreshImages(context);
        } catch (error) { if (valid(context) && token === generationSequence) { $("media-generation-status").textContent = `生成任务读取失败：${error.message}`; retry = ++generationFailures < 3; } }
        finally {
          if (valid(context) && token === generationSequence && (retry || (generationFailures === 0 && generationJobs.some((job) => job.status === "running")))) generationTimer = setTimeout(() => loadGeneration(context), 3000);
        }
      }
      async function generate(kind) {
        if (!Object.hasOwn(KINDS, kind) || $("media-generate-" + kind).disabled) return;
        const context = capture(), selectedSource = sourceKind;
        generationPending = true; renderGenerationControls();
        $("media-generation-status").textContent = `正在提交第 ${context.chapter} 章${KINDS[kind]}任务……`;
        try {
          await api(generationPath(context), { method: "POST", body: { directory: context.directory, kind, source: selectedSource, instructions: kind === "audio" ? "" : $("media-generation-instructions").value.trim(), placement: $("media-placement").value || "chapter-end" } });
          if (valid(context)) { $("media-generation-status").textContent = "任务已提交，生成结果将保存在本章。可以切换页面，稍后回来查看。"; await loadGeneration(context); }
        } catch (error) { if (valid(context)) $("media-generation-status").textContent = error.message; else toast(error.message, "error"); }
        finally { generationPending = false; renderGenerationControls(); }
      }
      function setConfigBusy(value) {
        configBusy = value;
        for (const kind of Object.keys(KINDS)) {
          for (const node of model(kind, "form").querySelectorAll("input, select, textarea, button")) node.disabled = value;
          model(kind, "default").disabled = value;
          if (!value) { model(kind, "key").disabled = model(kind, "key-action").value !== "replace"; model(kind, "delete").disabled = !model(kind, "saved").value; }
        }
      }
      function renderConfig() {
        const summary = $("media-connection-summary"); summary.replaceChildren();
        for (const [kind, title] of Object.entries(KINDS)) {
          const connections = (config.connections || []).filter((item) => item.kind === kind);
          const select = model(kind, "saved"), previous = select.value;
          select.replaceChildren(option("", `＋ 添加${title}连接`));
          for (const item of connections) select.append(option(item.id, `${item.name} / ${item.model}`));
          select.value = connections.some((item) => item.id === previous) ? previous : "";
          const selected = selectedConnection(kind), input = model(kind, "default");
          input.replaceChildren(option("", "未选择"));
          for (const item of connections) input.append(option(item.id, `${item.name} / ${item.model}`));
          input.value = selected?.id || "";
          summary.append(el("p", "media-connection-line", `${title}：${selected ? `${selected.name} / ${selected.model}` : "未配置"}`));
        }
        setConfigBusy(configBusy); renderGenerationControls();
      }
      async function loadConfig() {
        if (configBusy) return;
        const token = ++configSequence, initial = !configLoaded;
        if (initial) setConfigBusy(true);
        try {
          const result = await api("/api/media-models", { normalizeResponse: false });
          if (token !== configSequence) return;
          config = result; renderConfig();
          if (!configLoaded) { for (const kind of Object.keys(KINDS)) fillModel(kind, ""); configLoaded = true; }
        } catch (error) { if (token === configSequence) for (const kind of Object.keys(KINDS)) model(kind, "status").textContent = `配置读取失败：${error.message}`; }
        finally { if (initial && token === configSequence) setConfigBusy(false); }
      }
      function fillModel(kind, id) {
        const item = (config.connections || []).find((row) => row.id === id && row.kind === kind) || {};
        model(kind, "saved").value = item.id || "";
        for (const [field, target] of Object.entries({ name: "name", protocol: "protocol", model: "id", baseURL: "url", description: "description" })) model(kind, target).value = item[field] || (field === "protocol" ? "openai-compatible" : "");
        if (kind === "audio") model(kind, "voice").value = item.voice || "";
        model(kind, "key").value = ""; model(kind, "key-action").value = item.id ? "keep" : "replace";
        model(kind, "activate").checked = !item.id || config.selected?.[kind] === item.id;
        model(kind, "key-hint").textContent = item.hasAPIKey ? `已保存 Key：${item.apiKeyHint || "已隐藏"}` : "未保存 Key；本地免密服务可选择保留并留空。";
        setConfigBusy(configBusy);
      }
      async function configMutation(kind, action, onSaved) {
        if (configBusy) return false;
        ++configSequence; setConfigBusy(true); model(kind, "status").textContent = "正在保存……";
        try {
          config = await action(); renderConfig(); onSaved?.(config);
          model(kind, "status").textContent = "已保存。仅在章节页点击生成后调用模型。"; return true;
        } catch (error) { renderConfig(); model(kind, "status").textContent = error.message; return false; }
        finally { setConfigBusy(false); }
      }
      function showSettings(type = "text") {
        if (typeof type === "boolean") type = type ? "image" : "text";
        if (!["text", ...Object.keys(KINDS)].includes(type)) type = "text";
        for (const kind of ["text", ...Object.keys(KINDS)]) {
          $("settings-" + kind + "-panel").classList.toggle("hidden", kind !== type);
          const tab = $("settings-" + kind + "-tab"); tab.classList.toggle("selected", kind === type); tab.setAttribute("aria-pressed", String(kind === type));
        }
        if (type !== "text") loadConfig();
      }
      function openSettings(kind) {
        showSettings(kind); const dialog = $("settings-dialog"); if (!dialog.open) dialog.showModal();
      }
      async function open(number, kind = "image") {
        sync();
        if (getChapters().some((entry) => entry.number === Number(number))) { chapter = Number(number); sequence++; $("media-chapter").value = String(chapter); }
        setView("illustrations");
        $("media-generation-" + kind)?.scrollIntoView?.({ block: "nearest" });
      }
      function bindEvents() {
        $("media-chapter").addEventListener("change", () => { chapter = Number($("media-chapter").value); sequence++; $("media-file").value = ""; $("media-caption").value = ""; load(); });
        $("media-refresh").addEventListener("click", load);
        $("media-upload-form").addEventListener("submit", upload);
        $("illustrate-current-chapter").addEventListener("click", () => open(getContext().chapter));
        $("media-copy-text").addEventListener("click", async () => { try { await navigator.clipboard.writeText(sourceText); toast("章节内容已复制"); } catch (_) { status("复制失败，请在右侧文本中手动选择复制。"); } });
        $("media-export-bundle").addEventListener("click", async () => {
          if (!chapter || mediaBusy || mediaLoading) return;
          const context = capture(); setMediaBusy(true); status("正在打包正文、规划和配图……");
          try { await download(`${mediaPath(context)}/export?${query(context)}`, `chapter-${chapter}-media.zip`); if (valid(context)) status("章节素材包已导出，包含正文、规划与配图。"); }
          catch (error) { if (valid(context)) status(error.message); }
          finally { setMediaBusy(false); }
        });
        $("settings-text-tab").addEventListener("click", () => showSettings("text"));
        $("media-open-settings").addEventListener("click", () => openSettings("image"));
        $("video-current-chapter").addEventListener("click", () => open(getContext().chapter, "video"));
        $("read-current-chapter").addEventListener("click", () => open(getContext().chapter, "audio"));
        $("media-generation-source").addEventListener("change", () => { sourceKind = $("media-generation-source").value === "draft" ? "draft" : "final"; load(); });
        for (const kind of Object.keys(KINDS)) {
          $("settings-" + kind + "-tab").addEventListener("click", () => showSettings(kind));
          $("media-configure-" + kind).addEventListener("click", () => openSettings(kind));
          $("media-generate-" + kind).addEventListener("click", () => generate(kind));
          model(kind, "saved").addEventListener("change", () => fillModel(kind, model(kind, "saved").value));
          model(kind, "key-action").addEventListener("change", () => { if (model(kind, "key-action").value !== "replace") model(kind, "key").value = ""; setConfigBusy(configBusy); });
          model(kind, "default").addEventListener("change", () => configMutation(kind, () => api("/api/media-models/select", { method: "POST", body: { kind, id: model(kind, "default").value }, normalizeResponse: false })));
          model(kind, "form").addEventListener("submit", (event) => {
            event.preventDefault();
            const body = { id: model(kind, "saved").value, name: model(kind, "name").value.trim(), kind, protocol: model(kind, "protocol").value, baseURL: model(kind, "url").value.trim(), model: model(kind, "id").value.trim(), voice: kind === "audio" ? model(kind, "voice").value.trim() : "", description: model(kind, "description").value.trim(), apiKeyAction: model(kind, "key-action").value, apiKey: model(kind, "key-action").value === "replace" ? model(kind, "key").value : "", activate: model(kind, "activate").checked };
            return configMutation(kind, () => api("/api/media-models", { method: "POST", body, normalizeResponse: false }), (result) => fillModel(kind, result.savedID));
          });
          model(kind, "delete").addEventListener("click", () => { const id = model(kind, "saved").value; if (id) return configMutation(kind, () => api(`/api/media-models/${encodeURIComponent(id)}`, { method: "DELETE", normalizeResponse: false }), () => fillModel(kind, "")); });
        }
      }
      return { bindEvents, sync, reset, load, loadConfig, syncReader, open, upload, showSettings, generate, loadGeneration };
    },
  };
})();
