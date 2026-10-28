"use strict";
const $ = (s) => {
    const el = document.querySelector(s);
    if (!el)
        throw new Error("missing " + s);
    return el;
};
const esc = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const clamp = (n) => Math.max(0, Math.min(100, Number.isFinite(n) ? n : 0));
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
        return (bps / 1e9).toFixed(2) + "G";
    if (bps >= 1e6)
        return (bps / 1e6).toFixed(1) + "M";
    if (bps >= 1e3)
        return (bps / 1e3).toFixed(0) + "K";
    return bps.toFixed(0);
}
function mbps(n) {
    if (!n || n <= 0)
        return "—";
    return n >= 1000 ? (n / 1000).toFixed(2) + " Gbps" : n.toFixed(0) + " Mbps";
}
function clock(mhz) {
    if (!mhz || mhz <= 0)
        return "—";
    return (mhz / 1000).toFixed(2) + " GHz";
}
function watts(w) { return w > 0 ? w.toFixed(1) + " W" : "—"; }
function temp(c) { return c > 0 ? c.toFixed(0) + "°C" : "—"; }
function timeText(s) {
    if (!s)
        return "—";
    const d = new Date(s);
    if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000)
        return "—";
    return d.toLocaleString();
}
const state = {
    info: null,
    nodes: [],
    selected: localStorage.getItem("maio.node") || "",
    view: "desktop",
    filter: "all",
    search: "",
    timer: 0,
    busy: false,
    history: new Map(),
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
        state.timer = window.setInterval(() => { void refresh(true); }, 2000);
    }
    catch (e) {
        showLogin(e.message === "unauthorized" ? "" : e.message);
    }
}
function aggregate(n) {
    const m = n.metrics || {};
    let used = 0, total = 0, power = 0, limit = 0, max = 0, temp = 0, memTemp = 0, bw = 0;
    for (const g of m.gpus || []) {
        used += g.vram_used || 0;
        total += g.vram_total || 0;
        power += g.power_w || 0;
        limit += g.power_limit_w || 0;
        max += g.power_max_w || 0;
        temp = Math.max(temp, g.temp_c || 0);
        memTemp = Math.max(memTemp, g.mem_temp_c || 0);
        bw = Math.max(bw, g.pcie_gbs || 0);
    }
    const pct = total ? 100 * used / total : 0;
    const rate = (m.rx_bps || 0) + (m.tx_bps || 0);
    const link = (m.net_link_mbps || 0) * 1e6;
    const loadPct = clamp(((m.cpu_percent || 0) + (m.mem_percent || 0) + pct) / 3);
    const loadState = loadPct < 50 ? "流畅" : loadPct <= 90 ? "平稳" : "繁忙";
    return { used, total, pct, power, limit, max, temp, memTemp, bw, rate, link, loadPct, loadState };
}
function sample(n) {
    const m = n.metrics || {};
    const a = aggregate(n);
    return {
        t: Date.now(), cpu: m.cpu_percent || 0, mem: m.mem_percent || 0, vram: a.pct, load: a.loadPct,
        rx: m.rx_bps || 0, tx: m.tx_bps || 0, gpuPower: a.power, gpuLimit: a.limit, gpuMax: a.max,
        cpuPower: m.cpu_power_w || 0, cpuMax: m.cpu_power_max_w || 0,
        cpuTemp: m.cpu_temp_c || 0, gpuTemp: a.temp,
    };
}
function record() {
    for (const n of state.nodes) {
        const list = state.history.get(n.id) || [];
        list.push(sample(n));
        if (list.length > 450)
            list.splice(0, list.length - 450);
        state.history.set(n.id, list);
    }
}
function hist(id) { return state.history.get(id) || []; }
async function refresh(silent = false) {
    if (state.busy)
        return;
    state.busy = true;
    try {
        const [info, nodes] = await Promise.all([api("/v1/info"), api("/v1/nodes")]);
        state.info = info;
        state.nodes = nodes.nodes || [];
        record();
        if (!state.nodes.some((n) => n.id === state.selected)) {
            const first = state.nodes.find((n) => n.state === "online") || state.nodes[0];
            state.selected = first?.id || "";
        }
        if (state.selected)
            localStorage.setItem("maio.node", state.selected);
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
function render() {
    const online = state.nodes.filter((n) => n.state === "online").length;
    $("#node-count").textContent = `${online}/${state.nodes.length}`;
    $("#side-node-count").textContent = `${online}/${state.nodes.length}`;
    $("#app-version").textContent = state.info ? `v${state.info.version}` : "v—";
    $("#desktop-view").hidden = state.view !== "desktop";
    $("#detail-view").hidden = state.view !== "detail";
    if (state.view === "detail" && state.nodes.some((n) => n.id === state.selected))
        renderDetail();
    else
        renderDesktop();
}
function filtered() {
    const q = state.search.trim().toLowerCase();
    return state.nodes.filter((n) => {
        if (state.filter === "online" && n.state !== "online")
            return false;
        if (state.filter === "offline" && n.state === "online")
            return false;
        if (!q)
            return true;
        return [n.name, n.host, n.role, n.id].some((v) => String(v).toLowerCase().includes(q));
    });
}
function renderDesktop() {
    const rows = filtered();
    $("#empty").hidden = rows.length > 0;
    $("#nodes-body").innerHTML = rows.map((n) => {
        const a = aggregate(n);
        const h = hist(n.id);
        const online = n.state === "online";
        return `<tr data-id="${esc(n.id)}">
      <td><div class="node-name">${esc(n.name)}</div></td>
      <td><span class="host">${esc(n.host || "—")}</span></td>
      <td><span class="status ${online ? "" : "off"}"><i></i>${online ? "在线" : "离线"}</span></td>
      <td><div class="cell-metric"><b>${n.hardware.cpu_cores} 核 / ${(n.metrics.cpu_percent || 0).toFixed(1)}%</b>${sparkline(h.map((s) => s.cpu), "var(--purple)")}</div></td>
      <td><div class="cell-metric"><b>${bytes(n.metrics.mem_used)} / ${bytes(n.metrics.mem_total)}</b>${sparkline(h.map((s) => s.mem), "var(--amber)")}</div></td>
      <td><div class="cell-metric"><b>${bytes(a.used)} / ${bytes(a.total)}</b>${sparkline(h.map((s) => s.vram), "var(--cyan)")}</div></td>
      <td>${esc(n.role)}</td>
      <td><div class="ops"><button class="op" data-open="${esc(n.id)}" title="查看性能">◉</button></div></td>
    </tr>`;
    }).join("");
}
function sparkline(values, color) {
    const w = 96, h = 14, pad = 1;
    const v = values.slice(-60);
    if (v.length < 2)
        return `<svg class="spark" viewBox="0 0 ${w} ${h}"><line x1="0" y1="${h - 2}" x2="${w}" y2="${h - 2}" stroke="#3a3a42"/></svg>`;
    const max = Math.max(100, ...v), min = 0;
    const pts = v.map((n, i) => {
        const x = pad + (i / (v.length - 1)) * (w - pad * 2);
        const y = h - pad - ((n - min) / (max - min || 1)) * (h - pad * 2);
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(" ");
    return `<svg class="spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none"><polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.4"/></svg>`;
}
function gauge(pct, color) {
    const p = clamp(pct), c = 2 * Math.PI * 26, off = c * (1 - p / 100);
    return `<svg class="mini-gauge" viewBox="0 0 68 68">
    <circle class="track" cx="34" cy="34" r="26"></circle>
    <circle class="value" cx="34" cy="34" r="26" stroke="${color}" stroke-dasharray="${c.toFixed(1)}" stroke-dashoffset="${off.toFixed(1)}"></circle>
    <text x="34" y="37.5">${p.toFixed(0)}%</text></svg>`;
}
function card(title, icon, pct, color, main, sub, stateText = "") {
    return `<article class="card">
    <div class="card-top"><div class="card-title"><span class="card-ico">${icon}</span>${esc(title)}</div>${gauge(pct, color)}</div>
    <div class="card-main"><b>${main}</b>${stateText ? `<span class="state ${color === "var(--red)" ? "red" : color === "var(--amber)" ? "amber" : "green"}">${esc(stateText)}</span>` : ""}</div>
    <div class="card-sub">${sub}</div>
  </article>`;
}
function openNode(id) {
    state.selected = id;
    state.view = "detail";
    localStorage.setItem("maio.node", id);
    render();
    $(".content").scrollTop = 0;
}
function openDesktop() {
    state.view = "desktop";
    render();
}
function renderDetail() {
    const n = state.nodes.find((x) => x.id === state.selected);
    if (!n) {
        openDesktop();
        return;
    }
    const m = n.metrics;
    const a = aggregate(n);
    const h = hist(n.id);
    const cpuPct = clamp(m.cpu_percent || 0);
    const usedCores = (n.hardware.cpu_cores || 0) * cpuPct / 100;
    const loadClass = a.loadPct < 50 ? "var(--green)" : a.loadPct <= 90 ? "var(--amber)" : "var(--red)";
    $("#detail-name").textContent = n.name;
    $("#detail-sub").textContent = `${n.host || "—"} · ${n.role} · ${n.os}/${n.arch} · v${n.version}`;
    const st = $("#detail-state");
    st.textContent = n.state === "online" ? "在线" : "离线";
    st.style.color = n.state === "online" ? "var(--green)" : "var(--red)";
    $("#cards").innerHTML = [
        card("负载", "◇", a.loadPct, loadClass, `${a.loadPct.toFixed(1)}%`, `CPU ${(m.cpu_percent || 0).toFixed(0)}% · 内存 ${(m.mem_percent || 0).toFixed(0)}% · 显存 ${a.pct.toFixed(0)}%`, a.loadState),
        card("CPU", "▤", cpuPct, "var(--purple)", `${cpuPct.toFixed(1)}%`, `${usedCores.toFixed(1)} / ${n.hardware.cpu_cores} 核 · ${clock(m.cpu_freq_mhz)}<br>${temp(m.cpu_temp_c)} · 功耗 ${watts(m.cpu_power_w)} / 最大 ${watts(m.cpu_power_max_w)}`),
        card("内存", "▣", clamp(m.mem_percent || 0), "var(--amber)", `${bytes(m.mem_used)} / ${bytes(m.mem_total)}`, `频率 ${m.mem_freq_mhz ? m.mem_freq_mhz + " MHz" : "—"}`),
        card("显存", "▦", a.pct, "var(--cyan)", `${bytes(a.used)} / ${bytes(a.total)}`, `功耗 ${watts(a.power)} / ${watts(a.limit)} · ${temp(a.temp)}<br>带宽 ${a.bw ? a.bw.toFixed(1) + " GB/s" : "—"}`),
        card("流量", "◈", a.link ? clamp(100 * a.rate / a.link) : 0, "var(--blue)", `${bitrate(a.rate)}/s`, `↑ ${bitrate(m.tx_bps || 0)}/s · ↓ ${bitrate(m.rx_bps || 0)}/s<br>带宽 ${mbps(m.net_link_mbps || 0)}`),
    ].join("");
    $("#load-chart").innerHTML = lineChart(h, [{ key: "load", color: "var(--purple)" }], 100, "%");
    $("#load-details").innerHTML = `
    <dt>CPU</dt><dd>${(m.cpu_percent || 0).toFixed(1)}%</dd>
    <dt>内存</dt><dd>${(m.mem_percent || 0).toFixed(1)}%</dd>
    <dt>显存</dt><dd>${a.pct.toFixed(1)}%</dd>`;
    $("#cpu-model").textContent = n.hardware.cpu_model;
    $("#cpu-chart").innerHTML = lineChart(h, [{ key: "cpu", color: "var(--purple)" }], 100, "%");
    $("#cpu-details").innerHTML = `
    <dt>核心</dt><dd>${n.hardware.cpu_cores}</dd>
    <dt>频率</dt><dd>${clock(m.cpu_freq_mhz)} / ${clock(m.cpu_freq_max_mhz)}</dd>
    <dt>温度</dt><dd>${temp(m.cpu_temp_c)}</dd>
    <dt>功耗</dt><dd>${watts(m.cpu_power_w)} / ${watts(m.cpu_power_max_w)}</dd>`;
    $("#gpu-models").innerHTML = (n.hardware.gpus || []).map((g) => `<div class="gpu-model"><b>GPU ${g.index}</b><span>${esc(g.name)}</span></div>`).join("") || `<div class="muted">没有检测到 GPU</div>`;
    renderConfig(n);
}
function lineChart(h, series, max, unit) {
    const w = 660, ht = 150, l = 34, r = 8, t = 8, b = 18;
    const innerW = w - l - r, innerH = ht - t - b;
    const pts = h.slice(-120);
    if (!pts.length)
        return `<div class="muted" style="padding:22px 0">等待采样…</div>`;
    const y = (v) => t + innerH - Math.max(0, Math.min(1, v / max)) * innerH;
    const x = (i) => l + (pts.length === 1 ? innerW : (i / (pts.length - 1)) * innerW);
    const grid = [0, .25, .5, .75, 1].map((f) => {
        const yy = t + innerH - f * innerH;
        return `<line class="grid" x1="${l}" y1="${yy}" x2="${w - r}" y2="${yy}"/><text class="axis" x="4" y="${yy + 3}">${(max * f).toFixed(max <= 10 ? 1 : 0)}${unit}</text>`;
    }).join("");
    const paths = series.map((s) => {
        const d = pts.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(Number(p[s.key])).toFixed(1)}`).join(" ");
        return `<path class="line" d="${d}" stroke="${s.color}"/>`;
    }).join("");
    return `<svg viewBox="0 0 ${w} ${ht}" preserveAspectRatio="none">${grid}${paths}
    <text class="axis" x="${l}" y="${ht - 4}">-${Math.round(pts.length * 2 / 60)}m</text>
    <text class="axis" x="${w - r - 20}" y="${ht - 4}">now</text></svg>`;
}
function renderConfig(n) {
    const m = n.metrics;
    const a = aggregate(n);
    const port = n.api_port || (n.role === "coordinator" ? (state.info?.port || 0) : 0);
    const addr = port ? `${n.host}:${port}` : n.host;
    const gpus = (m.gpus || []).map((g) => {
        const info = (n.hardware.gpus || []).find((x) => x.index === g.index);
        return `<div class="gpu"><div class="gpu-head">GPU ${g.index} · ${esc(info?.name || "unknown")}</div>
      <dl class="kv compact">
        <dt>显存</dt><dd>${bytes(g.vram_used)} / ${bytes(g.vram_total)} (${g.vram_percent.toFixed(1)}%)</dd>
        <dt>功耗</dt><dd>${watts(g.power_w)} / ${watts(g.power_limit_w)}（最大 ${watts(g.power_max_w)}）</dd>
        <dt>温度</dt><dd>${temp(g.temp_c)} / 显存 ${temp(g.mem_temp_c)}</dd>
        <dt>利用率</dt><dd>${g.utilization.toFixed(0)}%</dd>
        <dt>显存频率</dt><dd>${g.mem_clock_mhz ? g.mem_clock_mhz + " MHz" : "—"}</dd>
        <dt>PCIe</dt><dd>${g.pcie_gen ? `Gen${g.pcie_gen} x${g.pcie_width} · ${g.pcie_gbs.toFixed(1)} GB/s` : "—"}</dd>
        <dt>驱动</dt><dd>${esc(info?.driver || "—")}</dd>
        <dt>计算能力</dt><dd>${esc(info?.compute || "—")}</dd>
      </dl></div>`;
    }).join("") || `<div class="muted">没有检测到 GPU</div>`;
    const models = (n.models || []).map((x) => `${x.name}${x.quant ? ` (${x.quant})` : ""}`).join("、") || "—";
    const links = (n.links || []).map((l) => `${l.kind} ${l.ip}`).join("；") || "—";
    $("#config-body").innerHTML = `
    <div class="config-section"><h3>节点</h3><dl class="kv compact">
      <dt>名称</dt><dd>${esc(n.name)}</dd><dt>节点 ID</dt><dd>${esc(n.id)}</dd>
      <dt>角色</dt><dd>${esc(n.role)} / ${esc(n.state)}</dd><dt>地址</dt><dd>${esc(addr)}</dd>
      <dt>系统</dt><dd>${esc(n.os)} / ${esc(n.arch)}</dd><dt>版本</dt><dd>${esc(n.version)} (${n.protocol})</dd>
      <dt>加入</dt><dd>${timeText(n.joined_at)}</dd><dt>链路</dt><dd>${esc(links)}</dd>
      <dt>模型</dt><dd>${esc(models)}</dd><dt>RPC</dt><dd>${n.rpc.available ? "online" : "off"}</dd>
    </dl></div>
    <div class="config-section"><h3>CPU / 内存 / 磁盘</h3><dl class="kv compact">
      <dt>CPU</dt><dd>${esc(n.hardware.cpu_model)}</dd><dt>核心</dt><dd>${n.hardware.cpu_cores}</dd>
      <dt>频率</dt><dd>${clock(m.cpu_freq_mhz)} / ${clock(m.cpu_freq_max_mhz)}</dd><dt>温度</dt><dd>${temp(m.cpu_temp_c)}</dd>
      <dt>CPU 功耗</dt><dd>${watts(m.cpu_power_w)} / ${watts(m.cpu_power_max_w)}</dd>
      <dt>内存</dt><dd>${bytes(m.mem_used)} / ${bytes(m.mem_total)}</dd><dt>内存频率</dt><dd>${m.mem_freq_mhz ? m.mem_freq_mhz + " MHz" : "—"}</dd>
      <dt>磁盘</dt><dd>${esc(n.hardware.disk_path)} · 可用 ${bytes(n.hardware.disk_free)}</dd><dt>显存总功耗</dt><dd>${watts(a.power)} / ${watts(a.limit)}</dd>
    </dl></div>
    <div class="config-section"><h3>GPU（${(n.hardware.gpus || []).length}）</h3><div class="gpu-list">${gpus}</div></div>`;
}
function init() {
    $("#login-form").addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const input = $("#login-token");
        const token = input.value.trim();
        const err = $("#login-error");
        err.textContent = "";
        if (!token) {
            err.textContent = "请输入 Token";
            return;
        }
        const btn = $("#login-form button[type=submit]");
        btn.disabled = true;
        try {
            await api("/v1/web/login", { method: "POST", body: JSON.stringify({ token }) });
            input.value = "";
            await boot();
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
    $("#refresh").addEventListener("click", () => { void refresh(); });
    $("#back").addEventListener("click", openDesktop);
    document.querySelector(".nav-item")?.addEventListener("click", openDesktop);
    $("#search").addEventListener("input", (ev) => {
        state.search = ev.target.value;
        renderDesktop();
    });
    document.querySelectorAll(".group-tab").forEach((b) => {
        b.addEventListener("click", () => {
            state.filter = b.dataset.filter || "all";
            document.querySelectorAll(".group-tab").forEach((x) => x.classList.toggle("active", x === b));
            renderDesktop();
        });
    });
    $("#nodes-body").addEventListener("click", (ev) => {
        const t = ev.target;
        const open = t.closest("[data-open]");
        const row = t.closest("tr[data-id]");
        if (open) {
            openNode(open.dataset.open);
            return;
        }
        if (row)
            openNode(row.dataset.id);
    });
    void boot();
}
init();
