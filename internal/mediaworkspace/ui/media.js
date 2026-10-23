(() => {
  "use strict";

  const KINDS = { image: "AI 生图", video: "视频生成", audio: "语音 / 配音" };
  window.NovelMedia = {
    create({ api, el, button, getContext, getChapters, setView, toast, download }) {
      const $ = (id) => document.getElementById(id);
      let directory = "", chapter = 0, epoch = -1, sequence = 0, chaptersKey = "";
      let items = [], sourceText = "", sourceTitle = "", mediaBusy = false, mediaLoading = false;
      let config = { connections: [], selected: {} }, configBusy = false, configSequence = 0, configLoaded = false;
      let readerKey = "", readerSequence = 0, readerLoadedAt = 0, readerImagesKey = "";
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
      }
      function reset() {
        directory = getContext().directory || "";
        epoch = getContext().epoch;
        chapter = 0; sequence++; chaptersKey = ""; items = []; sourceText = ""; sourceTitle = ""; mediaLoading = false;
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
          if (previous !== chapter) { sequence++; items = []; sourceText = ""; renderGallery(); }
        }
        $("illustrate-current-chapter").disabled = !context.chapter;
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
        if (!chapter || !directory) { status("先生成章节规划，再为章节添加配图。"); return; }
        const token = ++sequence; context.sequence = token;
        mediaLoading = true; setMediaBusy(mediaBusy);
        items = []; renderGallery(); status("正在读取章节配图……");
        sourceTitle = entry?.title || `第 ${chapter} 章`;
        const plan = [entry?.coreEvent, entry?.hook ? `悬念：${entry.hook}` : "", ...(entry?.scenes || []).map((scene, index) => `场景 ${index + 1}：${typeof scene === "string" ? scene : JSON.stringify(scene)}`)].filter(Boolean).join("\n\n");
        sourceText = plan;
        $("media-source-title").textContent = sourceTitle;
        $("media-source-text").textContent = plan || "本章尚无正文或详细规划。";
        $("media-source-meta").textContent = "已保存的章节规划";
        const jobs = [api(`${mediaPath(context)}?${query(context)}`)];
        const hasContent = Boolean(entry?.hasContent || ["final", "draft"].includes(entry?.source));
        if (hasContent) {
          const params = query(context); if (entry.source === "draft") params.set("source", "draft");
          jobs.push(api(`/api/chapters/${chapter}?${params}`));
        }
        const results = await Promise.allSettled(jobs);
        if (!valid(context)) return;
        mediaLoading = false;
        if (results[0].status === "fulfilled") {
          try { items = validateResult(results[0].value, context); renderGallery(); status(""); }
          catch (error) { status(error.message); }
        } else status(`配图读取失败：${results[0].reason.message}`);
        if (results[1]?.status === "fulfilled") {
          sourceText = results[1].value.content || plan;
          $("media-source-text").textContent = sourceText || "正文为空。";
          $("media-source-meta").textContent = entry.source === "draft" ? "当前已保存草稿" : "当前正式正文";
        } else if (results[1]?.status === "rejected") $("media-source-meta").textContent = `正文读取失败，当前展示规划：${results[1].reason.message}`;
        setMediaBusy(mediaBusy);
        await loadConfig();
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
      function setConfigBusy(value) {
        configBusy = value;
        for (const formID of ["media-model-form", "media-model-defaults"]) {
          for (const node of $(formID).querySelectorAll("input, select, textarea, button")) node.disabled = value;
        }
        if (!value) { $("media-model-key").disabled = $("media-model-key-action").value !== "replace"; $("media-model-delete").disabled = !$("media-model-saved").value; $("media-model-voice").disabled = $("media-model-kind").value !== "audio"; }
      }
      function renderConfig() {
        const select = $("media-model-saved"), previous = select.value;
        select.replaceChildren(option("", "＋ 添加媒体连接"));
        for (const item of config.connections || []) select.append(option(item.id, `${KINDS[item.kind] || item.kind} · ${item.name} / ${item.model}`));
        select.value = (config.connections || []).some((item) => item.id === previous) ? previous : "";
        const defaults = $("media-model-defaults"), summary = $("media-connection-summary"); defaults.replaceChildren(); summary.replaceChildren();
        for (const [kind, title] of Object.entries(KINDS)) {
          const selected = (config.connections || []).find((item) => item.id === config.selected?.[kind]);
          summary.append(el("p", "media-connection-line", `${title}：${selected ? `${selected.name} / ${selected.model}` : "未配置"}`));
          const label = el("label", "", title), input = el("select"); input.append(option("", "未选择"));
          for (const item of config.connections || []) if (item.kind === kind) input.append(option(item.id, `${item.name} / ${item.model}`));
          input.value = selected?.id || "";
          input.addEventListener("change", () => configMutation("media-model-status", () => api("/api/media-models/select", { method: "POST", body: { kind, id: input.value } })));
          label.append(input); defaults.append(label);
        }
        setConfigBusy(configBusy);
      }
      async function loadConfig() {
        if (configBusy) return;
        const token = ++configSequence;
        const initial = !configLoaded;
        if (initial) setConfigBusy(true);
        try {
          const result = await api("/api/media-models", { normalizeResponse: false });
          if (token !== configSequence) return;
          config = result; renderConfig();
          if (!configLoaded) { fillModel(""); configLoaded = true; }
        } catch (error) { if (token === configSequence) $("media-model-status").textContent = `媒体配置读取失败：${error.message}`; }
        finally { if (initial && token === configSequence) setConfigBusy(false); }
      }
      function fillModel(id) {
        const item = (config.connections || []).find((row) => row.id === id) || {};
        $("media-model-saved").value = item.id || "";
        for (const [field, target] of Object.entries({ name: "name", kind: "kind", protocol: "protocol", model: "id", baseURL: "url", voice: "voice", description: "description" })) $("media-model-" + target).value = item[field] || (field === "kind" ? "image" : field === "protocol" ? "openai-compatible" : "");
        $("media-model-key").value = ""; $("media-model-key-action").value = item.id ? "keep" : "replace";
        $("media-model-activate").checked = !item.id || config.selected?.[item.kind] === item.id;
        $("media-model-key-hint").textContent = item.hasAPIKey ? `已保存 Key：${item.apiKeyHint || "已隐藏"}` : "未保存 Key；本地免密服务可选择保留并留空。";
        setConfigBusy(configBusy);
      }
      async function configMutation(statusID, action, onSaved) {
        if (configBusy) return false;
        ++configSequence; setConfigBusy(true); $(statusID).textContent = "正在保存……";
        try {
          config = await action(); renderConfig(); onSaved?.(config);
          $(statusID).textContent = "已保存。媒体生成不会自动开始。"; return true;
        } catch (error) { renderConfig(); $(statusID).textContent = error.message; return false; }
        finally { setConfigBusy(false); }
      }
      function showSettings(media) {
        $("settings-text-panel").classList.toggle("hidden", media);
        $("settings-media-panel").classList.toggle("hidden", !media);
        for (const [id, selected] of [["settings-text-tab", !media], ["settings-media-tab", media]]) { $(id).classList.toggle("selected", selected); $(id).setAttribute("aria-pressed", String(selected)); }
        if (media) loadConfig();
      }
      async function open(number) {
        sync();
        if (getChapters().some((entry) => entry.number === Number(number))) { chapter = Number(number); sequence++; $("media-chapter").value = String(chapter); }
        setView("illustrations");
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
        $("settings-text-tab").addEventListener("click", () => showSettings(false));
        $("settings-media-tab").addEventListener("click", () => showSettings(true));
        $("media-open-settings").addEventListener("click", () => { showSettings(true); const dialog = $("settings-dialog"); if (!dialog.open) dialog.showModal(); });
        $("media-model-saved").addEventListener("change", () => fillModel($("media-model-saved").value));
        $("media-model-kind").addEventListener("change", () => { if ($("media-model-kind").value !== "audio") $("media-model-voice").value = ""; setConfigBusy(configBusy); });
        $("media-model-key-action").addEventListener("change", () => { if ($("media-model-key-action").value !== "replace") $("media-model-key").value = ""; setConfigBusy(configBusy); });
        $("media-model-form").addEventListener("submit", (event) => {
          event.preventDefault();
          const body = { id: $("media-model-saved").value, name: $("media-model-name").value.trim(), kind: $("media-model-kind").value, protocol: $("media-model-protocol").value, baseURL: $("media-model-url").value.trim(), model: $("media-model-id").value.trim(), voice: $("media-model-voice").value.trim(), description: $("media-model-description").value.trim(), apiKeyAction: $("media-model-key-action").value, apiKey: $("media-model-key").value, activate: $("media-model-activate").checked };
          if (body.apiKeyAction !== "replace") body.apiKey = "";
          if (body.kind !== "audio") body.voice = "";
          configMutation("media-model-status", () => api("/api/media-models", { method: "POST", body, normalizeResponse: false }), (result) => fillModel(result.savedID));
        });
        $("media-model-delete").addEventListener("click", () => { const id = $("media-model-saved").value; if (id) configMutation("media-model-status", () => api(`/api/media-models/${encodeURIComponent(id)}`, { method: "DELETE", normalizeResponse: false }), () => fillModel("")); });
      }
      return { bindEvents, sync, reset, load, loadConfig, syncReader, open, upload, showSettings };
    },
  };
})();
