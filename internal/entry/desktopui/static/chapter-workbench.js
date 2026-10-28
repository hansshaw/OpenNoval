(() => {
  "use strict";

  window.NovelChapterWorkbench = {
    create({ api, el, button, getContext, getChapters, canRewrite, isBusy, setBusy, refresh, repaint, syncRevisions, toast }) {
      const $ = (id) => document.getElementById(id);
      const drafts = new Map(), selections = new Map(), projectSync = new Map();
      const androidDrafts = Boolean(window.ainovelAndroid), pendingBackups = new Map();
      let backupTimer = 0, backupFailed = false;
      const fields = { title: "edit-chapter-title", volumeTitle: "edit-volume-title", coreEvent: "edit-core-event", hook: "edit-chapter-hook", scenes: "edit-chapter-scenes", content: "edit-chapter-content" };
      let key = "", activeEpoch = -1, generation = 0, current = null, versions = [], selectedVersion = "", currentHash = "";
      let loading = false, loadingVersions = false, saving = false, syncRequired = false, lastVersionsAt = 0;
      let observedSource = "", observedBusy = false, editorNeedsRefresh = false;
      let editorMode = false;
      const contextKey = (context) => `${context.directory || ""}\n${context.chapter || 0}`;
      const valid = (context, token) => token === generation && key === contextKey(context) && contextKey(context) === contextKey(getContext()) && context.epoch === getContext().epoch;
      const selection = () => {
        const directory = getContext().directory || "";
        if (!selections.has(directory)) selections.set(directory, { chapters: new Set(), instruction: $("chapter-feedback").value || "", autoApply: false });
        return selections.get(directory);
      };
      const planValue = (value) => JSON.stringify([value.title, value.volumeTitle, value.coreEvent, value.hook, value.scenes]);
      const dirtyPlan = () => Boolean(current?.loaded && planValue(current.value) !== planValue(current.base));
      const dirtyContent = () => Boolean(current?.loaded && current.value.content !== current.base.content);
      const selectedNumbers = () => [...selection().chapters].filter((number) => canRewrite(getChapters().find((chapter) => chapter.number === number))).sort((a, b) => a - b);
      const message = (value) => { $("chapter-editor-status").textContent = value + (backupFailed ? " 手机草稿备份未成功，请及时点击保存正文。" : ""); };

      const backupKey = (draftKey) => "ainovel.android.chapter-draft.v1:" + encodeURIComponent(draftKey);
      const validBackup = (record) => record?.version === 1 && typeof record.revision === "string" && record.value &&
        ["title", "volumeTitle", "coreEvent", "hook", "content"].every((field) => typeof record.value[field] === "string") &&
        Array.isArray(record.value.scenes) && record.value.scenes.every((scene) => typeof scene === "string");
      const draftDirty = (draft) => draft?.loaded && (planValue(draft.value) !== planValue(draft.base) || draft.value.content !== draft.base.content);

      function readBackup(draftKey) {
        if (!androidDrafts) return null;
        try {
          const saved = JSON.parse(window.localStorage.getItem(backupKey(draftKey)) || "null");
          return validBackup(saved) ? saved : null;
        } catch (_) { backupFailed = true; return null; }
      }

      function flushBackups() {
        if (!androidDrafts) return;
        clearTimeout(backupTimer);
        backupTimer = 0;
        for (const [draftKey, draft] of pendingBackups) {
          try {
            if (draftDirty(draft) || draft.recovery) {
              window.localStorage.setItem(backupKey(draftKey), JSON.stringify({ version: 1, revision: draft.revision,
                value: draft.value, recovery: draft.recovery || null, updatedAt: Date.now() }));
            } else window.localStorage.removeItem(backupKey(draftKey));
            pendingBackups.delete(draftKey);
          } catch (_) { /* Retain this entry so the next lifecycle flush retries it. */ }
        }
        backupFailed = pendingBackups.size > 0;
        if (backupFailed) message("手机草稿尚未备份。当前编辑器内容仍保留。");
      }

      function backupDraft(draftKey = key, draft = current) {
        if (!androidDrafts || !draft?.loaded) return;
        pendingBackups.set(draftKey, draft);
        clearTimeout(backupTimer);
        backupTimer = setTimeout(flushBackups, 300);
      }

      function resolveBackup(restore) {
        if (!current?.recovery || saving || isBusy()) return;
        if (restore && draftDirty(current) && !window.confirm("载入手机草稿会替换编辑器内当前未保存的修改，作品的正式内容不会改变。继续载入？")) return;
        if (!restore && !window.confirm("删除这份旧手机草稿备份？已保存的作品和当前编辑器内容不会改变。")) return;
        if (restore) current.value = structuredClone(current.recovery.value);
        current.recovery = null;
        fillEditor();
        backupDraft();
        flushBackups();
        message(restore ? "手机草稿已载入编辑器，请核对标题、规划和正文后分别保存。正式内容尚未改变。" : "旧手机草稿备份已删除，当前编辑器内容保持不变。");
        syncDisabled();
      }

      function fillEditor() {
        for (const [field, id] of Object.entries(fields)) $(id).value = field === "scenes" ? (current?.value.scenes || []).join("\n") : current?.value[field] || "";
      }

      function syncDisabled() {
        const context = getContext(), busy = isBusy() || saving;
        const loaded = Boolean(current?.loaded && context.chapter);
        for (const [field, id] of Object.entries(fields)) $(id).disabled = !loaded || busy || (field === "content" ? !(current?.hasContent || current?.manual) : !current?.hasPlan);
        $("save-chapter-plan").disabled = !loaded || !current?.hasPlan || busy || !dirtyPlan();
        $("save-chapter-content").disabled = !loaded || !(current?.hasContent || current?.manual) || busy || !dirtyContent();
        $("reset-chapter-edits").disabled = !loaded || busy || (!dirtyPlan() && !dirtyContent());
        $("reload-chapter-versions").disabled = !context.chapter || loadingVersions;
        $("apply-chapter-version").disabled = busy || !selectedVersion || versions.find((version) => version.id === selectedVersion)?.active !== false;
        $("sync-chapter-revisions").disabled = busy || !syncRequired;
        $("chapter-draft-recovery").classList.toggle("hidden", !current?.recovery);
        $("restore-local-chapter-draft").disabled = busy || !current?.recovery;
        $("discard-local-chapter-draft").disabled = busy || !current?.recovery;
        $("chapter-sync-required").classList.toggle("hidden", !syncRequired);
        const numbers = selectedNumbers();
        $("rewrite-selected-count").textContent = numbers.length ? `已勾选 ${numbers.length} 章：${numbers.join("、")}` : "勾选目录中的已完成章节，可一次重写多章；未勾选时使用当前章。";
        const fallback = canRewrite(getChapters().find((chapter) => chapter.number === context.chapter));
        $("send-chapter-feedback").disabled = busy || !context.approved || (!numbers.length && !fallback);
        $("chapter-feedback").disabled = saving;
        $("rewrite-auto-apply").disabled = busy;
        $("select-rewrite-chapters").disabled = !getChapters().some(canRewrite);
        $("clear-rewrite-chapters").disabled = !selection().chapters.size;
        $("send-chapter-feedback").textContent = numbers.length ? `按指令重写所选 ${numbers.length} 章` : "按指令重写当前章";
      }

      function renderVersions() {
        const list = $("chapter-version-list");
        list.replaceChildren();
        if (!getContext().chapter) list.append(el("p", "empty-state", "选择已有正文的章节查看版本。"));
        else if (!versions.length) list.append(el("p", "empty-state", loadingVersions ? "正在读取版本……" : "本章暂无正文版本。完成生成后可在此查看与选择。"));
        for (const version of versions) {
          const active = selectedVersion === version.id;
          const item = button(`版本 ${version.number}${version.active ? " · 当前正式版" : ""}`, `chapter-version-button${active ? " selected" : ""}`, () => selectVersion(version.id));
          item.setAttribute("aria-pressed", active ? "true" : "false");
          item.title = [version.title, version.instruction, version.createdAt ? new Date(version.createdAt).toLocaleString() : ""].filter(Boolean).join(" · ");
          list.append(item);
        }
        const viewed = versions.find((version) => version.id === selectedVersion);
        $("chapter-version-status").textContent = viewed
          ? `正在查看版本 ${viewed.number}${viewed.active ? "，这是当前正式版。" : "，尚未覆盖正式版。"}${viewed.instruction ? ` 重写指令：${viewed.instruction}` : ""}`
          : "新重写内容默认保留为候选版本，选择后可阅读并采用。";
        syncDisabled();
      }

      function renderReading() {
        const version = versions.find((item) => item.id === selectedVersion);
        if (!version) return false;
        const context = getContext();
        $("reader-meta").textContent = `第 ${context.chapter} 章 · 版本 ${version.number}${version.active ? " · 当前正式版" : " · 候选版，尚未覆盖"} · ${(version.content || "").replace(/\s/g, "").length} 字`;
        const savedTitle = getChapters().find((chapter) => chapter.number === context.chapter)?.title || current?.base.title;
        $("reader-title").textContent = (version.active ? savedTitle || version.title : version.title || savedTitle) || `第 ${context.chapter} 章`;
        $("reader-content").textContent = version.content || "正文为空。";
        $("reader-content").classList.toggle("empty-state", !version.content);
        $("reader-notes-wrap").classList.add("hidden");
        return true;
      }

      function selectVersion(id) {
        const changed = selectedVersion !== id;
        selectedVersion = id;
        setEditorMode(false);
        renderVersions();
        renderReading();
        if (changed) $("chapter-reader-scroll").scrollTop = 0;
      }

      function revealControl(area, target) {
        const top = target.getBoundingClientRect?.().top, areaTop = area.getBoundingClientRect?.().top;
        const toolbarBottom = $("chapter-workspace-toolbar").getBoundingClientRect?.().bottom || 0;
        const headerBottom = document.querySelector?.(".topbar")?.getBoundingClientRect?.().bottom || 0;
        if (Number.isFinite(top) && Number.isFinite(areaTop)) area.scrollTop += top - Math.max(areaTop, toolbarBottom, headerBottom) - 8;
        target.focus?.({ preventScroll: true });
      }

      function setEditorMode(edit, focus = false) {
        editorMode = Boolean(edit);
        window.ainovelMobile?.showChapterRegion(editorMode ? "editor" : "reader");
        $("chapter-reader").classList.toggle("hidden", editorMode);
        $("chapter-editor").classList.toggle("hidden", !editorMode);
        for (const [id, selected] of [["chapter-read-tab", !editorMode], ["chapter-edit-tab", editorMode]]) {
          $(id).classList.toggle("selected", selected);
          $(id).setAttribute("aria-pressed", String(selected));
        }
        if (focus) {
          if (editorMode && !(current?.hasContent || current?.manual)) $("chapter-plan-editor").open = true;
          const target = $(editorMode ? ((current?.hasContent || current?.manual) ? "edit-chapter-content" : "edit-chapter-title") : "chapter-reader-scroll");
          if (editorMode) revealControl($("chapter-editor-fields"), target);
          else target.focus?.({ preventScroll: true });
        }
      }

      function jumpToRegion(region) {
        window.ainovelMobile?.showChapterRegion(region);
        if (region === "reader" || region === "editor") setEditorMode(region === "editor");
        const target = $({ directory: "chapter-directory", reader: "chapter-reader", editor: "chapter-editor", feedback: "chapter-actions" }[region]);
        if (!target) return;
        target.scrollIntoView?.({ block: region === "editor" ? "end" : "start", inline: "nearest", behavior: "instant" });
        // Keep the workbench toolbar below the app's sticky navigation. Scrolling
        // an inner input through every ancestor would pull that toolbar behind it.
        $("chapter-workspace").scrollIntoView?.({ block: "start", inline: "nearest", behavior: "instant" });
        if (region === "feedback") {
          target.scrollTop = 0;
          revealControl(target, $("chapter-feedback"));
        }
        else if (region === "directory") revealControl($("chapter-directory-tools"), $("chapter-search"));
        else setEditorMode(region === "editor", true);
      }

      async function loadVersions({ force = false } = {}) {
        const context = { ...getContext() }, token = generation;
        if (!context.directory || !context.chapter || loadingVersions || (!force && Date.now() - lastVersionsAt < 3000)) return;
        if (!context.manual && !canRewrite(getChapters().find((chapter) => chapter.number === context.chapter))) {
          versions = [];
          selectedVersion = currentHash = "";
          syncRequired = Boolean(projectSync.get(context.directory));
          lastVersionsAt = Date.now();
          renderVersions();
          return;
        }
        loadingVersions = true;
        if (!versions.length) renderVersions();
        try {
          const result = await api(`/api/chapters/${context.chapter}/versions?${new URLSearchParams({ directory: context.directory })}`);
          if (!valid(context, token)) return;
          if (result.directory !== context.directory || Number(result.chapter) !== context.chapter) throw new Error("返回的章节不匹配，请刷新重试。");
          versions = Array.isArray(result.versions) ? result.versions : [];
          const changedContent = currentHash && currentHash !== result.currentContentHash;
          currentHash = result.currentContentHash || "";
          if (result.syncRequired) projectSync.set(context.directory, true);
          syncRequired = Boolean(projectSync.get(context.directory));
          if (!versions.some((version) => version.id === selectedVersion)) selectedVersion = versions.find((version) => version.active)?.id || "";
          lastVersionsAt = Date.now();
          renderVersions();
          renderReading();
          if (changedContent) { editorNeedsRefresh = true; loadEditor({ force: true }); }
        } catch (error) {
          if (valid(context, token)) $("chapter-version-status").textContent = `版本读取失败：${error.message}`;
        } finally {
          if (valid(context, token)) { loadingVersions = false; syncDisabled(); }
        }
      }

      async function loadEditor({ force = false } = {}) {
        const context = { ...getContext() }, token = generation;
        if (!context.directory || !context.chapter || context.busy || loading || (current?.loaded && (!force || dirtyPlan() || dirtyContent()))) return;
        loading = true;
        if (!current?.loaded) message("正在读取可编辑内容……");
        try {
          const result = await api(`/api/chapters/${context.chapter}/edit?${new URLSearchParams({ directory: context.directory })}`);
          if (!valid(context, token)) return;
          if (result.directory !== context.directory || Number(result.chapter) !== context.chapter) throw new Error("返回的章节不匹配，请刷新重试。");
          if (current?.loaded && (dirtyPlan() || dirtyContent())) return;
          const value = { title: result.title || "", volumeTitle: result.volumeTitle || "", coreEvent: result.coreEvent || "", hook: result.hook || "", scenes: Array.isArray(result.scenes) ? result.scenes : [], content: result.content || "" };
          current = { loaded: true, value: structuredClone(value), base: structuredClone(value), revision: result.revision, hasContent: Boolean(result.hasContent), manual: Boolean(result.manual), hasPlan: result.hasPlan !== false };
          const backup = readBackup(key);
          if (backup) {
            if (backup.revision === result.revision) {
              current.value = structuredClone(backup.value);
              current.recovery = validBackup(backup.recovery) ? backup.recovery : null;
            } else current.recovery = backup;
          }
          drafts.set(key, current);
          editorNeedsRefresh = false;
          fillEditor();
          message(current.recovery ? "发现旧手机草稿，作品版本已更新；请使用上方按钮决定是否载入核对。" : backup && draftDirty(current)
            ? "已恢复上次未保存的手机草稿，核对后点击保存。正式内容尚未改变。"
            : current.manual ? "手动创作：可以修改标题、撰写或清空正文。正文每次保存都会保留历史版本。" : "编辑规划后需重新审查并确认，后续生成将使用保存的规划。正文保存会留存原版并采用新版本。");
        } catch (error) {
          if (valid(context, token)) { editorNeedsRefresh = true; message(`编辑内容读取失败：${error.message}`); }
        } finally {
          if (valid(context, token)) { loading = false; syncDisabled(); }
        }
      }

      function sync() {
        const context = getContext(), nextKey = contextKey(context);
        if (key !== nextKey || activeEpoch !== context.epoch) {
          key = nextKey;
          activeEpoch = context.epoch;
          generation++;
          loading = loadingVersions = saving = false;
          versions = [];
          selectedVersion = currentHash = "";
          syncRequired = Boolean(projectSync.get(context.directory));
          observedSource = "";
          observedBusy = Boolean(context.busy);
          editorNeedsRefresh = true;
          lastVersionsAt = 0;
          current = drafts.get(key) || null;
          fillEditor();
          $("chapter-reader-scroll").scrollTop = 0;
          $("chapter-editor-fields").scrollTop = 0;
          $("edit-chapter-content").scrollTop = 0;
          $("chapter-feedback").value = selection().instruction;
          $("rewrite-auto-apply").checked = selection().autoApply;
          $("chapter-editor-title").textContent = context.chapter ? `编辑第 ${context.chapter} 章的规划与正文` : "编辑章节规划与正文";
          message(current?.loaded ? (dirtyPlan() || dirtyContent() ? "本章有未保存修改，已保留在此工作台。" : "可编辑当前正式版与章节规划。") : "选择章节后可编辑规划、标题与正文。");
          renderVersions();
        }
        const chapter = getChapters().find((item) => item.number === context.chapter);
        const source = JSON.stringify([chapter?.title, chapter?.volumeTitle, chapter?.coreEvent, chapter?.hook, chapter?.scenes, chapter?.words, canRewrite(chapter), context.approved, context.fingerprint]);
        if (source !== observedSource || observedBusy && !context.busy) { editorNeedsRefresh = true; lastVersionsAt = 0; }
        observedSource = source;
        observedBusy = Boolean(context.busy);
        if (context.directory && context.chapter) {
          if (context.busy && !current?.loaded) message("本项目正在运行，任务结束或暂停后可编辑已保存的内容。");
          loadEditor({ force: editorNeedsRefresh });
          loadVersions();
        }
        syncDisabled();
      }

      function chapterRow(chapter, item) {
        if (getContext().manual) return item;
        const row = el("div", "chapter-select-row");
        const label = el("label", "chapter-rewrite-check");
        const checkbox = el("input");
        checkbox.type = "checkbox";
        checkbox.checked = selection().chapters.has(chapter.number);
        checkbox.disabled = !canRewrite(chapter);
        checkbox.setAttribute("aria-label", `勾选第 ${chapter.number} 章批量重写`);
        checkbox.addEventListener("change", () => {
          if (checkbox.checked && selection().chapters.size >= 100) { checkbox.checked = false; toast("每次最多重写 100 章，请先取消其他勾选。", "error"); return; }
          if (checkbox.checked) selection().chapters.add(chapter.number);
          else selection().chapters.delete(chapter.number);
          syncDisabled();
        });
        label.append(checkbox);
        row.append(label, item);
        return row;
      }

      async function saveEdits(savePlan) {
        const context = { ...getContext() }, token = generation;
        if (isBusy() || saving || !current?.loaded || (savePlan ? !dirtyPlan() : !(current.hasContent || current.manual) || !dirtyContent())) return;
        const draft = current;
        const submitted = structuredClone(draft.value);
        saving = true;
        setBusy("chapterEdit", true);
        message(savePlan ? "正在保存章节规划……" : "正在保存正文为新版本并采用……");
        syncDisabled();
        try {
          const result = await api(`/api/chapters/${context.chapter}/edit`, { method: "POST", body: {
            directory: context.directory, expectedRevision: draft.revision, savePlan, saveContent: !savePlan,
            ...submitted,
          } });
          if (result.directory !== context.directory || Number(result.chapter) !== context.chapter) throw new Error("保存返回的章节不匹配，请刷新后核对。");
          const saved = { title: result.title || "", volumeTitle: result.volumeTitle || "", coreEvent: result.coreEvent || "", hook: result.hook || "", scenes: result.scenes || [], content: result.content || "" };
          // A user can read another chapter while this request is pending.
          // Acknowledge the captured draft even if its form is no longer shown,
          // so returning to it does not reuse an already-consumed revision.
          draft.revision = result.revision;
          draft.hasContent = Boolean(result.hasContent);
          draft.manual = Boolean(result.manual);
          draft.hasPlan = result.hasPlan !== false;
          draft.base = structuredClone(saved);
          for (const field of savePlan ? ["title", "volumeTitle", "coreEvent", "hook", "scenes"] : ["content"]) {
            if (JSON.stringify(draft.value[field]) === JSON.stringify(submitted[field])) draft.value[field] = saved[field];
          }
          backupDraft(contextKey(context), draft);
          flushBackups();
          if (!result.manual && (result.syncRequired || !savePlan)) projectSync.set(context.directory, true);
          if (!valid(context, token)) return;
          fillEditor();
          syncRequired = Boolean(projectSync.get(context.directory));
          message(result.manual ? (savePlan ? "章节标题已保存。" : "正文已保存，原版保留在版本列表中。") : savePlan ? "规划已保存。请到规划审核重新审查并确认，再继续生成。" : "正文已保存并采用，原版保留在版本列表中。请同步修订后继续生成。");
          await refresh();
          if (valid(context, token)) {
            selectedVersion = "";
            await loadVersions({ force: true });
            toast(savePlan ? "章节规划已保存" : "正文新版本已保存并采用");
          }
        } catch (error) {
          if (valid(context, token)) message(`保存失败：${error.message}；未保存修改已保留。`);
        } finally {
          setBusy("chapterEdit", false);
          if (valid(context, token)) { saving = false; syncDisabled(); }
        }
      }

      async function rewrite() {
        const context = { ...getContext() }, token = generation;
        if (isBusy() || !context.approved) return;
        const numbers = selectedNumbers();
        if (!numbers.length && canRewrite(getChapters().find((chapter) => chapter.number === context.chapter))) numbers.push(context.chapter);
        const instruction = $("chapter-feedback").value.trim();
        if (!numbers.length) { toast("请勾选已完成章节，或打开要重写的已完成章节。", "error"); return; }
        if (numbers.length > 100) { toast("每次最多重写 100 章，请减少勾选后再提交。", "error"); return; }
        if (!instruction) { toast("请填写明确的重写指令。", "error"); $("chapter-feedback").focus(); return; }
        const autoApply = Boolean($("rewrite-auto-apply").checked);
        setBusy("chapterRewrite", true);
        $("chapter-feedback-status").textContent = "正在启动重写任务……";
        try {
          await api("/api/chapter-rewrites", { method: "POST", body: { directory: context.directory, chapters: numbers, instruction, autoApply } });
          if (!valid(context, token)) return;
          $("chapter-feedback-status").textContent = `已提交 ${numbers.length} 章重写${autoApply ? "，完成后自动采用新版本；原版仍保留。" : "，新稿将作为候选版本，原稿保持当前正式版。"}`;
          await refresh();
        } catch (error) {
          if (valid(context, token)) $("chapter-feedback-status").textContent = `重写启动失败：${error.message}`;
        } finally { setBusy("chapterRewrite", false); if (valid(context, token)) syncDisabled(); }
      }

      async function applyVersion() {
        const context = { ...getContext() }, token = generation;
        const version = versions.find((item) => item.id === selectedVersion);
        if (isBusy() || !version || version.active || !currentHash) return;
        setBusy("chapterVersion", true);
        $("chapter-version-status").textContent = `正在采用版本 ${version.number}，原版会保留……`;
        try {
          await api(`/api/chapters/${context.chapter}/versions/${encodeURIComponent(version.id)}/apply`, { method: "POST", body: { directory: context.directory, expectedContentHash: currentHash } });
          if (!valid(context, token)) return;
          $("chapter-version-status").textContent = context.manual ? `正在恢复版本 ${version.number} 的正文，完成后自动刷新。` : `版本 ${version.number} 已提交采用，完成后需同步修订再继续生成。`;
          await refresh();
        } catch (error) {
          if (valid(context, token)) $("chapter-version-status").textContent = `版本采用失败：${error.message}`;
        } finally { setBusy("chapterVersion", false); if (valid(context, token)) syncDisabled(); }
      }

      function onResult(result) {
        if (!result?.action) return;
        if (result.directory && result.directory !== getContext().directory) return;
        if (key !== contextKey(getContext()) || activeEpoch !== getContext().epoch) { sync(); return; }
        if (result.syncRequired) projectSync.set(getContext().directory, true);
        if (result.action === "sync" && !result.error) projectSync.delete(getContext().directory);
        syncRequired = Boolean(projectSync.get(getContext().directory));
        syncDisabled();
        if (result.error && ["chapter-rewrites", "apply-chapter-version"].includes(result.action)) {
          const id = result.action === "chapter-rewrites" ? "chapter-feedback-status" : "chapter-version-status";
          $(id).textContent = `任务未完成：${result.error}；已保存的版本可在列表中查看。`;
        } else if (result.action === "chapter-rewrites") $("chapter-feedback-status").textContent = "重写任务已完成，可逐章切换版本查看或采用。";
        lastVersionsAt = 0;
        loadVersions({ force: true });
        loadEditor({ force: true });
      }

      function bindEvents() {
        $("chapter-read-tab").addEventListener("click", () => setEditorMode(false));
        $("chapter-edit-tab").addEventListener("click", () => setEditorMode(true));
        for (const region of ["directory", "reader", "editor", "feedback"]) $("chapter-jump-" + region).addEventListener("click", () => jumpToRegion(region));
        for (const [field, id] of Object.entries(fields)) $(id).addEventListener("input", () => {
          if (!current?.loaded || saving) return;
          current.value[field] = field === "scenes" ? $(id).value.split("\n").map((line) => line.trim()).filter(Boolean) : $(id).value;
          backupDraft();
          message(androidDrafts ? "有未保存修改，手机会备份草稿；正式写入作品请点击保存。" : "有未保存修改；切换章节或项目时会在此工作台保留草稿。");
          syncDisabled();
        });
        $("save-chapter-plan").addEventListener("click", () => saveEdits(true));
        $("save-chapter-content").addEventListener("click", () => saveEdits(false));
        $("reset-chapter-edits").addEventListener("click", async () => {
          if (!current?.loaded || saving) return;
          current.value = structuredClone(current.base);
          backupDraft();
          flushBackups();
          fillEditor();
          message("已恢复为最近读取或保存的内容。");
          syncDisabled();
          await loadEditor({ force: true });
        });
        $("reload-chapter-versions").addEventListener("click", () => loadVersions({ force: true }));
        $("apply-chapter-version").addEventListener("click", applyVersion);
        $("sync-chapter-revisions").addEventListener("click", syncRevisions);
        $("select-rewrite-chapters").addEventListener("click", () => { for (const chapter of getChapters().filter(canRewrite)) { if (selection().chapters.size >= 100) break; selection().chapters.add(chapter.number); } repaint(); });
        $("clear-rewrite-chapters").addEventListener("click", () => { selection().chapters.clear(); repaint(); });
        $("chapter-feedback").addEventListener("input", () => { selection().instruction = $("chapter-feedback").value; });
        $("rewrite-auto-apply").addEventListener("change", () => { selection().autoApply = Boolean($("rewrite-auto-apply").checked); });
        $("restore-local-chapter-draft").addEventListener("click", () => resolveBackup(true));
        $("discard-local-chapter-draft").addEventListener("click", () => resolveBackup(false));
        if (androidDrafts) {
          window.addEventListener("pagehide", flushBackups);
          window.addEventListener("beforeunload", flushBackups);
          document.addEventListener("visibilitychange", () => { if (document.hidden) flushBackups(); });
        }
      }

      return { sync, syncDisabled, chapterRow, renderReading, rewrite, onResult, bindEvents, loadEditor, loadVersions, saveEdits, applyVersion, selectVersion, setEditorMode, jumpToRegion };
    },
  };
})();
