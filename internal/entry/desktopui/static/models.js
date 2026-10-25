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

    let editingModelKey = "";

    function configPayload() {
      const form = $("#config-form");
      const data = Object.fromEntries(new FormData(form));
      if (data.type !== "openai") data.api = "";
      const limitValue = (raw) => {
        const text = String(raw ?? "").trim();
        if (text === "") return 0;
        const parsed = Number.parseInt(text, 10);
        return Number.isFinite(parsed) ? Math.max(0, parsed) : 0;
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

    function discoveryKey() {
      const fields = $("#config-form").elements;
      return JSON.stringify([fields.name.value.trim(), fields.type.value, fields.api.value, fields.base_url.value.trim()]);
    }

    function fillModelFields(force = false) {
      const form = $("#config-form");
      const model = form.elements.model.value.trim();
      const provider = form.elements.name.value.trim();
      const key = JSON.stringify([provider, model]);
      if (force || editingModelKey !== key) {
        editingModelKey = key;
        const detail = selectedModelDetail();
        form.elements.context_window.value = detail?.configured_context_window > 0 ? String(detail.configured_context_window) : "";
        form.elements.max_output_tokens.value = detail?.configured_max_output_tokens > 0 ? String(detail.configured_max_output_tokens) : "";
      }
      const saved = $("#saved-model");
      saved.value = [...saved.options].some((item) => item.value === model) ? model : "";
      updateLimitHint();
    }

    function updateLimitHint() {
      const form = $("#config-form");
      const hint = $("#model-limit-hint");
      const detail = selectedModelDetail();
      const discovered = state.modelDiscovery?.key === discoveryKey()
        ? state.modelDiscovery.details[form.elements.model.value.trim()] : null;
      form.elements.context_window.placeholder = detail ? `自动（当前 ${formatTokenCount(detail.context_window)}）` : "自动";
      form.elements.max_output_tokens.placeholder = detail ? `自动（当前 ${formatTokenCount(detail.max_output_tokens)}）` : "自动";
      const automatic = "留空或填 0，保存后自动获取上游参数；上游未声明时采用内置参考或兼容回退。填写数字则作为此模型的独立覆盖值。";
      if (discovered) {
        hint.textContent = `检测结果：上下文窗口 ${formatTokenCount(discovered.context_window)}（${limitSourceLabel(discovered.context_source || discovered.source)}），单次最大输出 ${formatTokenCount(discovered.max_output_tokens)}（${limitSourceLabel(discovered.output_source || discovered.source)}）。${automatic}`;
      } else if (detail) {
        hint.textContent = `当前生效：上下文窗口 ${formatTokenCount(detail.context_window)}（${limitSourceLabel(detail.context_source || detail.source)}），单次最大输出 ${formatTokenCount(detail.max_output_tokens)}（${limitSourceLabel(detail.output_source || detail.source)}）。${automatic}`;
      } else {
        hint.textContent = `上下文窗口用于对话预算与压缩，最大输出用于单次回复预算。${automatic}`;
      }
    }

    function resetModelLimits() {
      const form = $("#config-form");
      form.elements.context_window.value = "";
      form.elements.max_output_tokens.value = "";
      $("#config-status").textContent = "已取消此模型的手动限制，保存后自动采用可获取的上游参数。";
    }

    function renderSavedModels(provider, selected) {
      const select = $("#saved-model");
      select.replaceChildren(option("", "＋ 添加新模型", !selected));
      for (const name of provider.models || []) select.append(option(name, name, name === selected));
      select.value = selected || "";
    }

    function fillProviderForm(name = "", selectedModel = "") {
      state.settingsRevision += 1;
      const config = state.config || {};
      const provider =
        (config.providers || []).find((item) => item.name === name) ||
        {};
      const form = $("#config-form");
      form.elements.name.value = provider.name || "";
      form.elements.type.value = provider.type || "openai";
      form.elements.base_url.value = provider.base_url || "";
      form.elements.model.value = selectedModel || (name === config.provider ? config.model : provider.models?.[0] || "");
      form.elements.models.value = (provider.models || []).join("\n");
      form.elements.api.value = provider.api || "chat";
      form.elements.api_key.value = "";
      renderSavedModels(provider, form.elements.model.value);
      renderModelOptions(
        provider.models || [],
        form.elements.model.value,
      );
      fillModelFields(true);
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
        state.modelDiscovery = { provider: name, key: discoveryKey(), details };
        if (!currentModel && models.length) {
          form.elements.model.value = models[0];
        }
        fillModelFields();
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
        fillProviderForm(payload.name, payload.model);
        const warning = result.warning ? ` ${result.warning}` : "";
        if (result.error) {
          status.textContent = `配置已保存，但项目连接未就绪：${result.error}${warning}`;
          toast(status.textContent, "error");
        } else {
          status.textContent = `模型配置已保存。可选择已保存模型继续修改；默认模型在窗口顶部单独切换。${warning}`;
          toast(warning ? `模型库已保存。${warning}` : "模型库已保存");
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
      $("#reset-model-limits").addEventListener("click", resetModelLimits);
      $("#saved-model").addEventListener("change", (event) => {
        $("#config-form").elements.model.value = event.target.value;
        fillModelFields(true);
      });
      for (const field of ["model", "name"]) {
        $("#config-form").elements[field].addEventListener(
          "input",
          () => fillModelFields(),
        );
        $("#config-form").elements[field].addEventListener(
          "change",
          () => fillModelFields(),
        );
      }
      for (const field of ["type", "api", "base_url", "api_key"]) {
        $("#config-form").elements[field].addEventListener("input", () => {
          state.modelDiscovery = { provider: "", details: {} };
          updateLimitHint();
        });
      }
      $("#config-form").addEventListener("submit", saveConfig);
      }
      return { bindEvents, modelSelection, renderRuntimeModels };
    },
  };
})();
