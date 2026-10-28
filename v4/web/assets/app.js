"use strict";
const $ = (s) => {
    const el = document.querySelector(s);
    if (!el)
        throw new Error("missing " + s);
    return el;
};
const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
function bytes(n) {
    if (!n || n <= 0)
        return "—";
    const u = ["B", "KiB", "MiB", "GiB", "TiB"];
    let v = n, i = 0;
    while (v >= 1024 && i < u.length - 1) {
        v /= 1024;
        i++;
    }
    return `${v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)} ${u[i]}`;
}
function bitrate(bps) {
    if (!bps || bps <= 0)
        return "0";
    if (bps >= 1e9)
        return (bps / 1e9).toFixed(2) + " Gbps";
    if (bps >= 1e6)
        return (bps / 1e6).toFixed(1) + " Mbps";
    if (bps >= 1e3)
        return (bps / 1e3).toFixed(0) + " Kbps";
    return bps.toFixed(0) + " bps";
}
function mbps(mbps) {
    if (!mbps || mbps <= 0)
        return "—";
    if (mbps >= 1000)
        return (mbps / 1000).toFixed(mbps % 1000 ? 2 : 0) + " Gbps";
    return mbps.toFixed(0) + " Mbps";
}
function clock(mhz) {
    if (!mhz || mhz <= 0)
        return "—";
    return (mhz / 1000).toFixed(2) + " GHz";
}
function timeText(s) {
    if (!s)
        return "—";
    const d = new Date(s);
    if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000)
        return "—";
    return d.toLocaleString();
}
const clamp = (n) => Math.max(0, Math.min(100, Number.isFinite(n) ? n : 0));
const state = {
    info: null,
    nodes: [],
    selected: localStorage.getItem("maio.node") || "",
    timer: 0,
    busy: false,
};
async function api(path, init) {
    const res = await fetch(path, {
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
        ...init,
    });
    if (res.status === 401 && !path.endsWith("/v1/web/login")) {
        showLogin("登录已过期，请重新输入 Token");
        throw new Error("unauthorized");
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
        throw new Error(data?.error || `HTTP ${res.status}`);
    return data;
}
function showLogin(message = "") {
    $("#app").hidden = true;
    $("#login").hidden = false;
    $("#login-error").textContent = message;
    window.clearInterval(state.timer);
}
function showApp() {
    $("#login").hidden = true;
    $("#app").hidden = false;
}
async function boot() {
    try {
        await api("/v1/web/me");
        showApp();
        await refresh();
        window.clearInterval(state.timer);
        state.timer = window.setInterval(() => { void refresh(true); }, 3000);
    }
    catch (e) {
        showLogin(e.message === "unauthorized" ? "" : e.message);
    }
}
async function login(token) {
    await api("/v1/web/login", { method: "POST", body: JSON.stringify({ token }) });
    await boot();
}
function selectedNode() {
    return state.nodes.find((n) => n.id === state.selected) || null;
}
async function refresh(silent = false) {
    if (state.busy)
        return;
    state.busy = true;
    try {
        const [info, nodes] = await Promise.all([
            api("/v1/info"),
            api("/v1/nodes"),
        ]);
        state.info = info;
        state.nodes = nodes.nodes || [];
        if (!state.nodes.some((n) => n.id === state.selected)) {
            const online = state.nodes.find((n) => n.state === "online") || state.nodes[0];
            state.selected = online?.id || "";
            if (state.selected)
                localStorage.setItem("maio.node", state.selected);
        }
        render();
    }
    catch (e) {
        if (!silent)
            showLogin(e.message);
    }
    finally {
        state.busy = false;
    }
}
function loadInfo(n) {
    const m = n.metrics || {};
    let pct = m.load_percent || 0;
    if (!pct) {
        pct = Math.max(m.cpu_percent || 0, m.mem_percent || 0, ...(m.gpus || []).map((g) => g.vram_percent || 0));
    }
    pct = clamp(pct);
    const label = pct < 50 ? "流畅" : pct <= 90 ? "平稳" : "繁忙";
    const color = pct < 50 ? "var(--green)" : pct <= 90 ? "var(--amber)" : "var(--red)";
    return { pct, state: label, color };
}
function gauge(percent, unit, color) {
    const p = clamp(percent);
    const c = 2 * Math.PI * 52;
    const offset = c * (1 - p / 100);
    return `<svg class="gauge" viewBox="0 0 120 120" role="img" aria-label="${p.toFixed(0)}%">
    <circle class="track" cx="60" cy="60" r="52"></circle>
    <circle class="value" cx="60" cy="60" r="52" stroke="${color}"
      stroke-dasharray="${c.toFixed(1)}" stroke-dashoffset="${offset.toFixed(1)}"></circle>
    <text class="pct" x="60" y="64">${p.toFixed(0)}%</text>
    <text class="unit" x="60" y="82">${esc(unit)}</text>
  </svg>`;
}
function metricCard(title, percent, unit, color, primary, sub, stateText = "", stateClass = "") {
    return `<article class="metric">
    <div class="metric-title">${esc(title)}</div>
    ${gauge(percent, unit, color)}
    ${stateText ? `<div class="state ${stateClass}">${esc(stateText)}</div>` : ""}
    <div class="primary-value">${primary}</div>
    <div class="sub">${sub}</div>
  </article>`;
}
function render() {
    const info = state.info;
    const nodes = state.nodes;
    const online = nodes.filter((n) => n.state === "online").length;
    $("#node-count").textContent = `${online}/${nodes.length}`;
    $("#app-version").textContent = info ? `v${info.version}` : "v—";
    const sel = $("#node-select");
    sel.innerHTML = nodes.length
        ? nodes.map((n) => `<option value="${esc(n.id)}" ${n.id === state.selected ? "selected" : ""}>${esc(n.name)} · ${esc(n.role)} · ${esc(n.state)}</option>`).join("")
        : `<option value="">没有节点</option>`;
    const n = selectedNode();
    if (!n) {
        $("#metrics").innerHTML = `<div class="empty">没有节点。启动协调者后这里会出现节点。</div>`;
        $("#config-body").innerHTML = `<div class="empty">没有可显示的节点配置。</div>`;
        $("#config-hint").textContent = "";
        return;
    }
    const m = n.metrics || {};
    const load = loadInfo(n);
    const cores = n.hardware.cpu_cores || 0;
    const usedCores = cores * clamp(m.cpu_percent) / 100;
    const vram = (m.gpus || []).reduce((a, g) => ({
        used: a.used + (g.vram_used || 0),
        total: a.total + (g.vram_total || 0),
        util: Math.max(a.util, g.utilization || 0),
        bw: Math.max(a.bw, g.pcie_gbs || 0),
        pct: 0,
    }), { used: 0, total: 0, util: 0, bw: 0, pct: 0 });
    vram.pct = vram.total ? 100 * vram.used / vram.total : 0;
    const rate = (m.rx_bps || 0) + (m.tx_bps || 0);
    const linkBps = (m.net_link_mbps || 0) * 1e6;
    const netPct = linkBps > 0 ? 100 * rate / linkBps : 0;
    $("#metrics").innerHTML = [
        metricCard("负载", load.pct, "负载", load.color, `CPU ${(m.cpu_percent || 0).toFixed(0)}% · 内存 ${(m.mem_percent || 0).toFixed(0)}% · 显存 ${vram.pct.toFixed(0)}%`, "", load.state, load.pct < 50 ? "green" : load.pct <= 90 ? "amber" : "red"),
        metricCard("CPU", m.cpu_percent || 0, "占用", "var(--accent)", `${usedCores.toFixed(1)} / ${cores} 核`, `频率 <b>${clock(m.cpu_freq_mhz)}</b> / ${clock(m.cpu_freq_max_mhz)}`),
        metricCard("内存", m.mem_percent || 0, "占用", "var(--green)", `${bytes(m.mem_used)} / ${bytes(m.mem_total)}`, `频率 <b>${m.mem_freq_mhz ? m.mem_freq_mhz + " MHz" : "—"}</b>`),
        metricCard("显存", vram.pct, "占用", "var(--cyan, #55d6ff)", `${bytes(vram.used)} / ${bytes(vram.total)}`, `带宽 <b>${vram.bw ? vram.bw.toFixed(1) + " GB/s" : "—"}</b>`),
        metricCard("流量", netPct, "带宽", "var(--amber)", `${bitrate(rate)} / ${linkBps ? mbps(m.net_link_mbps) : "—"}`, `↓ <b>${bitrate(m.rx_bps || 0)}</b> · ↑ <b>${bitrate(m.tx_bps || 0)}</b><br>带宽 <b>${m.net_link_mbps ? mbps(m.net_link_mbps) : "—"}</b>`),
    ].join("");
    renderConfig(n);
}
function renderConfig(n) {
    const m = n.metrics || {};
    const port = n.api_port || (n.role === "coordinator" ? (state.info?.port || 0) : 0);
    const addr = port ? `${n.host}:${port}` : n.host;
    const gpus = m.gpus && m.gpus.length
        ? m.gpus.map((g) => {
            const info = (n.hardware.gpus || []).find((x) => x.index === g.index);
            return `<div class="gpu">
          <div class="gpu-head">GPU ${g.index} · ${esc(info?.name || "unknown")}
            <span>${esc(info?.backend || "")}</span></div>
          <dl class="kv">
            <dt>显存</dt><dd>${bytes(g.vram_used)} / ${bytes(g.vram_total)} (${g.vram_percent.toFixed(1)}%)</dd>
            <dt>利用率</dt><dd>${g.utilization.toFixed(0)}%</dd>
            <dt>显存频率</dt><dd>${g.mem_clock_mhz ? g.mem_clock_mhz + " MHz" : "—"}</dd>
            <dt>PCIe</dt><dd>${g.pcie_gen ? `Gen${g.pcie_gen} x${g.pcie_width} · ${g.pcie_gbs.toFixed(1)} GB/s` : "—"}</dd>
            <dt>驱动</dt><dd>${esc(info?.driver || "—")}</dd>
            <dt>计算能力</dt><dd>${esc(info?.compute || "—")}</dd>
          </dl>
        </div>`;
        }).join("")
        : (n.hardware.gpus || []).map((g) => `<div class="gpu">
        <div class="gpu-head">GPU ${g.index} · ${esc(g.name)}<span>${esc(g.backend)}</span></div>
        <dl class="kv">
          <dt>显存</dt><dd>${bytes(g.vram_total)}（空闲 ${bytes(g.vram_free)}）</dd>
          <dt>驱动</dt><dd>${esc(g.driver || "—")}</dd>
          <dt>计算能力</dt><dd>${esc(g.compute || "—")}</dd>
        </dl>
      </div>`).join("");
    const modelList = (n.models || []).length
        ? (n.models || []).map((x) => `${esc(x.name)}${x.quant ? ` (${esc(x.quant)})` : ""}`).join("、")
        : "—";
    const links = (n.links || []).map((l) => `${esc(l.kind)} ${esc(l.ip)}${l.interface ? ` (${esc(l.interface)})` : ""}`).join("；") || "—";
    const warnings = (n.hardware.warnings || []).length
        ? `<div class="config-section"><h3>警告</h3><div class="sub">${n.hardware.warnings.map(esc).join("<br>")}</div></div>`
        : "";
    $("#config-hint").textContent = `节点 ${n.name} · 最后心跳 ${timeText(n.last_seen)}`;
    $("#config-body").innerHTML = `
    <div class="config-section">
      <h3>节点</h3>
      <dl class="kv">
        <dt>名称</dt><dd>${esc(n.name)}</dd>
        <dt>节点 ID</dt><dd>${esc(n.id)}</dd>
        <dt>角色 / 状态</dt><dd>${esc(n.role)} / ${esc(n.state)}</dd>
        <dt>地址</dt><dd>${esc(addr)}</dd>
        <dt>系统</dt><dd>${esc(n.os)} / ${esc(n.arch)}</dd>
        <dt>版本</dt><dd>${esc(n.version)} (protocol ${n.protocol})</dd>
        <dt>加入 / 心跳</dt><dd>${timeText(n.joined_at)} / ${timeText(n.last_seen)}</dd>
        <dt>RPC</dt><dd>${n.rpc.available ? "online" : "off"}${n.rpc.error ? ` · ${esc(n.rpc.error)}` : ""}</dd>
        <dt>链路</dt><dd>${links}</dd>
        <dt>模型</dt><dd>${modelList}</dd>
      </dl>
    </div>
    <div class="config-section">
      <h3>CPU / 内存 / 磁盘</h3>
      <dl class="kv">
        <dt>CPU 型号</dt><dd>${esc(n.hardware.cpu_model)}</dd>
        <dt>核心</dt><dd>${n.hardware.cpu_cores}</dd>
        <dt>频率</dt><dd>${clock(m.cpu_freq_mhz)} / 最大 ${clock(m.cpu_freq_max_mhz)}</dd>
        <dt>内存</dt><dd>${bytes(m.mem_used)} / ${bytes(m.mem_total)}（可用 ${bytes(n.hardware.mem_free)}）</dd>
        <dt>内存频率</dt><dd>${m.mem_freq_mhz ? m.mem_freq_mhz + " MHz" : "—"}</dd>
        <dt>磁盘</dt><dd>${esc(n.hardware.disk_path)} · 可用 ${bytes(n.hardware.disk_free)}</dd>
      </dl>
    </div>
    <div class="config-section">
      <h3>GPU（${(n.hardware.gpus || []).length}）</h3>
      <div class="gpu-list">${gpus || `<div class="empty">没有检测到 GPU</div>`}</div>
    </div>
    ${warnings}`;
}
function init() {
    $("#login-form").addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const token = $("#login-token").value.trim();
        const err = $("#login-error");
        err.textContent = "";
        if (!token) {
            err.textContent = "请输入 Token";
            return;
        }
        const btn = $("#login-form button[type=submit]");
        btn.disabled = true;
        try {
            await login(token);
            $("#login-token").value = "";
        }
        catch (e) {
            err.textContent = e.message;
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
    $("#node-select").addEventListener("change", () => {
        state.selected = $("#node-select").value;
        localStorage.setItem("maio.node", state.selected);
        render();
    });
    void boot();
}
init();
