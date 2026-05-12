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
        "webconfig": "airplanes-webconfig.service",
        "update":    "airplanes-update.service",
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

    // ===== Runtime state =====

    const app = document.getElementById("app");
    const headerTitleEl = document.getElementById("wc-header-title");
    const backBtn = document.getElementById("wc-back-btn");
    const themeBtn = document.getElementById("wc-theme-btn");

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
        panelFn();
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

    let rateBaseline = null;
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
                    const rate = computeMsgRate(feed);
                    let parts = [];
                    if (n !== null) parts.push(n + " aircraft");
                    if (rate !== null) parts.push(rate.toFixed(0) + " msg/s");
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

        // Meta line: webconfig version. Per-service rates are owned by
        // the readsb tile (computeMsgRate has side effects, so it runs
        // exactly once per poll, not twice).
        const parts = [];
        const v = payload.version;
        if (v) parts.push("v" + v);
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
            tile.root.setAttribute("data-state", "unknown");
            return;
        }
        const sev = ph.severity || "na";
        tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + sev;
        tile.metaEl.textContent = ph.summary || "—";
        tile.root.setAttribute("data-state", sev);
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
        return { root, tiles, appTiles, piHealth };
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
            tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + (ok ? "ok" : "err");
            tile.metaEl.textContent = ok ? a.meta : (a.meta + " · unreachable");
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
                "No claim secret yet — apl-feed claim register will create one."));
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

    function renderConfigCard(parent, resp) {
        parent.replaceChildren();
        if (!resp || !resp.ok) {
            parent.appendChild(el("p", { class: "error", role: "alert" },
                (resp && resp.payload && resp.payload.error) || "could not load config"));
            return;
        }
        const values = resp.payload.values || {};
        const inputs = {};

        const fieldId = (key) => "config-" + key.toLowerCase().replace(/_/g, "-");

        const field = (key, label, attrs) => {
            const id = fieldId(key);
            const a = Object.assign({ id, name: key, value: values[key] || "", type: "text" }, attrs || {});
            const input = el("input", a);
            inputs[key] = input;
            return el("div", { class: "field" },
                el("label", { for: id }, label, " ", el("code", {}, key)),
                input,
            );
        };

        const dump978On = (values["UAT_INPUT"] || "") !== "";
        const uatId = fieldId("UAT_INPUT");
        const uat = el("input", {
            id: uatId,
            type: "checkbox",
            name: "UAT_INPUT",
            checked: dump978On ? "" : null,
        });
        inputs["UAT_INPUT"] = uat;

        // 978 dependent fields. Defaults mirror dump978-fa.sh's wrapper
        // fallback ("978" / "42.1") so the form pre-renders the same value
        // the daemon would use if the user simply toggles UAT on.
        const sdrSerialId = fieldId("DUMP978_SDR_SERIAL");
        const dump978Serial = el("input", {
            id: sdrSerialId,
            type: "text",
            name: "DUMP978_SDR_SERIAL",
            value: values["DUMP978_SDR_SERIAL"] || "978",
            placeholder: "978",
        });
        inputs["DUMP978_SDR_SERIAL"] = dump978Serial;

        const gainId = fieldId("DUMP978_GAIN");
        const dump978Gain = el("input", {
            id: gainId,
            type: "text",
            name: "DUMP978_GAIN",
            value: values["DUMP978_GAIN"] || "42.1",
            placeholder: "42.1",
            inputmode: "decimal",
        });
        inputs["DUMP978_GAIN"] = dump978Gain;

        // MLAT_ENABLED is a separate boolean toggle in the new schema. Default
        // to "true" when the key is absent (e.g. on a fresh feed.env that
        // hasn't been written yet) to match the daemon's MLAT_ENABLED:-true.
        const mlatOn = (values["MLAT_ENABLED"] || "true") === "true";
        const mlatId = fieldId("MLAT_ENABLED");
        const mlat = el("input", {
            id: mlatId,
            type: "checkbox",
            name: "MLAT_ENABLED",
            checked: mlatOn ? "" : null,
        });
        inputs["MLAT_ENABLED"] = mlat;

        // MLAT_PRIVATE hides the feed name on the public MLAT map. Default
        // false (name shown) when absent, matching airplanes-mlat.sh's
        // MLAT_PRIVATE:-false. Position is never shown precisely regardless
        // of the toggle.
        const mlatPrivateOn = (values["MLAT_PRIVATE"] || "false") === "true";
        const mlatPrivateId = fieldId("MLAT_PRIVATE");
        const mlatPrivate = el("input", {
            id: mlatPrivateId,
            type: "checkbox",
            name: "MLAT_PRIVATE",
            checked: mlatPrivateOn ? "" : null,
        });
        inputs["MLAT_PRIVATE"] = mlatPrivate;

        const err = errorEl();
        const submit = el("button", { type: "submit", class: "wc-btn-primary" }, "Save & restart");

        // Gate Save on the same MLAT → geo requirement that
        // configspec.ValidateConsistency enforces server-side. Reading
        // the inputs directly (rather than capturing the saved values)
        // means flipping the checkbox or editing a coord live-updates
        // the inline error and the Save button.
        const recheck = () => {
            const lat = inputs.LATITUDE.value;
            const lon = inputs.LONGITUDE.value;
            const alt = inputs.ALTITUDE.value;
            if (!mlat.checked) {
                err.textContent = "";
                submit.disabled = false;
                return;
            }
            // mlat is on — every coord must parse to the same strict shape
            // configspec.go requires AND not be the (0,0) placeholder pair.
            const latOk = isValidLatitude(lat);
            const lonOk = isValidLongitude(lon);
            const altOk = isValidAltitude(alt);
            const isPlaceholder = latOk && lonOk
                && Number(lat.trim()) === 0 && Number(lon.trim()) === 0;
            if (!latOk || !lonOk || !altOk || isPlaceholder) {
                const missing = [];
                if (!latOk) missing.push("latitude");
                else if (!lonOk) missing.push("longitude");
                else if (isPlaceholder) missing.push("non-(0,0) coordinates");
                if (!altOk) missing.push("altitude");
                err.textContent = "Set valid " + missing.join(", ") + " to enable MLAT.";
                submit.disabled = true;
            } else {
                err.textContent = "";
                submit.disabled = false;
            }
        };

        const form = el("form", {
            class: "config-form",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                submit.disabled = true;
                submit.textContent = "Saving…";
                const updates = {
                    LATITUDE: inputs.LATITUDE.value.trim(),
                    LONGITUDE: inputs.LONGITUDE.value.trim(),
                    ALTITUDE: inputs.ALTITUDE.value.trim(),
                    MLAT_USER: inputs.MLAT_USER.value.trim(),
                    MLAT_ENABLED: mlat.checked ? "true" : "false",
                    MLAT_PRIVATE: mlatPrivate.checked ? "true" : "false",
                    GAIN: inputs.GAIN.value.trim(),
                    UAT_INPUT: uat.checked ? "127.0.0.1:30978" : "",
                    DUMP978_SDR_SERIAL: dump978Serial.value.trim(),
                    DUMP978_GAIN: dump978Gain.value.trim(),
                };
                // Don't write DUMP978_* keys when UAT is toggled off — they
                // would be no-ops the daemon never reads, and writing them
                // anyway would let a stale serial/gain accumulate in feed.env
                // even after the user disabled 978. The wrapper falls back
                // to compiled-in defaults (978 / 42.1) on next enable.
                if (!uat.checked) {
                    delete updates.DUMP978_SDR_SERIAL;
                    delete updates.DUMP978_GAIN;
                }
                // Always send MLAT_ENABLED, MLAT_PRIVATE, and UAT_INPUT —
                // they're explicit toggles whose "false"/"" form is
                // meaningful. Strip other keys when empty so the user can
                // leave a field unchanged from the current value rather
                // than blanking it.
                for (const k of Object.keys(updates)) {
                    if (k !== "UAT_INPUT" && k !== "MLAT_ENABLED" && k !== "MLAT_PRIVATE" && k !== "MLAT_USER" && updates[k] === "") delete updates[k];
                }
                const r = await postJSON("/api/config", { updates });
                submit.disabled = false;
                submit.textContent = "Save & restart";
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    err.textContent = (r.payload && r.payload.error) || "save failed";
                    return;
                }
                // Reload the dashboard so the new values + restart show up.
                // If the server reported a restart failure, pass a one-shot
                // flash through navigate options — without this, the warning
                // would clear immediately when navigate() rerenders.
                const pending = (r.payload && r.payload.pending_restart) || [];
                const opts = { title: null, showBack: false };
                if (pending.length > 0) {
                    opts.flash = {
                        level: "warn",
                        text: "Saved, but service restart failed: " + pending.join(", ")
                            + ". The dashboard reflects the previously running service "
                            + "until you restart manually: sudo systemctl restart "
                            + pending.join(" "),
                    };
                }
                navigate(dashboard, opts);
            },
        },
            el("fieldset", { class: "config-fieldset" },
                el("legend", {}, "Location"),
                field("LATITUDE", "Latitude", { inputmode: "decimal", placeholder: "51.5" }),
                field("LONGITUDE", "Longitude", { inputmode: "decimal", placeholder: "-0.1" }),
                field("ALTITUDE", "Altitude", { placeholder: "120m" }),
            ),
            el("fieldset", { class: "config-fieldset" },
                el("legend", {}, "MLAT"),
                field("MLAT_USER", "MLAT name", { placeholder: "alice" }),
                el("div", { class: "field" },
                    el("label", { for: mlatId }, mlat, " Enable MLAT", " ", el("code", {}, "MLAT_ENABLED")),
                ),
                el("div", { class: "field" },
                    el("label", { for: mlatPrivateId }, mlatPrivate, " Hide MLAT name on public map", " ", el("code", {}, "MLAT_PRIVATE")),
                ),
            ),
            field("GAIN", "Gain", { placeholder: "auto" }),
            el("div", { class: "field" },
                el("label", { for: uatId }, uat, " Enable 978 UAT", " ", el("code", {}, "UAT_INPUT")),
            ),
            el("div", { class: "field" },
                el("label", { for: sdrSerialId }, "978 SDR serial", " ", el("code", {}, "DUMP978_SDR_SERIAL")),
                dump978Serial,
            ),
            el("div", { class: "field" },
                el("label", { for: gainId }, "978 gain", " ", el("code", {}, "DUMP978_GAIN")),
                dump978Gain,
            ),
            submit,
            err,
        );
        parent.appendChild(form);

        // Wire the gate listeners now that the form is in the DOM. Each
        // input edit and the MLAT toggle re-run the same check.
        inputs.LATITUDE.addEventListener("input", recheck);
        inputs.LONGITUDE.addEventListener("input", recheck);
        inputs.ALTITUDE.addEventListener("input", recheck);
        mlat.addEventListener("change", recheck);
        recheck();

        const readOnly = Object.keys(values).filter(k =>
            !["LATITUDE", "LONGITUDE", "ALTITUDE", "MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE", "GAIN", "UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN"].includes(k)
        ).sort();
        if (readOnly.length > 0) {
            const tbl = el("dl", { class: "config-list" });
            for (const k of readOnly) {
                tbl.appendChild(el("dt", {}, k));
                tbl.appendChild(el("dd", {}, values[k] === "" ? "(empty)" : values[k]));
            }
            parent.appendChild(el("details", {},
                el("summary", { class: "muted" }, "Advanced (read-only)"),
                tbl,
            ));
        }
    }

    // ===== Action row =====

    function buildActionsRow() {
        const refresh = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(dashboard, { title: null, showBack: false }),
        }, "Refresh");

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

        const change = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(changePasswordPanel, { title: "Change password", showBack: true }),
        }, "Change password");

        const rebootBtn = el("button", {
            type: "button", class: "wc-btn-danger",
            onclick: async () => {
                if (!confirm("Reboot the feeder now?")) return;
                rebootBtn.disabled = true;
                rebootBtn.textContent = "Rebooting…";
                await postJSON("/api/reboot", {});
                navigate(() => render(el("div", { class: "wc-card" },
                    el("h2", {}, "Rebooting…"),
                    el("p", {}, "The feeder is restarting. This page will go offline for ~30 seconds."),
                )), { title: "Rebooting", showBack: false });
            },
        }, "Reboot");

        const logout = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: async () => { await postJSON("/api/auth/logout", {}); await boot(); },
        }, "Log out");

        const wifiBtn = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(wifiPanel, { title: "Wi-Fi networks", showBack: true }),
        }, "Wi-Fi networks");

        return el("div", { class: "actions" }, refresh, updateBtn, updateLog, wifiBtn, change, rebootBtn, logout);
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

    // ===== Dashboard =====

    async function dashboard() {
        const heroEl = buildHero();
        const tileGrid = buildTileGrid();
        const identityBody = buildIdentityCardBody();
        const configBody = el("div", {}, el("p", { class: "muted" }, "loading…"));

        const identityCard = el("section", { class: "wc-card" }, el("h2", {}, "Identity"), identityBody);
        const configCard = el("section", { class: "wc-card" }, el("h2", {}, "Configuration"), configBody);

        const flashNode = buildFlashNode(consumePendingFlash());
        const renderArgs = [];
        if (flashNode) renderArgs.push(flashNode);
        renderArgs.push(
            heroEl.root,
            tileGrid.root,
            el("div", { class: "wc-split" }, identityCard, configCard),
            buildActionsRow(),
        );
        render.apply(null, renderArgs);

        const ctx = {
            heroEl, tileGrid, identityBody, configBody,
            configValues: {},
            lastIdentity: null,
        };
        dashboardCtx = ctx;

        // Initial fetch.
        const [identity, status, config] = await Promise.all([
            getJSON("/api/identity"),
            getJSON("/api/status"),
            getJSON("/api/config"),
        ]);
        if (ctx !== dashboardCtx) return;
        if (handleAuthFailure(identity) || handleAuthFailure(status) || handleAuthFailure(config)) return;

        ctx.configValues = (config && config.payload && config.payload.values) || {};
        ctx.lastIdentity = identity && identity.payload ? identity.payload : null;

        renderIdentityCard(identityBody, identity);
        renderConfigCard(configBody, config);
        updateHero(heroEl, status, ctx.configValues);
        updateTiles(tileGrid, status, ctx.configValues);
        updateAppTiles(tileGrid);

        // Start partial-refresh poll. Hero + tiles + identity update;
        // config card stays put.
        statusTimer = setInterval(renderStatusSnapshot, STATUS_REFRESH_MS);
    }

    async function renderStatusSnapshot() {
        const ctx = dashboardCtx;
        if (!ctx) return;

        // Pause polling while user is typing in config form. Belt-and-
        // braces: nothing in the snapshot path mutates the form, but the
        // fetch is still wasted work if the user is mid-edit.
        const formEl = ctx.configBody && ctx.configBody.querySelector("form.config-form");
        if (formEl && document.activeElement && formEl.contains(document.activeElement)) return;

        const [identity, status] = await Promise.all([
            getJSON("/api/identity"),
            getJSON("/api/status"),
        ]);
        if (ctx !== dashboardCtx) return;
        if (handleAuthFailure(identity) || handleAuthFailure(status)) return;

        updateHero(ctx.heroEl, status, ctx.configValues);
        updateTiles(ctx.tileGrid, status, ctx.configValues);
        updateAppTiles(ctx.tileGrid);

        const idPayload = identity && identity.payload ? identity.payload : null;
        if (idPayload && identityChanged(ctx.lastIdentity, idPayload)) {
            renderIdentityCard(ctx.identityBody, identity);
            ctx.lastIdentity = idPayload;
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
            onclick: () => navigate(dashboard, { title: null, showBack: false }),
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
                navigate(dashboard, { title: null, showBack: false });
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

    function logViewer(slug) {
        const pre = el("pre", { class: "log-output" });
        const unit = LOG_SLUG_TO_UNIT[slug] || slug;
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
            pre.appendChild(document.createTextNode(ev.data + "\n"));
            pre.scrollTop = pre.scrollHeight;
        };
        es.onerror = () => {
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

    async function boot() {
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
            navigate(dashboard, { title: null, showBack: false });
        } else {
            navigate(loginPanel, { title: null, showBack: false });
        }
    }

    // ===== Wire-up =====

    if (themeBtn) themeBtn.addEventListener("click", toggleTheme);
    if (backBtn) backBtn.addEventListener("click", () => navigate(dashboard, { title: null, showBack: false }));

    boot().catch((e) => corruptPanel("Fatal error: " + (e && e.message || e)));
})();
