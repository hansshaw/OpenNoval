(() => {
  "use strict";

  window.NovelJobs = {
    create({ api, el, button, getProjects, getConfig, toast, refresh }) {
      const $ = (id) => document.getElementById(id);
      let jobs = [];
      let events = [];
      let pending = false;
      let source = null;
      let limits = null;
      let limitsDirty = false;
      let limitsSaving = false;
      let limitsVersion = 0;
      let renderedLimitsKey = "";
      const providerInputs = new Map();
      const active = (job) => ["queued", "running", "canceling"].includes(job.status);
      const statusLabel = { queued: "已排队", running: "正在创作", canceling: "正在停止", succeeded: "已完成", failed: "失败", canceled: "已取消", aborted: "进程中断" };

      function validLimit(value, maximum) {
        return Number.isInteger(value) && value >= 1 && value <= maximum;
      }

      function readLimits(result) {
        const value = result?.limits || result;
        const maximum = value?.maxSupported || 4096;
        if (!validLimit(maximum, 4096) || !validLimit(value?.maxConcurrent, maximum) || !validLimit(value?.providerLimit, maximum)) {
          throw new Error("服务端未返回有效并发配置，请刷新重试。");
        }
        const overrides = value.providerLimits || {};
        if (typeof overrides !== "object" || Array.isArray(overrides) || Object.values(overrides).some((limit) => !validLimit(limit, maximum))) {
          throw new Error("服务端返回的连接并发配置无效，请刷新重试。");
        }
        return { ...value, maxSupported: maximum, providerLimits: overrides };
      }

      function syncLimitsControls() {
        const disabled = !limits || !limits.editable || limitsSaving;
        $("job-max-concurrent").disabled = disabled;
        $("job-provider-limit").disabled = disabled;
        for (const input of providerInputs.values()) input.disabled = disabled;
        $("save-job-limits").disabled = disabled;
        $("reset-job-limits").disabled = disabled || !limitsDirty;
      }

      function limitsFormKey() {
        return JSON.stringify([
          limits.maxConcurrent, limits.providerLimit, limits.maxSupported, limits.editable,
          Object.entries(limits.providerLimits).sort(([a], [b]) => a.localeCompare(b)),
          (getConfig()?.providers || []).map((provider) => provider.name),
        ]);
      }

      function renderLimitsForm() {
        $("job-max-concurrent").value = limits.maxConcurrent;
        $("job-provider-limit").value = limits.providerLimit;
        $("job-max-concurrent").max = limits.maxSupported;
        $("job-provider-limit").max = limits.maxSupported;
        const list = $("job-provider-limits");
        list.replaceChildren();
        providerInputs.clear();
        for (const provider of getConfig()?.providers || []) {
          const label = el("label", "", provider.name);
          const input = el("input");
          input.type = "number";
          input.min = "1";
          input.max = String(limits.maxSupported);
          input.step = "1";
          input.placeholder = `沿用默认（${limits.providerLimit}）`;
          input.dataset.limitProvider = provider.name;
          input.value = Object.hasOwn(limits.providerLimits, provider.name) ? limits.providerLimits[provider.name] : "";
          label.append(input);
          list.append(label);
          providerInputs.set(provider.name, input);
        }
        if (!providerInputs.size) list.append(el("p", "hint", "保存模型连接后，可在这里分别设置并发上限。"));
        renderedLimitsKey = limitsFormKey();
        syncLimitsControls();
      }

      function updateLimits(value) {
        limits = readLimits(value);
        const count = Object.keys(limits.providerLimits).length;
        $("job-limits").textContent = `最多同时运行 ${limits.maxConcurrent} 个项目，每个连接默认同时运行 ${limits.providerLimit} 个任务${count ? `，${count} 个连接有单独上限` : ""}。同一项目按顺序执行；前台创作与后台创作互斥。`;
        if (!limitsDirty && !limitsSaving && renderedLimitsKey !== limitsFormKey()) renderLimitsForm();
        if (!limits.editable) $("job-limits-status").textContent = "当前服务暂不支持修改并发配置。";
        else if (!limitsDirty && !limitsSaving && ($("job-limits-status").textContent === "正在加载并发配置…" || $("job-limits-status").textContent.startsWith("并发配置加载失败"))) $("job-limits-status").textContent = `支持 1–${limits.maxSupported}，可在后台任务运行时保存。`;
      }

      async function loadLimits() {
        const version = limitsVersion;
        try {
          const result = await api("/api/jobs/limits");
          if (version === limitsVersion && !limitsSaving) updateLimits(result);
        } catch (error) {
          if (version === limitsVersion) $("job-limits-status").textContent = `并发配置加载失败：${error.message}`;
        }
      }

      function editLimits() {
        if (limitsSaving) return;
        limitsDirty = true;
        $("job-limits-status").textContent = "有未保存的更改。";
        syncLimitsControls();
      }

      async function saveLimits(event) {
        event.preventDefault();
        if (!limits?.editable || limitsSaving) return;
        const maximum = limits.maxSupported;
        const maxConcurrent = Number($("job-max-concurrent").value);
        const providerLimit = Number($("job-provider-limit").value);
        const providerLimits = Object.create(null);
        try {
          if (!validLimit(maxConcurrent, maximum) || !validLimit(providerLimit, maximum)) throw new Error(`并发上限须为 1–${maximum} 的整数。`);
          for (const [name, input] of providerInputs) {
            if (!input.value.trim()) continue;
            const value = Number(input.value);
            if (!validLimit(value, maximum)) throw new Error(`${name} 的并发上限须为 1–${maximum} 的整数，或留空沿用默认。`);
            providerLimits[name] = value;
          }
        } catch (error) { $("job-limits-status").textContent = error.message; return; }
        limitsSaving = true;
        limitsVersion++;
        syncLimitsControls();
        $("job-limits-status").textContent = "正在保存并发设置…";
        try {
          const result = readLimits(await api("/api/jobs/limits", { method: "POST", body: { maxConcurrent, providerLimit, providerLimits } }));
          limitsDirty = false;
          limitsSaving = false;
          limitsVersion++;
          updateLimits(result);
          $("job-limits-status").textContent = "并发设置已保存。提高上限会立即补派，降低上限会等待在途任务结束。";
          toast("并发设置已保存");
        } catch (error) {
          $("job-limits-status").textContent = `保存失败：${error.message}`;
        } finally {
          limitsSaving = false;
          syncLimitsControls();
        }
      }

      function render() {
        const list = $("job-list");
        const expanded = new Set(Array.from(list.querySelectorAll("details[open]"), (detail) => detail.dataset.jobId));
        const projectsByPath = new Map(getProjects().map((project) => [project.path, project]));
        const eventsByJob = new Map();
        for (const event of events) {
          if (!eventsByJob.has(event.job_id)) eventsByJob.set(event.job_id, []);
          eventsByJob.get(event.job_id).push(event);
        }
        list.replaceChildren();
        if (!jobs.length) list.append(el("p", "empty-state", "暂无后台任务。先在项目中心为非当前项目安排正文批次或恢复任务。"));
        for (const job of jobs.slice().reverse()) {
          const card = el("article", "panel job-card");
          const project = projectsByPath.get(job.project_id);
          const heading = el("div", "panel-heading");
          heading.append(el("h3", "", project?.title || job.payload?.project_name || job.project_id.split(/[\\/]/).pop()), el("span", "soft-badge", statusLabel[job.status] || job.status));
          card.append(heading);
          card.append(el("p", "hint", `${job.kind === "batch" ? "正文批次" : "恢复创作"} · ${job.provider_id} / ${job.payload?.model || "已绑定模型"} · 成本 $${Number(job.cost_usd || 0).toFixed(4)}`));
          if (job.budget_usd) card.append(el("p", "hint", `本任务预算 $${Number(job.budget_usd).toFixed(2)}，已产生的请求费用仍会记账。`));
          if (job.error) card.append(el("p", "form-status", job.error));
          const ownEvents = (eventsByJob.get(job.id) || []).slice(-12);
          const details = el("details", "raw-details");
          details.dataset.jobId = job.id;
          details.open = expanded.has(job.id);
          details.append(el("summary", "", "查看此任务活动"));
          for (const event of ownEvents) details.append(el("p", "hint", `${event.kind} · ${event.message || ""}`));
          card.append(details);
          if (active(job)) {
            const cancel = button("停止此任务", "text-button danger-text", () => cancelJob(job));
            cancel.disabled = pending || job.status === "canceling";
            card.append(cancel);
          }
          list.append(card);
        }
      }

      async function load({ quiet = false } = {}) {
        const version = limitsVersion;
        try {
          const result = await api("/api/jobs");
          jobs = result.jobs || [];
          const page = result.events || {};
          events = page.events || page.Events || [];
          if (version === limitsVersion && !limitsSaving) updateLimits(result.limits);
          $("job-status").textContent = "";
          render();
        } catch (error) {
          $("job-status").textContent = `后台任务加载失败：${error.message}`;
          if (!limits) $("job-limits-status").textContent = "并发配置加载失败，请刷新重试。";
        }
      }

      function open(project) {
        source = project;
        $("job-source").textContent = `项目：${project.title || "未命名项目"}`;
        $("job-form").reset();
        const select = $("job-model");
        select.replaceChildren();
        const config = getConfig() || {};
        for (const provider of config.providers || []) {
          for (const model of provider.models || []) {
            const choice = el("option", "", `${provider.name} / ${model}`);
            choice.value = JSON.stringify([provider.name, model]);
            choice.selected = provider.name === config.provider && model === config.model;
            select.append(choice);
          }
        }
        $("job-form-status").textContent = "";
        $("job-dialog").showModal();
      }

      async function submit(event) {
        event.preventDefault();
        if (!source || pending) return;
        const selection = $("job-model").value;
        if (!selection) { $("job-form-status").textContent = "请先保存模型连接。"; return; }
        const [provider, model] = JSON.parse(selection);
        pending = true;
        $("submit-job").disabled = true;
        try {
          await api("/api/jobs", { method: "POST", body: {
            directory: source.path, kind: $("job-kind").value, provider, model,
            count: Number($("job-count").value), budgetUSD: Number($("job-budget").value || 0),
          } });
          $("job-dialog").close();
          toast("后台任务已安排，可在任务中心查看进度");
          await Promise.all([load(), refresh()]);
        } catch (error) { $("job-form-status").textContent = error.message; }
        finally { pending = false; $("submit-job").disabled = false; }
      }

      async function cancelJob(job) {
        if (pending) return;
        pending = true;
        try {
          await api(`/api/jobs/${encodeURIComponent(job.id)}/cancel`, { method: "POST", body: {} });
          await load();
          toast("已发送停止请求，正在等待该项目安全退出");
        } catch (error) { $("job-status").textContent = error.message; }
        finally { pending = false; render(); }
      }

      syncLimitsControls();
      $("refresh-jobs").addEventListener("click", () => load());
      $("job-limits-settings").addEventListener("toggle", () => { if ($("job-limits-settings").open) loadLimits(); });
      $("job-limits-form").addEventListener("input", editLimits);
      $("job-limits-form").addEventListener("submit", saveLimits);
      $("reset-job-limits").addEventListener("click", () => {
        if (!limits || limitsSaving) return;
        limitsDirty = false;
        renderLimitsForm();
        $("job-limits-status").textContent = "已恢复服务器当前保存值。";
      });
      $("job-form").addEventListener("submit", submit);
      for (const id of ["close-job", "cancel-job-dialog"]) $(id).addEventListener("click", () => $("job-dialog").close());
      return { load, open, hasActive: (directory) => jobs.some((job) => job.project_id === directory && active(job)) };
    },
  };
})();
