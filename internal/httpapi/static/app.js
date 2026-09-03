const state = {token: "", session: false, task: null, lang: "zh", tasks: [], agents: [], devices: [], notifications: [], taskLimit: 24};
const $ = (id) => document.getElementById(id);
const tr = (zh, en) => state.lang === "zh" ? zh : en;

const providerNames = {codex: "Codex", claude: "Claude Code", codebuddy: "CodeBuddy", gemini: "Gemini CLI", cursor: "Cursor", opencode: "OpenCode"};
const healthOrder = {blocked: 0, attention: 1, healthy: 2, active: 2, completed: 3};
const priorityOrder = {critical: 0, high: 1, medium: 2, low: 3};

const agentCatalog = [
  {provider: "codex", name: "OpenAI Codex", levelZh: "原生恢复", levelEn: "Native resume", detailZh: "JSONL 捕获、产物、原生 session resume、跨 Agent 恢复", detailEn: "JSONL capture, Artifacts, native session resume, cross-Agent recovery"},
  {provider: "claude", name: "Claude Code", levelZh: "专属适配", levelEn: "Dedicated adapter", detailZh: "Claude 结构化日志、工具调用、文件变更、跨 Agent 恢复", detailEn: "Claude structured logs, tool calls, file changes, cross-Agent recovery"},
  {provider: "codebuddy", name: "CodeBuddy", levelZh: "专属适配", levelEn: "Dedicated adapter", detailZh: "会话发现、增量捕获、产物校验、跨 Agent 恢复", detailEn: "Session discovery, incremental capture, Artifact verification, cross-Agent recovery"},
  {provider: "gemini", name: "Gemini CLI", levelZh: "专属适配", levelEn: "Dedicated adapter", detailZh: "消息、函数调用、状态和产物连续性", detailEn: "Message, function-call, status, and Artifact continuity"},
  {provider: "cursor", name: "Cursor Agent", levelZh: "专属适配", levelEn: "Dedicated adapter", detailZh: "Composer/Agent 导出日志与工作区产物连续性", detailEn: "Composer/Agent export logs and workspace Artifact continuity"},
  {provider: "opencode", name: "OpenCode", levelZh: "专属适配", levelEn: "Dedicated adapter", detailZh: "结构化事件捕获和供应商中立恢复", detailEn: "Structured event capture and vendor-neutral recovery"},
];

function el(tag, text, className = "") {
  const node = document.createElement(tag);
  node.textContent = text;
  if (className) node.className = className;
  return node;
}

function toast(message) {
  const node = $("toast");
  node.textContent = message;
  node.classList.add("show");
  setTimeout(() => node.classList.remove("show"), 3000);
}

function applyLanguage() {
  document.documentElement.lang = state.lang === "zh" ? "zh-CN" : "en";
  document.title = tr("Agent Continuity 管理控制台", "Agent Continuity Console");
  document.querySelectorAll("[data-zh]").forEach((node) => {
    node.textContent = state.lang === "zh" ? node.dataset.zh : node.dataset.en;
  });
  document.querySelectorAll("[data-zh-placeholder]").forEach((node) => {
    node.placeholder = state.lang === "zh" ? node.dataset.zhPlaceholder : node.dataset.enPlaceholder;
  });
  $("language-toggle").textContent = state.lang === "zh" ? "EN" : "中文";
  renderAgentCatalog();
}

async function api(path, options = {}) {
  if (!state.token && !state.session) throw new Error(tr("请先登录账号或连接访问凭据。", "Sign in or connect a credential first."));
  const headers = {...(options.headers || {}), "X-Portal-Request":"1"};
  if (state.token) headers.Authorization = `Bearer ${state.token}`;
  if (options.body && typeof options.body !== "string") headers["Content-Type"] = "application/json";
  const response = await fetch(path, {
    ...options,
    headers,
    body: options.body && typeof options.body !== "string" ? JSON.stringify(options.body) : options.body,
  });
  const contentType = response.headers.get("content-type") || "";
  const data = contentType.includes("json") ? await response.json() : await response.text();
  if (!response.ok) { if (response.status===401) document.dispatchEvent(new Event("portal-auth-expired")); throw new Error(data?.error?.message || data?.message || `HTTP ${response.status}`); }
  return data;
}

function renderItems(target, items, formatter) {
  target.replaceChildren();
  if (!items.length) {
    target.textContent = tr("暂无记录。", "No records.");
    target.className = "feed empty";
    return;
  }
  target.className = "feed";
  items.forEach((item) => target.append(formatter(item)));
}

function formatDate(value) {
  if (!value) return tr("未知时间", "Unknown time");
  return new Date(value).toLocaleString(state.lang === "zh" ? "zh-CN" : "en-US");
}

function statusLabel(value) {
  const zh = {active: "进行中", healthy: "健康", attention: "需关注", completed: "已完成", complete: "已完成", paused: "已暂停", blocked: "受阻", working: "执行中", open: "待处理", resolved: "已解决", synced: "已同步", native: "原生恢复", rehydrate: "跨 Agent 恢复", unsupported: "不支持"};
  return state.lang === "zh" ? (zh[value] || value || "未知") : (value || "unknown");
}

function priorityLabel(value) {
  const zh = {critical: "紧急", high: "高", medium: "中", low: "低"};
  return state.lang === "zh" ? (zh[value] || value || "普通") : (value || "normal");
}

function providerName(value) {
  return providerNames[value] || value || tr("未指定", "Unassigned");
}

function taskHealth(task) {
  if (task.metadata?.health_status) return task.metadata.health_status;
  if (task.status === "completed" || task.status === "complete") return "completed";
  const progress = Number(task.metadata?.progress || 0);
  if (task.metadata?.priority === "critical" && progress < 50) return "attention";
  return "healthy";
}

function taskProgress(task) {
  const value = Number(task.metadata?.progress);
  return Number.isFinite(value) ? Math.max(0, Math.min(100, value)) : 0;
}

function providerRoute(task) {
  const primary = providerName(task.metadata?.primary_provider);
  const defaultTargets = {codex: "claude", claude: "codebuddy", codebuddy: "gemini", gemini: "cursor", cursor: "codex", opencode: "codex"};
  const secondaryProvider = task.metadata?.secondary_provider || defaultTargets[task.metadata?.primary_provider];
  const secondary = secondaryProvider ? providerName(secondaryProvider) : tr("待分配", "Unassigned");
  return `${primary} → ${secondary}`;
}

function compactNumber(value) {
  return new Intl.NumberFormat(state.lang === "zh" ? "zh-CN" : "en-US", {notation: "compact", maximumFractionDigits: 1}).format(Number(value || 0));
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KiB`;
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`;
  return `${(bytes / 1024 ** 3).toFixed(1)} GiB`;
}

function metricLabel(metric) {
  const zh = {"api.mutation": "API 变更操作", "artifact.bytes": "产物存储与传输"};
  const en = {"api.mutation": "API mutations", "artifact.bytes": "Artifact storage & transfer"};
  return (state.lang === "zh" ? zh : en)[metric] || metric;
}

function planLabel(plan) {
  const zh = {free: "免费版", pro: "专业版", team: "团队版", enterprise: "企业版"};
  const en = {free: "Free", pro: "Pro", team: "Team", enterprise: "Enterprise"};
  return (state.lang === "zh" ? zh : en)[plan] || plan;
}

function billingStatusLabel(status) {
  const zh = {active: "生效中", trialing: "试用中", past_due: "已逾期", canceled: "已取消"};
  const en = {active: "active", trialing: "trialing", past_due: "past due", canceled: "canceled"};
  return (state.lang === "zh" ? zh : en)[status] || status;
}

function formatUsageQuantity(metric, quantity, unit) {
  if (unit === "byte" || metric.endsWith(".bytes")) return formatBytes(quantity);
  return `${compactNumber(quantity)} ${unit || ""}`.trim();
}

function limitLabel(key) {
  const zh = {members: "成员", devices: "设备", artifact_bytes: "产物容量", retained_tasks: "保留任务", monthly_cross_agent_resumes: "每月跨 Agent 恢复", api_calls: "API 调用", synchronized_events: "同步事件", egress_bytes: "出口流量", custom_policy: "自定义策略"};
  const en = {members: "Members", devices: "Devices", artifact_bytes: "Artifact capacity", retained_tasks: "Retained tasks", monthly_cross_agent_resumes: "Monthly cross-Agent resumes", api_calls: "API calls", synchronized_events: "Synchronized events", egress_bytes: "Egress", custom_policy: "Custom policy"};
  return (state.lang === "zh" ? zh : en)[key] || key;
}

function formatLimit(key, value) {
  if (value === -1) return tr("不限", "Unlimited");
  if (typeof value === "boolean") return value ? tr("支持", "Enabled") : tr("不支持", "Disabled");
  if (key.endsWith("_bytes")) return formatBytes(value);
  return compactNumber(value);
}

function deviceByID(deviceID) {
  return state.devices.find((device) => device.device_id === deviceID);
}

function devicePlatform(device) {
  const value = `${device?.name || ""}`.toLowerCase();
  if (value.includes("iphone") || value.includes("ipad") || value.includes("ios")) return "iOS";
  if (value.includes("android")) return "Android";
  if (value.includes("windows")) return "Windows";
  if (value.includes("linux") || value.includes("runner")) return "Linux";
  if (value.includes("browser") || value.includes("web")) return "Web";
  if (value.includes("mac")) return "macOS";
  return tr("通用设备", "Generic");
}

function deviceName(deviceID) {
  if (!deviceID) return tr("未记录来源设备", "No origin device");
  return deviceByID(deviceID)?.name || tr(`设备 ${deviceID.slice(0, 14)}…`, `Device ${deviceID.slice(0, 14)}…`);
}

function pill(text, kind = "neutral") {
  return el("span", text, `pill ${kind}`);
}

function taskEventCount(task) {
  const value = Number(task.metadata?.event_volume);
  return Number.isFinite(value) ? value : (task.metadata?.demo_seed ? 6 : task.head_event_ids?.length || 0);
}

function taskArtifactCount(task) {
  const value = Number(task.metadata?.artifact_count);
  return Number.isFinite(value) ? value : (task.metadata?.demo_seed ? 1 : 0);
}

function eventLabel(value) {
  const zh = {"user.input": "用户输入", "agent.output": "Agent 输出", "tool.call": "工具调用", "tool.result": "工具结果", "decision": "决策", "status.changed": "状态变更", "agent.error": "Agent 错误", "artifact.changed": "产物变更", "progress.updated": "进度更新"};
  return state.lang === "zh" ? (zh[value] || value) : value;
}

function switchPanel(panelID) {
  document.querySelectorAll(".tab,.panel").forEach((node) => node.classList.remove("active"));
  const tab = document.querySelector(`[data-panel="${panelID}"]`);
  if (tab) tab.classList.add("active");
  $(panelID).classList.add("active");
}

function renderAgentCatalog() {
  const target = $("agent-catalog");
  target.replaceChildren();
  agentCatalog.forEach((agent) => {
    const card = el("article", "", "agent-card");
    const heading = el("div", "", "agent-heading");
    heading.append(el("strong", agent.name), el("span", agent.provider, "provider-chip"));
    card.append(heading, el("span", state.lang === "zh" ? agent.levelZh : agent.levelEn, "support-level"), el("p", state.lang === "zh" ? agent.detailZh : agent.detailEn, "muted"));
    target.append(card);
  });
}

function distributionRow(label, value, total, kind) {
  const row = el("div", "", "distribution-row");
  const heading = el("div", "", "distribution-heading");
  heading.append(el("span", label), el("strong", String(value)));
  const track = el("div", "", "distribution-track");
  const bar = el("span", "", `distribution-bar ${kind}`);
  bar.style.width = `${total ? Math.max(3, Math.round(value / total * 100)) : 0}%`;
  track.append(bar);
  row.append(heading, track);
  return row;
}

function renderTaskTable(target, tasks, limit = 8) {
  target.replaceChildren();
  if (!tasks.length) {
    target.textContent = tr("当前没有需要处理的任务。", "No tasks require attention.");
    target.className = "empty";
    return;
  }
  target.className = "task-table";
  tasks.slice(0, limit).forEach((task) => {
    const row = el("button", "", "task-table-row");
    row.type = "button";
    const identity = el("span", "", "task-table-identity");
    identity.append(el("strong", task.title), el("small", `${task.metadata?.scenario || tr("通用场景", "General")} · ${task.metadata?.owner || tr("未分配", "Unassigned")}`));
    const route = el("span", providerRoute(task), "task-table-route");
    const status = pill(statusLabel(taskHealth(task)), taskHealth(task));
    const progress = el("span", `${taskProgress(task)}%`, "task-table-progress");
    row.append(status, identity, route, progress);
    row.addEventListener("click", () => loadTask(task));
    target.append(row);
  });
}

function renderOperations(tasks, agents, devices) {
  const healthCounts = tasks.reduce((counts, task) => ({...counts, [taskHealth(task)]: (counts[taskHealth(task)] || 0) + 1}), {});
  const healthTarget = $("operations-health");
  healthTarget.replaceChildren();
  healthTarget.className = "distribution";
  [["healthy", "healthy"], ["attention", "attention"], ["blocked", "blocked"], ["completed", "completed"]].forEach(([health, kind]) => {
    healthTarget.append(distributionRow(statusLabel(health), healthCounts[health] || 0, tasks.length, kind));
  });

  const activeAgents = agents.filter((item) => !item.revoked_at);
  const providerCounts = activeAgents.reduce((counts, item) => ({...counts, [item.provider]: (counts[item.provider] || 0) + 1}), {});
  const agentTarget = $("operations-agents");
  agentTarget.replaceChildren();
  agentTarget.className = "distribution";
  Object.entries(providerCounts).sort((a, b) => b[1] - a[1]).forEach(([provider, count]) => agentTarget.append(distributionRow(providerName(provider), count, activeAgents.length, "agent")));

  const activeDevices = devices.filter((item) => !item.revoked_at);
  const recoveryValues = tasks.map((task) => Number(task.metadata?.resume_success_rate)).filter(Number.isFinite);
  const recoveryRate = recoveryValues.length ? Math.round(recoveryValues.reduce((sum, value) => sum + value, 0) / recoveryValues.length) : 100;
  const handoffTasks = tasks.filter((task) => Number(task.metadata?.cross_agent_handoffs) > 0).length;
  const coverage = tasks.length ? Math.round(handoffTasks / tasks.length * 100) : 0;
  renderItems($("operations-trust"), [
    [tr("跨 Agent 连续性覆盖", "Cross-Agent coverage"), `${coverage}%`, "accent"],
    [tr("平均恢复成功率", "Average resume success"), `${recoveryRate}%`, "accent"],
    [tr("签名设备", "Signing devices"), activeDevices.length, "neutral"],
    [tr("因果 DAG 任务", "Causal DAG tasks"), tasks.filter((task) => (task.head_event_ids || []).length > 0).length, "neutral"],
  ], ([label, value, kind]) => {
    const item = el("div", "", "trust-metric");
    item.append(el("span", label), pill(String(value), kind));
    return item;
  });

  const scenarioCounts = tasks.reduce((counts, task) => {
    const scenario = task.metadata?.scenario || tr("通用场景", "General");
    counts[scenario] = (counts[scenario] || 0) + 1;
    return counts;
  }, {});
  const scenarioTarget = $("operations-scenarios");
  scenarioTarget.replaceChildren();
  scenarioTarget.className = "scenario-cloud";
  Object.entries(scenarioCounts).sort((a, b) => b[1] - a[1]).forEach(([name, count]) => scenarioTarget.append(pill(`${name} · ${count}`, "neutral")));
}

async function loadHome() {
  try {
    const responses = await Promise.allSettled([
      api("/v1/tasks"), api("/v1/agents"), api("/v1/devices"), api("/v1/notifications?unread_only=true&limit=20"),
    ]);
    const values = responses.map((result) => result.status === "fulfilled" ? result.value : null);
    state.tasks = values[0]?.tasks || [];
    state.agents = values[1]?.agents || [];
    state.devices = values[2]?.devices || [];
    state.notifications = values[3]?.notifications || [];
    const tasks = state.tasks;
    const agents = state.agents;
    const devices = state.devices;
    const attentionTasks = tasks.filter((task) => ["attention", "blocked"].includes(taskHealth(task)));
    const healthyTasks = tasks.filter((task) => taskHealth(task) === "healthy").length;
    const handoffTasks = tasks.filter((task) => Number(task.metadata?.cross_agent_handoffs) > 0).length;
    const recoveryValues = tasks.map((task) => Number(task.metadata?.resume_success_rate)).filter(Number.isFinite);
    const recoveryRate = recoveryValues.length ? Math.round(recoveryValues.reduce((sum, value) => sum + value, 0) / recoveryValues.length) : 100;
    const eventCount = tasks.reduce((sum, task) => sum + taskEventCount(task), 0);
    const artifactCount = tasks.reduce((sum, task) => sum + taskArtifactCount(task), 0);
    const stats = [
      [tr("任务总量", "Total tasks"), tasks.length, tr("跨场景连续性任务", "continuity tasks")],
      [tr("健康运行", "Healthy"), healthyTasks, tr("无需人工介入", "no intervention")],
      [tr("风险 / 阻塞", "Risk / blocked"), attentionTasks.length, tr("需要运营关注", "needs attention"), attentionTasks.length ? "danger" : ""],
      [tr("跨 Agent 覆盖", "Cross-Agent coverage"), `${tasks.length ? Math.round(handoffTasks / tasks.length * 100) : 0}%`, tr("具备接力记录", "with handoffs")],
      [tr("Agent 实例", "Agent instances"), agents.filter((item) => !item.revoked_at).length, tr("6 个主流平台", "6 providers")],
      [tr("恢复成功率", "Resume success"), `${recoveryRate}%`, tr("最近演示任务平均值", "demo average")],
    ];
    const target = $("home-stats");
    target.replaceChildren();
    stats.forEach(([label, value, note, kind]) => {
      const card = el("article", "", `card stat ${kind || ""}`.trim());
      card.append(el("span", label, "eyebrow"), el("strong", String(value)), el("p", note, "muted"));
      target.append(card);
    });
    $("home-scale-badge").textContent = tr(`${tasks.length} 任务 · ${eventCount} 事件 · ${artifactCount} 产物`, `${tasks.length} tasks · ${eventCount} events · ${artifactCount} artifacts`);
    renderOperations(tasks, agents, devices);
    const sortedAttention = attentionTasks.sort((a, b) => (priorityOrder[a.metadata?.priority] ?? 9) - (priorityOrder[b.metadata?.priority] ?? 9) || Number(a.metadata?.sla_remaining_minutes || 9999) - Number(b.metadata?.sla_remaining_minutes || 9999));
    renderTaskTable($("attention-tasks"), sortedAttention, 10);
    const recent = [...tasks].sort((a, b) => Number(a.metadata?.last_activity_minutes ?? 9999) - Number(b.metadata?.last_activity_minutes ?? 9999));
    renderTaskTable($("recent-tasks"), recent, 8);
    const signals = [
      ...sortedAttention.slice(0, 8).map((task) => ({kind: taskHealth(task), subject: `${task.title} · ${Number(task.metadata?.sla_remaining_minutes) < 0 ? tr("SLA 已超时", "SLA breached") : tr(`SLA 剩余 ${task.metadata?.sla_remaining_minutes ?? "-"} 分钟`, `SLA ${task.metadata?.sla_remaining_minutes ?? "-"} min`)}`})),
      ...state.notifications,
    ];
    renderItems($("home-notifications"), signals, (item) => el("div", `${statusLabel(item.kind)} · ${item.subject}${item.created_at ? ` · ${formatDate(item.created_at)}` : ""}`, `feed-item signal ${item.kind}`));
  } catch (error) { toast(error.message); }
}

function renderTaskCard(task) {
  const card = el("article", "", "card task");
  const heading = el("div", "", "task-card-heading");
  const tags = el("div", "", "task-card-tags");
  tags.append(pill(statusLabel(taskHealth(task)), taskHealth(task)), pill(`${tr("优先级", "Priority")} ${priorityLabel(task.metadata?.priority)}`, task.metadata?.priority === "critical" ? "blocked" : "neutral"));
  heading.append(tags, el("span", task.metadata?.scenario || tr("通用场景", "General"), "meta"));
  const title = el("h3", task.title);
  const objective = el("p", task.objective, "muted task-objective");
  const route = el("div", "", "route-line");
  route.append(el("span", providerRoute(task), "route-providers"), el("span", `${task.metadata?.cross_agent_handoffs || 0} ${tr("次接力", "handoffs")}`, "meta"));
  const originDevice = deviceByID(task.origin_device);
  const origin = el("div", "", "origin-device");
  const originIdentity = el("span", "", "origin-device-name");
  originIdentity.append(el("span", tr("来源设备", "Origin device"), "meta"), el("strong", deviceName(task.origin_device)));
  origin.append(originIdentity, pill(devicePlatform(originDevice), "neutral"));
  const progress = el("div", "", "progress-track");
  const bar = el("span", "", `progress-bar ${taskHealth(task)}`);
  bar.style.width = `${taskProgress(task)}%`;
  progress.append(bar);
  const metrics = el("div", "", "task-card-metrics");
  metrics.append(
    el("span", `${taskProgress(task)}% ${tr("进度", "progress")}`),
    el("span", `${task.metadata?.resume_success_rate || "-"}% ${tr("恢复", "resume")}`),
    el("span", `${taskEventCount(task)} ${tr("事件", "events")}`),
    el("span", `${taskArtifactCount(task)} ${tr("产物", "artifacts")}`),
  );
  const footer = el("div", "", "task-card-footer");
  const sla = Number(task.metadata?.sla_remaining_minutes);
  footer.append(el("span", task.metadata?.owner || tr("未分配", "Unassigned")), el("span", Number.isFinite(sla) ? (sla < 0 ? tr(`SLA 超时 ${Math.abs(sla)} 分钟`, `SLA breached ${Math.abs(sla)} min`) : tr(`SLA 剩余 ${sla} 分钟`, `SLA ${sla} min left`)) : formatDate(task.updated_at), sla < 0 ? "danger-text" : "meta"));
  card.append(heading, title, objective, route, origin, progress, metrics, footer);
  card.addEventListener("click", () => loadTask(task));
  return card;
}

function renderTaskList() {
  const query = $("task-search").value.trim().toLowerCase();
  const health = $("task-health-filter").value;
  const provider = $("task-provider-filter").value;
  const priority = $("task-priority-filter").value;
  const filtered = state.tasks.filter((task) => {
    const searchable = `${task.title} ${task.objective} ${task.metadata?.scenario || ""} ${task.metadata?.owner || ""}`.toLowerCase();
    return (!query || searchable.includes(query)) && (!health || taskHealth(task) === health) && (!provider || task.metadata?.primary_provider === provider || task.metadata?.secondary_provider === provider) && (!priority || task.metadata?.priority === priority);
  }).sort((a, b) => (healthOrder[taskHealth(a)] ?? 9) - (healthOrder[taskHealth(b)] ?? 9) || (priorityOrder[a.metadata?.priority] ?? 9) - (priorityOrder[b.metadata?.priority] ?? 9) || Number(a.metadata?.last_activity_minutes ?? 9999) - Number(b.metadata?.last_activity_minutes ?? 9999));
  const target = $("tasks");
  target.replaceChildren();
  target.className = "grid task-grid";
  filtered.slice(0, state.taskLimit).forEach((task) => target.append(renderTaskCard(task)));
  if (!filtered.length) {
    target.textContent = tr("没有符合筛选条件的任务。", "No tasks match the filters.");
    target.className = "empty";
  }
  $("task-results-summary").textContent = tr(`显示 ${Math.min(filtered.length, state.taskLimit)} / ${filtered.length} 条，共 ${state.tasks.length} 条任务`, `Showing ${Math.min(filtered.length, state.taskLimit)} / ${filtered.length}, ${state.tasks.length} total`);
  $("load-more-tasks").hidden = filtered.length <= state.taskLimit;
}

async function loadTasks() {
  try {
    const data = await api("/v1/tasks");
    state.tasks = data.tasks || [];
    renderTaskList();
    $("auth-status").textContent = tr(`已连接 · ${state.tasks.length} 个可见任务`, `Connected · ${state.tasks.length} visible task(s)`);
  } catch (error) { toast(error.message); }
}

function conflictItem(conflict) {
  const item = el("div", "", "feed-item");
  item.append(el("strong", `${tr("冲突", "Conflict")}: ${conflict.kind}`), el("p", conflict.resolved ? statusLabel("resolved") : `${statusLabel("open")} · ${tr("策略", "strategy")} ${conflict.strategy}`, "muted"));
  if (!conflict.resolved) {
    const button = el("button", tr("手动解决", "Resolve manually"));
    button.addEventListener("click", async () => {
      try {
        await api(`/v1/tasks/${encodeURIComponent(conflict.task_id)}/conflicts/${encodeURIComponent(conflict.conflict_id)}/resolve`, {method: "POST", body: {strategy: "manual", notes: "Resolved from Web console"}});
        await loadTask(state.task);
      } catch (error) { toast(error.message); }
    });
    item.append(button);
  }
  return item;
}

function renderBranches(branches) {
  const target = $("branches");
  target.replaceChildren();
  target.className = "dag";
  branches.forEach((branch) => {
    const lane = el("article", "", "dag-lane");
    lane.append(el("span", statusLabel(branch.status), "eyebrow"), el("strong", branch.name), el("code", branch.branch_id));
    (branch.head_event_ids || []).forEach((head) => lane.append(el("div", `HEAD ${head}`, "dag-head")));
    if (!(branch.head_event_ids || []).length) lane.append(el("div", tr("暂无分支事件", "No branch events yet"), "muted"));
    target.append(lane);
  });
  if (!branches.length) target.textContent = tr("暂无分支。", "No branches.");
}

function displayPayload(payload) {
  if (typeof payload === "string") return payload;
  const value = payload?.payload ?? payload?.message ?? payload?.text ?? payload;
  return typeof value === "string" ? value : JSON.stringify(value ?? {});
}

async function loadTask(task) {
  state.task = task;
  $("detail-title").textContent = task.title;
  $("sync-badge").textContent = tr("同步中…", "Syncing…");
  switchPanel("detail-panel");
  try {
    const [events, artifacts, conflicts, participants, branches, agents, permissions, audit] = await Promise.all([
      api(`/v1/tasks/${encodeURIComponent(task.task_id)}/events?include_payload=true`),
      api(`/v1/tasks/${encodeURIComponent(task.task_id)}/artifacts`),
      api(`/v1/tasks/${encodeURIComponent(task.task_id)}/conflicts?include_resolved=true`),
      api(`/v1/tasks/${encodeURIComponent(task.task_id)}/participants`),
      api(`/v1/tasks/${encodeURIComponent(task.task_id)}/branches`),
      api(`/v1/tasks/${encodeURIComponent(task.task_id)}/agents`),
      api(`/v1/tasks/${encodeURIComponent(task.task_id)}/permissions`).catch(() => ({permissions: []})),
      api("/v1/audit/events?limit=200").catch(() => ({audit_events: []})),
    ]);
    const summary = $("task-summary");
    summary.replaceChildren();
    summary.className = "card task-summary";
    const summaryTags = el("div", "", "task-card-tags");
    summaryTags.append(pill(statusLabel(taskHealth(task)), taskHealth(task)), pill(`${tr("优先级", "Priority")} ${priorityLabel(task.metadata?.priority)}`, "neutral"), pill(task.metadata?.scenario || tr("通用场景", "General"), "neutral"));
    const originDevice = deviceByID(task.origin_device);
    const deviceSummary = el("div", "", "origin-device detail-origin-device");
    const deviceIdentity = el("span", "", "origin-device-name");
    deviceIdentity.append(el("span", tr("来源设备", "Origin device"), "meta"), el("strong", deviceName(task.origin_device)));
    deviceSummary.append(deviceIdentity, pill(devicePlatform(originDevice), "neutral"), el("code", task.origin_device || "-"));
    summary.append(summaryTags, el("p", task.objective), el("strong", providerRoute(task), "route-providers"), deviceSummary, el("p", tr(`${taskEventCount(task)} 个事件 · ${taskArtifactCount(task)} 个产物 · ${task.metadata?.cross_agent_handoffs || 0} 次跨 Agent 接力 · 更新于 ${formatDate(task.updated_at)}`, `${taskEventCount(task)} events · ${taskArtifactCount(task)} artifacts · ${task.metadata?.cross_agent_handoffs || 0} handoffs · updated ${formatDate(task.updated_at)}`), "muted"));
    renderItems($("events"), events.events, (event) => {
      const item = el("div", "", "feed-item timeline-item");
      item.append(el("strong", `#${event.server_sequence} · ${eventLabel(event.event_type)}`), el("span", providerName(event.provider_id) || event.device_id, "meta"), el("code", event.content_digest));
      return item;
    });
    const conversationEvents = events.events.filter((event) => ["user.input", "agent.output", "human.message.created", "agent.message.created"].includes(event.event_type));
    renderItems($("conversation"), conversationEvents, (event) => el("div", `${eventLabel(event.event_type)} · ${displayPayload(event.payload)}`, "feed-item"));
    renderItems($("artifacts"), artifacts.artifacts, (artifact) => el("div", `${artifact.filename} · ${artifact.size} bytes · ${artifact.content_hash}`, "feed-item"));
    const versionResponses = await Promise.allSettled((artifacts.artifacts || []).slice(0, 20).map((artifact) => api(`/v1/tasks/${encodeURIComponent(task.task_id)}/artifacts/${encodeURIComponent(artifact.artifact_id)}/versions`)));
    const versions = versionResponses.flatMap((response) => response.status === "fulfilled" ? (response.value.versions || response.value.artifacts || []) : []);
    renderItems($("artifact-versions"), versions, (version) => el("div", `${version.filename} · ${version.version_id} · parent ${version.parent_version_id || "root"} · ${version.content_hash}`, "feed-item"));
    const collaboration = $("collaboration");
    collaboration.replaceChildren();
    collaboration.className = "feed";
    conflicts.conflicts.forEach((conflict) => collaboration.append(conflictItem(conflict)));
    participants.participants.forEach((participant) => collaboration.append(el("div", `${participant.kind}: ${participant.role} · ${statusLabel(participant.status)} · ${participant.participant_id}`, "feed-item")));
    if (!conflicts.conflicts.length && !participants.participants.length) collaboration.textContent = tr("暂无记录。", "No records.");
    renderBranches(branches.branches);
    renderItems($("task-agents"), agents.agent_bindings, (binding) => {
      const profile = state.agents.find((agent) => agent.agent_id === binding.agent_id);
      const item = el("div", "", "feed-item agent-binding");
      item.append(el("strong", profile?.name || binding.agent_id), pill(statusLabel(binding.resume_mode), binding.resume_mode === "native" ? "accent" : "neutral"), el("p", `${providerName(profile?.provider)} · ${(binding.capabilities || []).join(" · ")}`, "muted"));
      return item;
    });
    renderItems($("task-permissions"), permissions.permissions || [], (permission) => {
      const item = el("div", "", "feed-item");
      item.append(el("span", `${permission.account_id} · ${permission.permission}`));
      if (permission.account_id !== task.account_id) {
        const revoke = el("button", tr("撤销", "Revoke"));
        revoke.addEventListener("click", async () => {
          try {
            await api(`/v1/tasks/${encodeURIComponent(task.task_id)}/permissions/${encodeURIComponent(permission.account_id)}`, {method: "DELETE"});
            await loadTask(task);
          } catch (error) { toast(error.message); }
        });
        item.append(revoke);
      }
      return item;
    });
    $("task-metadata").textContent = JSON.stringify({status: task.status, origin_device: task.origin_device, origin_device_name: deviceName(task.origin_device), organization_id: task.organization_id, team_id: task.team_id, metadata: task.metadata}, null, 2);
    renderItems($("task-audit"), (audit.audit_events || []).filter((item) => item.resource_id === task.task_id || item.details?.task_id === task.task_id), (item) => el("div", `${item.action} · ${item.resource_type}/${item.resource_id} · ${formatDate(item.created_at)}`, "feed-item"));
    renderItems($("workspace-collaborations"), participants.participants, (participant) => el("div", `${participant.kind} · ${participant.role} · ${statusLabel(participant.status)}`, "feed-item"));
    $("sync-badge").textContent = conflicts.conflicts.some((conflict) => !conflict.resolved) ? tr("存在冲突", "Conflict") : tr("已同步", "Synced");
  } catch (error) {
    $("sync-badge").textContent = tr("同步错误", "Sync error");
    toast(error.message);
  }
}

async function grantTaskPermission() {
  if (!state.task) return toast(tr("请先选择任务。", "Select a task first."));
  const accountID = $("permission-account").value.trim();
  if (!accountID) return toast(tr("请输入账户 ID。", "Enter an account ID."));
  try {
    await api(`/v1/tasks/${encodeURIComponent(state.task.task_id)}/permissions/${encodeURIComponent(accountID)}`, {method: "PUT", body: {permission: $("permission-level").value}});
    $("permission-account").value = "";
    await loadTask(state.task);
  } catch (error) { toast(error.message); }
}

async function previewResume() {
  if (!state.task) return toast(tr("请先选择任务。", "Select a task first."));
  try {
    $("resume-preview").textContent = JSON.stringify(await api(`/v1/tasks/${encodeURIComponent(state.task.task_id)}/continuity-package?agent=${encodeURIComponent($("target-agent").value)}`), null, 2);
  } catch (error) { toast(error.message); }
}

async function share() {
  if (!state.task) return toast(tr("请先选择任务。", "Select a task first."));
  try {
    const data = await api(`/v1/tasks/${encodeURIComponent(state.task.task_id)}/invitations`, {method: "POST", body: {permission: $("share-permission").value, ttl_seconds: Number($("share-ttl").value)}});
    $("share-result").textContent = `${tr("一次性令牌，有效期至", "One-time token, expires")} ${formatDate(data.invitation.expires_at)}:\n${data.token}`;
  } catch (error) { toast(error.message); }
}

async function loadWorkspace() {
  try {
    const responses = await Promise.allSettled([api("/v1/agents"), api("/v1/devices?include_revoked=true"), api("/v1/organizations")]);
    const values = responses.map((result) => result.status === "fulfilled" ? result.value : null);
    state.agents = values[0]?.agents || [];
    state.devices = values[1]?.devices || [];
    $("device-count-badge").textContent = tr(`${state.devices.length} 台已登记`, `${state.devices.length} registered`);
    const providerGroups = Object.entries(state.agents.filter((item) => !item.revoked_at).reduce((groups, item) => {
      (groups[item.provider] ||= []).push(item);
      return groups;
    }, {}));
    renderItems($("agents"), providerGroups, ([provider, items]) => {
      const utilization = items.map((item) => Number(item.card?.utilization)).filter(Number.isFinite);
      const average = utilization.length ? Math.round(utilization.reduce((sum, value) => sum + value, 0) / utilization.length) : 0;
      const node = el("div", "", "feed-item fleet-row");
      const heading = el("div", "", "fleet-heading");
      heading.append(el("strong", providerName(provider)), pill(`${items.length} ${tr("个实例", "instances")}`, "accent"));
      node.append(heading, el("p", `${tr("平均负载", "Avg utilization")} ${average}% · ${items.map((item) => item.card?.role).filter(Boolean).join(" / ")}`, "muted"));
      return node;
    });
    renderItems($("devices"), state.devices, (item) => {
      const node = el("div", "", "feed-item device-row");
      const heading = el("div", "", "device-heading");
      const identity = el("div", "", "device-identity");
      identity.append(el("span", tr("设备名称", "Device name"), "eyebrow"), el("strong", item.name));
      heading.append(identity, pill(devicePlatform(item), "neutral"));
      const status = el("div", "", "device-status-line");
      status.append(pill(item.revoked_at ? tr("已停用", "Revoked") : tr("签名有效", "Signing active"), item.revoked_at ? "blocked" : "healthy"), el("span", tr(`登记于 ${formatDate(item.created_at)}`, `Registered ${formatDate(item.created_at)}`), "muted"));
      node.append(heading, status, el("code", item.device_id));
      return node;
    });
    if (state.tasks.length) renderTaskList();
    const organizations = values[2]?.organizations || [];
    const teamResponses = await Promise.allSettled(organizations.map((item) => api(`/v1/organizations/${encodeURIComponent(item.organization_id)}/teams`)));
    renderItems($("organizations"), organizations, (item) => {
      const index = organizations.indexOf(item);
      const teams = teamResponses[index]?.status === "fulfilled" ? (teamResponses[index].value.teams || []) : [];
      const node = el("div", "", "feed-item organization-row");
      node.append(el("strong", item.name), pill(item.plan, "accent"), el("p", tr(`${teams.length} 个团队 · ${teams.map((team) => team.name).join("、")}`, `${teams.length} teams · ${teams.map((team) => team.name).join(", ")}`), "muted"));
      return node;
    });
  } catch (error) { toast(error.message); }
}

async function loadSecurity() {
  try {
    const responses = await Promise.allSettled([api("/v1/api-keys"), api("/v1/audit/events?limit=100")]);
    const values = responses.map((result) => result.status === "fulfilled" ? result.value : null);
    renderItems($("security-api-keys"), values[0]?.api_keys || [], (item) => el("div", `${item.name} · ${item.prefix} · ${(item.scopes || []).join(", ")} · ${item.revoked_at ? tr("已停用", "revoked") : tr("活跃", "active")}`, "feed-item"));
    renderItems($("audit-events"), values[1]?.events || values[1]?.audit_events || [], (item) => el("div", `${item.action} · ${item.resource_type}/${item.resource_id} · ${formatDate(item.created_at)}`, "feed-item"));
  } catch (error) { toast(error.message); }
}

async function loadBilling() {
  $("billing-load-status").textContent = tr("加载中…", "Loading…");
  try {
    const rangeDays = Number($("billing-range").value || 0);
    const from = rangeDays ? `&from=${encodeURIComponent(new Date(Date.now() - rangeDays * 86400000).toISOString())}` : "";
    const responses = await Promise.allSettled([api("/v1/billing/accounts"), api(`/v1/usage?limit=1000${from}`)]);
    const failure = responses.find((result) => result.status === "rejected");
    if (failure) throw failure.reason;
    const values = responses.map((result) => result.status === "fulfilled" ? result.value : null);
    const accounts = values[0]?.billing_accounts || [];
    const usage = (values[1]?.usage || []).sort((a, b) => new Date(b.recorded_at) - new Date(a.recorded_at));
    renderItems($("billing-accounts"), accounts, (item) => {
      const node = el("div", "", "feed-item billing-account-card");
      const heading = el("div", "", "billing-account-heading");
      heading.append(el("strong", `${planLabel(item.plan)} ${tr("套餐", "plan")}`), pill(billingStatusLabel(item.status), "healthy"));
      const period = el("p", tr(`计费周期：${formatDate(item.current_period_start)} 至 ${formatDate(item.current_period_end)} · 币种 ${item.currency}`, `Billing period: ${formatDate(item.current_period_start)} to ${formatDate(item.current_period_end)} · ${item.currency}`), "muted");
      const limits = el("div", "", "billing-limits");
      Object.entries(item.limits || {}).forEach(([key, value]) => {
        const limit = el("div", "", "billing-limit");
        limit.append(el("span", limitLabel(key), "meta"), el("strong", formatLimit(key, value)));
        limits.append(limit);
      });
      node.append(heading, period, limits);
      return node;
    });
    const aggregates = Object.entries(usage.reduce((result, item) => {
      const current = result[item.metric] || {quantity: 0, unit: item.unit, records: 0};
      current.quantity += Number(item.quantity || 0);
      current.records += 1;
      result[item.metric] = current;
      return result;
    }, {}));
    const mutationCount = aggregates.find(([metric]) => metric === "api.mutation")?.[1]?.quantity || 0;
    const artifactBytes = aggregates.find(([metric]) => metric === "artifact.bytes")?.[1]?.quantity || 0;
    const billingStats = $("billing-summary");
    billingStats.replaceChildren();
    [[tr("当前窗口记录", "Window records"), usage.length], [tr("API 变更", "API mutations"), mutationCount], [tr("产物计量", "Artifact usage"), formatBytes(artifactBytes)], [tr("可见任务", "Visible tasks"), state.tasks.length], [tr("Agent 实例", "Agent instances"), state.agents.filter((item) => !item.revoked_at).length], [tr("计费账户", "Billing accounts"), accounts.length]].forEach(([label, value]) => {
      const card = el("article", "", "card stat compact");
      card.append(el("span", label, "eyebrow"), el("strong", String(value)));
      billingStats.append(card);
    });
    renderItems($("billing-usage"), aggregates.sort((a, b) => b[1].quantity - a[1].quantity), ([metric, aggregate]) => {
      const node = el("div", "", "feed-item usage-row");
      node.append(el("strong", metricLabel(metric)), pill(formatUsageQuantity(metric, aggregate.quantity, aggregate.unit), "accent"), el("span", `${aggregate.records} ${tr("条记录", "records")} · ${metric}`, "muted"));
      return node;
    });
    renderItems($("billing-usage-detail"), usage.slice(0, 100), (item) => {
      const node = el("div", "", "usage-detail-row");
      const identity = el("div", "", "usage-detail-identity");
      identity.append(el("strong", metricLabel(item.metric)), el("code", `${tr("记录", "Record")}: ${item.usage_id}`), el("code", `${tr("幂等键", "Idempotency key")}: ${item.idempotency_key}`));
      node.append(identity, pill(formatUsageQuantity(item.metric, item.quantity, item.unit), "accent"), el("span", item.resource_id || tr("账户级", "Account level"), "usage-resource"), el("time", formatDate(item.recorded_at)));
      return node;
    });
    $("billing-usage-detail").className = usage.length ? "usage-detail" : "usage-detail empty";
    $("billing-detail-count").textContent = tr(`显示最近 ${Math.min(usage.length, 100)} / ${usage.length} 条`, `Latest ${Math.min(usage.length, 100)} / ${usage.length}`);
    $("billing-load-status").textContent = tr(`已加载 ${usage.length} 条`, `${usage.length} loaded`);
    await loadBillingFinance(accounts);
  } catch (error) {
    $("billing-load-status").textContent = tr("加载失败", "Load failed");
    toast(error.message);
  }
}

async function loadDeveloper() {
  try {
    const responses = await Promise.allSettled([api("/v1/api-keys"), api("/v1/oauth/clients"), api("/v1/webhooks"), api("/v1/usage?limit=50")]);
    const values = responses.map((result) => result.status === "fulfilled" ? result.value : null);
    renderItems($("api-keys"), values[0]?.api_keys || [], (item) => el("div", `${item.name} · ${item.prefix} · ${item.revoked_at ? tr("已停用", "revoked") : tr("活跃", "active")}`, "feed-item"));
    renderItems($("oauth-clients"), values[1]?.clients || [], (item) => el("div", `${item.name} · ${item.client_id} · ${item.revoked_at ? tr("已停用", "revoked") : tr("活跃", "active")}`, "feed-item"));
    renderItems($("webhooks"), values[2]?.webhooks || [], (item) => el("div", `${item.url} · ${(item.events || []).join(", ")}`, "feed-item"));
    renderItems($("usage"), values[3]?.usage || [], (item) => el("div", `${item.metric}: ${item.quantity} ${item.unit} · ${formatDate(item.recorded_at)}`, "feed-item"));
  } catch (error) { toast(error.message); }
}

async function loadMarketplace() {
  try {
    const responses = await Promise.allSettled([api("/v1/marketplace/listings?status=published"), api("/v1/marketplace/installations"), api("/v1/marketplace/transactions")]);
    const values = responses.map((result) => result.status === "fulfilled" ? result.value : null);
    renderItems($("marketplace-listings"), values[0]?.listings || [], (item) => {
      const node = el("div", "", "feed-item listing-row");
      const price = item.price_minor ? `${item.currency === "CNY" ? "¥" : item.currency + " "}${(item.price_minor / 100).toFixed(2)}` : tr("免费", "Free");
      node.append(el("strong", item.name), pill(item.kind, "neutral"), el("p", `${price} · v${item.version} · ${(item.capabilities || []).join(" / ")}`, "muted"));
      return node;
    });
    renderItems($("marketplace-installations"), values[1]?.installations || [], (item) => el("div", `${statusLabel(item.status)} · ${item.version} · ${item.listing_id}`, "feed-item"));
    renderItems($("marketplace-transactions"), values[2]?.transactions || [], (item) => el("div", `${item.transaction_id} · ${statusLabel(item.status)} · ${(item.amount_minor / 100).toFixed(2)} ${item.currency}`, "feed-item"));
  } catch (error) { toast(error.message); }
}

const panelLoaders = {"home-panel": loadHome, "tasks-panel": loadTasks, "workspace-panel": loadWorkspace, "security-panel": loadSecurity, "billing-panel": loadBilling, "developer-panel": loadDeveloper, "marketplace-panel": loadMarketplace};

async function activatePanel(panelID) {
  switchPanel(panelID);
  if ((!state.token && !state.session) || !panelLoaders[panelID]) return;
  await panelLoaders[panelID]();
}

document.querySelectorAll(".tab").forEach((tab) => tab.addEventListener("click", () => activatePanel(tab.dataset.panel)));
$("language-toggle").addEventListener("click", async () => {
  const activePanel = document.querySelector(".panel.active")?.id;
  state.lang = state.lang === "zh" ? "en" : "zh";
  applyLanguage();
  if (state.token || state.session) await Promise.all([loadHome(), loadTasks(), loadWorkspace()]);
  if (state.task && activePanel === "detail-panel") await loadTask(state.task);
  if ((state.token || state.session) && activePanel && !["home-panel", "tasks-panel", "workspace-panel", "detail-panel"].includes(activePanel) && panelLoaders[activePanel]) await panelLoaders[activePanel]();
});
$("connect").addEventListener("click", async () => {
  state.token = $("token").value.trim();
  if (!state.token) return toast(tr("请输入访问凭据。", "Enter a credential."));
  document.dispatchEvent(new Event("portal-bearer-connected"));
  await Promise.all([loadHome(), loadTasks(), loadWorkspace()]);
  const activePanel = document.querySelector(".panel.active")?.id;
  if (activePanel && !["home-panel", "tasks-panel", "workspace-panel"].includes(activePanel) && panelLoaders[activePanel]) await panelLoaders[activePanel]();
});
$("refresh-home").addEventListener("click", loadHome);
$("refresh-tasks").addEventListener("click", loadTasks);
[$("task-search"), $("task-health-filter"), $("task-provider-filter"), $("task-priority-filter")].forEach((control) => control.addEventListener(control.tagName === "INPUT" ? "input" : "change", () => {
  state.taskLimit = 24;
  renderTaskList();
}));
$("clear-task-filters").addEventListener("click", () => {
  $("task-search").value = "";
  $("task-health-filter").value = "";
  $("task-provider-filter").value = "";
  $("task-priority-filter").value = "";
  state.taskLimit = 24;
  renderTaskList();
});
$("load-more-tasks").addEventListener("click", () => {
  state.taskLimit += 24;
  renderTaskList();
});
$("preview-resume").addEventListener("click", previewResume);
$("create-share").addEventListener("click", share);
$("grant-permission").addEventListener("click", grantTaskPermission);
$("refresh-workspace").addEventListener("click", loadWorkspace);
$("refresh-security").addEventListener("click", loadSecurity);
$("refresh-billing").addEventListener("click", loadBilling);
$("billing-range").addEventListener("change", () => { if (state.token || state.session) loadBilling(); });
$("refresh-developer").addEventListener("click", loadDeveloper);
$("refresh-marketplace").addEventListener("click", loadMarketplace);
window.addEventListener("online", () => { $("network-dot").classList.remove("offline"); $("network-label").textContent = tr("在线", "Online"); });
window.addEventListener("offline", () => { $("network-dot").classList.add("offline"); $("network-label").textContent = tr("离线 · 本地守护进程继续运行", "Offline · local daemon continues"); });

applyLanguage();
