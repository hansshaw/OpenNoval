(() => {
  "use strict";

  window.NovelModels = {
    create({ state, $, $$, api, option, setBusy, refreshAll, syncDisabledControls, toast }) {
    function modelSelection(provider, model) {
      return provider && model ? JSON.stringify([provider, model]) : "";
    }

    function renderRuntimeModels() {
      const select = $("#runtime-model");
      const config = state.config || {};
      const selected = modelSelection(config.provider, config.model);
      const pending = select.value;
      const previous = select.dataset.current;
      select.replaceChildren();
      for (const provider of config.providers || []) {
        for (const model of provider.models || []) {
          select.append(option(modelSelection(provider.name, model), `${provider.name} / ${model}`, false));
        }
      }
      if (!select.options.length) select.append(option("", "请先配置模型", true));
      const preservePending = previous === selected && [...select.options].some((item) => item.value === pending);
      select.value = preservePending ? pending : selected;
      select.dataset.current = selected;
      $("#runtime-model-hint").textContent = selected
        ? `当前默认：${config.provider} / ${config.model}。暂停后切换，后续请求使用所选模型；角色专属模型仍按项目配置执行。`
        : "请在“模型与连接设置”保存连接和模型库。支持多个连接，每个连接可保存多个模型。";
      syncDisabledControls();
    }

    async function selectRuntimeModel() {
      const value = $("#runtime-model").value;
      if (!value) return;
      const [provider, model] = JSON.parse(value);
      setBusy("modelSwitch", true);
      try {
        const result = await api("/api/config/select", { method: "POST", body: { provider, model } });
        await refreshAll();
        toast(result.error || `已切换默认模型：${provider} / ${model}`, result.error ? "error" : "");
      } catch (error) {
        toast(error.message, "error");
      } finally {
        setBusy("modelSwitch", false);
      }
    }

    function configPayload() {
      const form = $("#config-form");
      const data = Object.fromEntries(new FormData(form));
      if (data.type !== "openai") data.api = "";
      const limitValue = (raw) => {
        const text = String(raw ?? "").trim();
        if (text === "") return null;
        const parsed = Number.parseInt(text, 10);
        return Number.isFinite(parsed) ? Math.max(0, parsed) : null;
      };
      return {
        provider: state.config?.provider || "",
        name: data.name.trim(),
        model: data.model.trim(),
        type: data.type,
        api: data.api,
        base_url: data.base_url.trim(),
        api_key: data.api_key,
        context_window: limitValue(data.context_window),
        max_output_tokens: limitValue(data.max_output_tokens),
      };
    }

    function modelLibraryPayload() {
      const payload = configPayload();
      const names = $("#config-form").elements.models.value.split(/[\r\n,，]+/).map((name) => name.trim()).filter(Boolean);
      payload.models = [...new Set([...names, payload.model])];
      payload.activate = false;
      return payload;
    }

    function renderModelOptions(models, selected = "") {
      const list = $("#model-options");
      list.replaceChildren();
      for (const model of [...new Set(models || [])]) {
        if (model) list.append(option(model, model, model === selected));
      }
    }

    const LIMIT_SOURCE_LABELS = {
      model_config: "模型配置",
      config: "旧版全局配置",
      registry: "内置注册表",
      legacy_global: "旧版全局配置",
      context_window_cap: "上下文窗口上限",
      upstream: "上游声明",
      unknown: "上游未声明",
      default: "未验证默认值",
      unverified_default: "未验证默认值",
    };

    function formatTokenCount(value) {
      const number = Number(value) || 0;
      return number > 0 ? number.toLocaleString("zh-CN") : "未知";
    }

    function limitSourceLabel(source) {
      return LIMIT_SOURCE_LABELS[source] || source || "未知";
    }

    function selectedModelDetail() {
      const form = $("#config-form");
      const model = String(form.elements.model?.value || "").trim();
      const providerName = String(form.elements.name?.value || "").trim();
      if (!model) return null;
      const provider =
        (state.config?.providers || []).find(
          (item) => item.name === providerName,
        ) || null;
      return (
        (provider?.model_details || []).find((item) => item.name === model) ||
        null
      );
    }

    function updateLimitHint() {
      const form = $("#config-form");
      const hint = $("#model-limit-hint");
      const detail = selectedModelDetail();
      const providerName = form.elements.name.value.trim();
      const discovered = state.modelDiscovery.provider === providerName
        ? state.modelDiscovery.details[form.elements.model.value.trim()] : null;
      $("#apply-model-limits").classList.toggle("hidden", !discovered);
      if (!detail) {
        form.elements.context_window.placeholder = "自动";
        form.elements.max_output_tokens.placeholder = "自动";
        hint.textContent = discovered
          ? `上游参考：上下文窗口 ${formatTokenCount(discovered.context_window)}，单次最大输出 ${formatTokenCount(discovered.max_output_tokens)}。这些值尚未写入配置；点击“应用当前模型的上游参数”后保存才会生效。`
          : "上下文窗口决定本地压缩阈值；单次最大输出会作为 max_tokens 发送给上游。留空表示沿用当前生效值，填 0 表示恢复自动推断。";
        return;
      }
      form.elements.context_window.placeholder = `自动（当前 ${formatTokenCount(detail.context_window)}）`;
      form.elements.max_output_tokens.placeholder = `自动（当前 ${formatTokenCount(detail.max_output_tokens)}）`;
      const contextSource =
        detail.context_source || detail.source || "unknown";
      const outputSource = detail.output_source || detail.source || "unknown";
      hint.textContent = `当前生效：上下文窗口 ${formatTokenCount(detail.context_window)}（${limitSourceLabel(contextSource)}），单次最大输出 ${formatTokenCount(detail.max_output_tokens)}（${limitSourceLabel(outputSource)}）。保存时填写数字可覆盖检测值。`;
    }

    function applyDiscoveredLimits() {
      const form = $("#config-form");
      const provider = form.elements.name.value.trim();
      const model = form.elements.model.value.trim();
      const detail = state.modelDiscovery.provider === provider ? state.modelDiscovery.details[model] : null;
      if (!detail) return;
      if (Number(detail.context_window) > 0) form.elements.context_window.value = detail.context_window;
      if (Number(detail.max_output_tokens) > 0) form.elements.max_output_tokens.value = detail.max_output_tokens;
      $("#config-status").textContent = "已填入参考参数，保存模型库后生效。";
    }

    function fillProviderForm(name = "") {
      state.settingsRevision += 1;
      const config = state.config || {};
      const provider =
        (config.providers || []).find((item) => item.name === name) ||
        {};
      const form = $("#config-form");
      form.elements.name.value = provider.name || "";
      form.elements.type.value = provider.type || "openai";
      form.elements.base_url.value = provider.base_url || "";
      form.elements.model.value = name === config.provider ? config.model : provider.models?.[0] || "";
      form.elements.models.value = (provider.models || []).join("\n");
      form.elements.api.value = provider.api || "chat";
      form.elements.api_key.value = "";
      form.elements.context_window.value = "";
      form.elements.max_output_tokens.value = "";
      renderModelOptions(
        provider.models || [],
        form.elements.model.value,
      );
      updateLimitHint();
      $("#config-status").textContent = provider.has_api_key
        ? `已保存 Key：${provider.api_key_hint || "已隐藏"}`
        : provider.requires_api_key === false ? "此连接允许不使用 API Key。" : "尚未保存 API Key。";
    }

    function setSettingsBusy(busy) {
      state.settingsBusy = busy;
      for (const control of $$("input, select, textarea, button", $("#config-form"))) {
        if (control.id !== "close-settings") control.disabled = busy;
      }
    }

    function renderSavedProviders(selected) {
      const select = $("#saved-provider");
      select.replaceChildren(option("", "＋ 添加新的连接", !selected));
      for (const provider of state.config?.providers || []) {
        select.append(option(provider.name, `${provider.name} · ${provider.models?.length || 0} 个模型`, provider.name === selected));
      }
    }

    function openSettings() {
      const selected = state.config?.provider || state.config?.providers?.[0]?.name || "";
      renderSavedProviders(selected);
      fillProviderForm(selected);
      const dialog = $("#settings-dialog");
      if (!dialog.open) dialog.showModal();
    }

    async function testConfig() {
      if (state.settingsBusy) return;
      const payload = configPayload();
      const revision = state.settingsRevision;
      const status = $("#config-status");
      setSettingsBusy(true);
      status.textContent = "正在测试连接，最多等待 30 秒……";
      try {
        await api("/api/config/test", {
          method: "POST",
          body: payload,
        });
        if (revision !== state.settingsRevision) return;
        status.textContent = "连接成功，可以保存。";
        toast("模型连接测试成功");
      } catch (error) {
        if (revision === state.settingsRevision) status.textContent = error.message;
      } finally {
        setSettingsBusy(false);
      }
    }

    async function fetchModels() {
      if (state.settingsBusy) return;
      const revision = state.settingsRevision;
      const form = $("#config-form");
      const status = $("#config-status");
      const data = Object.fromEntries(new FormData(form));
      const name = String(data.name || "").trim();
      const currentModel = String(data.model || "").trim();
      if (!name) {
        status.textContent = "请先填写 Provider 名称。";
        form.elements.name.focus();
        return;
      }
      if (!String(data.base_url || "").trim()) {
        status.textContent = "请先填写 Base URL。";
        form.elements.base_url.focus();
        return;
      }
      setSettingsBusy(true);
      status.textContent = "正在从上游获取模型列表……";
      try {
        const result = await api("/api/config/models", {
          method: "POST",
          body: {
            provider: state.config?.provider || "",
            name,
            type: data.type || "",
            api: data.type === "openai" ? data.api || "chat" : "",
            base_url: String(data.base_url || "").trim(),
            api_key: String(data.api_key || ""),
          },
        });
        if (revision !== state.settingsRevision) return;
        const models = Array.isArray(result.models) ? result.models : [];
        renderModelOptions(models, currentModel);
        const details = {};
        for (const detail of Array.isArray(result.details) ? result.details : []) {
          if (detail?.id) details[detail.id] = detail;
        }
        state.modelDiscovery = { provider: name, details };
        if (!currentModel && models.length) {
          form.elements.model.value = models[0];
        }
        updateLimitHint();
        status.textContent = `已获取 ${models.length} 个模型，可从输入框候选列表选择，也可继续手动输入。`;
        toast(`已获取 ${models.length} 个可用模型`);
      } catch (error) {
        if (revision === state.settingsRevision) status.textContent = error.message;
      } finally {
        setSettingsBusy(false);
      }
    }

    async function saveConfig(event) {
      event.preventDefault();
      if (state.settingsBusy) return;
      const payload = modelLibraryPayload();
      const revision = state.settingsRevision;
      const status = $("#config-status");
      setSettingsBusy(true);
      status.textContent = "正在保存模型库……";
      try {
        const result = await api("/api/config", {
          method: "POST",
          body: payload,
        });
        await refreshAll();
        if (revision !== state.settingsRevision) return;
        renderSavedProviders(payload.name);
        fillProviderForm(payload.name);
        if (result.error) {
          status.textContent = `配置已保存，但项目连接未就绪：${result.error}`;
          toast(status.textContent, "error");
        } else {
          status.textContent = "模型库已保存，可继续添加其他连接；在创作总览选择当前默认模型。";
          toast("模型库已保存");
        }
      } catch (error) {
        if (revision === state.settingsRevision) status.textContent = error.message;
      } finally {
        setSettingsBusy(false);
      }
    }

      function bindEvents() {
      $("#open-settings").addEventListener("click", openSettings);
      $("#saved-provider").addEventListener("change", (event) => fillProviderForm(event.target.value));
      $("#runtime-model").addEventListener("change", syncDisabledControls);
      $("#select-runtime-model").addEventListener("click", selectRuntimeModel);
      $("#test-config").addEventListener("click", testConfig);
      $("#fetch-models").addEventListener("click", fetchModels);
      $("#apply-model-limits").addEventListener("click", applyDiscoveredLimits);
      for (const field of ["model", "name"]) {
        $("#config-form").elements[field].addEventListener(
          "input",
          updateLimitHint,
        );
        $("#config-form").elements[field].addEventListener(
          "change",
          updateLimitHint,
        );
      }
      $("#config-form").addEventListener("submit", saveConfig);
      }
      return { bindEvents, modelSelection, renderRuntimeModels };
    },
  };
})();
