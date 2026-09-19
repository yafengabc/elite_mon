// ------------------------------------------------------------------
// Mobile-only layer, loaded before main.js.
//
// The panel is served by elite_mon itself, so it can fetch "/api/status" as a
// relative path. The app cannot: its pages come from the phone, which makes its
// origin http://localhost -- a relative fetch would ask the phone, not the PC.
// So the PC's address is kept here (localStorage) and prefixed onto every API
// call. main.js only knows about apiBase() / setServerStatus() / showSetup().
// ------------------------------------------------------------------

const SERVER_KEY = "elitemon.server";

// Trailing slashes would produce "//api/status"; strip them on the way in.
function normalizeAddr(raw){
    return String(raw || "").trim().replace(/\/+$/, "");
}

function apiBase(){
    return normalizeAddr(localStorage.getItem(SERVER_KEY));
}

function showSetup(msg){
    const box = document.getElementById("setup");
    if(box){ box.hidden = false; }
    const m = document.getElementById("setup-msg");
    if(m){ m.textContent = msg || ""; m.className = "setup-msg"; }
    const input = document.getElementById("server-input");
    if(input){
        input.value = apiBase();
        // Focus is only useful once the sheet is actually on screen.
        setTimeout(function(){ input.focus(); }, 50);
    }
}

function hideSetup(){
    const box = document.getElementById("setup");
    if(box){ box.hidden = true; }
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
    const raw = input ? input.value : "";
    const addr = normalizeAddr(raw);

    if(!/^https?:\/\/[^\s/]+/i.test(addr)){
        if(msg){ msg.textContent = "请填写完整地址，例如 http://192.168.1.5:8088"; msg.className = "setup-msg warn"; }
        return;
    }
    if(msg){ msg.textContent = "正在测试连接…"; msg.className = "setup-msg info"; }
    try{
        const res = await fetch(addr + "/api/status", {cache:"no-store"});
        if(!res.ok){ throw new Error("HTTP " + res.status); }
        localStorage.setItem(SERVER_KEY, addr);
        // A reload keeps one code path: init() now finds an address and starts
        // polling, with no half-initialised state to reconcile.
        location.reload();
    }catch(e){
        if(msg){
            msg.textContent = "连不上：" + (e && e.message ? e.message : e) +
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

    // The gear is how you get back here after the first run.
    const gear = document.getElementById("btn-setup");
    if(gear){ gear.addEventListener("click", function(){ showSetup(); }); }

    const box = document.getElementById("setup");
    if(box){
        box.addEventListener("click", function(ev){
            if(ev.target === box){ hideSetup(); }   // tap the backdrop to dismiss
        });
    }
}

wireSetup();
