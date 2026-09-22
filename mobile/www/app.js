// ------------------------------------------------------------------
// Mobile-only layer, loaded before main.js.
//
// The panel is served by elite_mon itself, so it can fetch "/api/status" as a
// relative path. The app cannot: its pages come from the phone, which makes its
// origin http://localhost -- a relative fetch would ask the phone, not the PC.
// So the PC's address is kept here (localStorage) and prefixed onto every API
// call. main.js only knows about apiBase() / setServerStatus() / showSetup().
//
// This layer also owns the bottom tab bar: four fixed pages (monitor /
// bounty / events / settings) instead of one long scrolling page.
// ------------------------------------------------------------------

const SERVER_KEY = "elitemon.server";
const HISTORY_KEY = "elitemon.history";
const TAB_KEY = "elitemon.tab";
const TABS = ["monitor", "bounty", "events", "settings"];
const HISTORY_MAX = 8;

// Accepts a bare IP ("192.168.1.5"), host:port, or a full URL. A missing
// scheme becomes http:// and a missing port becomes the panel's default 8088,
// so typing just the IP is enough to connect.
function normalizeAddr(raw){
    let s = String(raw || "").trim().replace(/\/+$/, "");
    if(!s){ return ""; }
    if(!/^https?:\/\//i.test(s)){ s = "http://" + s; }
    // No port and no path after the host: append the default port.
    if(/^https?:\/\/[^/:]+$/i.test(s)){ s += ":8088"; }
    return s;
}

function apiBase(){
    return normalizeAddr(localStorage.getItem(SERVER_KEY));
}

// ------------------------------------------------------------------
// Connection history: every address that ever connected successfully, most
// recent first, so a known PC can be re-picked with one tap instead of
// retyped. Only successful connections are recorded -- a typo the probe
// rejected never pollutes the list.
// ------------------------------------------------------------------
function getHistory(){
    try{
        const raw = JSON.parse(localStorage.getItem(HISTORY_KEY) || "[]");
        return Array.isArray(raw) ? raw.filter(function(a){ return typeof a === "string" && a; }) : [];
    }catch(e){
        return [];   // a corrupted value must not break the settings page
    }
}

function rememberServer(addr){
    if(!addr){ return; }
    const next = [addr].concat(getHistory().filter(function(a){ return a !== addr; }));
    try{ localStorage.setItem(HISTORY_KEY, JSON.stringify(next.slice(0, HISTORY_MAX))); }catch(e){}
}

function forgetServer(addr){
    const next = getHistory().filter(function(a){ return a !== addr; });
    try{ localStorage.setItem(HISTORY_KEY, JSON.stringify(next)); }catch(e){}
}

// Render the chips. Hidden entirely on first run, when there is nothing to show.
function renderHistory(){
    const box = document.getElementById("server-history");
    if(!box){ return; }
    const list = getHistory();
    const current = apiBase();
    if(!list.length){ box.hidden = true; box.innerHTML = ""; return; }
    box.hidden = false;
    let s = '<div class="hist-title">历史地址</div><div class="hist-chips">';
    list.forEach(function(addr){
        s += '<span class="hist-chip' + (addr === current ? " current" : "") + '">'
            + '<button type="button" class="hist-use" data-addr="' + escAttr(addr) + '">'
            + escHtml(addr) + '</button>'
            + '<button type="button" class="hist-del" data-addr="' + escAttr(addr) + '"'
            + ' title="移除">×</button>'
            + '</span>';
    });
    s += '</div>';
    box.innerHTML = s;

    box.querySelectorAll(".hist-use").forEach(function(b){
        b.addEventListener("click", function(){ useHistory(b.dataset.addr); });
    });
    box.querySelectorAll(".hist-del").forEach(function(b){
        b.addEventListener("click", function(ev){ ev.stopPropagation(); dropHistory(b.dataset.addr); });
    });
}

function escHtml(s){
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
function escAttr(s){
    return escHtml(s).replace(/"/g, "&quot;");
}

// Fill the field and probe it, exactly like typing it by hand.
function useHistory(addr){
    const input = document.getElementById("server-input");
    if(input){ input.value = addr; }
    saveServer();
}

// A chip can be removed without leaving the settings page.
function dropHistory(addr){
    forgetServer(addr);
    renderHistory();
    const m = document.getElementById("setup-msg");
    if(m){
        m.textContent = apiBase() === addr
            ? "已从历史中移除（当前连接不受影响，仍在使用该地址）"
            : "已从历史中移除";
        m.className = "setup-msg info";
    }
}

// ------------------------------------------------------------------
// Tab bar. Pages stay in the DOM while hidden, so main.js can keep
// rendering into them no matter which tab is on screen.
// ------------------------------------------------------------------
function switchTab(name){
    if(TABS.indexOf(name) < 0){ name = "monitor"; }
    TABS.forEach(function(t){
        const page = document.getElementById("page-" + t);
        if(page){ page.hidden = (t !== name); }
        const btn = document.querySelector('#tabbar button[data-tab="' + t + '"]');
        if(btn){ btn.className = (t === name) ? "active" : ""; }
    });
    // Show the stored address as the starting point for edits, plus the list
    // of addresses that connected before (so a known PC is one tap away).
    if(name === "settings"){
        const input = document.getElementById("server-input");
        if(input && document.activeElement !== input){ input.value = apiBase(); }
        renderHistory();
    }
    try{ localStorage.setItem(TAB_KEY, name); }catch(e){}
    window.scrollTo(0, 0);
}

function currentTab(){
    try{
        const t = localStorage.getItem(TAB_KEY);
        if(t && TABS.indexOf(t) >= 0){ return t; }
    }catch(e){}
    return "monitor";
}

// main.js calls this when no server is configured yet (and via the gear).
// It now lands on the settings tab instead of popping a sheet.
function showSetup(msg){
    switchTab("settings");
    const m = document.getElementById("setup-msg");
    if(m){ m.textContent = msg || ""; m.className = "setup-msg"; }
    const input = document.getElementById("server-input");
    if(input){
        input.value = apiBase();
        setTimeout(function(){ input.focus(); }, 50);
    }
    renderHistory();
}

// Shown under the title so the address being polled is never a guess.
function setServerStatus(state, base){
    const line = document.getElementById("server-line");
    if(!line){ return; }
    const label = state === "ok" ? "已连接" : (state === "err" ? "连接失败" : "连接中");
    line.className = "server-line " + (state === "ok" ? "good" : (state === "err" ? "warn" : "info"));
    line.textContent = label + " · " + (base || "未设置服务器");
}

// Ask the PC once before storing the address, so a typo is reported here instead
// of turning into an endless "connection failed" loop on the dashboard.
async function saveServer(){
    const input = document.getElementById("server-input");
    const msg = document.getElementById("setup-msg");
    const addr = normalizeAddr(input ? input.value : "");

    if(!/^https?:\/\/[^\s/]+/i.test(addr)){
        if(msg){ msg.textContent = "请填写电脑 IP 或完整地址，例如 192.168.1.5"; msg.className = "setup-msg warn"; }
        return;
    }
    if(msg){ msg.textContent = "正在测试连接 " + addr + " …"; msg.className = "setup-msg info"; }
    try{
        const res = await fetch(addr + "/api/status", {cache:"no-store"});
        if(!res.ok){ throw new Error("HTTP " + res.status); }
        // Only an address that actually answered goes into the history.
        rememberServer(addr);
        localStorage.setItem(SERVER_KEY, addr);
        // Land on the dashboard after connecting, and keep one code path:
        // the reload lets init() find an address and start polling.
        try{ localStorage.setItem(TAB_KEY, "monitor"); }catch(e){}
        location.reload();
    }catch(e){
        if(msg){
            msg.textContent = "连不上 " + addr + "：" + (e && e.message ? e.message : e) +
                "。确认手机与该电脑在同一局域网、elite_mon 正在运行，且端口未被防火墙拦截。";
            msg.className = "setup-msg warn";
        }
    }
}

function wireSetup(){
    const btnSave = document.getElementById("server-save");
    if(btnSave){ btnSave.addEventListener("click", saveServer); }

    const input = document.getElementById("server-input");
    if(input){
        input.addEventListener("keydown", function(ev){
            if(ev.key === "Enter"){ saveServer(); }
        });
    }

    // The gear is a shortcut to the settings tab.
    const gear = document.getElementById("btn-setup");
    if(gear){ gear.addEventListener("click", function(){ switchTab("settings"); }); }

    const bar = document.getElementById("tabbar");
    if(bar){
        bar.querySelectorAll("button").forEach(function(btn){
            btn.addEventListener("click", function(){ switchTab(btn.dataset.tab); });
        });
    }

    switchTab(currentTab());
}

wireSetup();
