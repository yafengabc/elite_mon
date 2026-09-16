// Player messages, ship names and target names from the Journal are untrusted; escape before building HTML.
function esc(s){
    return String(s == null ? "" : s).replace(/[&<>"']/g, function(c){
        return {"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c];
    });
}

// ------------------------------------------------------------------
// i18n: the panel keeps no translation of its own. The whole table is fetched
// once from /api/i18n, which returns the active language overlaid on the base
// one - so a new language is a new src/lang/<code>.toml and nothing else.
// T() looks an id up and substitutes {0}, {1}, ...; the Go side uses the very
// same templates, which keeps both surfaces worded identically.
// ------------------------------------------------------------------
let curLang = "zh";
let I18N = {};

function T(id, ...args){
    const s = I18N[id];
    if(s === undefined){ return id; }
    if(!args.length){ return s; }
    // A function replacement keeps $& and friends in the values literal.
    return s.replace(/\{(\d+)\}/g, function(m, n){
        const v = args[Number(n)];
        return v === undefined ? m : String(v);
    });
}

// Skip the redraw when content is unchanged, avoiding a pointless DOM rebuild every 3 s.
function setHTML(id, html){
    const el = document.getElementById(id);
    if(el && el.innerHTML !== html){ el.innerHTML = html; }
}

function setText(id, t){
    const el = document.getElementById(id);
    if(el){ el.textContent = t; }
}

// Fill every element carrying data-i18n. index.html ships the Chinese text as a
// fallback so the page is readable before (and without) the table, so an id the
// table does not know leaves the existing text alone instead of showing the id
// (T returns the id itself on a miss).
function applyStaticTexts(){
    const title = T("panel.window_title");
    if(title !== "panel.window_title"){ document.title = title; }
    document.querySelectorAll("[data-i18n]").forEach(function(el){
        const id = el.getAttribute("data-i18n");
        const s = T(id);
        if(s !== id){ el.textContent = s; }
    });
}

function clearPanels(){
    setHTML("summary", "<div class='line'>—</div>");
    setHTML("cargo",   "<div class='line'>—</div>");
    setHTML("bounty",  "<div class='line'>—</div>");
    setHTML("msg",     "<div class='line'>—</div>");
}

// ------------------------------------------------------------------
// Kill-trend chart: hand-drawn SVG, no external chart library (the panel must open offline).
// ------------------------------------------------------------------

// The chart plots kill rate, i.e. kills per hour. One trend point is one bucket
// (10 min in Go - trendBucket in elite_monitor.go - which also decides the
// timespan above the chart), so a cell's own count needs this factor to reach the
// same unit as the rolling hour count the backend sends. Keep the two in step.
const TREND_CELLS_PER_HOUR = 6;

function renderTrend(pts, winText){
    const el = document.getElementById("trend");
    if(!el){ return; }
    if(!pts || !pts.length){
        setHTML("trend", "<div class='line'>" + T("panel.no_trend") + "</div>");
        return;
    }

    // One shared axis, in kills per hour: the rolling hour count already is one,
    // and a cell's own count is extrapolated to it. Both curves then live on the
    // same scale, so the taller line is genuinely the busier one.
    const rate = function(p){ return Number(p.kills) * TREND_CELLS_PER_HOUR; };
    let maxR = 0;
    pts.forEach(function(p){
        const r = rate(p), h = Number(p.kills_hour);
        if(r > maxR){ maxR = r; }
        if(h > maxR){ maxR = h; }
    });
    if(maxR === 0){
        setHTML("trend", "<div class='line'>" + T("panel.trend_none", esc(winText || T("panel.session"))) + "</div>");
        return;
    }
    // A quartered axis needs a max that divides by 4; rates are whole numbers, so
    // every tick label stays whole too.
    maxR = Math.max(4, Math.ceil(maxR / 4) * 4);

    // The tick column sits on the right, so R reserves room for the labels and L
    // is only breathing space before the oldest point.
    const W = 940, H = 220, L = 34, R = 54, T0 = 20, B = 30;
    const iw = W - L - R, ih = H - T0 - B;
    const n = pts.length;
    const step = n > 1 ? iw / (n - 1) : 0;
    const X = function(i){ return n > 1 ? L + step * i : L + iw / 2; };
    const Y = function(v){ return T0 + ih - ih * (v / maxR); };

    let g = "";

    // Horizontal grid plus a single tick column on the right: both series share it
    const axisX = L + iw;
    for(let i = 0; i <= 4; i++){
        const y = T0 + ih * i / 4;
        g += '<line x1="' + L + '" y1="' + y + '" x2="' + axisX + '" y2="' + y + '" stroke="#2a2a3a" stroke-width="1"/>';
        g += '<text x="' + (axisX + 8) + '" y="' + (y + 4) + '" fill="#6a6a7a" font-size="11" text-anchor="start">' + Math.round(maxR * (4 - i) / 4) + '</text>';
    }
    // Unit for those bare tick numbers
    g += '<text x="' + (axisX + 8) + '" y="' + (T0 - 8) + '" fill="#6a6a7a" font-size="10" text-anchor="start">' + esc(T("panel.axis_rate")) + '</text>';

    // Time labels: thin out to about 8; labelling every point is unreadable
    const labelEvery = Math.max(1, Math.ceil(n / 8));
    for(let i = 0; i < n; i += labelEvery){
        g += '<text x="' + X(i).toFixed(1) + '" y="' + (T0 + ih + 18) + '" fill="#6a6a7a" font-size="11" text-anchor="middle">' + esc(pts[i].time_local) + '</text>';
    }

    // Hover targets: one transparent rect per column, showing that column's numbers on hover
    const colW = n > 1 ? step : iw;
    for(let i = 0; i < n; i++){
        const tip = T("panel.tip", pts[i].time_local, rate(pts[i]),
            Number(pts[i].kills), Number(pts[i].kills_hour), Number(pts[i].bounty).toLocaleString());
        g += '<rect x="' + (X(i) - colW / 2).toFixed(1) + '" y="' + T0 + '" width="' + colW.toFixed(1) + '" height="' + ih + '" fill="transparent"><title>' + esc(tip) + '</title></rect>';
    }

    // Two series on the shared axis: hour-average (blue) drawn first, the recent
    // activity rate (green) on top
    let pr = "", ph = "";
    pts.forEach(function(p, i){
        pr += (i ? " " : "") + X(i).toFixed(1) + "," + Y(rate(p)).toFixed(1);
        ph += (i ? " " : "") + X(i).toFixed(1) + "," + Y(Number(p.kills_hour)).toFixed(1);
    });
    g += '<polyline points="' + ph + '" fill="none" stroke="#4cf" stroke-width="2" stroke-linejoin="round"/>';
    g += '<polyline points="' + pr + '" fill="none" stroke="#6f8" stroke-width="2" stroke-linejoin="round"/>';

    const legend = '<div class="tlegend">'
        + '<span class="k">■ ' + T("panel.legend_10m") + '</span>'
        + '<span class="b">■ ' + T("panel.legend_1h") + '</span>'
        + '<span class="note">' + T("panel.trend_all", esc(winText || T("panel.session"))) + '</span>'
        + '</div>';

    setHTML("trend", legend + '<svg viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="xMidYMid meet">' + g + '</svg>');
}

function render(data){
    const win = data.stat_window_text || T("common.stat_window_1h");

    if(data.error){
        setHTML("loginfo", '<span class="warn">' + esc(data.error) + '</span>');
        clearPanels(); // clear on error; stale values would look like everything is fine
        return;
    }

    let head = T("panel.log_file", esc(data.log_file_name));
    if(data.updated_at){ head += T("panel.updated_suffix", esc(data.updated_at)); }
    setHTML("loginfo", head);
    renderTrend(data.kill_trend, data.trend_window_text);

    // Total bounty includes mission rewards, so show the subtotal to reveal the breakdown
    function missionNote(v){
        const n = Number(v || 0);
        return n > 0 ? ' <span class="info">' + T("common.mission_note", n.toLocaleString()) + '</span>' : '';
    }

    let s = "";
    s += '<div class="line">' + T("summary.total_kills", '<span class="good">' + Number(data.total_kills) + '</span>') + '</div>';
    s += '<div class="line">' + T("panel.total_bounty", '<span class="good">' + Number(data.total_bounty).toLocaleString() + ' Cr</span>') + missionNote(data.total_mission_reward) + '</div>';
    s += '<div class="line">' + T("summary.span_kills", esc(win), '<span class="info">' + Number(data.hour_kills) + '</span>') + '</div>';
    s += '<div class="line">' + T("panel.span_bounty", esc(win), '<span class="info">' + Number(data.hour_bounty).toLocaleString() + ' Cr</span>') + missionNote(data.hour_mission_reward) + '</div>';

    // Mission progress: baseline done count plus completions from redirects (already computed
    // backend-side), folded into the summary
    const mDone = Number(data.mission_done);
    const mTotal = Number(data.mission_total);
    const mActive = Number(data.mission_active);
    if(mTotal > 0){
        s += '<div class="line">' + T("summary.missions",
            '<span class="good">' + mDone + '</span>',
            '<span class="info">' + mTotal + '</span>',
            mActive) + '</div>';
    }else{
        s += '<div class="line">' + T("summary.missions_none") + '</div>';
    }
    setHTML("summary", s);

    // ship_info replaces the old cargo_info (the DOM id is still cargo)
    let shipInfoHtml = "";
    const shipData = data.ship_info;
    if(shipData){
        const fuelPercentStr = (Number(shipData.fuel_percent) * 100).toFixed(1);
        shipInfoHtml += '<div class="line"><strong>' + T("ship.vessel_label") + '</strong>' + esc(shipData.vessel) + '</div>';
        // in_fighter is the Status.json InFighter / InSRV flag: when true, vessel is the
        // vehicle you are riding while fuel and cargo still belong to the mothership
        if(shipData.ship_name || shipData.ship_ident){
            // With a ship name, merge the "in fighter / SRV" tag onto the same line
            const fighterTag = shipData.in_fighter
                ? ' &nbsp; <span class="highlight">' + T("ship.in_fighter") + '</span>'
                : '';
            shipInfoHtml += '<div class="line">' + T("ship.name",
                shipData.ship_name ? esc(shipData.ship_name) : "—",
                shipData.ship_ident ? esc(shipData.ship_ident) : "—") + fighterTag + '</div>';
        } else if(shipData.in_fighter){
            // Without a ship name line, use its own row so the hint is not lost
            shipInfoHtml += '<div class="line"><span class="highlight">' + T("ship.in_fighter") + '</span></div>';
        }
        shipInfoHtml += '<div class="line">' + T("ship.cargo", Number(shipData.cargo_used), Number(shipData.cargo_max)) + '</div>';
        shipInfoHtml += '<div class="line">' + T("ship.main_fuel",
            '<span class="good">' + Number(shipData.fuel_main_current).toFixed(2) + '</span>',
            Number(shipData.fuel_main_max), fuelPercentStr) + '</div>';
        shipInfoHtml += '<div class="line">' + T("ship.reserve_fuel", Number(shipData.fuel_reserve).toFixed(2)) + '</div>';
    }
    setHTML("cargo", shipInfoHtml || "<div class='line'>" + T("summary.ship_unavailable") + "</div>");

    let blist = "";
    if(data.bounty_records){
        const revBounty = [...data.bounty_records].reverse();
        const creditStr = function(r){ return Number(r.credits).toLocaleString() + " Cr"; };
        revBounty.forEach(function(r){
            // Five cells handed to the #bounty grid to column-align, instead of padding
            // with spaces -- in Android fallback fonts spaces and digits differ in width, which
            // skews the separators. Mission rows are already labelled by the backend, so only
            // recolour them here to set them apart.
            const cls = r.is_mission ? "highlight" : "good";
            blist += '<span class="line">' + esc(r.time_local) + '</span>'
                + '<span class="line bsep">|</span>'
                + '<span class="line bamt ' + cls + '">' + esc(creditStr(r)) + '</span>'
                + '<span class="line bsep">|</span>'
                + '<span class="line">' + esc(r.ship_type || (r.is_mission ? T("bounty.mission") : T("bounty.unknown_ship"))) + '</span>';
        });
    }
    setHTML("bounty", blist || "<div class='line'>" + T("panel.no_kill_log") + "</div>");

    let mlist = "";
    if(data.message_lines){
        const revMsg = [...data.message_lines].reverse();
        revMsg.forEach(function(m){
            mlist += '<div class="line">' + esc(m) + '</div>';
        });
    }
    setHTML("msg", mlist || "<div class='line'>" + T("panel.no_events") + "</div>");
}

async function poll(){
    try{
        const res = await fetch("/api/status", {cache:"no-store"});
        if(!res.ok){ throw new Error("HTTP " + res.status); } // awaiting without checking the status fails silently
        render(await res.json());
    }catch(e){
        console.error(e);
        setHTML("loginfo", '<span class="warn">' + T("panel.connect_failed", esc(e.message)) + '</span>');
        clearPanels(); // clear here too, or stale data keeps hiding the failure
    }
}

// The string table must arrive before the first paint, or the page would briefly
// show raw ids. A failure here is not fatal: ids are readable-ish and the panels
// still populate, and /api/status reports the real error.
async function init(){
    try{
        const res = await fetch("/api/i18n", {cache:"no-store"});
        if(res.ok){
            const d = await res.json();
            curLang = d.lang || "zh";
            I18N = d.table || {};
            if(d.tag){ document.documentElement.lang = d.tag; }
        }
    }catch(e){ console.error(e); }
    applyStaticTexts();
    await poll();
    setInterval(poll, 3000);
}

init();
