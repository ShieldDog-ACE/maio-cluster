"use strict";
/* maio v4.1 web console. No framework: DOM + fetch + SSE. */
const $ = (sel) => {
    const el = document.querySelector(sel);
    if (!el)
        throw new Error("missing element " + sel);
    return el;
};
const S = {
    info: null,
    nodes: [],
    jobs: [],
    models: [],
    local: [],
    plugins: [],
    marketPlugins: [],
    marketModels: [],
    system: null,
    joinCode: "",
    view: "overview",
    busy: false,
};
const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const fmtBytes = (n) => {
    if (!n || n < 0)
        return "-";
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    let v = n, i = 0;
    while (v >= 1024 && i < units.length - 1) {
        v /= 1024;
        i++;
    }
    return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
};
const fmtTime = (s) => {
    if (!s)
        return "-";
    const d = new Date(s);
    if (Number.isNaN(d.getTime()))
        return "-";
    return d.toLocaleString();
};
class AuthError extends Error {
}
class ApiError extends Error {
    constructor(message, status) {
        super(message);
        this.status = status;
    }
}
async function api(path, init) {
    const res = await fetch(path, {
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
        ...init,
    });
    if (res.status === 401) {
        if (!path.endsWith("/v1/web/login"))
            showLogin("登录已过期，请重新输入 Token");
        throw new AuthError("登录已过期");
    }
    const text = await res.text();
    let data = null;
    try {
        data = text ? JSON.parse(text) : null;
    }
    catch {
        data = { error: text };
    }
    if (!res.ok)
        throw new ApiError(data?.error || `HTTP ${res.status}`, res.status);
    return data;
}
let toastTimer = 0;
function toast(message, bad = false) {
    const el = $("#toast");
    el.textContent = message;
    el.classList.toggle("bad", bad);
    el.hidden = false;
    window.clearTimeout(toastTimer);
    toastTimer = window.setTimeout(() => { el.hidden = true; }, 4200);
}
function showLogin(message = "") {
    $("#app").hidden = true;
    $("#login").hidden = false;
    $("#login-error").textContent = message;
    renderRecent();
}
function showApp() {
    $("#login").hidden = true;
    $("#app").hidden = false;
    const server = location.origin;
    const recent = loadRecent();
    if (S.info && !recent.includes(server)) {
        recent.unshift(server);
        localStorage.setItem("maio.recent", JSON.stringify(recent.slice(0, 5)));
    }
}
function loadRecent() {
    try {
        const raw = localStorage.getItem("maio.recent");
        return raw ? JSON.parse(raw).filter((x) => typeof x === "string") : [];
    }
    catch {
        return [];
    }
}
function renderRecent() {
    const list = loadRecent();
    $("#login-recent").hidden = list.length === 0;
    $("#login-recent-list").innerHTML = list
        .map((u) => `<button type="button" data-server="${esc(u)}">${esc(u.replace(/^https?:\/\//, ""))}</button>`)
        .join("");
    for (const btn of document.querySelectorAll("#login-recent-list button")) {
        btn.onclick = () => {
            $("#login-server").value = btn.dataset.server || "";
        };
    }
}
/* ---------- boot / auth ---------- */
async function boot() {
    try {
        const me = await api("/v1/web/me");
        S.info = me.cluster;
        showApp();
        await refreshAll();
        switchView("overview");
    }
    catch (err) {
        if (err instanceof AuthError)
            showLogin();
        else
            showLogin("无法连接协调者：" + err.message);
    }
}
async function login(token) {
    await api("/v1/web/login", {
        method: "POST",
        body: JSON.stringify({ token }),
    });
    await boot();
}
function initLogin() {
    $("#login-server").value = location.origin;
    $("#login-form").addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const server = $("#login-server").value.trim();
        const token = $("#login-token").value.trim();
        const err = $("#login-error");
        err.textContent = "";
        if (!token) {
            err.textContent = "请输入集群 Token";
            return;
        }
        let target;
        try {
            target = new URL(server || location.origin);
        }
        catch {
            err.textContent = "地址格式不对";
            return;
        }
        if (target.origin !== location.origin) {
            location.href = target.origin + "/?login=1";
            return;
        }
        const btn = $("#login-form button[type=submit]");
        btn.disabled = true;
        try {
            await login(token);
            $("#login-token").value = "";
        }
        catch (e) {
            err.textContent = e instanceof ApiError && e.status === 401
                ? "Token 不正确" : e.message;
        }
        finally {
            btn.disabled = false;
        }
    });
    $("#logout").addEventListener("click", async () => {
        try {
            await api("/v1/web/logout", { method: "POST" });
        }
        catch { /* ignore */ }
        showLogin();
    });
}
/* ---------- refresh ---------- */
const safe = async (p, fallback) => {
    try {
        return await p;
    }
    catch {
        return fallback;
    }
};
async function refreshAll() {
    const [info, nodes, jobs, models, local, plugins, system, join] = await Promise.all([
        api("/v1/info"),
        api("/v1/nodes"),
        api("/v1/jobs"),
        safe(api("/v1/models"), { models: [] }),
        safe(api("/v1/models/local"), { models: [] }),
        safe(api("/v1/plugins"), { plugins: [] }),
        safe(api("/v1/system"), null),
        safe(api("/v1/cluster/join-code"), { code: "" }),
    ]);
    S.info = info;
    S.nodes = nodes.nodes || [];
    S.jobs = jobs.jobs || [];
    S.models = models.models || [];
    S.local = local.models || [];
    S.plugins = plugins.plugins || [];
    S.system = system;
    S.joinCode = join.code || "";
    render();
}
async function refreshMarket(force = false) {
    const q = force ? "?refresh=1" : "";
    const [p, m] = await Promise.all([
        api("/v1/market/plugins" + q),
        api("/v1/market/models" + q),
    ]);
    S.marketPlugins = p.plugins || [];
    S.marketModels = m.models || [];
    renderMarket(p.base_url || "");
    renderModels();
}
/* ---------- render ---------- */
function render() {
    const info = S.info;
    if (!info)
        return;
    $("#cluster-chip").textContent = `${info.name} · ${info.nodes} 节点`;
    $("#cluster-sub").textContent =
        `${info.id} · protocol ${info.protocol} · ${info.tls ? "TLS" : "plain HTTP"} · ${info.host || "?"}:${info.port}`;
    renderOverview();
    renderChatModels();
    renderModels();
    renderPlugins();
    renderSettings();
}
function renderOverview() {
    const online = S.nodes.filter((n) => n.state === "online").length;
    const gpus = S.nodes.reduce((a, n) => a + (n.hardware.gpus?.length || 0), 0)
        || (S.system?.hardware.gpus?.length || 0);
    $("#overview-cards").innerHTML = [
        ["节点", `${online}/${S.nodes.length}`, "在线 / 总数"],
        ["模型", String(S.models.length), "集群可见"],
        ["插件", String(S.plugins.length), "已安装"],
        ["GPU", String(gpus), "集群加速卡"],
        ["版本", S.info?.version || "-", `protocol ${S.info?.protocol || "-"}`],
    ].map(([k, v, s]) => `<div class="card"><div class="k">${esc(k)}</div><div class="v">${esc(v)}</div><div class="s">${esc(s)}</div></div>`).join("");
    $("#nodes-count").textContent = `${S.nodes.length} 台`;
    $("#nodes-table").innerHTML = S.nodes.length ? `<table>
    <thead><tr><th>节点</th><th>角色</th><th>状态</th><th>硬件</th><th>RPC</th></tr></thead>
    <tbody>${S.nodes.map((n) => `<tr>
      <td><strong>${esc(n.name)}</strong><br><small class="muted">${esc(n.host)}:${n.api_port}</small></td>
      <td>${esc(n.role)}</td>
      <td><span class="badge ${n.state === "online" ? "ok" : "bad"}">${esc(n.state)}</span></td>
      <td>${esc(n.hardware.gpus?.length ? n.hardware.gpus.map((g) => `${g.backend}:${g.name}`).join(", ") : "cpu")}
        <br><small class="muted">${esc(fmtBytes(n.hardware.mem_total))} RAM</small></td>
      <td>${n.rpc.available ? `<span class="badge ok">${n.rpc.devices} dev</span>` : `<span class="badge">off</span>`}</td>
    </tr>`).join("")}</tbody></table>` : `<div class="empty">还没有节点。启动协调者后这里会出现本机节点。</div>`;
    $("#jobs-count").textContent = `${S.jobs.length} 个`;
    $("#jobs-list").innerHTML = S.jobs.length ? S.jobs.slice(0, 8).map((j) => `
    <div class="job">
      <div><strong>${esc(j.model)}</strong> <small>${esc(j.mode)} · ${esc(j.id.slice(0, 12))}</small></div>
      <div><span class="badge ${j.state === "done" ? "ok" : j.state === "failed" ? "bad" : ""}">${esc(j.state)}</span></div>
      <small>${esc(fmtTime(j.created_at))} · ${j.tokens} tokens${j.error ? " · " + esc(j.error) : ""}</small>
    </div>`).join("") : `<div class="empty">还没有任务。</div>`;
    renderHardware();
}
function renderHardware() {
    const hw = S.system?.hardware;
    if (!hw) {
        $("#hardware").innerHTML = `<div class="empty">硬件信息不可用。</div>`;
        return;
    }
    const gpuHtml = (hw.gpus || []).map((g) => `<div class="hw">
      <div class="k">${esc(g.backend)} #${g.index}</div>
      <div class="v">${esc(g.name)}</div>
      <div class="k">${esc(fmtBytes(g.vram_free))} free / ${esc(fmtBytes(g.vram_total))}</div>
    </div>`).join("");
    $("#hardware").innerHTML = `
    <div class="hw"><div class="k">CPU</div><div class="v">${esc(hw.cpu_model)}</div><div class="k">${hw.cpu_cores} cores</div></div>
    <div class="hw"><div class="k">内存</div><div class="v">${esc(fmtBytes(hw.mem_free))} free</div><div class="k">${esc(fmtBytes(hw.mem_total))} total</div></div>
    <div class="hw"><div class="k">磁盘</div><div class="v">${esc(fmtBytes(hw.disk_free))} free</div><div class="k">${esc(S.system?.models_dir || "")}</div></div>
    ${gpuHtml || `<div class="hw"><div class="k">GPU</div><div class="v">cpu only</div><div class="k">未检测到加速卡</div></div>`}`;
}
function renderChatModels() {
    const sel = $("#chat-model");
    const names = Array.from(new Set([...S.local.map((m) => m.name), ...S.models.map((m) => m.name)]));
    const prev = sel.value;
    sel.innerHTML = names.length
        ? names.map((n) => `<option value="${esc(n)}">${esc(n)}</option>`).join("")
        : `<option value="">没有可用模型</option>`;
    if (names.includes(prev))
        sel.value = prev;
}
function renderModels() {
    $("#local-models-count").textContent = `${S.local.length} 个`;
    $("#local-models").innerHTML = S.local.length ? `<table>
    <thead><tr><th>名称</th><th>量化</th><th>层</th><th>大小</th><th>路径</th></tr></thead>
    <tbody>${S.local.map((m) => `<tr>
      <td><strong>${esc(m.name)}</strong></td>
      <td>${esc(m.gguf?.quant || "-")}</td>
      <td>${m.gguf?.layers ?? "-"}</td>
      <td>${esc(fmtBytes(m.size_bytes))}</td>
      <td><small class="muted">${esc(m.abs_path)}</small></td>
    </tr>`).join("")}</tbody></table>` : `<div class="empty">还没有模型。用右侧“拉取模型”从市场下载。</div>`;
    const pull = $("#pull-market");
    const prev = pull.value;
    pull.innerHTML = `<option value="">— 选择市场模型 —</option>` +
        S.marketModels.map((m) => `<option value="${esc(m.name)}">${esc(m.name)} · ${esc(m.quant || "")}</option>`).join("");
    if (prev)
        pull.value = prev;
}
function renderPlugins() {
    $("#plugins-count").textContent = `${S.plugins.length} 个`;
    $("#plugins-list").innerHTML = S.plugins.length ? S.plugins.map((p) => `
    <div class="plugin">
      <div class="row"><h4>${esc(p.name)} <span class="badge">${esc(p.version)}</span></h4>
        <span class="muted small">${esc(p.author || "")}</span></div>
      <p>${esc(p.description || "—")}</p>
      <code>${esc(p.repo)}</code>
      <div class="actions">
        <button class="ghost" data-plugin-update="${esc(p.name)}">更新</button>
        <button class="ghost" data-plugin-remove="${esc(p.name)}">删除</button>
      </div>
    </div>`).join("") : `<div class="empty">还没有插件。去“市场”安装第一个。</div>`;
    bindPluginActions();
}
function renderMarket(baseUrl) {
    $("#market-base").textContent = baseUrl;
    const installed = new Set(S.plugins.map((p) => p.name));
    $("#market-plugins").innerHTML = S.marketPlugins.length ? S.marketPlugins.map((p) => `
    <div class="plugin">
      <div class="row"><h4>${esc(p.name)}</h4><span class="muted small">${esc(p.author)}</span></div>
      <p>${esc(p.description)}</p>
      <code>${esc(p.repo)}</code>
      <div class="actions">
        <button class="primary" data-market-install="${esc(p.name)}" ${installed.has(p.name) ? "disabled" : ""}>
          ${installed.has(p.name) ? "已安装" : "安装"}
        </button>
      </div>
    </div>`).join("") : `<div class="empty">市场为空，或暂时无法访问。</div>`;
    $("#market-models").innerHTML = S.marketModels.length ? `<table>
    <thead><tr><th>名称</th><th>作者</th><th>量化</th><th>上下文</th><th>说明</th><th></th></tr></thead>
    <tbody>${S.marketModels.map((m) => `<tr>
      <td><strong>${esc(m.name)}</strong></td>
      <td>${esc(m.author)}</td>
      <td>${esc(m.quant || "-")}</td>
      <td>${m.context || "-"}</td>
      <td><small class="muted">${esc(m.description)}</small></td>
      <td><button class="ghost" data-market-pull="${esc(m.name)}">下载</button></td>
    </tr>`).join("")}</tbody></table>` : `<div class="empty">模型市场为空，或暂时无法访问。</div>`;
    bindMarketActions();
}
function renderSettings() {
    const i = S.info;
    if (!i)
        return;
    $("#settings-cluster").innerHTML = [
        ["名称", i.name], ["ID", i.id], ["版本", i.version], ["协议", i.protocol],
        ["协调者", `${i.host || "?"}:${i.port}`], ["节点", i.nodes], ["TLS", i.tls ? "on" : "off"],
        ["启动", fmtTime(i.started_at)],
    ].map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join("");
    const s = S.system;
    $("#settings-paths").innerHTML = s ? [
        ["数据目录", s.data_dir], ["模型目录", s.models_dir], ["插件目录", s.plugins_dir],
        ["Web 目录", s.web_dir], ["Web 已安装", s.web_installed ? "yes" : "no（maio web install）"],
        ["市场地址", s.market_url],
    ].map(([k, v]) => `<dt>${esc(k)}</dt><dd><code>${esc(v)}</code></dd>`).join("") : "";
    $("#join-code").textContent = S.joinCode
        ? `maio cluster join '${S.joinCode}'`
        : "无法获取加入码（需要协调者在线）";
}
/* ---------- actions ---------- */
function bindPluginActions() {
    for (const el of document.querySelectorAll("[data-plugin-update]")) {
        el.onclick = async () => {
            const name = el.dataset.pluginUpdate;
            el.disabled = true;
            try {
                await api(`/v1/plugins/${encodeURIComponent(name)}/update`, { method: "POST" });
                toast(`已更新 ${name}`);
                await refreshAll();
            }
            catch (e) {
                toast(e.message, true);
                el.disabled = false;
            }
        };
    }
    for (const el of document.querySelectorAll("[data-plugin-remove]")) {
        el.onclick = async () => {
            const name = el.dataset.pluginRemove;
            if (!confirm(`删除插件 ${name}？`))
                return;
            el.disabled = true;
            try {
                await api(`/v1/plugins/${encodeURIComponent(name)}`, { method: "DELETE" });
                toast(`已删除 ${name}`);
                await refreshAll();
            }
            catch (e) {
                toast(e.message, true);
                el.disabled = false;
            }
        };
    }
}
function bindMarketActions() {
    for (const el of document.querySelectorAll("[data-market-install]")) {
        el.onclick = async () => {
            const name = el.dataset.marketInstall;
            el.disabled = true;
            el.textContent = "安装中…";
            try {
                await api("/v1/plugins/install", { method: "POST", body: JSON.stringify({ name }) });
                toast(`已安装 ${name}`);
                await refreshAll();
                await refreshMarket();
            }
            catch (e) {
                toast(e.message, true);
                el.disabled = false;
                el.textContent = "安装";
            }
        };
    }
    for (const el of document.querySelectorAll("[data-market-pull]")) {
        el.onclick = async () => {
            const name = el.dataset.marketPull;
            el.disabled = true;
            el.textContent = "下载中…";
            try {
                await api("/v1/models/pull", { method: "POST", body: JSON.stringify({ name }) });
                toast(`已下载 ${name}`);
                await refreshAll();
            }
            catch (e) {
                toast(e.message, true);
                el.disabled = false;
                el.textContent = "下载";
            }
        };
    }
}
async function pullModel(name, url) {
    const status = $("#pull-status");
    status.textContent = "下载中…大模型可能需要几分钟";
    try {
        await api("/v1/models/pull", { method: "POST", body: JSON.stringify({ name, url }) });
        status.textContent = "完成";
        toast(`已下载 ${name}`);
        await refreshAll();
    }
    catch (e) {
        status.textContent = "";
        toast(e.message, true);
    }
}
const chat = [];
function renderChat() {
    const log = $("#chat-log");
    log.innerHTML = chat.map((t) => `<div class="msg ${t.role}">${t.role === "bot" ? esc(t.text) : esc(t.text)}</div>`).join("");
    log.scrollTop = log.scrollHeight;
}
function pushChat(role, text) {
    const log = $("#chat-log");
    const div = document.createElement("div");
    div.className = `msg ${role}`;
    div.textContent = text;
    log.appendChild(div);
    log.scrollTop = log.scrollHeight;
    return div;
}
async function streamJob(id, onEvent) {
    const res = await fetch(`/v1/jobs/${encodeURIComponent(id)}/events`, {
        credentials: "same-origin",
        headers: { Accept: "text/event-stream" },
    });
    if (!res.ok || !res.body)
        throw new Error(`stream HTTP ${res.status}`);
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
        const { value, done } = await reader.read();
        if (done)
            break;
        buf += dec.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf("\n\n")) >= 0) {
            const block = buf.slice(0, idx);
            buf = buf.slice(idx + 2);
            for (const line of block.split("\n")) {
                if (line.startsWith("data: ")) {
                    try {
                        onEvent(JSON.parse(line.slice(6)));
                    }
                    catch { /* ignore malformed */ }
                }
            }
        }
    }
}
async function sendChat() {
    if (S.busy)
        return;
    const input = $("#chat-input");
    const text = input.value.trim();
    if (!text)
        return;
    const model = $("#chat-model").value;
    if (!model) {
        toast("没有可用模型，先拉取一个", true);
        return;
    }
    input.value = "";
    pushChat("user", text);
    chat.push({ role: "user", text });
    const bot = pushChat("bot", "");
    let out = "";
    const mode = $("#chat-mode").value;
    S.busy = true;
    const btn = $("#chat-form button[type=submit]");
    btn.disabled = true;
    try {
        const job = await api("/v1/jobs", {
            method: "POST",
            body: JSON.stringify({ model, prompt: text, mode, max_tokens: 256 }),
        });
        await streamJob(job.id, (ev) => {
            if (ev.type === "token" && typeof ev.text === "string") {
                out += ev.text;
                bot.textContent = out;
                bot.scrollIntoView({ block: "end" });
            }
            else if (ev.type === "done") {
                if (ev.state === "failed") {
                    out += `\n[失败] ${ev.error || ""}`;
                    bot.textContent = out;
                }
            }
        });
        chat.push({ role: "bot", text: out || "(空回复)" });
        if (!out)
            bot.textContent = "(空回复)";
    }
    catch (e) {
        bot.textContent = `[错误] ${e.message}`;
        chat.push({ role: "bot", text: `[错误] ${e.message}` });
    }
    finally {
        S.busy = false;
        btn.disabled = false;
    }
}
/* ---------- view switching ---------- */
const titles = {
    overview: "总览", chat: "对话", models: "模型", plugins: "插件", market: "市场", settings: "设置",
};
function switchView(view) {
    S.view = view;
    for (const el of document.querySelectorAll(".view"))
        el.hidden = true;
    const target = document.getElementById(`view-${view}`);
    if (target)
        target.hidden = false;
    for (const btn of document.querySelectorAll("#nav button")) {
        btn.classList.toggle("active", btn.dataset.view === view);
    }
    $("#view-title").textContent = titles[view] || view;
    if (view === "market" || view === "models") {
        if (S.marketModels.length === 0) {
            refreshMarket().catch((e) => toast(e.message, true));
        }
    }
}
/* ---------- init ---------- */
function initShell() {
    $("#nav").addEventListener("click", (ev) => {
        const btn = ev.target.closest("button[data-view]");
        if (btn)
            switchView(btn.dataset.view);
    });
    $("#refresh").addEventListener("click", () => {
        refreshAll().then(() => toast("已刷新")).catch((e) => toast(e.message, true));
    });
    $("#theme").addEventListener("click", () => {
        const root = document.documentElement;
        const next = root.dataset.theme === "dark" ? "light" : "dark";
        root.dataset.theme = next;
        localStorage.setItem("maio.theme", next);
    });
    const saved = localStorage.getItem("maio.theme");
    if (saved === "light" || saved === "dark")
        document.documentElement.dataset.theme = saved;
    $("#chat-form").addEventListener("submit", (ev) => { ev.preventDefault(); void sendChat(); });
    $("#chat-input").addEventListener("keydown", (ev) => {
        if (ev.key === "Enter" && !ev.shiftKey) {
            ev.preventDefault();
            void sendChat();
        }
    });
    $("#chat-clear").addEventListener("click", () => {
        chat.length = 0;
        $("#chat-log").innerHTML = "";
    });
    $("#pull-market").addEventListener("change", () => {
        const name = $("#pull-market").value;
        document.querySelector("#pull-name").value = name;
    });
    $("#pull-form").addEventListener("submit", (ev) => {
        ev.preventDefault();
        const url = $("#pull-url").value.trim();
        const name = $("#pull-name").value.trim();
        const sel = $("#pull-market").value;
        if (!url && !sel) {
            toast("选择市场模型或填写直链", true);
            return;
        }
        void pullModel(sel || name, url);
    });
    $("#market-refresh").addEventListener("click", () => {
        refreshMarket(true).then(() => toast("市场已刷新")).catch((e) => toast(e.message, true));
    });
    $("#copy-join").addEventListener("click", async () => {
        const text = $("#join-code").textContent || "";
        try {
            await navigator.clipboard.writeText(text);
            toast("已复制");
        }
        catch {
            toast("复制失败，请手动选择", true);
        }
    });
}
initLogin();
initShell();
void boot();
