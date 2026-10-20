(() => {
  "use strict";

  window.NovelJobs = {
    create({ api, el, button, getProjects, getConfig, toast, refresh }) {
      const $ = (id) => document.getElementById(id);
      let jobs = [];
      let events = [];
      let pending = false;
      let source = null;
      const active = (job) => ["queued", "running", "canceling"].includes(job.status);
      const statusLabel = { queued: "已排队", running: "正在创作", canceling: "正在停止", succeeded: "已完成", failed: "失败", canceled: "已取消", aborted: "进程中断" };

      function render() {
        const list = $("job-list");
        const expanded = new Set(Array.from(list.querySelectorAll("details[open]"), (detail) => detail.dataset.jobId));
        list.replaceChildren();
        if (!jobs.length) list.append(el("p", "empty-state", "暂无后台任务。先在项目中心为非当前项目安排正文批次或恢复任务。"));
        for (const job of jobs.slice().reverse()) {
          const card = el("article", "panel job-card");
          const project = getProjects().find((item) => item.path === job.project_id);
          const heading = el("div", "panel-heading");
          heading.append(el("h3", "", project?.title || job.project_id.split(/[\\/]/).pop()), el("span", "soft-badge", statusLabel[job.status] || job.status));
          card.append(heading);
          card.append(el("p", "hint", `${job.kind === "batch" ? "正文批次" : "恢复创作"} · ${job.provider_id} / ${job.payload?.model || "已绑定模型"} · 成本 $${Number(job.cost_usd || 0).toFixed(4)}`));
          if (job.budget_usd) card.append(el("p", "hint", `本任务预算 $${Number(job.budget_usd).toFixed(2)}，已产生的请求费用仍会记账。`));
          if (job.error) card.append(el("p", "form-status", job.error));
          const ownEvents = events.filter((event) => event.job_id === job.id).slice(-12);
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
        try {
          const result = await api("/api/jobs");
          jobs = result.jobs || [];
          const page = result.events || {};
          events = page.events || page.Events || [];
          const limits = result.limits || {};
          $("job-limits").textContent = `最多同时运行 ${limits.maxConcurrent || 2} 个项目，每个连接同时运行 ${limits.providerLimit || 1} 个任务。同一项目按顺序执行；前台创作需先暂停。`;
          render();
        } catch (error) { if (!quiet) $("job-status").textContent = error.message; }
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

      $("refresh-jobs").addEventListener("click", () => load());
      $("job-form").addEventListener("submit", submit);
      for (const id of ["close-job", "cancel-job-dialog"]) $(id).addEventListener("click", () => $("job-dialog").close());
      return { load, open, hasActive: (directory) => jobs.some((job) => job.project_id === directory && active(job)) };
    },
  };
})();
