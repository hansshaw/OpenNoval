(() => {
  "use strict";

  // Read-only projections of persisted creative records. All content is built
  // with text nodes; model prose, unknown fields and Markdown never become HTML.
  const labels = {
    title: "标题", name: "名称", summary: "摘要", synopsis: "作品简介", description: "说明", ready: "就绪状态",
    verdict: "评审结论", status: "状态", issues: "发现的问题", evidence: "依据", suggestion: "修改建议",
    artifact: "相关资料", severity: "严重程度", type: "类型", category: "类别", rule: "规则", boundary: "不可违反的边界",
    role: "角色定位", aliases: "别名与称号", arc: "成长轨迹 / 故事弧", traits: "性格特质", tier: "重要程度",
    ending_direction: "结局方向", open_threads: "待收束的长线", estimated_scale: "预计规模", last_updated: "更新时的已完成章节",
    foundation_audit: "基础设定审查", compass: "故事指南针", world_rules: "世界规则", characters: "人物档案",
    character_snapshots: "人物状态快照", timeline: "故事时间线", foreshadows: "伏笔台账", relationships: "人物关系",
    state_changes: "状态变化", character_a: "人物一", character_b: "人物二", relation: "关系", relations: "人物关系",
    chapter: "章节", chapters: "章节", volume: "卷", volumes: "分卷", index: "序号", arcs: "故事弧", goal: "目标",
    theme: "主题", final: "是否收官卷", core_event: "核心事件", hook: "章节悬念", scenes: "场景安排",
    volume_id: "卷序号", volume_title: "卷名", arc_id: "故事弧序号", arc_title: "故事弧标题", estimated_chapters: "预计章节数",
    dimensions: "分项评估", dimension: "评估维度", score: "评分", comment: "评语", scope: "评估范围", kind: "类型",
    contract_status: "章节约定完成情况", contract_misses: "未完成的约定", contract_notes: "约定核验说明",
    affected_chapters: "受影响章节", requires_change: "是否需要修改", review: "评审记录", key_events: "关键事件",
    required_beats: "必须完成的推进", forbidden_moves: "禁止的推进", continuity_checks: "连续性核查",
    evaluation_focus: "评审重点", emotion_target: "情绪目标", payoff_points: "呼应与兑现", hook_goal: "追读目标",
    contract: "章节约定", conflict: "冲突", emotion_arc: "情绪变化", notes: "备注", content: "内容", text: "正文",
    reason: "原因", error: "错误信息", event: "事件", time: "故事时间", planted_at: "埋设章节", resolved_at: "回收章节",
    entity: "人物 / 实体", field: "变化项目", old_value: "变化前", new_value: "变化后", motivation: "动机", power: "能力",
    facts: "章节事实", timeline_events: "时间线事件", foreshadow_updates: "伏笔推进", relationship_changes: "人物关系变化", cast_intros: "新出场人物",
    style_delta: "风格变化", origin: "创作来源", revision: "修订版次", accepted_at: "接纳时间",
    action: "行动", decider: "决策者", at: "时间", input_excerpt: "输入摘录", input_truncated: "输入是否已节选",
    checkpoint_seq: "对应检查点", step: "已完成步骤", occurred_at: "完成时间", model: "模型", provider: "连接",
    prompt: "指令", system_prompt: "系统指令", instructions: "执行要求", temperature: "温度", enabled: "是否启用",
    thinking: "共创助手", architect: "规划助手", writer: "写作助手", editor: "审阅助手", roles: "角色分工", role_directives: "角色执行要求",
    context_window: "上下文窗口", max_output_tokens: "最大输出", reasoning_effort: "推理强度", fallbacks: "备用模型",
    version: "版本", id: "记录编号", seq: "记录序号", fingerprint: "版本指纹", digest: "内容校验值",
    content_sha256: "正文校验值", sha256: "文件校验值", created_at: "创建时间", updated_at: "更新时间",
    duration_ms: "耗时（毫秒）", word_count: "正文长度", source_count: "语料数量", source_files: "语料文件",
    corpus: "参考语料", sources: "语料来源", source_dir: "语料目录", relative_path: "文件", size_bytes: "文件大小（字节）",
    mod_time: "文件更新时间", analyzed_at: "分析时间", source_reports: "单篇分析", synthesis: "综合风格画像",
    style: "表达风格", lexicon: "词汇偏好", plot_design: "情节设计", hook_design: "悬念设计", pacing_density: "节奏与密度",
    reader_engagement: "读者吸引力", role_guidance: "创作建议", narrative_voice: "叙述声音", sentence_rhythm: "句式节奏",
    prose_texture: "文字质感", perspective: "叙事视角", mood: "氛围", do_not_copy: "避免直接照搬",
    common_words: "常用词", emotion_words: "情绪用词", scene_words: "场景用词", transition_words: "衔接用词",
    signature_phrases: "代表性表达", opening_patterns: "开篇方式", escalation_patterns: "冲突升级",
    turning_point_patterns: "转折方式", payoff_patterns: "伏笔兑现", hook_types: "悬念类型", placement: "布局位置",
    cliffhanger_patterns: "章末悬念", payoff_rules: "回收规则", scene_density: "场景密度", information_release: "信息释放",
    dialogue_action_ratio: "对话与行动比例", compression_rules: "叙述压缩", methods: "表达方法", emotional_drivers: "情绪驱动力",
    progression_rewards: "进展与满足感", anti_patterns: "应避免的写法", style_observations: "风格观察",
    plot_patterns: "情节模式", hook_patterns: "悬念模式", pacing_notes: "节奏观察", reader_appeal: "阅读吸引力",
    reusable_techniques: "可借鉴技巧", warnings: "注意事项", prose: "叙述风格", dialogue: "人物对白", taboos: "禁忌", rules: "规则",
    voice: "语言风格", brief_role: "人物定位", first_seen_chapter: "首次出场", last_seen_chapter: "最近出场",
    appearance_count: "出场次数", appearance_chapters: "出场章节", next_chapter: "下一章", flow: "当前流程", phase: "创作阶段",
    deviation: "偏离描述", feedback: "调整意见", committed: "是否已保存", review_required: "是否需要审阅", review_reason: "审阅原因",
    hook_type: "悬念类型", dominant_strand: "主要故事线", arc_end: "是否故事弧结尾", volume_end: "是否卷末",
    needs_expansion: "是否需要展开下一故事弧", needs_new_volume: "是否需要规划下一卷", next_volume: "下一卷", next_arc: "下一故事弧", book_complete: "是否全书完结",
  };
  const enums = {
    artifact: { premise: "故事内核", "premise.md": "故事内核", compass: "故事指南针", "meta/compass.json": "故事指南针", characters: "人物档案", "characters.json": "人物档案", world_rules: "世界规则", "world_rules.json": "世界规则", outline: "章节目录", "outline.json": "章节目录", layered_outline: "分卷与故事弧", "layered_outline.json": "分卷与故事弧", foundation_audit: "基础设定审查", "meta/foundation_audit.json": "基础设定审查" },
    verdict: { accept: "通过，可接纳", polish: "建议打磨", rewrite: "需要重写", pass: "通过", fail: "未通过" },
    severity: { critical: "严重问题", error: "错误", warning: "提醒", info: "提示" },
    contract_status: { met: "已达成", partial: "部分达成", missed: "未达成" },
    tier: { core: "核心人物", important: "重要人物", secondary: "次要人物", decorative: "背景人物" },
    category: { magic: "魔法与力量", technology: "科技", geography: "地理", society: "社会", other: "其他" },
    scope: { chapter: "单章", global: "全书", arc: "故事弧", volume: "分卷" },
    kind: { chapter: "单章", global: "全书", arc: "故事弧", volume: "分卷", intervention: "用户干预", plan_start: "规划启动", volume_end: "卷末决策" },
    status: { planted: "已埋设", advanced: "已推进", resolved: "已回收", pending: "待处理", running: "进行中", completed: "已完成", failed: "失败", accepted: "已接纳", ready: "已就绪", missing: "尚未生成", stale: "需要更新" },
    origin: { generated: "AI 生成", user: "人工修订", legacy: "历史版本", manual: "人工创作", imported: "外部导入", revision: "修订" },
    action: { plant: "埋设伏笔", advance: "推进伏笔", resolve: "回收伏笔" },
    phase: { init: "初始化", premise: "故事内核规划", outline: "目录规划", planning: "规划中", writing: "创作中", complete: "已完结", completed: "已完成", ready: "就绪" },
    flow: { writing: "写作", reviewing: "审核", rewriting: "重写", polishing: "打磨" },
    decider: { arbiter: "流程裁定助手", architect: "规划助手", editor: "审阅助手" },
    reasoning_effort: { none: "关闭推理", minimal: "最少", low: "低", medium: "中", high: "高", xhigh: "很高", max: "最高" },
  };
  const metadata = new Set(["id", "seq", "version", "fingerprint", "digest", "content_sha256", "sha256", "checkpoint_seq"]);
  const keyName = key => String(key).replace(/([a-z0-9])([A-Z])/g, "$1_$2").toLowerCase();
  const label = key => Object.hasOwn(labels, keyName(key)) ? labels[keyName(key)] : String(key).replace(/([a-z0-9])([A-Z])/g, "$1 $2").replace(/_/g, " ");
  const enumText = (key, value) => {
    const canonical = keyName(key);
    return Object.hasOwn(enums, canonical) && Object.hasOwn(enums[canonical], value) ? enums[canonical][value] : undefined;
  };
  const translated = (key, value) => enumText(key, value) ?? String(value);
  const emptyValue = value => value == null || value === "" || (typeof value === "object" && Object.keys(value).length === 0);
  function node(tag, className = "", text) {
    const result = document.createElement(tag);
    result.className = className;
    if (text !== undefined) result.textContent = String(text);
    return result;
  }
  function inline(container, text) {
    let offset = 0;
    const pattern = /\*\*([^*\n]+)\*\*|`([^`\n]+)`/g;
    for (const match of text.matchAll(pattern)) {
      if (match.index > offset) container.append(node("span", "", text.slice(offset, match.index)));
      container.append(node(match[1] ? "strong" : "code", "", match[1] || match[2]));
      offset = match.index + match[0].length;
    }
    if (offset < text.length) container.append(node("span", "", text.slice(offset)));
  }
  function prose(text) {
    const result = node("div", "readable-prose");
    let paragraph = [], list = null;
    const flush = () => {
      if (paragraph.length) {
        const p = node("p"); inline(p, paragraph.join("\n")); result.append(p); paragraph = [];
      }
    };
    for (const line of String(text).replace(/\r\n?/g, "\n").split("\n")) {
      const heading = line.match(/^\s{0,3}#{1,6}\s+(.+)/);
      const item = line.match(/^\s*(?:([-*+])|\d+[.)、])\s+(.+)/);
      if (heading) {
        flush(); list = null; const h = node("h5"); inline(h, heading[1]); result.append(h);
      } else if (item) {
        flush(); const tag = item[1] ? "ul" : "ol";
        if (!list || list.tagName?.toLowerCase() !== tag) { list = node(tag); result.append(list); }
        const li = node("li"); inline(li, item[2]); list.append(li);
      } else if (!line.trim()) { flush(); list = null; }
      else { list = null; paragraph.push(line); }
    }
    flush();
    return result;
  }
  function unpack(value) {
    if (typeof value !== "string" || !/^[\[{]/.test(value.trim())) return value;
    try { const parsed = JSON.parse(value); return parsed && typeof parsed === "object" ? parsed : value; }
    catch (_) { return value; }
  }
  function valueNode(original, key = "", depth = 0, ancestors = new Set()) {
    const value = unpack(original);
    if (emptyValue(value)) return node("p", "readable-empty", "暂无记录");
    if (typeof value === "boolean") return node("span", "readable-status", keyName(key) === "ready" ? value ? "已就绪" : "未就绪" : value ? "是" : "否");
    if (typeof value !== "object") {
      const text = translated(key, value);
      if (enumText(key, value) !== undefined) return node("span", "readable-status", text);
      return prose(text);
    }
    if (ancestors.has(value)) return node("p", "readable-empty", "循环引用，无法展开");
    if (depth >= 8) {
      const holder = node("div", "readable-nested");
      const button = node("button", "text-button readable-more", "展开更多层级"); button.type = "button";
      button.addEventListener("click", () => holder.replaceChildren(valueNode(value, key, 0, ancestors)));
      holder.append(button); return holder;
    }
    const path = new Set(ancestors); path.add(value);
    if (Array.isArray(value)) {
      const result = node("div", "readable-list");
      const scalarItems = value.every(item => unpack(item) == null || typeof unpack(item) !== "object");
      const records = node(scalarItems ? "ul" : "div", scalarItems ? "readable-items" : "readable-records");
      const more = node("button", "text-button readable-more"); more.type = "button";
      let shown = 0;
      const appendBatch = () => {
        const end = Math.min(shown + 24, value.length);
        for (; shown < end; shown++) {
          const item = unpack(value[shown]);
          if (scalarItems) {
            const li = node("li"); li.append(valueNode(item, key, depth + 1, path)); records.append(li); continue;
          }
          const card = node("section", "readable-record");
          const entries = item && typeof item === "object" && !Array.isArray(item) ? Object.entries(item) : [];
          const identity = entries.find(([name, content]) => ["name", "title", "dimension"].includes(keyName(name)) && typeof content === "string" && content.trim());
          const heading = identity ? identity[1] : `${label(key) || "条目"} ${shown + 1}`;
          card.append(node("h5", "readable-record-title", heading));
          const body = identity ? Object.fromEntries(entries.filter(([name]) => name !== identity[0])) : item;
          if (!emptyValue(body)) card.append(valueNode(body, key, depth + 1, path));
          records.append(card);
        }
        more.hidden = shown >= value.length;
        more.textContent = `继续显示（剩余 ${value.length - shown} 项）`;
      };
      appendBatch(); more.addEventListener("click", appendBatch);
      result.append(records, more); return result;
    }
    const result = node("div", "readable-object");
    const fields = node("dl", "readable-fields");
    const metaFields = node("dl", "readable-fields");
    for (const [name, child] of Object.entries(value)) {
      const canonical = keyName(name);
      const row = node("div", "readable-field"); row.dataset.field = canonical;
      const term = node("dt", "", label(name));
      const definition = node("dd"); definition.append(valueNode(child, name, depth + 1, path));
      row.append(term, definition);
      (metadata.has(canonical) ? metaFields : fields).append(row);
    }
    if (fields.children.length) result.append(fields);
    if (metaFields.children.length) {
      const details = node("details", "readable-metadata");
      details.append(node("summary", "", "记录信息"), metaFields); result.append(details);
    }
    return result;
  }
  function render(container, value, empty = "暂无数据") {
    const unpacked = unpack(value);
    container.replaceChildren();
    const root = node("div", "readable-data");
    root.append(emptyValue(unpacked) ? node("p", "empty-state", empty) : valueNode(unpacked));
    container.append(root);
    if (container.classList.contains?.("readable-view")) container.setAttribute("tabindex", "0");
  }
  window.NovelReadableData = { render, label, translated };
})();
