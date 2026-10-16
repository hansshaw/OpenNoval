(() => {
  const $ = (s) => document.querySelector(s),
    $$ = (s) => [...document.querySelectorAll(s)];
  const state = {
    data: null,
    config: null,
    events: [],
    chapters: [],
    selected: 0,
    polling: false,
  };
  // Host DTOs use exported Go field names; keep the browser view model consistent.
  const normalize = (v) =>
    Array.isArray(v)
      ? v.map(normalize)
      : v && typeof v === "object"
        ? Object.fromEntries(
            Object.entries(v).map(([k, x]) => [
              k === "ID" ? "id" : k[0].toLowerCase() + k.slice(1),
              normalize(x),
            ]),
          )
        : v;
  const esc = (v) => String(v ?? "");
  const post = async (url, body) => {
    if (url === "/api/action" || url === "/api/project") body = { ...body, directory: state.data?.directory };
    const r = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const d = await r.json().catch(() => ({ error: "服务器响应无效" }));
    if (!r.ok) throw Error(d.error || "请求失败");
    return d;
  };
  const showAlert = (msg) => {
    const el = $("#alert");
    el.textContent = msg || "";
    el.classList.toggle("hidden", !msg);
    if (msg) {
      let t = $(".toast");
      if (!t) {
        t = document.createElement("div");
        t.className = "toast";
        t.setAttribute("role", "status");
        document.body.append(t);
      }
      t.textContent = msg;
      clearTimeout(state.toastTimer);
      state.toastTimer = setTimeout(() => t.remove(), 6000);
    }
  };
  const formatNum = (n) => Number(n || 0).toLocaleString("zh-CN");
  const inputIDs = ["prompt", "intervention", "plan-feedback", "chapter-feedback", "batch-count"];
  function saveInputs(dir) {
    if (!dir) return;
    try { localStorage.setItem("ainovel-inputs:" + dir, JSON.stringify(Object.fromEntries(inputIDs.map(id => [id, $("#"+id).value])))); } catch (_) {}
  }
  function restoreInputs(dir) {
    let inputs = {};
    try { inputs = JSON.parse(localStorage.getItem("ainovel-inputs:"+dir) || "{}"); } catch (_) {}
    inputIDs.forEach(id => $("#"+id).value = inputs[id] ?? (id === "batch-count" ? "1" : ""));
  }
  inputIDs.forEach(id => $("#"+id).addEventListener("input", () => saveInputs(state.data?.directory)));
  const stageNames = {new:"先生成故事规划",planning:"正在准备大纲与目录","plan-review":"等待你确认大纲与目录",ready:"规划已确认，可以生成正文",writing:"正文批次进行中","chapter-review":"最新章节等待反馈与验收",complete:"项目正文已完成"};
  function renderWorkflow(d) {
    const w=d.workflow || {}, idle=!d.busy && !d.stopping && !d.snapshot?.isRunning;
    $("#workflow-status").textContent = stageNames[w.stage] || "等待项目状态";
    const descriptions = {new:"填写需求后先生成大纲，正文会等待你的明确确认。",planning:"完成基础规划和审查后，将自动停在大纲确认阶段。","plan-review":"先阅读完整故事大纲和目录。可以多次提交修改意见，满意后确认当前版本。",ready:"在章节阅读页设置本批次章节数量。每章生成完成后都会暂停，等待你反馈和验收。",writing:"可以暂停并切换项目。恢复后沿用原有批次和进度，不会重新建书。","chapter-review":`第 ${w.latestChapter || 0} 章已完成。阅读后提交修改意见，或确认本章继续。`,complete:"你可以继续阅读、导出，或新建另一创作项目。"};
    $("#workflow-description").textContent = w.error || descriptions[w.stage] || "";
    const step = {new:0,planning:0,"plan-review":1,ready:2,writing:2,"chapter-review":3,complete:3}[w.stage] ?? 0;
    $$(".workflow-steps span").forEach((el,i) => el.classList.toggle("active",i===step));
    $("#open-latest").disabled = !w.latestChapter;
    $("#plan-status").textContent = w.approved ? "已确认当前版本" : w.canApprove ? "等待用户确认" : "规划尚未就绪";
    if (document.activeElement !== $("#batch-count") && !state.batchRestored) {
      $("#batch-count").value = w.batchSize || 1;
      state.batchRestored = true;
    }
    $("#chapter-feedback-title").textContent = w.latestChapter ? `第 ${w.latestChapter} 章 · 反馈与验收` : "章节反馈与验收";
    $("#batch-status").textContent = w.batchEnd ? `本批次至第 ${w.batchEnd} 章 · 已验收至 ${w.acceptedChapter || 0} 章` : "尚未开始正文批次";
    $("#accept-chapter").textContent = w.latestChapter < w.batchEnd ? "确认本章，继续下一章 →" : "确认本章，结束本批次";
    $("#next-chapter").textContent = w.stage === "writing" ? "重新设置正文批次 →" : "开始正文批次 →";
    if (!state.historyEl) {
      const details=document.createElement("details"); details.className="panel feedback-history";
      const summary=document.createElement("summary"); summary.textContent="当前项目的反馈记录";
      state.historyEl=document.createElement("div"); details.append(summary,state.historyEl); $(".content").append(details);
    }
    state.historyEl.replaceChildren();
    (w.feedback || []).slice().reverse().forEach(f => {
      const p=document.createElement("p"); p.textContent=`${new Date(f.time).toLocaleString("zh-CN")} · ${f.kind==="planning"?"规划修改":`第 ${f.chapter} 章反馈`}\n${f.text}`; state.historyEl.append(p);
    });
    if (!w.feedback?.length) state.historyEl.textContent="暂无反馈记录";
  }
  async function loadPlanning() {
    const dir=state.data?.directory;
    if (!dir || state.loadingPlan) return;
    state.loadingPlan=true;
    try {
      const r=await fetch("/api/planning?directory="+encodeURIComponent(dir));
      const d=normalize(await r.json());
      if (state.data?.directory!==dir) return;
      if (!r.ok) throw Error(d.error);
      state.planDirectory=dir; state.planFingerprint=d.workflow?.fingerprint || "";
      const signature=JSON.stringify([dir,d.premise,d.outline,d.layered,d.book]);
      if (signature === state.planSignature) { updateControls(); return; }
      state.planSignature=signature;
      $("#plan-title").textContent=d.book?.title || state.data?.project?.name || "故事规划";
      $("#plan-premise").textContent=d.premise || "尚未生成故事大纲";
      $("#plan-layered").textContent=d.layered?.length ? JSON.stringify(d.layered,null,2) : "当前使用平面章节规划";
      const box=$("#plan-outline"); box.replaceChildren();
      (d.outline || []).forEach(x => {
        const details=document.createElement("details"); details.open=true;
        const title=document.createElement("summary"); title.textContent=`第 ${x.chapter} 章 · ${x.title || "待定标题"}`;
        const text=document.createElement("p"); text.textContent=x.core_event || x.coreEvent || "";
        const more=document.createElement("pre"); more.className="plan-prose"; more.textContent=JSON.stringify(x,null,2);
        const extra=document.createElement("details"), label=document.createElement("summary"); label.textContent="查看场景与详细安排"; extra.append(label,more);
        details.append(title,text,extra); box.append(details);
      });
      if (!d.outline?.length) box.textContent="目录尚未生成";
      updateControls();
    } catch(e) { showAlert(e.message); } finally { state.loadingPlan=false; }
  }
  async function runAction(body) {
    try { await post("/api/action",body); await load(); } catch(e) { showAlert(e.message); }
  }
  function openProjectModal(rename) {
    state.renamingProject=rename;
    $("#project-modal-title").textContent=rename ? "重命名当前项目" : "新建创作项目";
    $("#project-name").value=rename ? state.data?.project?.name || state.data?.snapshot?.bookTitle || "" : "";
    $("#project-status").textContent="";
    $("#project-modal").classList.remove("hidden"); $("#project-name").focus();
  }
  function closeProjectModal() { $("#project-modal").classList.add("hidden"); }
  $("#close-project-modal").onclick=closeProjectModal;
  $("#project-modal .modal-backdrop").onclick=closeProjectModal;
  $("#project-form").onsubmit=async e => {
    e.preventDefault(); const button=e.target.querySelector("button[type=submit]"); button.disabled=true;
    try {
      const name=$("#project-name").value.trim();
      await post(state.renamingProject ? "/api/project" : "/api/workspaces",state.renamingProject ? {name} : {name,create:true});
      closeProjectModal(); await load();
    } catch(err) { $("#project-status").textContent=err.message; } finally { button.disabled=false; }
  };
  $("#open-planning").onclick=()=>setView("planning");
  $("#open-latest").onclick=()=>{setView("chapters"); readChapter(state.data.workflow.latestChapter);};
  $("#reload-planning").onclick=loadPlanning;
  $("#approve-plan").onclick=()=>runAction({action:"approve-plan",fingerprint:state.planFingerprint});
  $("#revise-plan").onclick=()=>runAction({action:"revise-plan",text:$("#plan-feedback").value.trim()});
  $("#accept-chapter").onclick=()=>runAction({action:"accept-chapter",chapter:state.data?.workflow?.latestChapter});
  $("#send-chapter-feedback").onclick=()=>runAction({action:"chapter-feedback",chapter:state.data?.workflow?.latestChapter,text:$("#chapter-feedback").value.trim()});
  function configPayload(form) {
    const p = Object.fromEntries(new FormData(form));
    if (p.type !== "openai") p.api = "";
    return p;
  }
  const time = (v) =>
    v
      ? new Date(v).toLocaleTimeString("zh-CN", {
          hour: "2-digit",
          minute: "2-digit",
        })
      : "";
  function setView(view) {
    $$(".view").forEach((x) =>
      x.classList.toggle("hidden", x.id !== `view-${view}`),
    );
    $$(".nav-item").forEach((x) =>
      x.classList.toggle("active", x.dataset.view === view),
    );
    if (view === "chapters") renderChapters();
    if (view === "activity") renderActivity();
    if (view === "planning") loadPlanning();
  }
  $$(".nav-item").forEach((b) =>
    b.addEventListener("click", () => setView(b.dataset.view)),
  );
  $$('[data-view]:not(.nav-item)').forEach((b) =>
    b.addEventListener("click", () => setView(b.dataset.view)),
  );
  function renderConfig() {
    const c = state.config;
    if (!c) return;
    const p =
      c.providers?.find((x) => x.name === c.provider) || c.providers?.[0];
    $("#metric-model").textContent = c.model || "未配置";
    $("#metric-provider").textContent = p?.name || "请先连接模型";
    if (p) {
      const f = $("#config-form");
      f.elements.name.value = p.name || "";
      f.elements.type.value = p.type || "openai";
      f.elements.base_url.value = p.base_url || "";
      f.elements.api.value = p.api || "chat";
      f.elements.model.value = c.model || p.models?.[0] || "";
    }
  }
  function renderState(d) {
    state.data = d;
    const snap = d.snapshot || {},
      ready = d.ready;
    const conn = $("#connection");
    conn.className = "connection " + (ready ? "ready" : "error");
    conn.lastChild.textContent = ready ? " 工作台就绪" : " 等待连接";
    $("#current-book strong").textContent =
      d.project?.name || snap.bookTitle || d.directory?.split(/[\\/]/).pop() || "当前作品";
    $("#current-book small").textContent = d.directory || "作品目录";
    $("#runtime-pill").textContent = d.stopping
      ? "正在暂停"
      : d.busy
        ? "正在处理"
        : ready
          ? snap.isRunning
            ? "创作进行中"
            : "准备就绪"
          : "等待配置";
    $("#hero-title").textContent = snap.bookTitle || (ready ? "准备开始你的故事" : "等待连接创作引擎");
    $("#hero-subtitle").textContent =
      d.error ||
      (ready
        ? "创作引擎已就绪，你可以从一个念头开始。"
        : "在设置中填写模型服务，连接后即可开始。");
    $("#metric-chapter").textContent = snap.currentChapter
      ? `第 ${snap.currentChapter} 章`
      : "—";
    $("#metric-progress").textContent = snap.totalChapters
      ? `${snap.completedCount || 0} / ${snap.totalChapters} 章已完成`
      : "等待创作";
    $("#metric-words").textContent = formatNum(snap.totalWordCount);
    $("#metric-cost").textContent =
      `$${Number(snap.totalCostUSD || 0).toFixed(2)}`;
    $("#metric-tokens").textContent =
      `输入 ${formatNum(snap.totalInputTokens)} · 输出 ${formatNum(snap.totalOutputTokens)}`;
    $("#start").disabled = !!(d.busy || d.stopping || !ready);
    renderAgents(snap.agents || []);
    renderOutline(snap.outline || []);
    renderWorkspaces();
    renderWorkflow(d);
    if (d.error && !ready && state.lastHostError!==d.error) showAlert(d.error);
    state.lastHostError=d.error;
  }
  function renderAgents(list) {
    const box = $("#agents");
    $("#agent-count").textContent = `${list.length} 位在线`;
    box.replaceChildren();
    if (!list.length) {
      box.innerHTML =
        '<div class="empty-small">开始创作后，这里会显示各 Agent 的执行状态。</div>';
      return;
    }
    list.forEach((a) => {
      const el = document.createElement("div");
      el.className = "agent";
      const avatar = document.createElement("span");
      avatar.className = "agent-avatar";
      avatar.textContent = esc(a.name).slice(0, 1) || "·";
      const info = document.createElement("div");
      info.className = "agent-info";
      const strong = document.createElement("strong");
      strong.textContent = esc(a.name);
      const small = document.createElement("small");
      small.textContent = esc(a.summary || a.taskKind || "待命");
      info.append(strong, small);
      const status = document.createElement("span");
      status.className = "agent-state";
      status.textContent = esc(a.state || "待命");
      el.append(avatar, info, status);
      box.append(el);
    });
  }
  function renderOutline(list) {
    const box = $("#outline");
    box.replaceChildren();
    if (!list.length) {
      box.innerHTML =
        '<div class="empty-small">开始创作后，章节大纲会在这里展开。</div>';
      return;
    }
    list.slice(0, 8).forEach((x) => {
      const el = document.createElement("div");
      el.className = "outline-item";
      const no = document.createElement("span");
      no.className = "outline-no";
      no.textContent = String(x.chapter || "").padStart(2, "0");
      const body = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = esc(x.title || `第 ${x.chapter} 章`);
      const core = document.createElement("small");
      core.textContent = esc(x.coreEvent || "");
      body.append(title, core);
      el.append(no, body);
      box.append(el);
    });
  }
  function updateControls() {
    const d = state.data,
      s = d.snapshot || {};
    $("#metric-model").textContent =
      s.modelName || state.config?.model || "未配置";
    $("#metric-provider").textContent =
      s.provider || state.config?.provider || "请先连接模型";
    $("#phase-label").textContent = d.busy
      ? "正在执行：" + d.job
      : s.statusLabel || s.phase || "";
    const idle = !d.busy && !d.stopping && !s.isRunning;
    const workflow = d.workflow || {};
    $("#start").disabled = !d.ready || !idle || workflow.started;
    $("#pause").disabled = !d.ready || d.stopping;
    $("#resume").disabled = !idle || !workflow.started;
    $("#resume").textContent = d.ready ? "恢复当前任务" : "连接并恢复任务";
    $("#retry").disabled = d.busy || d.stopping;
    $("#next-chapter").disabled = !d.ready || !idle || !workflow.approved || workflow.stage === "chapter-review" || workflow.stage === "complete";
    $("#approve-plan").disabled = !d.ready || !idle || !workflow.canApprove || workflow.approved || state.planDirectory !== d.directory || !state.planFingerprint;
    $("#revise-plan").disabled = !d.ready || !idle || !workflow.canApprove;
    $("#accept-chapter").disabled = !d.ready || !idle || workflow.stage !== "chapter-review";
    $("#send-chapter-feedback").disabled = !d.ready || !idle || !workflow.latestChapter || !workflow.approved;
    $("#new-workspace").disabled = !idle;
    $$('[data-command]').forEach(b=>b.disabled=!d.ready||d.busy||d.stopping);
  }
  async function load() {
    if (state.polling) return;
    state.polling = true;
    try {
      const [r, c] = await Promise.all([
        fetch("/api/state"),
        fetch("/api/config"),
      ]);
      if (!r.ok || !c.ok) throw Error("工作台响应异常");
      state.config = await c.json();
      const d = normalize(await r.json());
      if (state.data && state.data.directory !== d.directory) {
        saveInputs(state.data.directory);
        state.selected = 0;
        state.planFingerprint = "";
        state.planDirectory = "";
        state.batchRestored = false;
        state.events = [];
        restoreInputs(d.directory);
        $("#live-output").textContent = "等待创作引擎输出……";
        $("#reader-content").textContent = "请选择章节";
        $("#reader-meta").textContent = "选择左侧章节开始阅读";
        $("#reader-title").textContent = "章节阅读器";
        $("#reader-notes").textContent = "";
        $("#plan-premise").textContent = "加载当前项目规划……";
        $("#plan-outline").replaceChildren();
        $("#plan-layered").textContent = "";
      } else if (!state.data) {
        restoreInputs(d.directory);
      }
      renderState(d);
      updateControls();
      if (!$("#view-chapters").classList.contains("hidden")) renderChapters();
      if (!$("#view-chapters").classList.contains("hidden") && state.selected) { const [n,source]=String(state.selected).split(":"); readChapter(Number(n),source); }
      if (!$("#view-planning").classList.contains("hidden")) loadPlanning();
    } catch (e) {
      showAlert(e.message);
      $("#connection").className = "connection error";
      $("#connection").lastChild.textContent = " 连接中断";
    } finally {
      state.polling = false;
    }
  }
  function renderWorkspaces() {
    const directory = state.data?.directory;
    fetch("/api/workspaces")
      .then((r) => r.json())
      .then((list) => {
        if (directory !== state.data?.directory) return;
        const box = $("#workspace-list");
        box.replaceChildren();
        list.forEach((x) => {
          const b = document.createElement("button");
          b.className =
            "workspace " + (x.path === state.data?.directory ? "active" : "");
          const title = document.createElement("strong"), detail = document.createElement("small");
          title.textContent = x.title;
          const stages = {new:"未开始",planning:"规划中","plan-review":"待确认大纲",ready:"待生成正文",writing:"批次待继续","chapter-review":"待验收章节",complete:"已完结"};
          detail.textContent = `${x.completed || 0} 章 · ${x.status === "running" ? "运行中" : (stages[x.stage] || "已暂停")}`;
          b.append(title, detail);
          b.title = x.path;
          b.disabled = !!(state.data?.busy || state.data?.stopping || state.data?.snapshot?.isRunning);
          b.onclick = () =>
            post("/api/workspaces", { path: x.path })
              .then(load)
              .catch((e) => showAlert(e.message));
          box.append(b);
        });
      })
      .catch(() => {});
  }
  function connect() {
    if (state.es) state.es.close();
    state.es = new EventSource("/api/events");
    state.es.onmessage = (ev) => {
      try {
        const m = normalize(JSON.parse(ev.data));
        if (m.type === "history") {
          state.events = m.data.events || [];
          $("#live-output").textContent = m.data.stream || "等待创作引擎输出……";
          renderActivity();
        } else if (m.type === "delta") {
          const out = $("#live-output");
          out.textContent =
            (out.textContent === "等待创作引擎输出……" ? "" : out.textContent) +
            m.data;
          out.scrollTop = out.scrollHeight;
        } else if (m.type === "clear") $("#live-output").textContent = "";
        else if (m.type === "event") {
          if (m.data.id)
            state.events = state.events.filter((x) => x.id !== m.data.id);
          state.events.push(m.data);
          state.events = state.events.slice(-200);
          renderActivity();
        } else if (m.type === "result") {
          load();
          if (!m.data.ok) showAlert(m.data.error || "操作失败");
        } else if (m.type === "workspace") {
          state.events=[];state.selected=0;$("#live-output").textContent="等待创作引擎输出……";renderActivity();load();
        } else if (m.type === "refresh") load();
      } catch (_) {}
    };
    state.es.onerror = () => {
      state.es.close();
      setTimeout(connect, 3000);
    };
  }
  function renderActivity() {
    const box = $("#activity-list");
    if (!box) return;
    box.replaceChildren();
    if (!state.events.length) {
      box.innerHTML = '<div class="empty-small">暂无活动。</div>';
      return;
    }
    state.events
      .slice()
      .reverse()
      .forEach((e) => {
        const el = document.createElement("div");
        el.className = "activity-item";
        const t = document.createElement("time");
        t.textContent = time(e.time);
        const a = document.createElement("span");
        a.className = "agent-tag";
        a.textContent = esc(e.agent || e.category || "系统");
        const d = document.createElement("span");
        d.textContent = esc(e.summary || e.detail || "");
        el.append(t, a, d);
        box.append(el);
      });
  }
  async function readChapter(n,source='') {
    try {
      const directory=state.data?.directory;
      const d = await (await fetch(`/api/chapters/${n}?source=${source}&directory=${encodeURIComponent(directory)}`)).json();
      if(directory!==state.data?.directory)return;
      if (d.error) throw Error(d.error);
      state.selected = n+':'+source;
      $("#reader-meta").textContent = `第 ${n} 章${source==='draft'?' · 草稿':''}`;
      $("#reader-title").textContent = d.summary?.title || `第 ${n} 章`;
      if ($("#reader-content").textContent !== (d.content || "")) $("#reader-content").textContent = d.content || "";
      const notes = $("#reader-notes");
      notes.classList.remove("hidden");
      notes.textContent = [d.summary?.summary,d.review?.summary].filter(Boolean).join('\n\n');
      renderChapters();
    } catch (e) {
      showAlert(e.message);
    }
  }
  function renderChapters() {
    const box = $("#chapter-list");
    box.replaceChildren();
    const list = state.data?.chapters || [];
    if (!list.length) {
      box.innerHTML = '<div class="empty-small">还没有完成的章节。</div>';
      return;
    }
    list.forEach((x) => {
      const b = document.createElement("button");
      b.className =
        "chapter-button " + (state.selected === x.number+':'+(x.source||'') ? "active" : "");
      const strong = document.createElement("strong");
      strong.textContent = x.title || `第 ${x.number} 章`;
      const small = document.createElement("small");
      small.textContent = `${formatNum(x.words)} 字`;
      b.append(strong, small);
      b.onclick = () => readChapter(x.number,x.source||'');
      box.append(b);
    });
    if (!state.selected) { const last=list[list.length-1]; readChapter(last.number,last.source||''); }
  }
  function openModal() {
    $("#modal").classList.remove("hidden");
    renderConfig();
    $("#config-form").elements.api_key.value = "";
    $("#config-status").textContent = "";
  }
  function closeModal() {
    $("#modal").classList.add("hidden");
    $("#config-form").elements.api_key.value="";
  }
  $("#settings").onclick = openModal;
  $("#close-modal").onclick = closeModal;
  $("#modal .modal-backdrop").onclick = closeModal;
  $("#refresh").onclick = load;
  $$(".template-row button").forEach(
    (b) =>
      (b.onclick = () => {
        $("#prompt").value = b.dataset.template;
        $("#prompt").focus();
      }),
  );
  $("#start").onclick = () => {
    const text = $("#prompt").value.trim();
    if (!text) {
      showAlert("请先写下创作需求");
      return;
    }
    post("/api/action", { action: "start", text })
      .then(() => {
        $("#prompt").value = "";
        load();
      })
      .catch((e) => showAlert(e.message));
  };
  $("#prompt").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      $("#start").click();
    }
  });
  $("#test-config").onclick = async () => {
    const f = $("#config-form"),
      status = $("#config-status");
    status.textContent = "正在连接模型……";
    $("#test-config").disabled=true;
    try {
      await post("/api/config/test", configPayload(f));
      status.textContent = "连接成功，可以保存。";
    } catch (e) {
      status.textContent = e.message;
    } finally {
      $("#test-config").disabled=false;
    }
  };
  $("#config-form").onsubmit = async (e) => {
    e.preventDefault();
    const status = $("#config-status");
    try {
      const result=await post("/api/config", configPayload(e.target));
      if(result.error){status.textContent='已保存，连接未就绪：'+result.error;await load();return;}
      status.textContent = "已保存并连接";
      closeModal();
      await load();
    } catch (err) {
      status.textContent = err.message;
    }
  };
  $("#new-workspace").onclick = () => openProjectModal(false);
  $("#rename-project").onclick = () => openProjectModal(true);
  $("#next-chapter").onclick = () =>
    runAction({ action: "batch", count: Number($("#batch-count").value) });
  async function download(format) {
    try {
      const r = await fetch("/api/export", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ format }),
      });
      if (!r.ok) {
        const d = await r.json();
        throw Error(d.error);
      }
      const blob = await r.blob();
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `novel.${format}`;
      document.body.append(a);
      a.click();
      a.remove();setTimeout(()=>URL.revokeObjectURL(a.href),1000);
    } catch (e) {
      showAlert(e.message);
    }
  }
  $("#export-txt").onclick = () => download("txt");
  $("#export-epub").onclick = () => download("epub");
  ["resume", "pause", "retry"].forEach(
    (action) =>
      ($("#" + action).onclick = () =>
        post("/api/action", { action })
          .then(load)
          .catch((e) => showAlert(e.message))),
  );
  $$("[data-command]").forEach(
    (b) =>
      (b.onclick = () =>
        post("/api/action", {
          action: b.dataset.command,
          text: $("#intervention").value.trim(),
        })
          .then(load)
          .catch((e) => showAlert(e.message))),
  );
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { closeModal(); closeProjectModal(); }
  });
  load();
  connect();
  setInterval(load, 2500);
})();
