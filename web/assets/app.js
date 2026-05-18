// airplanes-webconfig SPA. Renders setup/login/dashboard against the
// JSON API. Built on a single navigate() helper so panel transitions
// always tear down EventSource streams, polling timers, and in-flight
// fetches before mounting the next view.
(function () {
    "use strict";

    // ===== Constants =====

    const STATUS_REFRESH_MS = 30_000;

    // Visual ordering of service tiles. We deliberately drop two units
    // that the Go server's MonitoredServices set still tracks:
    //   - lighttpd.service: if the user is seeing this dashboard via :80,
    //     lighttpd is necessarily up.
    //   - airplanes-webconfig.service: if the page loaded, this is up.
    // Showing tiles for those is just systemctl repeating what the user
    // already verified by being on the page.
    const MONITORED_SERVICES = [
        "airplanes-feed.service",
        "airplanes-mlat.service",
        "readsb.service",
        "dump978-fa.service",
        "airplanes-978.service",
    ];

    // User-facing apps reverse-proxied off the same hostname. Up state is
    // probed by HEAD-fetching the path; click opens in a new tab.
    const APP_TILES = [
        { id: "tar1090",    label: "tar1090",    meta: "live aircraft map", href: "/tar1090/" },
        { id: "graphs1090", label: "graphs1090", meta: "history graphs",    href: "/graphs1090/" },
    ];

    // Slug ↔ systemd unit. Mirrors the server-side whitelist in
    // internal/logs/logs.go; keep them in sync.
    const LOG_SLUG_TO_UNIT = {
        "feed":      "airplanes-feed.service",
        "mlat":      "airplanes-mlat.service",
        "readsb":    "readsb.service",
        "dump978":   "dump978-fa.service",
        "uat":       "airplanes-978.service",
        "claim":     "airplanes-claim.service",
        "webconfig":      "airplanes-webconfig.service",
        "update":           "airplanes-update.service",
        "system-upgrade":   "airplanes-system-upgrade.service",
        "webconfig-update": "airplanes-webconfig-update.service",
    };
    const UNIT_TO_LOG_SLUG = Object.fromEntries(
        Object.entries(LOG_SLUG_TO_UNIT).map(([s, u]) => [u, s])
    );

    // App tile icons (also Bootstrap-icons style).
    const APP_ICONS = {
        "tar1090":
            "M15.817.113A.5.5 0 0 1 16 .5v14a.5.5 0 0 1-.402.49l-5 1a.5.5 0 0 1-.196 0L5.5 15.01l-4.902.98A.5.5 0 0 1 0 15.5v-14a.5.5 0 0 1 .402-.49l5-1a.5.5 0 0 1 .196 0L10.5.99l4.902-.98a.5.5 0 0 1 .415.103M10 1.91l-4-.8v12.98l4 .8zm1 12.98 4-.8V1.11l-4 .8zm-6-.8V1.11l-4 .8v12.98z",
        "graphs1090":
            "M11 2a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v12h.5a.5.5 0 0 1 0 1H.5a.5.5 0 0 1 0-1H1v-3a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v3h1V7a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v7h1V2zm1 12h2V2h-2v12zm-3 0V7H7v7zm-5 0v-3H2v3z",
    };

    // Inline SVG icon paths (Bootstrap-icons-style geometry, single path
    // per icon, drawn at 16×16). currentColor lets the tile theme it.
    const SERVICE_ICONS = {
        "airplanes-feed.service":
            "M6.428 1.151C6.708.591 7.213 0 8 0s1.292.592 1.572 1.151C9.861 1.73 10 2.431 10 3v3.691l5.17 2.585a1.5 1.5 0 0 1 .83 1.342V12a.5.5 0 0 1-.582.493l-5.507-.918-.375 2.253 1.318 1.318A.5.5 0 0 1 10.5 16h-5a.5.5 0 0 1-.354-.854l1.319-1.318-.376-2.253-5.507.918A.5.5 0 0 1 0 12v-1.382a1.5 1.5 0 0 1 .83-1.342L6 6.691V3c0-.568.14-1.271.428-1.849Z",
        "airplanes-mlat.service":
            "M3.05 3.05a7 7 0 0 0 0 9.9.5.5 0 0 1-.707.707 8 8 0 0 1 0-11.314.5.5 0 0 1 .707.707m2.122 2.122a4 4 0 0 0 0 5.656.5.5 0 1 1-.708.708 5 5 0 0 1 0-7.072.5.5 0 0 1 .708.708m5.656-.708a.5.5 0 0 1 .708 0 5 5 0 0 1 0 7.072.5.5 0 1 1-.708-.708 4 4 0 0 0 0-5.656.5.5 0 0 1 0-.708m2.122-2.12a.5.5 0 0 1 .707 0 8 8 0 0 1 0 11.313.5.5 0 0 1-.707-.707 7 7 0 0 0 0-9.9.5.5 0 0 1 0-.707zM10 8a2 2 0 1 1-4 0 2 2 0 0 1 4 0",
        "readsb.service":
            "M3.05 3.05a7 7 0 0 0 0 9.9.5.5 0 0 1-.707.707 8 8 0 0 1 0-11.314.5.5 0 0 1 .707.707m2.122 2.122a4 4 0 0 0 0 5.656.5.5 0 1 1-.708.708 5 5 0 0 1 0-7.072.5.5 0 0 1 .708.708m5.656-.708a.5.5 0 0 1 .708 0 5 5 0 0 1 0 7.072.5.5 0 1 1-.708-.708 4 4 0 0 0 0-5.656.5.5 0 0 1 0-.708m2.122-2.12a.5.5 0 0 1 .707 0 8 8 0 0 1 0 11.313.5.5 0 0 1-.707-.707 7 7 0 0 0 0-9.9.5.5 0 0 1 0-.707zM10 8a2 2 0 1 1-4 0 2 2 0 0 1 4 0",
        "dump978-fa.service":
            "M11.196 8 15 14H1l3.804-6-3.792-6h13.976zM2.732 13h10.536L10.464 8.5h-4.93z M11.196 8 15 14H1l3.804-6-3.792-6h13.976z",
        "airplanes-978.service":
            "M11.196 8 15 14H1l3.804-6-3.792-6h13.976zM2.732 13h10.536L10.464 8.5h-4.93z",
        "lighttpd.service":
            "M0 8a8 8 0 1 1 16 0A8 8 0 0 1 0 8m7.5-6.923c-.67.204-1.335.82-1.887 1.855A8 8 0 0 0 5.145 4H7.5zM4.09 4a9.3 9.3 0 0 1 .64-1.539 7 7 0 0 1 .597-.933A7.03 7.03 0 0 0 2.255 4zm-.582 3.5c.03-.877.138-1.718.312-2.5H1.674a7 7 0 0 0-.656 2.5zM4.847 5a12.5 12.5 0 0 0-.338 2.5H7.5V5zM8.5 5v2.5h2.99a12.5 12.5 0 0 0-.337-2.5zM4.51 8.5a12.5 12.5 0 0 0 .337 2.5H7.5V8.5zm3.99 0V11h2.653q.187-.765.338-2.5zM5.145 12q.208.58.468 1.068c.552 1.035 1.218 1.65 1.887 1.855V12zm.182 2.472a7 7 0 0 1-.597-.933A9.3 9.3 0 0 1 4.09 12H2.255a7.03 7.03 0 0 0 3.072 2.472M3.82 11a13.7 13.7 0 0 1-.312-2.5h-2.49q.062.84.656 2.5zm6.853 3.472A7.03 7.03 0 0 0 13.745 12H11.91a9.3 9.3 0 0 1-.64 1.539 7 7 0 0 1-.597.933M8.5 12v2.923c.67-.204 1.335-.82 1.887-1.855q.26-.487.468-1.068zm3.68-1h2.146q.594-1.66.656-2.5h-2.49a13.7 13.7 0 0 1-.312 2.5m2.802-3.5a7 7 0 0 0-.656-2.5H12.18q.174.781.312 2.5zM11.27 2.461q.247.464.64 1.539h1.835a7.03 7.03 0 0 0-3.072-2.472q.318.426.597.933M10.855 4a8 8 0 0 0-.468-1.068C9.835 1.897 9.17 1.282 8.5 1.077V4z",
        "airplanes-webconfig.service":
            "M9.405 1.05c-.413-1.4-2.397-1.4-2.81 0l-.1.34a1.464 1.464 0 0 1-2.105.872l-.31-.17c-1.283-.698-2.686.705-1.987 1.987l.169.311c.446.82.023 1.841-.872 2.105l-.34.1c-1.4.413-1.4 2.397 0 2.81l.34.1a1.464 1.464 0 0 1 .872 2.105l-.17.31c-.698 1.283.705 2.686 1.987 1.987l.311-.169a1.464 1.464 0 0 1 2.105.872l.1.34c.413 1.4 2.397 1.4 2.81 0l.1-.34a1.464 1.464 0 0 1 2.105-.872l.31.17c1.283.698 2.686-.705 1.987-1.987l-.169-.311a1.464 1.464 0 0 1 .872-2.105l.34-.1c1.4-.413 1.4-2.397 0-2.81l-.34-.1a1.464 1.464 0 0 1-.872-2.105l.17-.31c.698-1.283-.705-2.686-1.987-1.987l-.311.169a1.464 1.464 0 0 1-2.105-.872zM8 10.93a2.929 2.929 0 1 1 0-5.86 2.929 2.929 0 0 1 0 5.858z",
    };

    // Bootstrap-icons cpu glyph (16×16 viewBox). Same single-path shape
    // SERVICE_ICONS uses; consumed by svgIcon().
    const PIHEALTH_ICON =
        "M5 0a.5.5 0 0 1 .5.5V2h1V.5a.5.5 0 0 1 1 0V2h1V.5a.5.5 0 0 1 1 0V2h1V.5a.5.5 0 0 1 1 0V2A2.5 2.5 0 0 1 14 4.5h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14A2.5 2.5 0 0 1 11.5 14H10v1.5a.5.5 0 0 1-1 0V14H8v1.5a.5.5 0 0 1-1 0V14H6v1.5a.5.5 0 0 1-1 0V14H4.5A2.5 2.5 0 0 1 2 11.5H.5a.5.5 0 0 1 0-1H2v-1H.5a.5.5 0 0 1 0-1H2v-1H.5a.5.5 0 0 1 0-1H2v-1H.5a.5.5 0 0 1 0-1H2A2.5 2.5 0 0 1 4.5 2V.5A.5.5 0 0 1 5 0m-.5 3A1.5 1.5 0 0 0 3 4.5v7A1.5 1.5 0 0 0 4.5 13h7a1.5 1.5 0 0 0 1.5-1.5v-7A1.5 1.5 0 0 0 11.5 3z";

    // Bootstrap-icons wifi glyph (16×16 viewBox).
    const WIFI_ICON =
        "M15.384 6.115a.485.485 0 0 0-.047-.736A12.44 12.44 0 0 0 8 3C5.259 3 2.723 3.882.663 5.379a.485.485 0 0 0-.048.736.518.518 0 0 0 .668.05A11.45 11.45 0 0 1 8 4c2.507 0 4.827.802 6.716 2.164.205.148.49.13.668-.049m-2.55 2.516a.482.482 0 0 0-.063-.745A8.46 8.46 0 0 0 8 7a8.46 8.46 0 0 0-4.77 1.886.482.482 0 0 0-.064.745.525.525 0 0 0 .654.065A7.46 7.46 0 0 1 8 8c1.71 0 3.29.578 4.18 1.696a.525.525 0 0 0 .654-.065zm-2.557 2.514a.483.483 0 0 0-.089-.745A4.47 4.47 0 0 0 8 10c-.83 0-1.605.247-2.188.4a.483.483 0 0 0-.089.745.525.525 0 0 0 .626.085A3.47 3.47 0 0 1 8 11c.488 0 .947.118 1.349.314a.525.525 0 0 0 .626-.085zM9.5 14.25a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0z";

    // ===== Runtime state =====

    const app = document.getElementById("app");
    const headerTitleEl = document.getElementById("wc-header-title");
    const brandVersionEl = document.getElementById("wc-brand-version");
    const backBtn = document.getElementById("wc-back-btn");
    const themeBtn = document.getElementById("wc-theme-btn");
    const refreshBtn = document.getElementById("wc-refresh-btn");

    let activeStream = null;       // current EventSource (log viewer)
    let statusTimer = null;        // setInterval handle for status poll
    let activeAbort = null;        // last issued AbortController
    let dashboardCtx = null;       // dashboard-render references; null when off-dashboard

    // ===== Theme =====

    function readStoredTheme() {
        try {
            const t = localStorage.getItem("webconfig-theme");
            return t === "light" || t === "dark" ? t : null;
        } catch (_) { return null; }
    }
    function writeStoredTheme(t) {
        try { localStorage.setItem("webconfig-theme", t); } catch (_) {}
    }
    (function applyStoredTheme() {
        const t = readStoredTheme();
        if (t) document.documentElement.setAttribute("data-theme", t);
    })();
    function toggleTheme() {
        const root = document.documentElement;
        const cur = root.getAttribute("data-theme")
            || (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
        const next = cur === "dark" ? "light" : "dark";
        root.setAttribute("data-theme", next);
        writeStoredTheme(next);
    }

    // ===== Header =====

    function setHeader(opts) {
        const o = opts || {};
        if (headerTitleEl) headerTitleEl.textContent = o.title ? "/ " + o.title : "";
        if (backBtn) backBtn.hidden = !o.showBack;
        if (refreshBtn) refreshBtn.hidden = !o.showRefresh;
    }

    // ===== Navigation (one cleanup point) =====

    // pendingFlash: one-shot banner consumed by the next panel that
    // calls consumePendingFlash(). Survives the navigate() teardown so
    // a "saved, but restart failed" message reaches the dashboard panel
    // even though navigate() resets dashboardCtx.
    let pendingFlash = null;

    function consumePendingFlash() {
        const f = pendingFlash;
        pendingFlash = null;
        return f;
    }

    function buildFlashNode(flash) {
        if (!flash || !flash.text) return null;
        const cls = flash.level === "warn" ? "wc-flash wc-flash--warn" : "wc-flash";
        return el("div", { class: cls, role: "status", "aria-live": "polite" }, flash.text);
    }

    function navigate(panelFn, opts) {
        if (activeStream) { try { activeStream.close(); } catch (_) {} activeStream = null; }
        if (statusTimer) { clearInterval(statusTimer); statusTimer = null; }
        if (activeAbort) { try { activeAbort.abort(); } catch (_) {} activeAbort = null; }
        dashboardCtx = null;
        if (opts && opts.flash) pendingFlash = opts.flash;
        setHeader(opts);
        return panelFn();
    }

    // Dashboard returns funnel through one helper so showRefresh, title,
    // and showBack stay consistent across every "back to dashboard" path.
    // Returns the panel promise so the refresh handler can await the
    // initial fetch and toggle the spinner around it.
    function navigateDashboard(extraOpts) {
        return navigate(dashboard, Object.assign(
            { title: null, showBack: false, showRefresh: true }, extraOpts || {}));
    }

    // ===== HTTP helpers =====

    async function getJSON(path) {
        const ctrl = new AbortController();
        activeAbort = ctrl;
        try {
            const resp = await fetch(path, {
                method: "GET",
                credentials: "same-origin",
                headers: { Accept: "application/json" },
                signal: ctrl.signal,
            });
            let payload = null;
            try { payload = await resp.json(); } catch (_) {}
            return { ok: resp.ok, status: resp.status, payload: payload || {} };
        } catch (e) {
            return {
                ok: false, status: 0, payload: { error: "network error" },
                aborted: e && e.name === "AbortError",
            };
        }
    }

    async function postJSON(path, body) {
        return sendJSON("POST", path, body);
    }

    async function putJSON(path, body) {
        return sendJSON("PUT", path, body);
    }

    async function deleteJSON(path, body) {
        return sendJSON("DELETE", path, body);
    }

    async function sendJSON(method, path, body) {
        const ctrl = new AbortController();
        activeAbort = ctrl;
        try {
            const init = {
                method,
                credentials: "same-origin",
                headers: { "Content-Type": "application/json" },
                signal: ctrl.signal,
            };
            if (body !== undefined) init.body = JSON.stringify(body);
            const resp = await fetch(path, init);
            let payload = null;
            try { payload = await resp.json(); } catch (_) {}
            return { ok: resp.ok, status: resp.status, payload: payload || {} };
        } catch (e) {
            return {
                ok: false, status: 0, payload: { error: "network error" },
                aborted: e && e.name === "AbortError",
            };
        }
    }

    function handleAuthFailure(resp) {
        if (resp && !resp.ok && resp.status === 401) {
            navigate(() => loginPanel("Session expired — log in again."), {});
            return true;
        }
        return false;
    }

    // ===== DOM helper =====

    function el(tag, attrs, ...children) {
        const node = document.createElement(tag);
        if (attrs) {
            for (const k of Object.keys(attrs)) {
                const v = attrs[k];
                if (v == null || v === false) continue;
                if (k === "class") node.className = v;
                else if (k === "html") node.innerHTML = v;
                else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
                else node.setAttribute(k, v === true ? "" : v);
            }
        }
        for (const c of children) {
            if (c == null || c === false) continue;
            if (Array.isArray(c)) {
                for (const inner of c) {
                    if (inner != null) node.appendChild(typeof inner === "string" ? document.createTextNode(inner) : inner);
                }
            } else {
                node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
            }
        }
        return node;
    }

    function clear() { app.replaceChildren(); }
    function render(...nodes) { clear(); for (const n of nodes) app.appendChild(n); }
    function errorEl() { return el("div", { class: "error", role: "alert" }); }

    // Mirror parse_report_status in feed/scripts/airplanes-diagnostics.sh:
    // accept true|yes|1|on as true and false|no|0|off as false, case-insensitive.
    // Unrecognised values (including empty) fall back to the supplied default,
    // so unmodified-but-non-canonical operator edits don't get silently coerced
    // when they save the form.
    function parseBoolish(value, defaultValue) {
        if (value === undefined || value === null) return defaultValue;
        const s = String(value).trim().toLowerCase();
        if (s === "") return defaultValue;
        if (s === "true" || s === "yes" || s === "1" || s === "on") return true;
        if (s === "false" || s === "no" || s === "0" || s === "off") return false;
        return defaultValue;
    }

    // Hidden username so password managers can save credentials against the implicit "admin" account.
    function hiddenUsernameField() {
        return el("input", { type: "text", name: "username", value: "admin", autocomplete: "username", readonly: true, hidden: true, "aria-hidden": "true", tabindex: "-1" });
    }

    function svgIcon(pathD, size) {
        const ns = "http://www.w3.org/2000/svg";
        const svg = document.createElementNS(ns, "svg");
        const w = String(size || 16);
        svg.setAttribute("width", w);
        svg.setAttribute("height", w);
        svg.setAttribute("viewBox", "0 0 16 16");
        svg.setAttribute("fill", "currentColor");
        svg.setAttribute("aria-hidden", "true");
        const path = document.createElementNS(ns, "path");
        path.setAttribute("d", pathD);
        svg.appendChild(path);
        return svg;
    }

    // ===== Safe claim URL =====

    function safeClaimHref(url) {
        try {
            const u = new URL(url);
            if (u.protocol !== "https:") return null;
            if (u.hostname !== "airplanes.live" && !u.hostname.endsWith(".airplanes.live")) return null;
            return u.toString();
        } catch (_) { return null; }
    }

    // ===== Status / tile classification =====

    // Msg-rate baseline advances on every poll. computeMsgRate has the same
    // semantics as before (side-effecting; second call inside one poll burns
    // the baseline and returns null), so the dashboard poll calls it exactly
    // once per cycle and stashes the value in lastMsgRate for the hero meta
    // line and the readsb tile to read independently. updateMsgRateFromStatus
    // is the single entry point — it gates on readsb being active (matches
    // the pre-refactor behaviour) and clears the baseline on inactivity so
    // recovery starts clean rather than averaging across the outage.
    let rateBaseline = null;
    let lastMsgRate = null;
    function updateMsgRateFromStatus(status) {
        const payload = (status && status.payload) || {};
        const services = payload.services || {};
        if (services["readsb.service"] !== "active") {
            rateBaseline = null;
            lastMsgRate = null;
            return;
        }
        lastMsgRate = computeMsgRate(payload.feed || null);
    }
    function computeMsgRate(feed) {
        if (!feed || typeof feed.now !== "number" || typeof feed.messages_counter !== "number") {
            rateBaseline = null;
            return null;
        }
        if (rateBaseline
            && feed.now > rateBaseline.now
            && feed.messages_counter >= rateBaseline.messages) {
            const dt = feed.now - rateBaseline.now;
            const rate = (feed.messages_counter - rateBaseline.messages) / dt;
            rateBaseline = { now: feed.now, messages: feed.messages_counter };
            return rate;
        }
        // First poll, time didn't advance, or counter went backwards (readsb restart).
        rateBaseline = { now: feed.now, messages: feed.messages_counter };
        return null;
    }

    // Mirror the bash validators in feed/scripts/lib/configure-validators.sh.
    // apl-feed apply is the authoritative server-side gate; the JS preview
    // here only suppresses Save while the user is editing. A JS-vs-bash
    // mismatch surfaces as "looks valid in the form but save failed", so
    // these accept/reject the same inputs as the bash side. Different
    // regex engines, semantically equivalent rules. Pinned by
    // test/test_validator_parity.sh.
    //
    // The /* @validator-parity */ markers delimit the block exported to
    // the parity test; keep them flanking exactly the shared symbols.

    /* @validator-parity start */
    const latLonRE = /^[+-]?\d+(?:\.\d+)?$/;
    const altitudeRE = /^(-?\d+(?:\.\d+)?)(?:m|ft)?$/;

    function isValidLatitude(v) {
        const s = (v || "").trim();
        if (!latLonRE.test(s)) return false;
        const f = Number(s);
        if (!Number.isFinite(f)) return false;
        return f >= -90 && f <= 90;
    }
    function isValidLongitude(v) {
        const s = (v || "").trim();
        if (!latLonRE.test(s)) return false;
        const f = Number(s);
        if (!Number.isFinite(f)) return false;
        return f >= -180 && f <= 180;
    }
    function isValidAltitude(v) {
        const s = (v || "").trim();
        const m = altitudeRE.exec(s);
        if (!m) return false;
        const f = Number(m[1]);
        return Number.isFinite(f) && f >= -1000 && f <= 10000;
    }

    // Wi-Fi validators — bash twin lives at
    // /usr/local/lib/airplanes/wifi-validators.sh (apl_wifi_valid_*). Pinned
    // by the same parity fixture. CRUCIAL: no trim. WPA passphrases and
    // SSIDs can legitimately carry leading/trailing whitespace, so pass the
    // value through verbatim — the form must not normalize.
    const wifiSSIDControlsRE = /[\x00-\x1f\x7f]/;
    function isValidWifiSSID(v) {
        const s = String(v == null ? "" : v);
        if (wifiSSIDControlsRE.test(s)) return false;
        const bytes = new TextEncoder().encode(s).length;
        return bytes >= 1 && bytes <= 32;
    }
    const wifiPSKHexRE = /^[A-Fa-f0-9]{64}$/;
    const wifiPSKASCIIRE = /^[\x20-\x7e]{8,63}$/;
    function isValidWifiPSK(v) {
        const s = String(v == null ? "" : v);
        if (wifiPSKHexRE.test(s)) return true;
        return wifiPSKASCIIRE.test(s);
    }
    function isValidWifiCountry(v) {
        return /^[A-Z]{2}$/.test(String(v == null ? "" : v));
    }
    const wifiPriorityRE = /^(0|[1-9][0-9]{0,2})$/;
    function isValidWifiPriority(v) {
        return wifiPriorityRE.test(String(v == null ? "" : v));
    }
    /* @validator-parity end */

    // previewLatLonSet — projection of unsaved form values onto the
    // daemon's "would MLAT be enabled?" rule. Used ONLY for the form-time
    // preview path and for the legacy fallback in classifyService when
    // the daemon's mlat_decision isn't available. The running daemon's
    // authoritative answer comes from payload.mlat_decision.
    //
    // Prefer the explicit GEO_CONFIGURED flag when present in the form
    // state — the form may be mid-edit with valid coords but GEO_CONFIGURED
    // still pointing at the placeholder value. Fall back to the lat/lon
    // derivation only when GEO_CONFIGURED is absent (legacy state-file).
    function previewLatLonSet(values) {
        if (values && Object.prototype.hasOwnProperty.call(values, "GEO_CONFIGURED")) {
            return (values.GEO_CONFIGURED || "").trim() === "true";
        }
        if (!isValidLatitude(values.LATITUDE) || !isValidLongitude(values.LONGITUDE)) return false;
        const lat = Number(String(values.LATITUDE || "").trim());
        const lon = Number(String(values.LONGITUDE || "").trim());
        return !(lat === 0 && lon === 0);
    }

    // locationSaved — drives the form cascade. previewLatLonSet covers the
    // GEO_CONFIGURED-first check; altitude is separate because the daemon
    // classifies altitude_empty as its own MLAT misconfig reason.
    function locationSaved(values) {
        return previewLatLonSet(values || {}) && isValidAltitude((values || {}).ALTITUDE || "");
    }

    // previewMlatDisabled — same projection semantics: read MLAT_ENABLED
    // from the unsaved form values. The running daemon's truth comes from
    // payload.mlat_decision.
    function previewMlatDisabled(values) {
        return (values.MLAT_ENABLED || "").trim() === "false";
    }

    function mlatDisabledReason(reason) {
        switch (reason) {
            case "mlat_enabled_false": return "MLAT_ENABLED=false";
            case "geo_not_configured": return "GEO_CONFIGURED=false (set latitude/longitude/altitude)";
            case "latitude_zero":      return "LATITUDE=0";
            case "longitude_zero":     return "LONGITUDE=0";
            default:                   return reason || "config";
        }
    }

    function mlatMisconfigReason(reason) {
        switch (reason) {
            case "mlat_private_invalid":  return "MLAT_PRIVATE must be 'true' or 'false'";
            case "altitude_empty":        return "ALTITUDE empty — set the antenna altitude";
            default:                      return "misconfigured (" + (reason || "unknown") + ")";
        }
    }

    function classifyMlatFromDecision(decision, serviceState) {
        if (decision.state === "disabled") {
            return { dot: "na", meta: "disabled — " + mlatDisabledReason(decision.reason) };
        }
        if (decision.state === "misconfigured") {
            return { dot: "err", meta: mlatMisconfigReason(decision.reason) };
        }
        // decision.state === "enabled"
        if (serviceState === "active") return { dot: "ok", meta: "mlat running" };
        if (serviceState === "failed") return { dot: "err", meta: "failed" };
        if (serviceState === "activating" || serviceState === "reloading") {
            return { dot: "warn", meta: "starting" };
        }
        return { dot: "err", meta: "mlat down" };
    }

    function uatMisconfigReason(reason) {
        switch (reason) {
            case "uat_input_invalid": return "UAT_INPUT invalid — set to '127.0.0.1:30978' or clear";
            default:                   return "misconfigured (" + (reason || "unknown") + ")";
        }
    }

    // classify978FromDecision mirrors classifyMlatFromDecision: the daemon's
    // published decision drives the tile state, with the unit's systemd
    // active state only consulted in the "enabled" branch (where transient
    // failures or starting-up states are user-visible).
    //
    // Used by both 978 tiles, but reads from different decision objects:
    //   - dump978-fa.service tile  → dump978fa_decision (the producer)
    //   - airplanes-978.service tile → uat_decision    (the consumer)
    //
    // Reason vocabularies differ across producer/consumer (no_hardware on
    // the producer, peer_no_hardware on the consumer); both render the
    // same family of "warn — no SDR" tiles so the user sees a coherent
    // story even though the underlying signals come from different files.
    function classify978FromDecision(decision, serviceState, activeLabel) {
        if (decision.state === "disabled") {
            // no_hardware is the dump978-fa hardware-probe self-disable;
            // render as warn (not the na/grey that uat_disabled gets) so
            // the user notices the SDR is missing rather than mistaking it
            // for an intentional config-off.
            if (decision.reason === "no_hardware") {
                return { dot: "warn", meta: "no 978 SDR detected" };
            }
            return { dot: "na", meta: "disabled — UAT off in config" };
        }
        if (decision.state === "misconfigured") {
            return { dot: "err", meta: uatMisconfigReason(decision.reason) };
        }
        // decision.state === "enabled"
        // peer_no_hardware: airplanes-978 is up but dump978-fa stood down
        // because no 978 SDR is present. The relay has no input — render
        // an honest idle tile instead of the green "relaying UAT" that
        // would mislead.
        if (decision.reason === "peer_no_hardware") {
            return { dot: "warn", meta: "idle — no 978 SDR detected" };
        }
        if (serviceState === "active") return { dot: "ok", meta: activeLabel };
        if (serviceState === "failed") return { dot: "err", meta: "failed" };
        if (serviceState === "activating" || serviceState === "reloading") {
            return { dot: "warn", meta: "starting" };
        }
        return { dot: "err", meta: "enabled but not running" };
    }

    function classifyService(unit, state, payload) {
        const feed = payload.feed || null;
        const configValues = payload.values || {};
        // Default falls through unknowns.
        const base = { dot: "warn", meta: state || "unknown" };

        switch (unit) {
            case "airplanes-feed.service": {
                if (state === "active") {
                    const n = feed && typeof feed.aircraft_count === "number" ? feed.aircraft_count : null;
                    if (n === null) return { dot: "ok", meta: "feeding" };
                    if (n === 0) return { dot: "ok", meta: "feeding · no aircraft visible" };
                    return { dot: "ok", meta: "feeding · " + n + " aircraft" };
                }
                if (state === "failed") return { dot: "err", meta: "failed" };
                return { dot: "err", meta: "not feeding" };
            }
            case "airplanes-mlat.service": {
                // Decision-first: an "active" daemon may be sleeping in
                // disabled-by-config mode. Consult the daemon's published
                // state file BEFORE the active-state shortcut.
                const mlatDecision = payload.mlat_decision || null;
                if (mlatDecision) {
                    return classifyMlatFromDecision(mlatDecision, state);
                }
                // Legacy fallback: pre-PR-1 daemon without a state file,
                // or transient server-side parse failure.
                if (state === "active") return { dot: "ok", meta: "mlat running" };
                if (state === "failed") return { dot: "err", meta: "failed" };
                if (previewMlatDisabled(configValues)) return { dot: "na", meta: "disabled — MLAT off in config" };
                if (!previewLatLonSet(configValues)) return { dot: "na", meta: "disabled — set lat/lon" };
                return { dot: "err", meta: "mlat down" };
            }
            case "readsb.service": {
                if (state === "active") {
                    const n = feed && typeof feed.aircraft_count === "number" ? feed.aircraft_count : null;
                    // lastMsgRate is computed once per poll by the dashboard
                    // loop; reading the cached value avoids double-calling
                    // computeMsgRate, which would burn its own baseline.
                    let parts = [];
                    if (n !== null) parts.push(n + " aircraft");
                    if (lastMsgRate !== null) parts.push(lastMsgRate.toFixed(0) + " msg/s");
                    else if (n !== null) parts.push("rate pending");
                    return { dot: "ok", meta: parts.join(" · ") || "active" };
                }
                if (state === "failed") return { dot: "err", meta: "failed" };
                return { dot: "err", meta: "decoder down" };
            }
            case "dump978-fa.service": {
                // Producer side: read dump978-fa's own decision (which
                // includes the new no_hardware reason from its USB-serial
                // probe). Falls back to the consumer's uat_decision if the
                // server didn't publish a separate producer decision (older
                // webconfig binary, missing state file).
                const dump978Decision = payload.dump978fa_decision || payload.uat_decision || null;
                if (dump978Decision) {
                    return classify978FromDecision(dump978Decision, state, "decoding 978");
                }
                // Legacy fallback: pre-PR-4 webconfig binary or daemon
                // without a state file.
                const enabled = !!(configValues.UAT_INPUT && configValues.UAT_INPUT.trim());
                return classify978Fallback(state, enabled, "decoding 978");
            }
            case "airplanes-978.service": {
                const uatDecision = payload.uat_decision || null;
                if (uatDecision) {
                    return classify978FromDecision(uatDecision, state, "relaying UAT");
                }
                const enabled = !!(configValues.UAT_INPUT && configValues.UAT_INPUT.trim());
                return classify978Fallback(state, enabled, "relaying UAT");
            }
            case "lighttpd.service": {
                if (state === "active") return { dot: "ok", meta: "serving on :80" };
                if (state === "failed") return { dot: "err", meta: "failed" };
                return { dot: "err", meta: "down" };
            }
            case "airplanes-webconfig.service": {
                if (state === "active") return { dot: "ok", meta: "running" };
                if (state === "failed" || state === "inactive") return { dot: "err", meta: state };
                if (state === "unknown") return { dot: "warn", meta: "unknown — observability gap" };
                return { dot: "warn", meta: state || "unknown" };
            }
        }
        return base;
    }

    // classify978Fallback is the pre-PR-4 UAT_INPUT-truthy classifier;
    // used only when the server didn't publish a uat_decision (older
    // webconfig binary, missing state file, schema violation).
    function classify978Fallback(state, enabled, activeLabel) {
        if (state === "active" && enabled) return { dot: "ok", meta: activeLabel };
        if (state === "active" && !enabled) return { dot: "warn", meta: "active but config disabled (drift)" };
        if (state === "inactive" && enabled) return { dot: "err", meta: "enabled but not running" };
        if (state === "inactive" && !enabled) return { dot: "na", meta: "disabled" };
        if (state === "failed") return { dot: "err", meta: "failed" };
        return { dot: "warn", meta: "unknown — observability gap" };
    }

    function classifyOverall(payload) {
        const services = payload.services || {};
        const configValues = payload.values || {};
        const get = (u) => services[u] || "unknown";
        const feedState = get("airplanes-feed.service");
        const readsbState = get("readsb.service");

        // Core: if feed or readsb is anything other than active, the
        // device is not feeding. Pessimistic on `unknown` because the
        // tile-level classifier already shows the user a red dot in
        // that case — saying "partial" in the hero would contradict it.
        if (feedState !== "active" || readsbState !== "active") {
            return { dot: "err", label: "down" };
        }

        // Both core services active. Optional services (978, mlat) only
        // demote us to "partial" if the user has opted into them. Decision-
        // first: the daemon's own answer to "is UAT expected?" wins over
        // the form-projected UAT_INPUT-truthy preview, which only kicks in
        // when no decision has been published.
        const uatDecision = payload.uat_decision || null;
        const uatIntendedRunning = uatDecision
            ? uatDecision.state === "enabled"
            : !!(configValues.UAT_INPUT && configValues.UAT_INPUT.trim());
        if (uatIntendedRunning) {
            const u1 = get("dump978-fa.service");
            const u2 = get("airplanes-978.service");
            if (u1 !== "active" || u2 !== "active") return { dot: "warn", label: "partial" };
        }
        // MLAT contributes to overall health when it's the daemon's intent
        // to run (decision.state === "enabled"). If the daemon decided
        // disabled or misconfigured, MLAT being non-active is the user's
        // intended state and shouldn't demote the hero. Falls back to
        // form-projected previewMlat* helpers if no decision is published.
        const mlatDecision = payload.mlat_decision || null;
        const mlatIntendedRunning = mlatDecision
            ? mlatDecision.state === "enabled"
            : (previewLatLonSet(configValues) && !previewMlatDisabled(configValues));
        if (mlatIntendedRunning && get("airplanes-mlat.service") !== "active") {
            return { dot: "warn", label: "partial" };
        }
        // Hardware health demotes only on err — a warn-level hardware
        // condition (75°C summer afternoon, history flags) shouldn't paint
        // the whole dashboard yellow when feeding is otherwise fine.
        const ph = payload.pi_health || null;
        if (ph && ph.severity === "err") {
            return { dot: "warn", label: "partial" };
        }
        return { dot: "ok", label: "healthy" };
    }

    // ===== Hero =====

    function buildHero() {
        const titleEl = el("p", { class: "wc-hero__title" }, "—");
        const metaEl = el("p", { class: "wc-hero__meta" }, "");
        const dotEl = el("span", { class: "wc-tile__dot wc-tile__dot--na" });
        const labelEl = el("span", {}, "—");
        const badgeEl = el("span", { class: "wc-hero__badge", "aria-live": "polite" }, dotEl, labelEl);
        const root = el("section", { class: "wc-hero" },
            el("div", { class: "wc-hero__body" }, titleEl, metaEl),
            badgeEl,
        );
        return { root, titleEl, metaEl, dotEl, labelEl };
    }

    function updateHero(heroEl, status, configValues) {
        const payload = (status && status.payload) || {};
        // configValues from the form is what the user has typed; fold it
        // into the payload's "values" so the classifier sees the merged
        // state (form values override server-snapshot values for cases
        // where the user has unsaved edits).
        payload.values = Object.assign({}, payload.values || {}, configValues || {});
        const services = payload.services || {};
        const feed = payload.feed || null;

        const overall = classifyOverall(payload);
        heroEl.dotEl.className = "wc-tile__dot wc-tile__dot--" + overall.dot;
        heroEl.labelEl.textContent = overall.label;

        // Title summarises the feed line.
        const aircraft = feed && typeof feed.aircraft_count === "number" ? feed.aircraft_count : null;
        if (services["airplanes-feed.service"] === "active" && aircraft !== null) {
            heroEl.titleEl.textContent = aircraft === 0
                ? "Feeding · no aircraft visible right now"
                : "Feeding " + aircraft + " aircraft";
        } else if (services["airplanes-feed.service"] === "active") {
            heroEl.titleEl.textContent = "Feeding";
        } else {
            heroEl.titleEl.textContent = "Not feeding";
        }

        // Meta line: live message rate from the readsb feed counter. This
        // is the canonical "is the feeder actually feeding" signal for a
        // user, so it earns the hero slot. The webconfig version is in the
        // header brand strip instead. lastMsgRate is set by the dashboard
        // poll exactly once per cycle (see renderStatusSnapshot); during
        // the first poll after a (re)load it's null until the baseline has
        // two samples.
        const parts = [];
        if (lastMsgRate !== null) parts.push(lastMsgRate.toFixed(0) + " msg/s");
        heroEl.metaEl.textContent = parts.join(" · ");
    }

    // ===== Service tiles =====

    function buildTile(unit) {
        const slug = UNIT_TO_LOG_SLUG[unit];
        const iconPath = SERVICE_ICONS[unit];
        const iconNode = el("span", { class: "wc-tile__icon" }, iconPath ? svgIcon(iconPath) : null);

        const title = unit.replace(/\.service$/, "");
        const titleEl = el("span", { class: "wc-tile__title" }, title);
        const metaEl = el("span", { class: "wc-tile__meta" }, "—");
        const dotEl = el("span", { class: "wc-tile__dot wc-tile__dot--na" });

        const tag = slug ? "a" : "div";
        const attrs = { class: "wc-tile wc-tile--service", "data-state": "unknown" };
        if (slug) {
            attrs.href = "#";
            attrs.role = "button";
            attrs.onclick = (e) => {
                e.preventDefault();
                navigate(() => logViewer(slug), { title: "Logs · " + slug, showBack: true });
            };
        }
        const root = el(tag, attrs,
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            dotEl,
        );
        return { root, titleEl, metaEl, dotEl };
    }

    function updateTile(tile, unit, payload) {
        const services = payload.services || {};
        const state = services[unit] || "unknown";
        const c = classifyService(unit, state, payload);
        tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + c.dot;
        tile.metaEl.textContent = c.meta;
        // title= recovers the full meta string when CSS ellipsis truncates
        // the tile (the fixed-row grid means long reasons clip on narrow
        // viewports). Hover / long-press surfaces the full text.
        tile.root.title = c.meta || "";
        tile.root.setAttribute("data-state", state);
    }

    function buildPiHealthTile() {
        const iconNode = el("span", { class: "wc-tile__icon" }, svgIcon(PIHEALTH_ICON));
        const titleEl  = el("span", { class: "wc-tile__title" }, "Raspberry Pi");
        const metaEl   = el("span", { class: "wc-tile__meta" }, "—");
        const dotEl    = el("span", { class: "wc-tile__dot wc-tile__dot--na" });
        const root = el("div", { class: "wc-tile wc-tile--hardware", "data-state": "unknown" },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            dotEl,
        );
        return { root, titleEl, metaEl, dotEl };
    }

    function updatePiHealthTile(tile, payload) {
        const ph = (payload && payload.pi_health) || null;
        if (!ph) {
            tile.dotEl.className = "wc-tile__dot wc-tile__dot--na";
            tile.metaEl.textContent = "—";
            tile.root.title = "";
            tile.root.setAttribute("data-state", "unknown");
            return;
        }
        const sev = ph.severity || "na";
        const summary = ph.summary || "—";
        tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + sev;
        tile.metaEl.textContent = summary;
        tile.root.title = summary;
        tile.root.setAttribute("data-state", sev);
    }

    // classifyWifi projects the /api/status.wifi payload into a tile
    // {dot, meta}. Returns null when there's no payload — the tile is
    // hidden in that case (Ethernet-only hosts have no WiFi field at
    // all). A connected interface with unparseable signal renders as
    // warn with just the SSID (conservative — we know it's up but
    // can't grade it).
    function classifyWifi(w) {
        if (!w) return null;
        if (!w.connected) return { dot: "na", meta: "not connected" };
        const displaySSID = (w.ssid && w.ssid.length) ? w.ssid : "(hidden)";
        if (typeof w.signal_pct !== "number") {
            return { dot: "warn", meta: displaySSID };
        }
        const pct = w.signal_pct;
        let dot = "ok";
        if (pct < 40) dot = "err";
        else if (pct < 60) dot = "warn";
        return { dot, meta: displaySSID + " · " + pct + "%" };
    }

    function buildWifiTile() {
        const iconNode = el("span", { class: "wc-tile__icon" }, svgIcon(WIFI_ICON));
        const titleEl  = el("span", { class: "wc-tile__title" }, "Wi-Fi");
        const metaEl   = el("span", { class: "wc-tile__meta" }, "—");
        const dotEl    = el("span", { class: "wc-tile__dot wc-tile__dot--na" });
        // Hidden until the first payload arrives — avoids a flash on
        // Ethernet-only hosts where the field is omitted entirely.
        const root = el("a", {
            class: "wc-tile wc-tile--wifi",
            "data-state": "unknown",
            href: "#",
            role: "button",
            hidden: true,
            onclick: (e) => {
                e.preventDefault();
                navigate(wifiPanel, { title: "Wi-Fi networks", showBack: true });
            },
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            dotEl,
        );
        return { root, titleEl, metaEl, dotEl };
    }

    function updateWifiTile(tile, payload) {
        const c = classifyWifi(payload && payload.wifi);
        if (!c) {
            tile.root.hidden = true;
            return;
        }
        tile.root.hidden = false;
        tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + c.dot;
        tile.metaEl.textContent = c.meta;
        tile.root.title = c.meta || "";
        tile.root.setAttribute("data-state", c.dot);
    }

    function buildTileGrid() {
        const services = el("div", { class: "wc-grid--tiles" });
        const apps = el("div", { class: "wc-grid--tiles" });
        const tiles = {};
        const appTiles = {};
        // Hardware tile leads the services grid — the .wc-tile--hardware
        // class differentiates it visually without needing a separate row.
        const piHealth = buildPiHealthTile();
        services.appendChild(piHealth.root);
        // WiFi tile follows, hidden by default until /api/status confirms
        // there is a WiFi adapter at all.
        const wifi = buildWifiTile();
        services.appendChild(wifi.root);
        for (const unit of MONITORED_SERVICES) {
            const t = buildTile(unit);
            tiles[unit] = t;
            services.appendChild(t.root);
        }
        for (const app of APP_TILES) {
            const t = buildAppTile(app);
            appTiles[app.id] = t;
            apps.appendChild(t.root);
        }
        // Wrap both grids in a fragment-like container so the dashboard
        // render() call gets a single root.
        const root = el("div", { class: "wc-tiles-stack" }, services, apps);
        return { root, tiles, appTiles, piHealth, wifi };
    }

    function buildAppTile(app) {
        const iconPath = APP_ICONS[app.id];
        const iconNode = el("span", { class: "wc-tile__icon" }, iconPath ? svgIcon(iconPath) : null);

        const titleEl = el("span", { class: "wc-tile__title" }, app.label);
        const metaEl = el("span", { class: "wc-tile__meta" }, app.meta || "—");
        const dotEl = el("span", { class: "wc-tile__dot wc-tile__dot--na" });
        const chev = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "↗");

        const root = el("a", {
            class: "wc-tile wc-tile--app",
            "data-state": "unknown",
            href: app.href,
            target: "_blank",
            rel: "noopener noreferrer",
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
            dotEl,
        );
        return { root, titleEl, metaEl, dotEl, href: app.href };
    }

    async function probeApp(href) {
        try {
            const ctrl = new AbortController();
            const timer = setTimeout(() => ctrl.abort(), 4000);
            const resp = await fetch(href, {
                method: "HEAD",
                credentials: "same-origin",
                signal: ctrl.signal,
                cache: "no-store",
            });
            clearTimeout(timer);
            return resp.ok;
        } catch (_) {
            return false;
        }
    }

    async function updateAppTiles(grid) {
        // Probe in parallel, but only mutate if the dashboard is still
        // mounted. ctx capture mirrors the renderStatusSnapshot pattern.
        const ctx = dashboardCtx;
        if (!ctx) return;
        const results = await Promise.all(APP_TILES.map(a => probeApp(a.href)));
        if (ctx !== dashboardCtx) return;
        APP_TILES.forEach((a, i) => {
            const tile = grid.appTiles[a.id];
            if (!tile) return;
            const ok = results[i];
            const meta = ok ? a.meta : (a.meta + " · unreachable");
            tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + (ok ? "ok" : "err");
            tile.metaEl.textContent = meta;
            tile.root.title = meta || "";
            tile.root.setAttribute("data-state", ok ? "active" : "inactive");
        });
    }

    function updateTiles(grid, status, configValues) {
        const payload = Object.assign({}, (status && status.payload) || {});
        // Form-time configValues override the snapshot's values (the user
        // may have unsaved edits). Each tile classifier reads from
        // payload.values and payload.mlat_decision; merging here keeps
        // the call sites simple.
        payload.values = Object.assign({}, payload.values || {}, configValues || {});
        if (grid.piHealth) updatePiHealthTile(grid.piHealth, payload);
        if (grid.wifi) updateWifiTile(grid.wifi, payload);
        for (const unit of MONITORED_SERVICES) {
            const tile = grid.tiles[unit];
            if (tile) updateTile(tile, unit, payload);
        }
    }

    // ===== Identity card =====

    function buildIdentityCardBody() {
        return el("div", {}, el("p", { class: "muted" }, "loading…"));
    }

    function renderIdentityCard(parent, resp) {
        parent.replaceChildren();
        if (!resp || !resp.ok) {
            parent.appendChild(el("p", { class: "error", role: "alert" },
                (resp && resp.payload && resp.payload.error) || "could not load identity"));
            return;
        }
        const id = resp.payload || {};
        if (!id.feeder_id) {
            parent.appendChild(el("p", { class: "muted" }, "Feeder ID will be assigned on first run."));
            return;
        }
        parent.appendChild(el("p", {}, el("strong", {}, "Feeder ID: "), id.feeder_id));
        if (!id.claim_secret_present) {
            parent.appendChild(el("p", { class: "muted" },
                "Claim secret not yet registered. The feeder retries every 5 minutes; click below to attempt now."));
            const registerErr = errorEl();
            const registerBtn = el("button", {
                class: "wc-btn-primary",
                type: "button",
                onclick: async () => {
                    registerBtn.disabled = true;
                    registerErr.textContent = "";
                    const r = await postJSON("/api/claim/register", {});
                    if (handleAuthFailure(r)) return;
                    if (!r.ok) {
                        registerBtn.disabled = false;
                        registerErr.textContent = (r.payload && r.payload.error) || "register failed";
                        return;
                    }
                    navigate(() => logViewer("claim"), { title: "Claim activity", showBack: true });
                },
            }, "Register now");
            const claimLog = el("button", {
                class: "wc-btn-ghost",
                type: "button",
                onclick: () => navigate(() => logViewer("claim"), { title: "Claim activity", showBack: true }),
            }, "Claim activity");
            parent.appendChild(el("div", { class: "actions" }, registerBtn, claimLog));
            parent.appendChild(registerErr);
            return;
        }
        const claimLog = el("button", {
            class: "wc-btn-ghost",
            type: "button",
            onclick: () => navigate(() => logViewer("claim"), { title: "Claim activity", showBack: true }),
        }, "Claim activity");

        const reveal = el("button", {
            class: "wc-btn-ghost",
            type: "button",
            onclick: async () => {
                reveal.disabled = true;
                const r = await postJSON("/api/identity/secret", {});
                reveal.disabled = false;
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    parent.replaceChildren(el("p", { class: "error", role: "alert" },
                        (r.payload && r.payload.error) || "reveal failed"));
                    return;
                }
                const safe = safeClaimHref(r.payload.claim_page);
                const linkOrText = safe
                    ? el("a", { href: safe, target: "_blank", rel: "noopener noreferrer" }, "Claim this feeder")
                    : el("span", { class: "muted" }, r.payload.claim_page || "");
                parent.replaceChildren(
                    el("p", {}, el("strong", {}, "Feeder ID: "), r.payload.feeder_id),
                    el("p", {}, el("strong", {}, "Claim secret: "), el("code", {}, r.payload.claim_secret)),
                    el("p", {}, linkOrText),
                    el("div", { class: "actions" }, claimLog),
                );
            },
        }, "Show claim secret");
        parent.appendChild(el("div", { class: "actions" }, reveal, claimLog));
    }

    function identityChanged(prev, next) {
        if (!prev) return true;
        return prev.feeder_id !== next.feeder_id
            || prev.claim_secret_present !== next.claim_secret_present;
    }

    // ===== Configuration card =====

    // canonicaliseAltitude mirrors internal/feedmeta/feedmeta.go's
    // canonicalizeForCompare for ALTITUDE: bare numerics get an "m"
    // suffix appended; anything already ending in "m" or "ft" is
    // returned as-is; empty stays empty. Used in the dirty comparator
    // so a saved "120m" and a user-typed bare "120" don't show as
    // dirty when the backend would canonicalise them to the same
    // value.
    function canonicaliseAltitude(v) {
        const t = (v || "").trim();
        if (t === "") return "";
        if (/m$/i.test(t) || /ft$/i.test(t)) return t;
        if (!isNaN(Number(t))) return t + "m";
        return t;
    }

    // sameValue compares an input value to a saved value applying the
    // canonicaliser appropriate for the key (today: altitude only).
    function sameValue(key, a, b) {
        if (key === "ALTITUDE") return canonicaliseAltitude(a) === canonicaliseAltitude(b);
        return (a || "") === (b || "");
    }

    // mountGroup wires up a single per-group form within a config tile.
    // It owns: dirty tracking against configState.savedValues, the
    // group's Save button visibility/disabled state, the in-flight
    // save lock, and the post-save refresh + cross-group recheck loop.
    //
    // Callers supply group-specific glue via the options:
    //   name        — string key into configState.dirtyGroups.
    //   formEl      — the <form> element that owns onsubmit.
    //   footerEl    — the .config-fieldset__footer that hosts the
    //                 button + inline error + pending-restart line.
    //   keys        — array of feed.env keys this group owns; used by
    //                 the default dirty check.
    //   readInputs  — () => { KEY: rawValue } current state from the
    //                 group's DOM. Plain values, not trimmed; the
    //                 dirty comparator handles canonical equality.
    //   isValid     — () => bool. Preflight gate, e.g. MLAT/geo. When
    //                 false: button is visible-but-disabled.
    //   payload     — () => { KEY: value }. The dirty-only payload to
    //                 POST. The helper passes it through verbatim.
    //   onSavedHook — optional () => void after a successful save +
    //                 global refresh. MLAT registers one here to
    //                 re-evaluate its geo gate when Location saves.
    //
    // The returned object exposes a recheck() the caller wires to
    // every input event in the group, so dirty-state and validation
    // update live without re-render.
    function mountGroup(opts) {
        const {
            name, formEl, footerEl,
            keys, readInputs, isValid, payload,
            onSavedHook,
        } = opts;

        const btn = el("button", {
            type: "submit",
            class: "wc-btn-primary",
        }, "Save changes");
        btn.hidden = true;
        const err = errorEl();
        const pending = el("p", { class: "config-fieldset__pending" });
        pending.hidden = true;
        footerEl.appendChild(btn);
        footerEl.appendChild(err);
        footerEl.appendChild(pending);

        const isDirty = () => {
            const cur = readInputs();
            for (const k of keys) {
                if (Object.prototype.hasOwnProperty.call(cur, k)) {
                    if (!sameValue(k, cur[k], configState.savedValues[k])) return true;
                }
            }
            return false;
        };

        const recheck = () => {
            if (isDirty()) {
                configState.dirtyGroups.add(name);
            } else {
                configState.dirtyGroups.delete(name);
            }
            const dirty = configState.dirtyGroups.has(name);
            btn.hidden = !dirty;
            const valid = !isValid || isValid();
            btn.disabled = !!configState.inFlight || !valid;
            updateDashboardDirtyFlag();
        };

        formEl.addEventListener("input", recheck);
        formEl.addEventListener("change", recheck);

        formEl.addEventListener("submit", async (e) => {
            e.preventDefault();
            // Snapshot the configState reference at submit time so a
            // dashboard re-mount mid-await (e.g. user clicked Refresh)
            // doesn't let this in-flight save mutate the new mount's
            // state. Every step after an await checks `state ===
            // configState` and bails if it diverged.
            const state = configState;
            if (state.inFlight) return;
            if (!isDirty()) return; // defensive: button shouldn't show
            err.textContent = "";
            pending.hidden = true;
            pending.textContent = "";
            state.inFlight = name;
            btn.disabled = true;
            btn.textContent = "Saving…";
            // Disable other groups' buttons while in-flight by firing
            // every group's recheck via the shared callback channel.
            for (const cb of state.recheckAll) cb();

            const body = payload();
            const r = await postJSON("/api/config", { updates: body });

            if (state !== configState) return; // dashboard re-mounted

            btn.textContent = "Save changes";
            if (handleAuthFailure(r)) {
                state.inFlight = null;
                return;
            }
            if (!r.ok) {
                // Forward apl-feed's structured error envelope. The
                // per-field map shape lands as a one-line summary —
                // good enough; we don't render field-by-field decor
                // for now.
                err.textContent = (r.payload && (r.payload.error || r.payload.message))
                    || "save failed";
                state.inFlight = null;
                for (const cb of state.recheckAll) cb();
                return;
            }

            // pending_restart is per-group news — surface inline rather
            // than via a navigateDashboard flash so the user stays on
            // their current view and sees the warning attached to the
            // change that caused it.
            const pendingRestart = (r.payload && r.payload.pending_restart) || [];
            if (pendingRestart.length > 0) {
                pending.hidden = false;
                pending.textContent = "Saved, but restart failed for: "
                    + pendingRestart.join(", ")
                    + ". Changes take effect after manual restart.";
            }

            // Authoritative refresh: GET /api/config so the server's
            // canonicalisations (ALTITUDE suffix, GEO_CONFIGURED auto-
            // derive) land back in configState.savedValues. Trusting
            // the submitted body would diverge from disk on those keys.
            const refreshed = await getJSON("/api/config");
            if (state !== configState) return; // dashboard re-mounted
            if (handleAuthFailure(refreshed)) {
                state.inFlight = null;
                return;
            }
            if (refreshed.ok) {
                state.savedValues = normaliseSavedValues((refreshed.payload && refreshed.payload.values) || {});
                if (dashboardCtx) dashboardCtx.configValues = state.savedValues;
            }
            state.inFlight = null;

            // Recompute dirty for every group: Location may have moved,
            // MLAT's geo gate may have unblocked, Privacy may be clean
            // again if the server canonicalised an equal value. Then
            // fire the onSaved hooks (e.g. MLAT recheck after Location
            // save).
            for (const cb of state.recheckAll) cb();
            for (const cb of state.onSaved) {
                try { cb(name); } catch (_) {}
            }
            if (onSavedHook) {
                try { onSavedHook(); } catch (_) {}
            }
        });

        configState.recheckAll.add(recheck);
        recheck();
        return { recheck };
    }

    // configState is the dashboard-scoped shared model for the
    // Configuration tile + Privacy tile. Owns the last-seen-saved
    // snapshot, which groups are dirty, the in-flight lock, and the
    // recheck/onSaved callback fanouts. Reset on every dashboard
    // mount; teardown happens implicitly via navigate() dropping
    // dashboardCtx.
    let configState;

    // normaliseSavedValues fills in the defaults the form inputs use
    // for explicit toggle keys, so the dirty comparator never compares
    // a literal "true"/"false" against undefined. The production
    // feed.env always carries these keys, but normalising defensively
    // keeps the comparator behaviour identical whether the backend
    // omits them or not.
    function normaliseSavedValues(values) {
        return Object.assign({
            MLAT_ENABLED: "true",
            MLAT_PRIVATE: "false",
            REPORT_STATUS: "true",
            REMOTE_CONFIG_ENABLED: "false",
        }, values || {});
    }

    function resetConfigState(values) {
        configState = {
            savedValues: normaliseSavedValues(values),
            dirtyGroups: new Set(),
            inFlight: null,
            recheckAll: new Set(),   // recheck() of every mounted group
            onSaved: new Set(),      // (savedGroupName) => void
        };
    }

    function updateDashboardDirtyFlag() {
        if (dashboardCtx) {
            dashboardCtx.configDirty = configState && configState.dirtyGroups.size > 0;
        }
    }

    function renderConfigCard(parent, resp) {
        parent.replaceChildren();
        if (!resp || !resp.ok) {
            parent.appendChild(el("p", { class: "error", role: "alert" },
                (resp && resp.payload && resp.payload.error) || "could not load config"));
            return;
        }
        configState.savedValues = resp.payload.values || {};

        const fieldId = (key) => "config-" + key.toLowerCase().replace(/_/g, "-");

        // Builds a <fieldset><legend>...</legend> with a footer slot the
        // group helper fills in. Returns { fieldset, body, footer, form }.
        // Each group is its own <form> so Enter-in-an-input submits the
        // owning group only, not anything else.
        const buildGroup = (legendText) => {
            const footer = el("div", { class: "config-fieldset__footer" });
            const body = el("div");
            const form = el("form", { class: "config-form" }, body, footer);
            const fieldset = el("fieldset", { class: "config-fieldset" },
                el("legend", {}, legendText),
                form,
            );
            return { fieldset, body, footer, form };
        };

        // ===== Location =====
        const loc = buildGroup("Location");

        const latId = fieldId("LATITUDE");
        const latInput = el("input", {
            id: latId, name: "LATITUDE", type: "text",
            value: configState.savedValues.LATITUDE || "",
            inputmode: "decimal", placeholder: "51.5",
        });
        const lonId = fieldId("LONGITUDE");
        const lonInput = el("input", {
            id: lonId, name: "LONGITUDE", type: "text",
            value: configState.savedValues.LONGITUDE || "",
            inputmode: "decimal", placeholder: "-0.1",
        });
        const altId = fieldId("ALTITUDE");
        const altInput = el("input", {
            id: altId, name: "ALTITUDE", type: "text",
            value: configState.savedValues.ALTITUDE || "",
            placeholder: "120m",
        });

        const summarise = () => {
            const sv = configState.savedValues;
            return (sv.LATITUDE || "?") + ", " + (sv.LONGITUDE || "?") + ", " + (sv.ALTITUDE || "?");
        };
        const summaryEl = el("div", { class: "location-summary" }, summarise());
        const editBtn = el("button", { type: "button", class: "wc-btn-ghost" }, "Edit");
        const locationSummary = el("div", { class: "location-card" }, summaryEl, editBtn);

        const locationEditor = el("div", { class: "location-inputs" },
            el("div", { class: "field" },
                el("label", { for: latId }, "Latitude"),
                el("p", { class: "field-help" },
                    "Decimal degrees, North-positive (e.g. ",
                    el("code", {}, "51.5074"),
                    "). MLAT needs ~10 m accuracy — use as many decimals as you can.",
                ),
                latInput,
            ),
            el("div", { class: "field" },
                el("label", { for: lonId }, "Longitude"),
                el("p", { class: "field-help" },
                    "Decimal degrees, East-positive (e.g. ",
                    el("code", {}, "-0.1278"),
                    " for London).",
                ),
                lonInput,
            ),
            el("div", { class: "field" },
                el("label", { for: altId }, "Altitude"),
                el("p", { class: "field-help" },
                    "Antenna height above sea level, in metres — not the building height. ",
                    "Used together with latitude/longitude to triangulate aircraft positions. ",
                    "Append ", el("code", {}, "ft"), " if you prefer feet.",
                ),
                altInput,
            ),
        );

        loc.body.appendChild(locationSummary);
        loc.body.appendChild(locationEditor);

        // Cancel must be declared before setEditing because the
        // setter toggles its visibility alongside the editor's. Save
        // visibility is owned by mountGroup (dirty-driven); Cancel
        // visibility is owned here (edit-mode-driven).
        const cancelBtn = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => {
                latInput.value = configState.savedValues.LATITUDE || "";
                lonInput.value = configState.savedValues.LONGITUDE || "";
                altInput.value = configState.savedValues.ALTITUDE || "";
                locGroup.recheck();   // forward-reference; assigned below
                // Clear any group error & pending restart from a prior
                // failed save attempt before collapsing.
                const errEl = loc.footer.querySelector(".error");
                if (errEl) errEl.textContent = "";
                const pendEl = loc.footer.querySelector(".config-fieldset__pending");
                if (pendEl) { pendEl.hidden = true; pendEl.textContent = ""; }
                setEditing(false);
            },
        }, "Cancel");
        // Cancel lives in the per-group footer alongside the Save
        // button mountGroup will append. Its visibility tracks edit
        // mode independently of the dirty state managed by mountGroup.
        loc.footer.appendChild(cancelBtn);

        // Editing state lives in DOM hidden flags, not in JS — the
        // body is always mounted so input events work whether collapsed
        // or expanded. setEditing also syncs Cancel since they share
        // the same edit-mode lifecycle.
        const setEditing = (editing) => {
            locationSummary.hidden = editing;
            locationEditor.hidden = !editing;
            cancelBtn.hidden = !editing;
            if (editing) setTimeout(() => latInput.focus(), 0);
        };
        // Initial state: edit mode iff Location isn't saved
        // (uninitialised feeder) so the user gets straight to setting
        // coords on first boot. Otherwise stay collapsed.
        setEditing(!locationSaved(configState.savedValues));

        editBtn.addEventListener("click", () => setEditing(true));

        const locGroup = mountGroup({
            name: "location",
            formEl: loc.form,
            footerEl: loc.footer,
            keys: ["LATITUDE", "LONGITUDE", "ALTITUDE"],
            readInputs: () => ({
                LATITUDE: latInput.value,
                LONGITUDE: lonInput.value,
                ALTITUDE: altInput.value,
            }),
            isValid: () => {
                return isValidLatitude(latInput.value)
                    && isValidLongitude(lonInput.value)
                    && isValidAltitude(altInput.value);
            },
            payload: () => {
                // Send only dirty keys, with the lat+lon pair rule so
                // backend GEO derivation gets both axes. ALTITUDE only
                // when it actually changed.
                const out = {};
                const latDirty = !sameValue("LATITUDE", latInput.value, configState.savedValues.LATITUDE);
                const lonDirty = !sameValue("LONGITUDE", lonInput.value, configState.savedValues.LONGITUDE);
                if (latDirty || lonDirty) {
                    out.LATITUDE = latInput.value.trim();
                    out.LONGITUDE = lonInput.value.trim();
                }
                if (!sameValue("ALTITUDE", altInput.value, configState.savedValues.ALTITUDE)) {
                    out.ALTITUDE = altInput.value.trim();
                }
                return out;
            },
            onSavedHook: () => {
                summaryEl.textContent = summarise();
                setEditing(false);
            },
        });

        // ===== MLAT =====
        const mlatG = buildGroup("MLAT");
        const mlatId = fieldId("MLAT_ENABLED");
        const mlatInput = el("input", {
            id: mlatId, type: "checkbox", name: "MLAT_ENABLED",
        });
        if ((configState.savedValues.MLAT_ENABLED || "true") === "true") mlatInput.checked = true;

        const mlatUserId = fieldId("MLAT_USER");
        const mlatUserInput = el("input", {
            id: mlatUserId, name: "MLAT_USER", type: "text",
            value: configState.savedValues.MLAT_USER || "",
            placeholder: "alice",
        });

        const mlatPrivateId = fieldId("MLAT_PRIVATE");
        const mlatPrivateInput = el("input", {
            id: mlatPrivateId, type: "checkbox", name: "MLAT_PRIVATE",
        });
        if ((configState.savedValues.MLAT_PRIVATE || "false") === "true") mlatPrivateInput.checked = true;

        const mlatGateMsg = el("p", { class: "help mlat-gate" });
        const setLocationLink = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => {
                setEditing(true);
                mlatG.fieldset.scrollIntoView({ behavior: "smooth", block: "nearest" });
                loc.fieldset.scrollIntoView({ behavior: "smooth", block: "start" });
            },
        }, "Set location");
        mlatGateMsg.appendChild(document.createTextNode("Location is required before enabling MLAT. "));
        mlatGateMsg.appendChild(setLocationLink);
        mlatGateMsg.hidden = true;

        const mlatSubFields = el("div", { class: "mlat-sub" },
            el("div", { class: "field" },
                el("label", { for: mlatUserId }, "MLAT name"),
                mlatUserInput,
            ),
            el("div", { class: "field" },
                el("label", { for: mlatPrivateId }, mlatPrivateInput, " Hide MLAT name on public map"),
            ),
        );

        mlatG.body.appendChild(el("div", { class: "field" },
            el("label", { for: mlatId }, mlatInput, " Enable MLAT"),
        ));
        mlatG.body.appendChild(mlatGateMsg);
        mlatG.body.appendChild(mlatSubFields);

        const updateMlatVisibility = () => {
            const geoOk = locationSaved(configState.savedValues);
            // Sub-fields only meaningful when MLAT is on AND geo is set.
            mlatSubFields.hidden = !(mlatInput.checked && geoOk);
            // Show the gate message ONLY when the user is trying to
            // enable MLAT without saved geo. Disabling MLAT is always
            // allowed (the user must be able to turn it off even with
            // bad on-disk geo).
            mlatGateMsg.hidden = !(mlatInput.checked && !geoOk);
        };
        mlatInput.addEventListener("change", updateMlatVisibility);
        updateMlatVisibility();

        const mlatGroup = mountGroup({
            name: "mlat",
            formEl: mlatG.form,
            footerEl: mlatG.footer,
            keys: ["MLAT_ENABLED", "MLAT_USER", "MLAT_PRIVATE"],
            readInputs: () => ({
                MLAT_ENABLED: mlatInput.checked ? "true" : "false",
                MLAT_USER: mlatUserInput.value,
                MLAT_PRIVATE: mlatPrivateInput.checked ? "true" : "false",
            }),
            isValid: () => {
                // Always allow saving when MLAT is being disabled. Only
                // gate on saved geo when the submission would enable
                // MLAT.
                if (!mlatInput.checked) return true;
                return locationSaved(configState.savedValues);
            },
            payload: () => {
                const out = {};
                const enabled = mlatInput.checked ? "true" : "false";
                if (enabled !== (configState.savedValues.MLAT_ENABLED || "true")) {
                    out.MLAT_ENABLED = enabled;
                }
                if (!sameValue("MLAT_USER", mlatUserInput.value, configState.savedValues.MLAT_USER)) {
                    out.MLAT_USER = mlatUserInput.value.trim();
                }
                const priv = mlatPrivateInput.checked ? "true" : "false";
                if (priv !== (configState.savedValues.MLAT_PRIVATE || "false")) {
                    out.MLAT_PRIVATE = priv;
                }
                return out;
            },
        });
        // After any save lands, re-evaluate MLAT visibility (Location
        // may have just saved valid geo, unblocking the gate).
        configState.onSaved.add(() => {
            updateMlatVisibility();
            mlatGroup.recheck();
        });

        // ===== Gain =====
        const gainG = buildGroup("Gain");
        const gainId = fieldId("GAIN");
        const gainInput = el("input", {
            id: gainId, name: "GAIN", type: "text",
            value: configState.savedValues.GAIN || "",
            placeholder: "auto",
        });
        gainG.body.appendChild(el("p", { class: "help" },
            "RTL-SDR gain in dB, or ", el("code", {}, "auto"),
            " for adaptive control. See ",
            el("a", {
                href: "https://github.com/wiedehopf/adsb-wiki/wiki/Optimizing-gain",
                target: "_blank",
                rel: "noopener noreferrer",
            }, "wiedehopf's gain guide"),
            ".",
        ));
        gainG.body.appendChild(el("div", { class: "field" },
            el("label", { for: gainId }, "Gain"),
            gainInput,
        ));
        mountGroup({
            name: "gain",
            formEl: gainG.form,
            footerEl: gainG.footer,
            keys: ["GAIN"],
            readInputs: () => ({ GAIN: gainInput.value }),
            isValid: () => true,
            payload: () => ({ GAIN: gainInput.value.trim() }),
        });

        // ===== 978 UAT =====
        const uatG = buildGroup("978 UAT");
        const uatId = fieldId("UAT_INPUT");
        const uatInput = el("input", {
            id: uatId, type: "checkbox", name: "UAT_INPUT",
        });
        if ((configState.savedValues.UAT_INPUT || "") !== "") uatInput.checked = true;

        const sdrSerialId = fieldId("DUMP978_SDR_SERIAL");
        const sdrSerialInput = el("input", {
            id: sdrSerialId, name: "DUMP978_SDR_SERIAL", type: "text",
            value: configState.savedValues.DUMP978_SDR_SERIAL || "978",
            placeholder: "978",
        });
        const dump978GainId = fieldId("DUMP978_GAIN");
        const dump978GainInput = el("input", {
            id: dump978GainId, name: "DUMP978_GAIN", type: "text",
            value: configState.savedValues.DUMP978_GAIN || "42.1",
            placeholder: "42.1", inputmode: "decimal",
        });

        const uatSub = el("div", { class: "dump978-sub" },
            el("div", { class: "field" },
                el("label", { for: sdrSerialId }, "978 SDR serial"),
                sdrSerialInput,
            ),
            el("div", { class: "field" },
                el("label", { for: dump978GainId }, "978 gain"),
                dump978GainInput,
            ),
        );
        uatSub.hidden = !uatInput.checked;
        uatInput.addEventListener("change", () => {
            uatSub.hidden = !uatInput.checked;
        });

        uatG.body.appendChild(el("div", { class: "field" },
            el("label", { for: uatId }, uatInput, " Enable 978 UAT"),
        ));
        uatG.body.appendChild(uatSub);

        mountGroup({
            name: "uat",
            formEl: uatG.form,
            footerEl: uatG.footer,
            // All three keys are in scope. `readInputs` only emits the
            // DUMP978_* keys when UAT is on, so the keys-in-cur check
            // in mountGroup.isDirty naturally ignores hidden sub-fields
            // — toggling UAT off after editing a sub-field returns the
            // group to clean. When UAT is on, sub-field edits bubble
            // through the form's "input" listener and recheck the
            // group, surfacing the Save button.
            keys: ["UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN"],
            readInputs: () => {
                const out = { UAT_INPUT: uatInput.checked ? "127.0.0.1:30978" : "" };
                if (uatInput.checked) {
                    out.DUMP978_SDR_SERIAL = sdrSerialInput.value;
                    out.DUMP978_GAIN = dump978GainInput.value;
                }
                return out;
            },
            isValid: () => true,
            payload: () => {
                const out = {};
                const want = uatInput.checked ? "127.0.0.1:30978" : "";
                if (want !== (configState.savedValues.UAT_INPUT || "")) {
                    out.UAT_INPUT = want;
                }
                if (uatInput.checked) {
                    if (!sameValue("DUMP978_SDR_SERIAL", sdrSerialInput.value, configState.savedValues.DUMP978_SDR_SERIAL)) {
                        out.DUMP978_SDR_SERIAL = sdrSerialInput.value.trim();
                    }
                    if (!sameValue("DUMP978_GAIN", dump978GainInput.value, configState.savedValues.DUMP978_GAIN)) {
                        out.DUMP978_GAIN = dump978GainInput.value.trim();
                    }
                }
                return out;
            },
        });

        // Assemble
        parent.appendChild(loc.fieldset);
        parent.appendChild(mlatG.fieldset);
        parent.appendChild(gainG.fieldset);
        parent.appendChild(uatG.fieldset);
    }

    // ===== Privacy & remote management tile =====

    function buildPrivacyCardBody() {
        return el("div", {}, el("p", { class: "muted" }, "loading…"));
    }

    function renderPrivacyCard(parent, resp) {
        parent.replaceChildren();
        if (!resp || !resp.ok) {
            parent.appendChild(el("p", { class: "error", role: "alert" },
                (resp && resp.payload && resp.payload.error) || "could not load privacy settings"));
            return;
        }
        const values = configState.savedValues;

        const fieldId = (key) => "privacy-" + key.toLowerCase().replace(/_/g, "-");

        const reportId = fieldId("REPORT_STATUS");
        const reportInput = el("input", {
            id: reportId, type: "checkbox", name: "REPORT_STATUS",
        });
        if (parseBoolish(values.REPORT_STATUS, true)) reportInput.checked = true;

        const remoteId = fieldId("REMOTE_CONFIG_ENABLED");
        const remoteInput = el("input", {
            id: remoteId, type: "checkbox", name: "REMOTE_CONFIG_ENABLED",
        });
        if (parseBoolish(values.REMOTE_CONFIG_ENABLED, false)) remoteInput.checked = true;

        const footer = el("div", { class: "config-fieldset__footer" });
        const body = el("div");
        const form = el("form", { class: "config-form" }, body, footer);
        const fieldset = el("fieldset", { class: "config-fieldset" },
            el("legend", {}, "Privacy & remote management"),
            form,
        );

        body.appendChild(el("div", { class: "field" },
            el("label", { for: reportId }, reportInput, " Share feeder health with airplanes.live"),
        ));
        body.appendChild(el("p", { class: "help" },
            "Sends CPU, memory, disk and temperature readings so you can see this feeder's status on your dashboard ",
            "at airplanes.live and get notified when something goes wrong. Off means no dashboard or alerts for this feeder.",
        ));
        body.appendChild(el("div", { class: "field" },
            el("label", { for: remoteId }, remoteInput, " Manage this feeder from the website"),
        ));
        body.appendChild(el("p", { class: "help" },
            "Lets you change position, altitude and MLAT name from your account at airplanes.live, ",
            "without opening this page. You can still edit everything here either way.",
        ));

        parent.appendChild(fieldset);

        mountGroup({
            name: "privacy",
            formEl: form,
            footerEl: footer,
            keys: ["REPORT_STATUS", "REMOTE_CONFIG_ENABLED"],
            readInputs: () => ({
                REPORT_STATUS: reportInput.checked ? "true" : "false",
                REMOTE_CONFIG_ENABLED: remoteInput.checked ? "true" : "false",
            }),
            isValid: () => true,
            payload: () => {
                const out = {};
                const rs = reportInput.checked ? "true" : "false";
                const rc = remoteInput.checked ? "true" : "false";
                if (rs !== (configState.savedValues.REPORT_STATUS || "true")) out.REPORT_STATUS = rs;
                if (rc !== (configState.savedValues.REMOTE_CONFIG_ENABLED || "false")) out.REMOTE_CONFIG_ENABLED = rc;
                return out;
            },
        });
    }

    // ===== Action row =====

    // requestReboot POSTs /api/reboot and handles 409 (server refuses while
    // a maintenance unit is busy) and other failures uniformly. On 202 it
    // navigates to the "Rebooting…" card. Used by both the action-row button
    // and the reboot-required banner.
    async function requestReboot(triggerBtn, confirmFirst) {
        if (confirmFirst && !confirm("Reboot the feeder now?")) return;
        if (triggerBtn) {
            triggerBtn.disabled = true;
            triggerBtn.textContent = "Rebooting…";
        }
        const r = await postJSON("/api/reboot", {});
        if (handleAuthFailure(r)) return;
        if (!r.ok) {
            if (triggerBtn) {
                triggerBtn.disabled = false;
                triggerBtn.textContent = "Reboot";
            }
            alert((r.payload && r.payload.error) || "reboot failed");
            return;
        }
        navigate(() => render(el("div", { class: "wc-card" },
            el("h2", {}, "Rebooting…"),
            el("p", {}, "The feeder is restarting. This page will go offline for ~30 seconds."),
        )), { title: "Rebooting", showBack: false });
    }

    function buildActionsRow() {
        const updateBtn = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: async () => {
                updateBtn.disabled = true;
                const r = await postJSON("/api/update", {});
                updateBtn.disabled = false;
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    alert((r.payload && r.payload.error) || "update failed");
                    return;
                }
                navigate(() => logViewer("update"), { title: "Update log", showBack: true });
            },
        }, "Run update");

        const updateLog = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(() => logViewer("update"), { title: "Update log", showBack: true }),
        }, "Update log");

        const sysUpgradeBtn = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: async () => {
                sysUpgradeBtn.disabled = true;
                const r = await postJSON("/api/system-upgrade", {});
                sysUpgradeBtn.disabled = false;
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    alert((r.payload && r.payload.error) || "system upgrade failed");
                    return;
                }
                navigate(() => logViewer("system-upgrade"), { title: "System upgrade log", showBack: true });
            },
        }, "Update system packages");

        const sysUpgradeLog = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(() => logViewer("system-upgrade"), { title: "System upgrade log", showBack: true }),
        }, "System upgrade log");

        const webUiUpdateBtn = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: async () => {
                webUiUpdateBtn.disabled = true;
                // Capture /health BEFORE issuing the update POST so the
                // progress view has a baseline to compare against once the
                // service restarts. Doing this after the POST opens a race
                // where a very fast restart could land the new version
                // here and the poller would then wait forever for a change.
                let preUpdateHealth = null;
                try {
                    const h = await fetch("/health", { cache: "no-store" });
                    if (h.ok) preUpdateHealth = await h.text();
                } catch (_) { /* ignore — poller still works without baseline */ }
                const r = await postJSON("/api/webconfig-update", {});
                webUiUpdateBtn.disabled = false;
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    alert((r.payload && r.payload.error) || "web UI update failed");
                    return;
                }
                navigate(() => webconfigUpdateProgress(preUpdateHealth), { title: "Web UI update", showBack: true });
            },
        }, "Update web UI");

        const webUiUpdateLog = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(() => logViewer("webconfig-update"), { title: "Web UI update log", showBack: true }),
        }, "Web UI update log");

        const change = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(changePasswordPanel, { title: "Change password", showBack: true }),
        }, "Change password");

        const rebootBtn = el("button", {
            type: "button", class: "wc-btn-danger",
            onclick: () => requestReboot(rebootBtn, true),
        }, "Reboot");

        const poweroffBtn = el("button", {
            type: "button", class: "wc-btn-danger",
            onclick: () => navigate(confirmPowerOffPanel, { title: "Power off", showBack: true }),
        }, "Power off");

        const logout = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: async () => { await postJSON("/api/auth/logout", {}); await boot(); },
        }, "Log out");

        const wifiBtn = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(wifiPanel, { title: "Wi-Fi networks", showBack: true }),
        }, "Wi-Fi networks");

        return el("div", { class: "actions" },
            updateBtn, updateLog,
            sysUpgradeBtn, sysUpgradeLog,
            webUiUpdateBtn, webUiUpdateLog,
            wifiBtn, change, rebootBtn, poweroffBtn, logout,
        );
    }

    // ===== Wi-Fi panel =====

    async function wifiPanel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading Wi-Fi networks…")));

        const [list, status] = await Promise.all([
            getJSON("/api/wifi"),
            getJSON("/api/wifi/status"),
        ]);
        if (handleAuthFailure(list) || handleAuthFailure(status)) return;

        const listPayload = (list && list.payload) || {};
        const statusPayload = (status && status.payload) || {};
        const networks = listPayload.networks || [];
        const nmAvailable = !!listPayload.networkmanager_available;
        const activeConn = listPayload.active_connection || null;
        const nonWifiUplinks = statusPayload.non_wifi_uplinks || [];
        const hasEthernet = nonWifiUplinks.length > 0;
        const managedNets = networks.filter(n => n.managed);

        const flashEl = el("div", { class: "wifi-flash" });

        function setFlash(msg, kind) {
            flashEl.replaceChildren();
            if (!msg) return;
            flashEl.appendChild(el("div", { class: "wifi-flash-" + (kind || "ok"), role: "status" }, msg));
        }
        if (!nmAvailable) {
            setFlash("NetworkManager is not available on this feeder — Wi-Fi changes won't take effect.", "warn");
        }

        const statusBanner = el("div", { class: "wifi-status" });
        renderStatusBanner();
        function renderStatusBanner() {
            statusBanner.replaceChildren();
            if (activeConn && activeConn.ssid) {
                statusBanner.appendChild(el("p", {},
                    el("span", { class: "wifi-dot-active" }, ""),
                    " Connected to ",
                    el("strong", {}, activeConn.ssid),
                    activeConn.device ? el("span", { class: "muted" }, " on " + activeConn.device) : null,
                ));
            } else if (hasEthernet) {
                const e = nonWifiUplinks[0];
                statusBanner.appendChild(el("p", { class: "muted" },
                    "Ethernet only — " + (e.device || "") + (e.ipv4 ? " (" + e.ipv4 + ")" : "")));
            } else {
                statusBanner.appendChild(el("p", { class: "muted" }, "No active uplink detected."));
            }
        }

        const addBtn = el("button", { type: "button", class: "wc-btn-primary" }, "Add network");
        const tableHost = el("div", {});
        const formHost = el("div", {});

        function renderTable() {
            tableHost.replaceChildren();
            if (networks.length === 0) {
                tableHost.appendChild(el("p", { class: "muted" }, "No saved networks."));
                return;
            }
            const rows = networks.map(n => {
                const editBtn = el("button", {
                    type: "button", class: "wc-btn-ghost",
                    disabled: n.managed ? null : "",
                    title: n.managed ? null : "Foreign keyfile — edit via SSH",
                    onclick: () => showForm(n),
                }, "Edit");
                const activateBtn = el("button", {
                    type: "button", class: "wc-btn-ghost",
                    disabled: (!nmAvailable || n.active) ? "" : null,
                    onclick: () => activateNetwork(n),
                }, "Activate");
                const deleteBtn = el("button", {
                    type: "button", class: "wc-btn-danger",
                    disabled: n.managed ? null : "",
                    onclick: () => deleteNetwork(n),
                }, "Delete");
                return el("tr", {},
                    el("td", {},
                        n.active ? el("span", { class: "wifi-dot-active", title: "Active" }, "") : el("span", { class: "wifi-dot-idle" }, ""),
                        " ",
                        el("strong", {}, n.ssid || "(unknown)"),
                        n.first_run_profile ? el("span", { class: "wifi-badge" }, "first-run") : null,
                        n.managed ? null : el("span", { class: "wifi-badge wifi-badge-warn" }, "foreign"),
                        n.hidden ? el("span", { class: "wifi-badge" }, "hidden") : null,
                    ),
                    el("td", { class: "wifi-priority" }, String(n.priority || 0)),
                    el("td", { class: "wifi-actions" }, editBtn, activateBtn, deleteBtn),
                );
            });
            const tbl = el("table", { class: "wifi-table" },
                el("thead", {}, el("tr", {},
                    el("th", {}, "Network"),
                    el("th", {}, "Priority"),
                    el("th", {}, "Actions"),
                )),
                el("tbody", {}, ...rows),
            );
            tableHost.appendChild(tbl);
        }

        // Soft + strong confirm for delete. Both can compose; we always send
        // both force flags if needed (helper enforces server-side).
        async function deleteNetwork(n) {
            const remainingManaged = managedNets.filter(m => m.id !== n.id).length;
            const needForceLast = remainingManaged === 0;
            const needForceActive = n.active && !hasEthernet;

            if (needForceActive) {
                const typed = prompt(
                    "Deleting the active Wi-Fi network and no Ethernet is plugged in.\n\n" +
                    "After this the feeder will lose its uplink. To recover you'll need " +
                    "physical access (SD card) or another network the feeder still knows.\n\n" +
                    'Type DELETE to confirm:');
                if (typed !== "DELETE") return;
            } else if (needForceLast) {
                if (!confirm("This is the last saved Wi-Fi network. Delete anyway?")) return;
            } else {
                if (!confirm("Delete Wi-Fi network \"" + (n.ssid || n.id) + "\"?")) return;
            }

            const r = await deleteJSON("/api/wifi/" + encodeURIComponent(n.id), {
                force_last: needForceLast,
                force_active_no_uplink: needForceActive,
            });
            if (handleAuthFailure(r)) return;
            if (!r.ok) {
                setFlash((r.payload && (r.payload.message || r.payload.reason || r.payload.error)) || "delete failed", "error");
                return;
            }
            setFlash("Deleted " + (n.ssid || n.id), "ok");
            await wifiPanel();
        }

        async function activateNetwork(n) {
            const r = await postJSON("/api/wifi/" + encodeURIComponent(n.id) + "/activate", {});
            if (handleAuthFailure(r)) return;
            if (!r.ok) {
                setFlash((r.payload && (r.payload.message || r.payload.reason || r.payload.nm_reason || r.payload.error)) || "activate failed", "error");
                return;
            }
            setFlash("Activating " + (n.ssid || n.id) + "…", "ok");
            await wifiPanel();
        }

        function showForm(existing) {
            const isEdit = !!existing;
            const ssid = el("input", { type: "text", required: true, value: existing ? (existing.ssid || "") : "" });
            const psk = el("input", {
                type: "password", autocomplete: "new-password",
                placeholder: isEdit && existing.has_psk ? "(unchanged — leave blank to keep)" : "8-63 chars or 64-hex",
            });
            const hidden = el("input", { type: "checkbox" });
            if (existing && existing.hidden) hidden.checked = true;
            const priority = el("input", {
                type: "number", min: "0", max: "999",
                value: String(existing ? (existing.priority || 0) : 0),
            });
            const testBox = el("input", { type: "checkbox", checked: "" });
            const submit = el("button", { type: "submit", class: "wc-btn-primary" }, isEdit ? "Save changes" : "Add network");
            const cancel = el("button", { type: "button", class: "wc-btn-ghost" }, "Cancel");
            cancel.onclick = () => { formHost.replaceChildren(); };
            const inlineErr = el("p", { class: "error", role: "alert" });

            const form = el("form", {
                class: "wifi-form",
                onsubmit: async (e) => {
                    e.preventDefault();
                    inlineErr.textContent = "";
                    const ssidVal = ssid.value;
                    const pskVal = psk.value;
                    if (!isValidWifiSSID(ssidVal)) {
                        inlineErr.textContent = "SSID must be 1-32 bytes, no control characters.";
                        return;
                    }
                    if (pskVal !== "" && !isValidWifiPSK(pskVal)) {
                        inlineErr.textContent = "Password must be 8-63 printable ASCII chars or 64 hex chars.";
                        return;
                    }
                    const priVal = priority.value || "0";
                    if (!isValidWifiPriority(priVal)) {
                        inlineErr.textContent = "Priority must be an integer 0-999.";
                        return;
                    }
                    const body = {
                        ssid: ssidVal,
                        hidden: hidden.checked,
                        priority: parseInt(priVal, 10),
                        test: testBox.checked,
                    };
                    // On edit: only include PSK when the user typed something.
                    // Empty input means "leave existing PSK alone". The helper
                    // honors explicit "" as "convert to open network" — guard
                    // against that surprise by sending no key at all.
                    if (!isEdit || pskVal !== "") body.psk = pskVal;

                    submit.disabled = true;
                    submit.textContent = testBox.checked ? "Testing (up to 30s)…" : "Saving…";
                    const url = isEdit ? "/api/wifi/" + encodeURIComponent(existing.id) : "/api/wifi";
                    const r = isEdit ? await putJSON(url, body) : await postJSON(url, body);
                    submit.disabled = false;
                    submit.textContent = isEdit ? "Save changes" : "Add network";
                    if (handleAuthFailure(r)) return;
                    if (!r.ok) {
                        const p = r.payload || {};
                        const errs = p.errors || {};
                        const parts = Object.keys(errs).map(k => k + ": " + errs[k]);
                        const reason = p.reason || p.message || p.error || "save failed";
                        inlineErr.textContent = parts.length ? parts.join("; ") : reason;
                        if (p.nm_reason) {
                            inlineErr.textContent += " (" + p.nm_reason.split("\n")[0] + ")";
                        }
                        return;
                    }
                    setFlash(isEdit ? "Updated " + ssidVal : "Added " + ssidVal, "ok");
                    await wifiPanel();
                },
            },
                el("h3", {}, isEdit ? "Edit " + (existing.ssid || existing.id) : "Add network"),
                el("div", { class: "field" }, el("label", {}, "SSID"), ssid),
                el("div", { class: "field" }, el("label", {}, "Password"), psk),
                el("div", { class: "field-row" },
                    el("label", {}, hidden, " Hidden network"),
                    el("label", {}, "Priority ", priority),
                ),
                el("div", { class: "field" },
                    el("label", {}, testBox, " Test connection before saving"),
                    el("p", { class: "muted" }, "Tries to join now; rolls back on failure. Connecting to a new network may briefly drop this page if the feeder is on Wi-Fi."),
                ),
                inlineErr,
                el("div", { class: "actions" }, submit, cancel),
            );
            formHost.replaceChildren(form);
            ssid.focus();
        }

        addBtn.onclick = () => showForm(null);

        renderTable();

        render(
            el("section", { class: "wc-card" },
                el("h2", {}, "Wi-Fi networks"),
                statusBanner,
                flashEl,
                el("div", { class: "actions" }, addBtn),
                tableHost,
                formHost,
                el("p", { class: "muted wifi-help" },
                    "Add a second network before removing the one this feeder is on. The first-run network from airplanes-config.txt is shown as 'first-run'."),
            ),
        );
    }

    // buildRebootBannerSlot returns an empty container that the dashboard
    // mounts at the top of the render tree. updateRebootBanner toggles its
    // contents based on the /api/status reboot_required flag — invisible
    // when false, yellow banner with a Reboot button when true.
    function buildRebootBannerSlot() {
        return el("div", { class: "wc-reboot-slot" });
    }

    function updateRebootBanner(slot, rebootRequired) {
        if (!slot) return;
        if (!rebootRequired) {
            slot.replaceChildren();
            return;
        }
        // Already rendered: don't rebuild the DOM (and lose the button's
        // event handler context) on each 30s poll. Detect by checking the
        // slot already has children. replaceChildren() above is the only
        // path that clears it, so this stays in sync with the actual DOM.
        if (slot.firstChild) return;
        const btn = el("button", { type: "button", class: "wc-btn-danger" }, "Reboot now");
        btn.onclick = () => requestReboot(btn, false);
        slot.replaceChildren(el("div", { class: "wc-flash wc-flash--warn" },
            el("span", {}, "A reboot is required to finish applying system updates."),
            btn,
        ));
    }

    // ===== Dashboard =====

    async function dashboard() {
        const heroEl = buildHero();
        const tileGrid = buildTileGrid();
        const identityBody = buildIdentityCardBody();
        const privacyBody = buildPrivacyCardBody();
        const configBody = el("div", {}, el("p", { class: "muted" }, "loading…"));

        const identityCard = el("section", { class: "wc-card" }, el("h2", {}, "Identity"), identityBody);
        const privacyCard = el("section", { class: "wc-card" }, el("h2", {}, "Privacy & remote management"), privacyBody);
        const configCard = el("section", { class: "wc-card" }, el("h2", {}, "Configuration"), configBody);

        // Left column stacks Identity + Privacy; right column holds
        // Configuration. .wc-split has align-items: start so the left
        // column doesn't stretch when Configuration is taller.
        const leftColumn = el("div", { class: "wc-stack" }, identityCard, privacyCard);

        const flashNode = buildFlashNode(consumePendingFlash());
        const rebootBanner = buildRebootBannerSlot();
        const renderArgs = [];
        if (flashNode) renderArgs.push(flashNode);
        renderArgs.push(
            rebootBanner,
            heroEl.root,
            tileGrid.root,
            el("div", { class: "wc-split" }, leftColumn, configCard),
            buildActionsRow(),
        );
        render.apply(null, renderArgs);

        const ctx = {
            heroEl, tileGrid, identityBody, privacyBody, configBody, rebootBanner,
            configValues: {},
            lastIdentity: null,
            configDirty: false,
        };
        dashboardCtx = ctx;

        // Reset shared config state for this dashboard mount. Privacy
        // and Configuration both read from configState.savedValues, so
        // it must exist before either render runs.
        resetConfigState({});

        // Reset the msg-rate baseline so returning to the dashboard after
        // navigating away doesn't compute a rate over the gap (the gap-time
        // average is meaningless to the user; "rate pending" for one poll
        // is honest).
        rateBaseline = null;
        lastMsgRate = null;

        // Initial fetch.
        const [identity, status, config] = await Promise.all([
            getJSON("/api/identity"),
            getJSON("/api/status"),
            getJSON("/api/config"),
        ]);
        if (ctx !== dashboardCtx) return;
        if (handleAuthFailure(identity) || handleAuthFailure(status) || handleAuthFailure(config)) return;

        const cfgValues = normaliseSavedValues((config && config.payload && config.payload.values) || {});
        ctx.configValues = cfgValues;
        configState.savedValues = cfgValues;
        ctx.lastIdentity = identity && identity.payload ? identity.payload : null;

        renderIdentityCard(identityBody, identity);
        renderConfigCard(configBody, config);
        renderPrivacyCard(privacyBody, config);
        // computeMsgRate is side-effecting (advances its baseline on every
        // call), so route through updateMsgRateFromStatus exactly once per
        // poll cycle and let updateHero + the readsb tile read lastMsgRate.
        updateMsgRateFromStatus(status);
        updateHero(heroEl, status, ctx.configValues);
        updateTiles(tileGrid, status, ctx.configValues);
        updateAppTiles(tileGrid);
        updateRebootBanner(rebootBanner, !!(status && status.payload && status.payload.reboot_required));

        // ctx.configDirty is maintained by updateDashboardDirtyFlag()
        // inside the per-group mount helper — every recheck() call
        // refreshes it from configState.dirtyGroups.size > 0. No
        // separate form-level input listener needed.

        // Start partial-refresh poll. Hero + tiles + identity update;
        // config card + privacy card stay put (their state is
        // managed by per-group saves, not by status polling).
        statusTimer = setInterval(renderStatusSnapshot, STATUS_REFRESH_MS);
    }

    // pollInFlight serialises status polls. setInterval doesn't await, so
    // under network pressure a slow poll could land AFTER a newer one; the
    // out-of-order older response would feed an older feed.now into
    // computeMsgRate, blanking the baseline for the next cycle. Dropping
    // overlapping polls keeps the baseline monotonic.
    let pollInFlight = false;
    async function renderStatusSnapshot() {
        const ctx = dashboardCtx;
        if (!ctx) return;
        if (pollInFlight) return;

        // Pause polling while the user is typing in any config or
        // privacy input. Each group is its own <form>, so check the
        // whole tile body rather than a single form. Belt-and-braces:
        // nothing in the snapshot path mutates these tiles, but the
        // fetch is still wasted work mid-edit.
        const editingIn = (host) => host && document.activeElement && host.contains(document.activeElement);
        if (editingIn(ctx.configBody) || editingIn(ctx.privacyBody)) return;

        pollInFlight = true;
        try {
            const [identity, status] = await Promise.all([
                getJSON("/api/identity"),
                getJSON("/api/status"),
            ]);
            if (ctx !== dashboardCtx) return;
            if (handleAuthFailure(identity) || handleAuthFailure(status)) return;

            // See the initial-render call site above.
            updateMsgRateFromStatus(status);
            updateHero(ctx.heroEl, status, ctx.configValues);
            updateTiles(ctx.tileGrid, status, ctx.configValues);
            updateAppTiles(ctx.tileGrid);
            updateRebootBanner(ctx.rebootBanner, !!(status && status.payload && status.payload.reboot_required));

            const idPayload = identity && identity.payload ? identity.payload : null;
            if (idPayload && identityChanged(ctx.lastIdentity, idPayload)) {
                renderIdentityCard(ctx.identityBody, identity);
                ctx.lastIdentity = idPayload;
            }
        } finally {
            pollInFlight = false;
        }
    }

    // ===== Setup / login / change-password / log-viewer / corrupt =====

    function setupPanel() {
        const err = errorEl();
        const username = hiddenUsernameField();
        const pw = el("input", { id: "setup-pw", name: "new-password", type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const confirmInput = el("input", { id: "setup-pw-confirm", name: "confirm-password", type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const submit = el("button", { type: "submit", class: "wc-btn-primary" }, "Set password");

        const form = el("form", {
            class: "wc-card",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                if (pw.value !== confirmInput.value) {
                    err.textContent = "Passwords do not match.";
                    return;
                }
                if (pw.value.length < 12) {
                    err.textContent = "Password must be at least 12 characters.";
                    return;
                }
                submit.disabled = true;
                submit.textContent = "Setting…";
                const r = await postJSON("/api/setup", { password: pw.value });
                submit.disabled = false;
                submit.textContent = "Set password";
                if (!r.ok) {
                    err.textContent = (r.payload && r.payload.error) || "Setup failed.";
                    return;
                }
                await boot();
            },
        },
            el("h2", {}, "Set webconfig password"),
            el("p", {}, "Choose the password used to administer this feeder. Minimum 12 characters."),
            username,
            el("div", { class: "field" }, el("label", { for: "setup-pw" }, "Password"), pw),
            el("div", { class: "field" }, el("label", { for: "setup-pw-confirm" }, "Confirm password"), confirmInput),
            submit,
            err,
        );
        render(form);
        pw.focus();
    }

    function loginPanel(initialError) {
        const err = errorEl();
        if (initialError) err.textContent = initialError;
        const username = hiddenUsernameField();
        const pw = el("input", { id: "login-pw", name: "current-password", type: "password", autocomplete: "current-password", required: true });
        const submit = el("button", { type: "submit", class: "wc-btn-primary" }, "Log in");

        const form = el("form", {
            class: "wc-card",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                submit.disabled = true;
                submit.textContent = "Logging in…";
                const r = await postJSON("/api/auth/login", { password: pw.value });
                submit.disabled = false;
                submit.textContent = "Log in";
                if (!r.ok) {
                    err.textContent = r.status === 423
                        ? "Too many failed attempts. Try again later."
                        : (r.payload && r.payload.error) || "Login failed.";
                    pw.value = "";
                    pw.focus();
                    return;
                }
                await boot();
            },
        },
            el("h2", {}, "Log in"),
            username,
            el("div", { class: "field" }, el("label", { for: "login-pw" }, "Password"), pw),
            submit,
            err,
        );
        render(form);
        pw.focus();
    }

    function changePasswordPanel() {
        const err = errorEl();
        const username = hiddenUsernameField();
        const oldPw = el("input", { id: "change-pw-old", name: "current-password", type: "password", autocomplete: "current-password", required: true });
        const newPw = el("input", { id: "change-pw-new", name: "new-password", type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const confirmInput = el("input", { id: "change-pw-confirm", name: "confirm-password", type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const submit = el("button", { type: "submit", class: "wc-btn-primary" }, "Change password");
        const cancel = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigateDashboard(),
        }, "Cancel");

        const form = el("form", {
            class: "wc-card",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                if (newPw.value !== confirmInput.value) {
                    err.textContent = "New passwords do not match.";
                    return;
                }
                submit.disabled = true;
                submit.textContent = "Saving…";
                const r = await postJSON("/api/auth/password", {
                    old_password: oldPw.value,
                    new_password: newPw.value,
                });
                submit.disabled = false;
                submit.textContent = "Change password";
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    err.textContent = (r.payload && r.payload.error) || "Change failed.";
                    return;
                }
                navigateDashboard();
            },
        },
            el("h2", {}, "Change webconfig password"),
            username,
            el("div", { class: "field" }, el("label", { for: "change-pw-old" }, "Current password"), oldPw),
            el("div", { class: "field" }, el("label", { for: "change-pw-new" }, "New password"), newPw),
            el("div", { class: "field" }, el("label", { for: "change-pw-confirm" }, "Confirm new password"), confirmInput),
            el("div", { class: "actions" }, cancel, submit),
            err,
        );
        render(form);
        oldPw.focus();
    }

    function confirmPowerOffPanel() {
        const PHRASE = "power off";
        const err = errorEl();
        const input = el("input", {
            id: "poweroff-confirm",
            type: "text",
            autocomplete: "off",
            autocapitalize: "none",
            autocorrect: "off",
            spellcheck: "false",
            required: true,
        });
        const submit = el("button", { type: "submit", class: "wc-btn-danger", disabled: true }, "Power off");
        const cancel = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(dashboard, { title: null, showBack: false }),
        }, "Cancel");

        const matches = () => input.value.trim().toLowerCase() === PHRASE;
        input.addEventListener("input", () => { submit.disabled = !matches(); });

        const form = el("form", {
            class: "wc-card",
            onsubmit: async (e) => {
                e.preventDefault();
                if (!matches()) return;
                err.textContent = "";
                submit.disabled = true;
                cancel.disabled = true;
                submit.textContent = "Powering off…";
                const r = await postJSON("/api/poweroff", {});
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    submit.disabled = false;
                    cancel.disabled = false;
                    submit.textContent = "Power off";
                    err.textContent = (r.payload && r.payload.error) || "Power off failed.";
                    return;
                }
                navigate(() => render(el("div", { class: "wc-card" },
                    el("h2", {}, "Powering off…"),
                    el("p", {}, "The feeder is shutting down. You'll need to disconnect and reconnect power to turn it back on."),
                )), { title: "Powering off", showBack: false });
            },
        },
            el("h2", {}, "Power off the feeder?"),
            el("p", {}, "Shuts the feeder down cleanly so graphs1090 stats and other in-memory data are flushed to disk — safer than pulling the power cord."),
            el("p", { class: "error" }, "Once powered off, the feeder cannot be turned back on remotely. You'll need physical access to disconnect and reconnect power."),
            el("p", {}, "Type ", el("code", {}, PHRASE), " below to confirm."),
            el("div", { class: "field" }, el("label", { for: "poweroff-confirm" }, "Confirmation"), input),
            el("div", { class: "actions" }, cancel, submit),
            err,
        );
        render(form);
        input.focus();
    }

    // webconfigUpdateProgress is a specialised log viewer for the webconfig
    // self-update flow. The standard logViewer closes its EventSource on the
    // first error and prints "[stream closed]" — that's correct for feed
    // updates but wrong here, where the update INTENTIONALLY restarts this
    // service mid-stream. We extend the pattern: tail the journal until SSE
    // dies, then poll /health until it responds with a different version
    // (success) or until we give up (failure). On success, reload so the
    // SPA reconnects to the new binary; on failure, stay on the page with
    // a hint to refresh manually.
    //
    // baselineHealth: the /health response text captured BEFORE the POST
    // that started this update. May be null (fetch failed); the poller
    // treats null as "any successful /health after SSE death counts as
    // restart", which is a slightly weaker signal but still safe.
    function webconfigUpdateProgress(baselineHealth) {
        const slug = "webconfig-update";
        const pre = el("pre", { class: "log-output" });
        const status = el("p", { class: "muted" }, "Update in progress; the page will reload when the new web UI is ready.");
        const placeholder = el("span", { class: "muted" }, "(waiting for the update to begin)");
        pre.appendChild(placeholder);
        const clearPlaceholder = () => {
            if (placeholder.parentNode === pre) pre.removeChild(placeholder);
        };
        render(
            el("section", { class: "wc-card" },
                el("h2", {}, "Web UI update"),
                status,
                pre,
            ),
        );

        // cancelled lets the polling loop bail out cleanly when the user
        // navigates away. activeAbort + activeStream are reset by navigate()
        // on transition; setting both here ties this panel's resources to
        // that lifecycle.
        let cancelled = false;
        let pollTimer = null;
        const ctrl = new AbortController();
        const prevAbort = activeAbort;
        activeAbort = {
            abort: () => {
                cancelled = true;
                if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
                try { ctrl.abort(); } catch (_) {}
                if (prevAbort && typeof prevAbort.abort === "function") prevAbort.abort();
            },
        };

        const es = new EventSource("/api/log/" + encodeURIComponent(slug));
        activeStream = es;
        es.onmessage = (ev) => {
            clearPlaceholder();
            pre.appendChild(document.createTextNode(ev.data + "\n"));
            pre.scrollTop = pre.scrollHeight;
        };
        es.onerror = () => {
            clearPlaceholder();
            pre.appendChild(document.createTextNode("[restart in progress]\n"));
            try { es.close(); } catch (_) {}
            if (activeStream === es) activeStream = null;
            if (cancelled) return;
            status.textContent = "Waiting for the new web UI to come back online…";
            pollForRestart();
        };

        const POLL_INTERVAL_MS = 1000;
        const POLL_MAX_ATTEMPTS = 90; // 90 seconds total
        let attempts = 0;
        function pollForRestart() {
            if (cancelled) return;
            attempts += 1;
            fetch("/health", { cache: "no-store", signal: ctrl.signal })
                .then((r) => (r.ok ? r.text() : null))
                .then((t) => {
                    if (cancelled) return;
                    if (t && (baselineHealth === null || t !== baselineHealth)) {
                        status.textContent = "Update applied — reloading…";
                        pre.appendChild(document.createTextNode("[restart complete: " + t.trim() + "]\n"));
                        pollTimer = setTimeout(() => { if (!cancelled) window.location.reload(); }, 500);
                        return;
                    }
                    scheduleNext();
                })
                .catch(() => { if (!cancelled) scheduleNext(); });
        }
        function scheduleNext() {
            if (cancelled) return;
            if (attempts >= POLL_MAX_ATTEMPTS) {
                status.textContent = "The web UI did not come back online within 90 seconds. Refresh the page once the feeder is reachable, or check the log for a rollback message.";
                return;
            }
            pollTimer = setTimeout(pollForRestart, POLL_INTERVAL_MS);
        }
    }

    function logViewer(slug) {
        const pre = el("pre", { class: "log-output" });
        const unit = LOG_SLUG_TO_UNIT[slug] || slug;
        // Placeholder shown until the first event arrives (or the stream
        // closes without one) — without it the <pre> is just an empty box,
        // which looks broken on services that haven't run yet (e.g. the
        // update log on a freshly booted feeder).
        const placeholder = el("span", { class: "muted" }, "(no log entries yet)");
        pre.appendChild(placeholder);
        const clearPlaceholder = () => {
            if (placeholder.parentNode === pre) pre.removeChild(placeholder);
        };
        render(
            el("section", { class: "wc-card" },
                el("h2", {}, "journalctl -u " + unit),
                el("p", { class: "muted" }, "Streaming live; close this view (use the Dashboard button above) to disconnect."),
                pre,
            ),
        );
        const es = new EventSource("/api/log/" + encodeURIComponent(slug));
        activeStream = es;
        es.onmessage = (ev) => {
            clearPlaceholder();
            pre.appendChild(document.createTextNode(ev.data + "\n"));
            pre.scrollTop = pre.scrollHeight;
        };
        es.onerror = () => {
            clearPlaceholder();
            pre.appendChild(document.createTextNode("[stream closed]\n"));
            try { es.close(); } catch (_) {}
            if (activeStream === es) activeStream = null;
        };
    }

    function loadingPanel(msg) {
        render(el("div", { class: "wc-card" }, el("p", {}, msg)));
    }

    function corruptPanel(msg) {
        render(el("div", { class: "wc-card" },
            el("h2", {}, "Recovery required"),
            el("p", {}, msg),
            el("p", {}, "Drop /boot/firmware/airplanes-reset-password on the SD card and reboot to start over."),
        ));
    }

    // ===== Bootstrap =====

    // populateBrandVersion fetches /health (public, no auth) once on boot and
    // writes the running version into the header brand strip. The body is
    // `ok <version>\n`; failures are silently swallowed — the brand strip
    // just stays without a version suffix.
    async function populateBrandVersion() {
        if (!brandVersionEl) return;
        try {
            const r = await fetch("/health", { cache: "no-store" });
            if (!r.ok) return;
            const body = await r.text();
            const m = body.match(/^ok\s+(\S+)/);
            if (!m) return;
            brandVersionEl.textContent = m[1];
            brandVersionEl.hidden = false;
        } catch (_) { /* ignore — header just stays version-less */ }
    }

    async function boot() {
        // Fire-and-forget; populating the brand version is independent of
        // the auth/setup boot flow.
        populateBrandVersion();
        navigate(() => loadingPanel("Loading…"), {});
        const stateResp = await getJSON("/api/state");
        if (!stateResp.ok) {
            navigate(() => corruptPanel((stateResp.payload && stateResp.payload.error) || "Server returned an unexpected error."), {});
            return;
        }
        if (stateResp.payload.state === "uninitialized") {
            navigate(setupPanel, { title: "First-time setup", showBack: false });
            return;
        }
        const who = await getJSON("/api/auth/whoami");
        if (who.ok) {
            navigateDashboard();
        } else {
            navigate(loginPanel, { title: null, showBack: false });
        }
    }

    // ===== Wire-up =====

    if (themeBtn) themeBtn.addEventListener("click", toggleTheme);
    if (backBtn) backBtn.addEventListener("click", () => navigateDashboard());
    if (refreshBtn) refreshBtn.addEventListener("click", async () => {
        if (refreshBtn.disabled) return;
        if (dashboardCtx && dashboardCtx.configDirty
            && !confirm("Discard unsaved configuration changes?")) {
            return;
        }
        refreshBtn.disabled = true;
        refreshBtn.classList.add("wc-btn-icon--spinning");
        try {
            await navigateDashboard();
        } finally {
            refreshBtn.classList.remove("wc-btn-icon--spinning");
            refreshBtn.disabled = false;
        }
    });

    boot().catch((e) => corruptPanel("Fatal error: " + (e && e.message || e)));
})();
