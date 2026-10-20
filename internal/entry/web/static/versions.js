(() => {
  "use strict";

  window.NovelVersions = {
    create({ api, el, button, getDirectory, isBusy, setBusy, refresh, toast }) {
      const $ = (id) => document.getElementById(id);
      let directory = "";
      let versions = [];
      let preview = null;
      let revision = 0;

      function reset() {
        revision += 1;
        directory = getDirectory();
        versions = [];
        preview = null;
        $("version-status").textContent = "";
        $("version-preview-title").textContent = "恢复差异预览";
        $("version-diff").replaceChildren(el("p", "empty-state", "选择一个版本查看差异。"));
        render();
      }

      function syncDisabled() {
        const busy = isBusy();
        for (const control of document.querySelectorAll("#view-versions button")) {
          control.disabled = busy || (control.id === "restore-version" && !preview);
        }
      }

      function render() {
        const list = $("version-list");
        list.replaceChildren();
        if (!versions.length) list.append(el("p", "empty-state", "暂无版本。暂停创作后保存第一个版本。"));
        for (const version of versions) {
          const row = el("article", "version-row");
          const copy = el("div", "source-copy");
          const files = version.files || [];
          copy.append(el("strong", "", version.name), el("small", "", `${new Date(version.createdAt).toLocaleString()} · ${files.length} 个文件`));
          row.append(copy, button("查看差异", "text-button", () => compare(version)));
          list.append(row);
        }
        syncDisabled();
      }

      async function load() {
        const current = getDirectory();
        if (!current) return;
        if (directory !== current) reset();
        const ticket = ++revision;
        try {
          const result = await api(`/api/versions?${new URLSearchParams({ directory: current })}`);
          if (ticket !== revision || current !== getDirectory()) return;
          versions = result.versions || [];
          render();
        } catch (error) {
          if (ticket === revision) $("version-status").textContent = error.message;
        }
      }

      async function create(event) {
        event.preventDefault();
        if (isBusy()) return;
        const current = getDirectory();
        const name = $("version-name").value.trim();
        if (!name) return;
        setBusy("versions", true);
        $("version-status").textContent = "正在校验并保存版本……";
        try {
          await api("/api/versions", { method: "POST", body: { directory: current, name } });
          if (current !== getDirectory()) return;
          $("version-name").value = "";
          $("version-status").textContent = "版本已保存。";
          await load();
        } catch (error) {
          if (current === getDirectory()) $("version-status").textContent = error.message;
        } finally { setBusy("versions", false); syncDisabled(); }
      }

      async function compare(version) {
        if (isBusy()) return;
        const current = getDirectory();
        setBusy("versions", true);
        preview = null;
        $("version-status").textContent = "正在校验版本并比较差异……";
        try {
          const result = await api(`/api/versions/${encodeURIComponent(version.id)}/compare?${new URLSearchParams({ directory: current })}`);
          if (current !== getDirectory()) return;
          preview = { ...result, id: version.id, directory: current };
          $("version-preview-title").textContent = `恢复到：${version.name}`;
          const list = $("version-diff");
          list.replaceChildren();
          for (const change of result.changes || []) {
            const detail = el("details", "version-change");
            const label = { added: "新增", modified: "修改", deleted: "删除" }[change.kind] || change.kind;
            detail.append(el("summary", "", `${label} · ${change.path}`));
            detail.append(el("pre", "json-pre", change.diff || "文件内容将按所选版本恢复。"));
            if (change.truncated) detail.append(el("p", "hint", "此文件差异较长，显示已截断。"));
            list.append(detail);
          }
          if (!(result.changes || []).length) list.append(el("p", "empty-state", "当前内容与此版本一致。"));
          $("version-status").textContent = result.truncated ? "预览已截断；恢复仍会校验全部文件。" : `已校验 ${(result.changes || []).length} 项差异。`;
        } catch (error) { $("version-status").textContent = error.message; }
        finally { setBusy("versions", false); syncDisabled(); }
      }

      async function restore() {
        if (isBusy() || !preview) return;
        const selected = preview;
        if (selected.directory !== getDirectory()) { reset(); return; }
        if (!confirm("确认按预览恢复当前项目？系统会先保存恢复前备份，旧批次运行许可将被清除。")) return;
        setBusy("versions", true);
        $("version-status").textContent = "正在备份当前内容并恢复版本……";
        try {
          const result = await api("/api/versions/restore", { method: "POST", body: {
            directory: selected.directory, id: selected.id, fingerprint: selected.fingerprint,
          } });
          preview = null;
          await refresh();
          await load();
          $("version-status").textContent = result.error || "版本已恢复，恢复前备份可在版本列表中查看。";
          toast(result.error || "版本已恢复", result.error ? "error" : "");
        } catch (error) {
          preview = null;
          $("version-status").textContent = `${error.message}；请重新预览后重试。`;
        } finally { setBusy("versions", false); syncDisabled(); }
      }

      $("version-create-form").addEventListener("submit", create);
      $("refresh-versions").addEventListener("click", load);
      $("restore-version").addEventListener("click", restore);
      return { load, reset, syncDisabled };
    },
  };
})();
