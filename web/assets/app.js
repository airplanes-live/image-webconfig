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
    // Order mirrors the dashboard layout: row 2 is the 1090 stack
    // (readsb → feed → mlat), row 3 is the 978 UAT stack.
    const MONITORED_SERVICES = [
        "readsb.service",
        "airplanes-feed.service",
        "airplanes-mlat.service",
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
        "update-orchestrator":  "airplanes-update-orchestrator.service",
        "fr24":      "airplanes-aggregator@fr24.service",
        "piaware":   "piaware.service",
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
    const HARDWARE_ICON =
        "M5 0a.5.5 0 0 1 .5.5V2h1V.5a.5.5 0 0 1 1 0V2h1V.5a.5.5 0 0 1 1 0V2h1V.5a.5.5 0 0 1 1 0V2A2.5 2.5 0 0 1 14 4.5h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14v1h1.5a.5.5 0 0 1 0 1H14A2.5 2.5 0 0 1 11.5 14H10v1.5a.5.5 0 0 1-1 0V14H8v1.5a.5.5 0 0 1-1 0V14H6v1.5a.5.5 0 0 1-1 0V14H4.5A2.5 2.5 0 0 1 2 11.5H.5a.5.5 0 0 1 0-1H2v-1H.5a.5.5 0 0 1 0-1H2v-1H.5a.5.5 0 0 1 0-1H2v-1H.5a.5.5 0 0 1 0-1H2A2.5 2.5 0 0 1 4.5 2V.5A.5.5 0 0 1 5 0m-.5 3A1.5 1.5 0 0 0 3 4.5v7A1.5 1.5 0 0 0 4.5 13h7a1.5 1.5 0 0 0 1.5-1.5v-7A1.5 1.5 0 0 0 11.5 3z";

    // Bootstrap-icons wifi glyph (16×16 viewBox).
    const WIFI_ICON =
        "M15.384 6.115a.485.485 0 0 0-.047-.736A12.44 12.44 0 0 0 8 3C5.259 3 2.723 3.882.663 5.379a.485.485 0 0 0-.048.736.518.518 0 0 0 .668.05A11.45 11.45 0 0 1 8 4c2.507 0 4.827.802 6.716 2.164.205.148.49.13.668-.049m-2.55 2.516a.482.482 0 0 0-.063-.745A8.46 8.46 0 0 0 8 7a8.46 8.46 0 0 0-4.77 1.886.482.482 0 0 0-.064.745.525.525 0 0 0 .654.065A7.46 7.46 0 0 1 8 8c1.71 0 3.29.578 4.18 1.696a.525.525 0 0 0 .654-.065zm-2.557 2.514a.483.483 0 0 0-.089-.745A4.47 4.47 0 0 0 8 10c-.83 0-1.605.247-2.188.4a.483.483 0 0 0-.089.745.525.525 0 0 0 .626.085A3.47 3.47 0 0 1 8 11c.488 0 .947.118 1.349.314a.525.525 0 0 0 .626-.085zM9.5 14.25a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0z";

    // Bootstrap-icons share-fill glyph — outbound data-sharing, distinct from
    // the broadcast/reception geometry the readsb/mlat tiles use.
    const AGGREGATOR_ICON =
        "M11 2.5a2.5 2.5 0 1 1 .603 1.628l-6.718 3.12a2.5 2.5 0 0 1 0 1.504l6.718 3.12a2.5 2.5 0 1 1-.488.876l-6.718-3.12a2.5 2.5 0 1 1 0-3.256l6.718-3.12A2.5 2.5 0 0 1 11 2.5";

    // Bootstrap-icons clipboard glyph (two subpaths in one d string).
    const COPY_ICON =
        "M4 1.5H3a2 2 0 0 0-2 2V14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V3.5a2 2 0 0 0-2-2h-1v1h1a1 1 0 0 1 1 1V14a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3.5a1 1 0 0 1 1-1h1z M9.5 1a.5.5 0 0 1 .5.5v1a.5.5 0 0 1-.5.5h-3a.5.5 0 0 1-.5-.5v-1a.5.5 0 0 1 .5-.5zm-3-1A1.5 1.5 0 0 0 5 1.5v1A1.5 1.5 0 0 0 6.5 4h3A1.5 1.5 0 0 0 11 2.5v-1A1.5 1.5 0 0 0 9.5 0z";
    // Bootstrap-icons check2 glyph, shown briefly after a successful copy.
    const COPY_DONE_ICON =
        "M13.854 3.646a.5.5 0 0 1 0 .708l-7 7a.5.5 0 0 1-.708 0l-3.5-3.5a.5.5 0 1 1 .708-.708L6.5 10.293l6.646-6.647a.5.5 0 0 1 .708 0";

    // ===== Runtime state =====

    const app = document.getElementById("app");
    const headerTitleEl = document.getElementById("wc-header-title");
    const brandVersionEl = document.getElementById("wc-brand-version");
    const backBtn = document.getElementById("wc-back-btn");
    const themeBtn = document.getElementById("wc-theme-btn");
    const refreshBtn = document.getElementById("wc-refresh-btn");
    const userMenu = document.getElementById("wc-user-menu");
    const userMenuChangePw = document.getElementById("wc-user-change-pw");
    const userMenuLogout = document.getElementById("wc-user-logout");
    const userMenuReboot = document.getElementById("wc-user-reboot");
    const userMenuPoweroff = document.getElementById("wc-user-poweroff");

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
        if (userMenu) {
            userMenu.hidden = !o.showUserMenu;
            if (!o.showUserMenu) closeUserMenu();
        }
    }

    // closeUserMenu collapses the <details> menu and tears down its
    // outside-click / Escape listeners. Safe to call when the menu is
    // already closed (no-op).
    let userMenuOutsideHandler = null;
    let userMenuKeyHandler = null;
    function closeUserMenu() {
        if (!userMenu) return;
        if (userMenu.open) userMenu.open = false;
        if (userMenuOutsideHandler) {
            document.removeEventListener("pointerdown", userMenuOutsideHandler, true);
            userMenuOutsideHandler = null;
        }
        if (userMenuKeyHandler) {
            document.removeEventListener("keydown", userMenuKeyHandler, true);
            userMenuKeyHandler = null;
        }
    }
    function openUserMenuHooks() {
        if (!userMenu) return;
        // Defer attaching so the click that opened the menu doesn't
        // immediately close it. The opening event is already in flight
        // when we get here; the next pointerdown is a genuine outside
        // (or inside) interaction.
        setTimeout(() => {
            if (!userMenu.open) return;
            userMenuOutsideHandler = (e) => {
                if (!userMenu.contains(e.target)) closeUserMenu();
            };
            userMenuKeyHandler = (e) => {
                if (e.key === "Escape") {
                    e.preventDefault();
                    closeUserMenu();
                    const summary = userMenu.querySelector("summary");
                    if (summary) summary.focus();
                }
            };
            document.addEventListener("pointerdown", userMenuOutsideHandler, true);
            document.addEventListener("keydown", userMenuKeyHandler, true);
        }, 0);
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
        let cls = "wc-flash";
        if (flash.level === "warn") cls += " wc-flash--warn";
        else if (flash.level === "ok") cls += " wc-flash--ok";
        const node = el("div", { class: cls, role: "status", "aria-live": "polite" },
            el("span", {}, flash.text));
        const dismiss = el("button", {
            type: "button",
            class: "wc-flash__dismiss",
            "aria-label": "Dismiss",
            title: "Dismiss",
        }, "×");
        dismiss.onclick = () => node.remove();
        node.appendChild(dismiss);
        return node;
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
            { title: null, showBack: false, showRefresh: true, showUserMenu: true },
            extraOpts || {}));
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
            // Drop any flash a mutation queued before its re-render hit the
            // 401 — it's stale and would otherwise surface on the dashboard
            // after re-login.
            pendingFlash = null;
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

    // copyTextToClipboard prefers the async Clipboard API but falls back to
    // an off-screen <textarea> + execCommand("copy"). The fallback is the
    // path that actually runs on a feeder: the webconfig is served over
    // plain HTTP on the LAN, which is not a secure context, so
    // navigator.clipboard is undefined. It is also used when the async API
    // is present but rejects (permission / user-activation quirks).
    // try/finally guarantees the textarea is removed even if select throws.
    function copyTextToClipboard(text) {
        const fallback = () => new Promise((resolve, reject) => {
            const ta = document.createElement("textarea");
            ta.value = text;
            ta.setAttribute("readonly", "");
            ta.style.position = "fixed";
            ta.style.top = "-9999px";
            ta.style.opacity = "0";
            document.body.appendChild(ta);
            try {
                ta.focus({ preventScroll: true });
                ta.select();
                ta.setSelectionRange(0, ta.value.length);
                if (document.execCommand("copy")) resolve();
                else reject(new Error("copy failed"));
            } catch (e) {
                reject(e);
            } finally {
                document.body.removeChild(ta);
            }
        });
        if (navigator.clipboard && window.isSecureContext) {
            return navigator.clipboard.writeText(text).catch(fallback);
        }
        return fallback();
    }

    // copyButton returns a small inline icon button that copies `value` and
    // briefly swaps to a check glyph (or an error state) for feedback.
    // `label` names the value for the aria-label / tooltip. The `gen` token
    // stops a slow or failed copy from overwriting the state of a newer
    // click, and stops a stale revert timer from firing.
    function copyButton(value, label) {
        const btn = el("button", {
            class: "wc-copy-btn",
            type: "button",
            "aria-label": "Copy " + label,
            title: "Copy " + label,
        }, svgIcon(COPY_ICON, 14));
        let gen = 0;
        btn.addEventListener("click", async () => {
            const myGen = ++gen;
            let done = true;
            try { await copyTextToClipboard(value); }
            catch (_) { done = false; }
            if (myGen !== gen) return;
            btn.replaceChildren(svgIcon(done ? COPY_DONE_ICON : COPY_ICON, 14));
            btn.classList.toggle("wc-copy-btn--done", done);
            btn.classList.toggle("wc-copy-btn--error", !done);
            btn.title = done ? "Copied" : "Copy failed — select manually";
            btn.setAttribute("aria-label",
                done ? label + " copied" : "Copy failed — select manually");
            setTimeout(() => {
                if (myGen !== gen) return;
                btn.replaceChildren(svgIcon(COPY_ICON, 14));
                btn.classList.remove("wc-copy-btn--done", "wc-copy-btn--error");
                btn.title = "Copy " + label;
                btn.setAttribute("aria-label", "Copy " + label);
            }, 1400);
        });
        return btn;
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

    // Mirror the bash validators in feed/scripts/lib/configure-validators.sh
    // (cross-repo) and files/usr/local/lib/airplanes/wifi-validators.sh
    // (in-repo). apl-feed apply is the authoritative server-side gate; the
    // JS preview here only suppresses Save while the user is editing. A
    // JS-vs-bash mismatch surfaces as "looks valid in the form but save
    // failed", so these accept/reject the same inputs as the bash side.
    // Different regex engines, semantically equivalent rules. Pinned by
    // internal/clientvalidators/parity_test.go, which runs the actual
    // shipped JS in a Node subprocess against the actual shipped bash
    // functions across a shared input-vector table.
    //
    // The /* @validator-parity */ markers delimit the block extracted by
    // the parity test's Node runner; keep them flanking exactly the
    // shared symbols.

    /* @validator-parity start */
    const latLonRE = /^[+-]?\d+(?:\.\d+)?$/;
    const altitudeShapeRE = /^(-?\d+(?:\.\d+)?)(m|ft)?$/;

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

    // altitudeToBareMetres mirrors AltitudeToBareMetres in
    // internal/feedmeta/feedmeta.go and altitude_to_bare_metres in
    // feed/scripts/lib/configure-validators.sh. Returns the
    // bare-metres canonical string for the input, or null if the
    // input fails the suffix-tolerant regex OR the post-conversion
    // metres value is outside [-1000, 10000].
    //
    //   ""        → ""           (tombstone passthrough)
    //   "<n>"     → "<n>"        (already bare)
    //   "<n>m"    → "<n>"        (strip metre suffix)
    //   "<n>ft"   → "<n>×0.3048" (convert feet)
    //
    // Fixed-point output: ten fractional digits before trimming
    // trailing zeros and a bare decimal point. Never emits
    // scientific notation. The shared fixture at
    // feed/test/fixtures/altitude-canonicalization.json (vendored
    // into image-webconfig at
    // internal/feedmeta/testdata/altitude-canonicalization.json)
    // pins the byte-exact expected outputs.
    function altitudeToBareMetres(input) {
        const s = (input == null ? "" : String(input)).trim();
        if (s === "") return "";
        const m = altitudeShapeRE.exec(s);
        if (!m) return null;
        const num = Number(m[1]);
        if (!Number.isFinite(num)) return null;
        const metres = (m[2] === "ft") ? num * 0.3048 : num;
        // Bash mirrors: range-check the %.10f-rounded value, not the
        // raw double, so 11th-decimal slop on a boundary input
        // rounds inside before the gate. Number.toFixed(10) is the
        // JS equivalent rounding; parse it back for the comparison.
        let out = metres.toFixed(10);
        // Preserve negative-zero sign so the canonical form matches
        // bash awk's "-0.0000000000" and Go's strconv.FormatFloat
        // output. Without this the JS canonical for "-0", "-0m", or
        // any rounded-to-zero negative input would diverge as "0"
        // while bash and Go emit "-0", causing the SPA's dirty
        // comparator to thrash.
        if (Object.is(metres, -0) && out[0] !== "-") {
            out = "-" + out;
        }
        const rounded = Number(out);
        if (!Number.isFinite(rounded) || rounded < -1000 || rounded > 10000) return null;
        if (out.indexOf(".") !== -1) {
            out = out.replace(/0+$/, "").replace(/\.$/, "");
        }
        return out;
    }

    function isValidAltitude(v) {
        const s = (v == null ? "" : String(v)).trim();
        if (s === "") return true;
        return altitudeToBareMetres(s) !== null;
    }

    // imperialLengthFromLanguages mirrors tempUnitFromAcceptLanguage's
    // region rule (internal/server/accept_language.go): any *-us regional
    // tag selects imperial (feet); the first other regional tag selects
    // metric; bare language tags are too weak a signal and are skipped.
    // The caller passes navigator.languages, already ordered
    // most-preferred-first, so list position stands in for the server's
    // q-weighting. Defaults to metric. Pure (no navigator) so the Go test
    // can pin it.
    function imperialLengthFromLanguages(langs) {
        const list = (langs && langs.length) ? langs : [];
        for (const tag of list) {
            const lower = String(tag || "").toLowerCase();
            if (lower === "" || lower === "*") continue;
            if (lower.endsWith("-us")) return true;
            if (lower.includes("-")) return false;
            // bare language tag — keep scanning for a regional signal
        }
        return false;
    }

    // altitudeDisplayValue renders a canonical bare-metres value for the
    // altitude field. Metric viewers — and any value that isn't a whole
    // number of feet — see "<metres>m". An imperial viewer sees "<feet>ft"
    // ONLY when that round-trips back to exactly the stored metres, so a
    // metres-entered value never shows as a fractional foot value or trips
    // the dirty comparator. Empty in → empty out (tombstone passthrough).
    function altitudeDisplayValue(metres, imperial) {
        const s = (metres == null ? "" : String(metres)).trim();
        if (s === "") return "";
        if (imperial) {
            const m = Number(s);
            if (Number.isFinite(m)) {
                const ft = Math.round(m / 0.3048);
                if (altitudeToBareMetres(ft + "ft") === altitudeToBareMetres(s)) {
                    return ft + "ft";
                }
            }
        }
        return s + "m";
    }

    // Shared numeric shape used by gain validators. Mirrors bash regex
    // `^-?[0-9]+([.][0-9]+)?$` — rejects scientific notation (`1e1`),
    // leading-dot (`.5`), trailing-dot (`1.`), explicit-plus (`+1`),
    // and hex (`0x10`), all of which would round-trip via Number() but
    // would be rejected server-side.
    const gainNumericRE = /^-?[0-9]+(?:\.[0-9]+)?$/;

    // isValidMlatUser mirrors feed/scripts/lib/configure-validators.sh:valid_mlat_user_strict
    // (regex ^[A-Za-z0-9_-]{1,64}$) and feed/scripts/apl-feed/mlat.sh's
    // _MLAT_USER_RE. Empty is valid on the JS side because the form posts
    // MLAT_USER="" when no name is entered; apl-feed apply forwards bare
    // empty and the mlat-client daemon falls back to "Anonymous-<short-id>".
    // Bash valid_mlat_user_strict REJECTS empty (callers handle empty
    // themselves) — the parity test pins this divergence per-side.
    const mlatUserRE = /^[A-Za-z0-9_-]{1,64}$/;
    function isValidMlatUser(v) {
        const s = (v == null ? "" : String(v)).trim();
        if (s === "") return true;
        return mlatUserRE.test(s);
    }

    // isValidGain mirrors feed/scripts/lib/configure-validators.sh:valid_gain.
    // Accepts auto|min|max, or a finite number in [0, 60]. Empty is NOT valid.
    function isValidGain(v) {
        const s = (v == null ? "" : String(v)).trim();
        if (s === "auto" || s === "min" || s === "max") return true;
        if (!gainNumericRE.test(s)) return false;
        const n = Number(s);
        return Number.isFinite(n) && n >= 0 && n <= 60;
    }

    // isValidDump978Serial mirrors valid_dump978_serial. Empty is valid
    // (treated as "no SDR serial selected").
    const dump978SerialRE = /^[0-9A-Za-z_-]{1,32}$/;
    function isValidDump978Serial(v) {
        const s = (v == null ? "" : String(v)).trim();
        if (s === "") return true;
        return dump978SerialRE.test(s);
    }

    // isValidDump978Gain mirrors valid_dump978_gain. dump978-fa rejects
    // auto/min/max (unlike readsb), so this validator does too. Empty is
    // NOT valid.
    function isValidDump978Gain(v) {
        const s = (v == null ? "" : String(v)).trim();
        if (!gainNumericRE.test(s)) return false;
        const n = Number(s);
        return Number.isFinite(n) && n >= 0 && n <= 60;
    }

    // Wi-Fi validators — bash twin lives at
    // /usr/local/lib/airplanes/wifi-validators.sh (apl_wifi_valid_*). Pinned
    // by the same parity test. CRUCIAL: no trim. WPA passphrases and SSIDs
    // can legitimately carry leading/trailing whitespace, so pass the value
    // through verbatim — the form must not normalize.
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

    // viewerUsesImperialLength reads the browser locale and applies the
    // region rule in imperialLengthFromLanguages. Kept outside the parity
    // block because it touches navigator, which the Node test harness that
    // extracts that block does not provide.
    function viewerUsesImperialLength() {
        const nav = (typeof navigator !== "undefined") ? navigator : null;
        const langs = (nav && nav.languages && nav.languages.length)
            ? nav.languages
            : [nav && nav.language ? nav.language : ""];
        return imperialLengthFromLanguages(langs);
    }

    // hasLatLonPrecision counts decimal places in a trimmed numeric string.
    // UI-only gate above isValidLatitude/isValidLongitude (which mirror the
    // cross-repo validator-parity contract and only enforce range). MLAT
    // triangulation degrades fast below ~10 m precision; 4 decimal places of
    // latitude give ~11 m at the equator and less at higher latitudes, which
    // is the floor we accept for both axes here.
    const LATLON_MIN_DECIMALS = 4;
    function hasLatLonPrecision(v) {
        const s = (v == null ? "" : String(v)).trim();
        const dot = s.indexOf(".");
        if (dot === -1) return false;
        const frac = s.slice(dot + 1);
        return frac.length >= LATLON_MIN_DECIMALS;
    }

    // refreshFieldError toggles a `<p class="wc-field-error">` element +
    // input[aria-invalid] based on the supplied predicate. Centralises the
    // decoration so every recheck path (input events, Cancel reset, save
    // hook, group visibility toggle, programmatic value write) keeps the
    // error UI consistent with the actual Save gate. Caller controls
    // empty-handling semantics via shouldShowError: empty-valid fields
    // (MLAT_USER, DUMP978_SDR_SERIAL, identity-import UUID/secret) hide
    // their error on empty input; empty-invalid fields (GAIN, DUMP978_GAIN)
    // surface the error even on empty.
    function refreshFieldError(inputEl, errorEl, shouldShowError) {
        const invalid = shouldShowError(inputEl.value);
        errorEl.hidden = !invalid;
        if (invalid) inputEl.setAttribute("aria-invalid", "true");
        else inputEl.removeAttribute("aria-invalid");
    }

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
        const hh = payload.hardware_health || null;
        if (hh && hh.severity === "err") {
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

    function buildHardwareTile() {
        const iconNode = el("span", { class: "wc-tile__icon" }, svgIcon(HARDWARE_ICON));
        const titleEl  = el("span", { class: "wc-tile__title" }, "Hardware");
        const metaEl   = el("span", { class: "wc-tile__meta" }, "—");
        const dotEl    = el("span", { class: "wc-tile__dot wc-tile__dot--na" });
        const chev     = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "›");
        const root = el("button", {
            type: "button",
            class: "wc-tile wc-tile--hardware wc-tile--nav",
            "data-state": "unknown",
            onclick: () => navigate(piHealthPanel, { title: "System metrics", showBack: true }),
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
            dotEl,
        );
        return { root, titleEl, metaEl, dotEl };
    }

    function updateHardwareTile(tile, payload) {
        const hh = (payload && payload.hardware_health) || null;
        if (!hh) {
            tile.dotEl.className = "wc-tile__dot wc-tile__dot--na";
            tile.metaEl.textContent = "—";
            tile.root.title = "";
            tile.root.setAttribute("data-state", "unknown");
            return;
        }
        const sev = hh.severity || "na";
        const summary = hh.summary || "—";
        tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + sev;
        tile.metaEl.textContent = summary;
        tile.root.title = summary;
        tile.root.setAttribute("data-state", sev);
    }

    // classifyWifi projects the /api/status.wifi payload into a tile
    // {dot, meta}. Returns a "not detected" classification when the
    // payload omits wifi entirely (Ethernet-only hosts) so the tile
    // stays in the layout as a navigable entry point. A connected
    // interface with unparseable signal renders as warn with just the
    // SSID (conservative — we know it's up but can't grade it).
    function classifyWifi(w) {
        if (!w) return { dot: "na", meta: "not detected" };
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
        const chev     = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "›");
        const root = el("button", {
            type: "button",
            class: "wc-tile wc-tile--wifi wc-tile--nav",
            "data-state": "unknown",
            onclick: () => navigate(wifiPanel, { title: "Wi-Fi networks", showBack: true }),
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
            dotEl,
        );
        return { root, titleEl, metaEl, dotEl };
    }

    function updateWifiTile(tile, payload) {
        const c = classifyWifi(payload && payload.wifi);
        tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + c.dot;
        tile.metaEl.textContent = c.meta;
        tile.root.title = c.meta || "";
        tile.root.setAttribute("data-state", c.dot);
    }

    function buildTileGrid() {
        const tiles = {};
        const appTiles = {};

        // Row 1: hardware + Wi-Fi + third-party aggregators (fills the
        // previously-empty third cell on the 3-col grid).
        const hardware = buildHardwareTile();
        const wifi = buildWifiTile();
        const aggregators = buildAggregatorTile();
        const row1 = el("div", { class: "wc-grid--tiles" }, hardware.root, wifi.root, aggregators.root);

        // Row 2: 1090 stack (readsb → feed → mlat).
        // Row 3: 978 UAT stack (dump978 + airplanes-978).
        const row2 = el("div", { class: "wc-grid--tiles" });
        const row3 = el("div", { class: "wc-grid--tiles" });
        const ROW3_UNITS = new Set(["dump978-fa.service", "airplanes-978.service"]);
        for (const unit of MONITORED_SERVICES) {
            const t = buildTile(unit);
            tiles[unit] = t;
            (ROW3_UNITS.has(unit) ? row3 : row2).appendChild(t.root);
        }

        // App tiles (tar1090, graphs1090) keep their own grid row below.
        const apps = el("div", { class: "wc-grid--tiles" });
        for (const app of APP_TILES) {
            const t = buildAppTile(app);
            appTiles[app.id] = t;
            apps.appendChild(t.root);
        }

        const root = el("div", { class: "wc-tiles-stack" }, row1, row2, row3, apps);
        return { root, tiles, appTiles, hardware, wifi, aggregators };
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
        if (grid.hardware) updateHardwareTile(grid.hardware, payload);
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
            // Allow restore from a backup before first-run assigns a
            // UUID: a freshly-flashed replacement device with a saved
            // backup file should be able to recover its identity here.
            parent.appendChild(el("div", { class: "wc-action-grid" },
                buildImportIdentityNavBtn(null),
                el("span", { class: "wc-action-grid__spacer", "aria-hidden": "true" }),
            ));
            return;
        }
        parent.appendChild(el("p", {}, el("strong", {}, "Feeder ID: "), id.feeder_id));
        const claimLog = el("button", {
            class: "wc-btn-ghost",
            type: "button",
            onclick: () => navigate(() => claimActivityPanel(), { title: "Claim activity", showBack: true }),
        }, "Claim activity");

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
                    navigate(() => claimActivityPanel(), { title: "Claim activity", showBack: true });
                },
            }, "Register now");
            // Row 1: Register now + Claim activity. Row 2: Import (col 1
            // only; col 2 stays empty so column alignment matches the
            // secret-present view).
            parent.appendChild(el("div", { class: "wc-action-grid" },
                registerBtn, claimLog,
                buildImportIdentityNavBtn(id.feeder_id),
                el("span", { class: "wc-action-grid__spacer", "aria-hidden": "true" }),
            ));
            parent.appendChild(registerErr);
            return;
        }

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
                if (!r.payload.feeder_id || !r.payload.claim_secret) {
                    parent.replaceChildren(el("p", { class: "error", role: "alert" },
                        "reveal returned incomplete data"));
                    return;
                }
                const safe = safeClaimHref(r.payload.claim_page);
                const linkOrText = safe
                    ? el("a", { href: safe, target: "_blank", rel: "noopener noreferrer" }, "Claim this feeder")
                    : el("span", { class: "muted" }, r.payload.claim_page || "");
                parent.replaceChildren(
                    el("p", { class: "wc-copy-row" },
                        el("strong", {}, "Feeder ID: "),
                        el("span", { class: "wc-copy-val" }, r.payload.feeder_id),
                        copyButton(r.payload.feeder_id, "feeder ID")),
                    el("p", { class: "wc-copy-row" },
                        el("strong", {}, "Claim secret: "),
                        el("code", { class: "wc-copy-val" }, r.payload.claim_secret),
                        copyButton(r.payload.claim_secret, "claim secret")),
                    el("p", {}, linkOrText),
                    el("div", { class: "wc-action-grid" }, claimLog),
                );
            },
        }, "Show claim secret");

        const exportBtn = el("button", {
            class: "wc-btn-ghost",
            type: "button",
            onclick: async () => {
                exportBtn.disabled = true;
                try {
                    const resp = await fetch("/api/identity/export", {
                        method: "POST",
                        credentials: "same-origin",
                        headers: { "Content-Type": "application/json" },
                    });
                    if (resp.status === 401) {
                        navigate(() => loginPanel("Session expired — log in again."), {});
                        return;
                    }
                    if (!resp.ok) {
                        let msg = "export failed";
                        try { const j = await resp.json(); if (j && j.error) msg = j.error; } catch (_) {}
                        alert(msg);
                        return;
                    }
                    const text = await resp.text();
                    let env;
                    try { env = JSON.parse(text); } catch (_) {
                        alert("export produced invalid JSON");
                        return;
                    }
                    const fid = (env && env.feeder_uuid) || "feeder";
                    const stamp = new Date().toISOString().slice(0, 10);
                    const filename = "airplanes-live-feeder-" + String(fid).slice(0, 8) + "-" + stamp + ".json";
                    const blob = new Blob([text], { type: "application/json" });
                    const url = URL.createObjectURL(blob);
                    const a = document.createElement("a");
                    a.href = url;
                    a.download = filename;
                    document.body.appendChild(a);
                    a.click();
                    document.body.removeChild(a);
                    setTimeout(() => URL.revokeObjectURL(url), 0);
                } finally {
                    exportBtn.disabled = false;
                }
            },
        }, "Export identity");

        // Row 1: Show claim secret + Claim activity.
        // Row 2: Export identity + Import identity.
        parent.appendChild(el("div", { class: "wc-action-grid" },
            reveal, claimLog,
            exportBtn, buildImportIdentityNavBtn(id.feeder_id),
        ));
    }

    // feederUUIDRE mirrors isCanonicalUUID in
    // internal/server/handlers.go: 8-4-4-4-12 lowercase hex. The server
    // rejects uppercase, so the UUID input lowercases on every keystroke
    // (matches apl-feed's canonicalize_uuid) and the payload is always
    // normalized before send.
    const feederUUIDRE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

    // claimSecretStripRE mirrors isCanonicalClaimSecret in
    // internal/server/handlers.go — strip hyphen, ASCII space, tab, LF,
    // CR. Deliberately NOT JS \s: \s also matches NBSP, form-feed,
    // vertical-tab, and U+2028 / U+2029 which the Go validator does NOT
    // strip, so using \s would let invalid input pass client-side and
    // fail server-side with the button green.
    const claimSecretStripRE = /[- \t\n\r]/g;

    function normalizedFeederUUID(v) {
        return String(v == null ? "" : v).trim().toLowerCase();
    }
    function isCanonicalFeederUUID(v) {
        return feederUUIDRE.test(normalizedFeederUUID(v));
    }
    function isCanonicalClaimSecret(v) {
        const s = String(v == null ? "" : v).replace(claimSecretStripRE, "");
        return s.length === 16 && /^[A-Za-z0-9]+$/.test(s);
    }

    // buildImportIdentityNavBtn returns the ghost button shown in the
    // identity card that navigates to the dedicated import sub-page.
    // currentFeederId is forwarded so the panel can show what's about to
    // be replaced; pass null in the "no identity yet" branch.
    function buildImportIdentityNavBtn(currentFeederId) {
        return el("button", {
            class: "wc-btn-ghost",
            type: "button",
            onclick: () => navigate(
                () => importIdentityPanel(currentFeederId),
                { title: "Import identity", showBack: true },
            ),
        }, "Import identity");
    }

    // Maximum size for an uploaded backup file. apl-feed's export
    // envelope is well under 1 KiB; 8 KiB is comfortable headroom for
    // any reasonable JSON formatting / future sidecar fields without
    // handing the FileReader a multi-megabyte file by accident.
    const IDENTITY_BACKUP_MAX_BYTES = 8192;

    // validateBackupEnvelope checks an uploaded JSON value against the
    // apl-feed backup envelope shape AND the per-field validators the
    // server applies. Returns { error } or normalized fields ready to
    // drop into the form inputs. Permissive on unknown keys so a future
    // export adding a sidecar field doesn't break today's UI.
    function validateBackupEnvelope(obj) {
        if (obj === null || typeof obj !== "object" || Array.isArray(obj)) {
            return { error: "File doesn't contain a JSON object." };
        }
        if (obj.schema_version !== 1) {
            return { error: "Unsupported backup schema (expected schema_version 1)." };
        }
        if (typeof obj.feeder_uuid !== "string") {
            return { error: "Backup is missing feeder_uuid." };
        }
        if (obj.claim === null || typeof obj.claim !== "object" || Array.isArray(obj.claim)) {
            return { error: "Backup is missing claim." };
        }
        if (typeof obj.claim.secret !== "string") {
            return { error: "Backup is missing claim.secret." };
        }
        const feederUUID = normalizedFeederUUID(obj.feeder_uuid);
        if (!isCanonicalFeederUUID(feederUUID)) {
            return { error: "Backup feeder_uuid is not a valid feeder ID." };
        }
        if (!isCanonicalClaimSecret(obj.claim.secret)) {
            return { error: "Backup claim.secret is not a valid claim secret." };
        }
        let claimVersion = null;
        if (obj.claim.version !== undefined && obj.claim.version !== null) {
            if (!Number.isSafeInteger(obj.claim.version)) {
                return { error: "Backup claim.version is not an integer." };
            }
            claimVersion = obj.claim.version;
        }
        return { feederUUID: feederUUID, claimSecret: obj.claim.secret, claimVersion: claimVersion };
    }

    // importIdentityPanel: full-page identity restore. Accepts an
    // uploaded backup JSON file (apl-feed backup export shape) OR direct
    // UUID + claim-secret input; runs the same validators the Go side
    // enforces in /api/identity/import before allowing submit, and
    // POSTs the canonical envelope.
    //
    // currentFeederId is the feeder_id currently on disk (or null when
    // no identity exists yet) — used in the page banner and the confirm
    // dialog so the user sees what they're about to replace.
    function importIdentityPanel(currentFeederId) {
        const err = errorEl();
        const fileInput = el("input", {
            id: "import-identity-file",
            type: "file", accept: ".json,application/json", autocomplete: "off",
        });
        const fileStatus = el("p", { class: "muted" });
        const uuidIn = el("input", {
            id: "import-identity-uuid",
            type: "text",
            autocomplete: "off",
            autocapitalize: "none",
            autocorrect: "off",
            spellcheck: "false",
            // Soft guard, not a hard limit: the canonical UUID is 36
            // chars but pasted clipboard text can have leading or
            // trailing whitespace/newlines. The input event handler
            // trims those before validating; the validator regex is
            // the authoritative length gate.
            maxlength: "64",
            placeholder: "11111111-2222-3333-4444-555555555555",
        });
        const secretIn = el("input", {
            id: "import-identity-secret",
            type: "text",
            autocomplete: "off",
            autocapitalize: "characters",
            autocorrect: "off",
            spellcheck: "false",
            maxlength: "32",
            placeholder: "ABCD-EFGH-IJKL-MNOP",
        });
        const submit = el("button", { type: "submit", class: "wc-btn-primary", disabled: true }, "Import");
        const cancel = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigateDashboard(),
        }, "Cancel");

        // storedVersion: the claim.version pulled from the most recent
        // uploaded JSON, if any. Cleared on any manual edit of UUID /
        // secret so we never submit fileA's version with fileB's (or
        // hand-typed) credentials.
        let storedVersion = null;
        // busy gates submit during the POST so completion routes through
        // updateSubmitState() rather than blindly re-enabling on now-
        // invalid input.
        let busy = false;

        // Per-field inline errors — mirror the config-tile pattern so the
        // user knows WHY submit is disabled, not just that it is.
        const uuidError = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" },
            "Format: 8-4-4-4-12 hex digits (UUID).");
        const secretError = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" },
            "16 letters/digits; dashes or spaces are ignored.");
        // Predicates trim because isCanonical* both normalise via stripRE
        // / trim+lowercase before testing; the surfaced error matches the
        // validator. Empty stays clean (the disabled button already
        // communicates "you haven't filled this in").
        const uuidShouldShowError = (v) => v.trim() !== "" && !isCanonicalFeederUUID(v);
        const secretShouldShowError = (v) => v.trim() !== "" && !isCanonicalClaimSecret(v);
        function refreshIdentityErrors() {
            refreshFieldError(uuidIn, uuidError, uuidShouldShowError);
            refreshFieldError(secretIn, secretError, secretShouldShowError);
        }

        function updateSubmitState() {
            submit.disabled = busy
                || !isCanonicalFeederUUID(uuidIn.value)
                || !isCanonicalClaimSecret(secretIn.value);
        }

        uuidIn.addEventListener("input", () => {
            // Silent normalize on input: trim + lowercase, matching
            // apl-feed's canonicalize_uuid. A clipboard paste often
            // carries leading/trailing whitespace that would otherwise
            // sit in the field invisibly and keep submit disabled with
            // no obvious reason. Caret tracking is best-effort —
            // clamping to the new length is correct for the typical
            // paste-at-end case.
            const before = uuidIn.value;
            const after = before.trim().toLowerCase();
            if (after !== before) {
                const start = uuidIn.selectionStart;
                const end = uuidIn.selectionEnd;
                uuidIn.value = after;
                const n = after.length;
                try { uuidIn.setSelectionRange(Math.min(start, n), Math.min(end, n)); }
                catch (_) { /* not focusable */ }
            }
            storedVersion = null;
            refreshFieldError(uuidIn, uuidError, uuidShouldShowError);
            updateSubmitState();
        });
        secretIn.addEventListener("input", () => {
            storedVersion = null;
            refreshFieldError(secretIn, secretError, secretShouldShowError);
            updateSubmitState();
        });

        fileInput.addEventListener("change", () => {
            // Capture the File reference, then clear the input so
            // picking the same filename twice in a row still fires change.
            const file = fileInput.files && fileInput.files[0];
            fileInput.value = "";
            if (!file) return;
            err.textContent = "";
            if (file.size === 0) {
                fileStatus.textContent = "";
                err.textContent = "Selected file is empty.";
                return;
            }
            if (file.size > IDENTITY_BACKUP_MAX_BYTES) {
                fileStatus.textContent = "";
                err.textContent = "Identity backup files are under 1 KB; selected file is too large.";
                return;
            }
            const reader = new FileReader();
            reader.onerror = () => {
                fileStatus.textContent = "";
                err.textContent = "Could not read the selected file.";
            };
            reader.onabort = () => {
                fileStatus.textContent = "";
                err.textContent = "Reading the selected file was cancelled.";
            };
            reader.onload = () => {
                const text = typeof reader.result === "string" ? reader.result : "";
                let parsed;
                try { parsed = JSON.parse(text); }
                catch (_) {
                    fileStatus.textContent = "";
                    err.textContent = "Not a valid JSON file.";
                    return;
                }
                const v = validateBackupEnvelope(parsed);
                if (v.error) {
                    fileStatus.textContent = "";
                    err.textContent = v.error;
                    return;
                }
                // Atomic prefill: only after every check passes. Manual
                // .value writes don't fire input events, so refresh the
                // inline field-errors and submit state explicitly (a
                // valid backup must clear any stale errors from a prior
                // manual edit).
                uuidIn.value = v.feederUUID;
                secretIn.value = v.claimSecret;
                storedVersion = v.claimVersion;
                fileStatus.textContent = "Loaded from " + file.name;
                refreshIdentityErrors();
                updateSubmitState();
            };
            reader.readAsText(file);
        });

        const form = el("form", {
            class: "wc-card",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                if (!isCanonicalFeederUUID(uuidIn.value)
                    || !isCanonicalClaimSecret(secretIn.value)) {
                    err.textContent = "Feeder ID and claim secret must be valid.";
                    return;
                }
                const targetUUID = normalizedFeederUUID(uuidIn.value);
                const currentLine = currentFeederId
                    ? "Current feeder ID: " + currentFeederId + "\n"
                    : "This feeder has no identity yet.\n";
                const prompt = currentLine
                    + "New feeder ID: " + targetUUID + "\n\n"
                    + "Importing replaces this feeder's identity with the supplied "
                    + "UUID and claim secret. Continue?";
                if (!confirm(prompt)) return;
                busy = true;
                updateSubmitState();
                cancel.disabled = true;
                submit.textContent = "Importing…";
                const body = {
                    schema_version: 1,
                    feeder_uuid: targetUUID,
                    claim: { secret: secretIn.value, version: storedVersion },
                };
                const r = await postJSON("/api/identity/import", body);
                busy = false;
                cancel.disabled = false;
                submit.textContent = "Import";
                updateSubmitState();
                if (handleAuthFailure(r)) return;
                if (!r.ok) {
                    err.textContent = (r.payload && r.payload.error) || "import failed";
                    return;
                }
                navigateDashboard({ flash: { text: "Identity imported.", level: "ok" } });
            },
        },
            el("h2", {}, "Import identity"),
            el("p", {},
                "Restore this feeder's identity from a JSON backup you exported earlier, "
                + "or enter the feeder ID and claim secret by hand."),
            currentFeederId
                ? el("p", {}, el("strong", {}, "Current feeder ID: "), currentFeederId)
                : el("p", { class: "muted" }, "This feeder has no identity yet."),
            el("div", { class: "field" },
                el("label", { for: "import-identity-file" }, "Identity backup (optional)"),
                fileInput,
                fileStatus,
            ),
            el("div", { class: "field" }, el("label", { for: "import-identity-uuid" }, "Feeder ID"), uuidIn, uuidError),
            el("div", { class: "field" }, el("label", { for: "import-identity-secret" }, "Claim secret"), secretIn, secretError),
            el("div", { class: "actions" }, cancel, submit),
            err,
        );
        render(form);
        uuidIn.focus();
    }

    function identityChanged(prev, next) {
        if (!prev) return true;
        return prev.feeder_id !== next.feeder_id
            || prev.claim_secret_present !== next.claim_secret_present;
    }

    // ===== Configuration card =====

    // canonicaliseAltitude mirrors internal/feedmeta/feedmeta.go's
    // canonicalizeForCompare for ALTITUDE: delegate to
    // altitudeToBareMetres so any input that parses converges to its
    // bare-metres canonical string (the form feed's apply layer now
    // stores on disk). Used in the dirty comparator so a saved
    // "121.92" and a user-typed "400ft" don't show as dirty.
    //
    // On parse failure (out-of-range or regex mismatch) return the
    // original input so the dirty-state comparator can still tell
    // them apart — silently collapsing unparseable input to the
    // empty/equal branch would mask a real change the validator
    // (and downstream apply layer) needs to surface.
    function canonicaliseAltitude(v) {
        const result = altitudeToBareMetres(v);
        if (result === null) return v == null ? "" : String(v);
        return result;
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
            // Defensive validity gate: Enter-key / programmatic submits
            // can bypass a disabled button. Re-check isValid() and bail
            // if the input is bad; recheck() resyncs the button state
            // so the user sees the disabled Save instead of a phantom
            // "Saving…" that never lands.
            if (isValid && !isValid()) {
                recheck();
                return;
            }
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
                // Forward apl-feed's structured error envelope. When the
                // helper rejects per-field (typical for apply: bad
                // LATITUDE, MLAT_USER, etc.) it returns a `errors` map of
                // `KEY → message`; flatten that into the summary so the
                // user can see exactly which field tripped server-side
                // rather than a generic "save failed".
                const p = r.payload || {};
                const summary = p.error || p.message || "save failed";
                const errs = (p.errors && typeof p.errors === "object" && !Array.isArray(p.errors))
                    ? p.errors : null;
                const fields = errs ? Object.keys(errs).map(k => k + ": " + errs[k]) : [];
                err.textContent = fields.length ? summary + " — " + fields.join("; ") : summary;
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
            MLAT_ENABLED: "false",
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

        // buildEditableField wraps a per-group set of inputs in a
        // read-only summary that an Edit button swaps to the editor.
        // The caller still owns mountGroup wiring (dirty/save), payload
        // shape, and any gating; this helper owns the visibility toggle,
        // summary refresh, focus management, the Cancel button (with
        // input-reset + footer-error-clear), and exposes `setEditing` /
        // `refreshSummary` so the per-group onSavedHook can collapse
        // back to read-only after a save lands.
        //
        // The Cancel button is appended to footerEl BEFORE mountGroup()
        // runs, so the footer DOM order matches Location's:
        // [Cancel, Save, error, pending-restart].
        //
        // opts:
        //   summarise()        -> string displayed when collapsed
        //   inputsWrapper      element that becomes hidden in read mode
        //   resetInputs()      restore inputs to saved (or default) values
        //   focusInput         element to focus when entering edit mode
        //   footerEl           the per-group footer (for Cancel + error reset)
        //   afterCancel()      optional extra cleanup after reset (e.g. recheck)
        const buildEditableField = (opts) => {
            const valueEl = el("span", { class: "wc-editable__value" });
            const editBtn = el("button", {
                type: "button", class: "wc-btn-ghost wc-editable__edit",
            }, "Edit");
            const summaryEl = el("div", { class: "wc-editable" }, valueEl, editBtn);

            const refreshSummary = () => {
                const v = opts.summarise();
                if (v == null || v === "") {
                    valueEl.textContent = "—";
                    valueEl.classList.add("wc-editable__value--placeholder");
                } else {
                    valueEl.textContent = v;
                    valueEl.classList.remove("wc-editable__value--placeholder");
                }
            };

            const setEditing = (editing) => {
                summaryEl.hidden = editing;
                opts.inputsWrapper.hidden = !editing;
                cancelBtn.hidden = !editing;
                if (editing && opts.focusInput) {
                    setTimeout(() => opts.focusInput.focus(), 0);
                }
            };

            const cancelBtn = el("button", {
                type: "button", class: "wc-btn-ghost",
                onclick: () => {
                    opts.resetInputs();
                    const errEl = opts.footerEl.querySelector(".error");
                    if (errEl) errEl.textContent = "";
                    const pendEl = opts.footerEl.querySelector(".config-fieldset__pending");
                    if (pendEl) { pendEl.hidden = true; pendEl.textContent = ""; }
                    setEditing(false);
                    if (opts.afterCancel) opts.afterCancel();
                },
            }, "Cancel");
            opts.footerEl.appendChild(cancelBtn);

            editBtn.addEventListener("click", () => setEditing(true));

            refreshSummary();
            setEditing(false);

            return { summaryEl, setEditing, refreshSummary, cancelBtn };
        };

        // ===== MLAT =====
        // Position (latitude/longitude/altitude) is only used for MLAT, so
        // it lives inside this group: the inputs stay hidden until the
        // operator enables MLAT, and a single Save writes the whole set
        // (coordinates + MLAT settings) in one apply — the backend derives
        // GEO_CONFIGURED from the lat/lon pair. This mirrors the apl-feed
        // mlat setup flow.
        const mlatG = buildGroup("MLAT");

        const mlatId = fieldId("MLAT_ENABLED");
        const mlatInput = el("input", {
            id: mlatId, type: "checkbox", name: "MLAT_ENABLED",
        });
        // Absent MLAT_ENABLED defaults to off: MLAT's posture is
        // "disabled until the operator opts in and sets a position", so an
        // uninitialised feeder renders unchecked with the location inputs
        // hidden. Production feed.env always carries the key explicitly.
        if ((configState.savedValues.MLAT_ENABLED || "false") === "true") mlatInput.checked = true;

        // --- Position inputs (revealed when MLAT is on) ---
        // A never-configured feeder carries placeholder coordinates (0/0) with
        // GEO_CONFIGURED=false. Treat the whole position as unset and render the
        // fields blank (so their placeholders show) instead of leaking the
        // sentinels in as if the operator had entered them. previewLatLonSet
        // honors the explicit GEO_CONFIGURED flag. initAlt is defined below,
        // after altDisplay.
        const geoConfigured = () => previewLatLonSet(configState.savedValues);
        const initLat = () => geoConfigured() ? (configState.savedValues.LATITUDE || "") : "";
        const initLon = () => geoConfigured() ? (configState.savedValues.LONGITUDE || "") : "";
        const latId = fieldId("LATITUDE");
        const latInput = el("input", {
            id: latId, name: "LATITUDE", type: "text",
            value: initLat(),
            inputmode: "decimal", placeholder: "51.5074",
        });
        const lonId = fieldId("LONGITUDE");
        const lonInput = el("input", {
            id: lonId, name: "LONGITUDE", type: "text",
            value: initLon(),
            inputmode: "decimal", placeholder: "-0.1278",
        });
        const altId = fieldId("ALTITUDE");
        // The unit is part of the field value (e.g. "20m" / "65ft"), not a
        // separate chip. altitudeDisplayValue renders the canonical bare-
        // metres value with its unit, showing feet only for imperial-locale
        // viewers and only when the conversion is exact. Feet remains an
        // input convenience; it is never what gets stored.
        const altImperial = viewerUsesImperialLength();
        const altDisplay = (sv) => altitudeDisplayValue(sv, altImperial);
        // Same unconfigured-feeder gate as lat/lon: don't pre-fill altitude
        // from the placeholder value when the position isn't configured.
        const initAlt = () => geoConfigured() ? altDisplay(configState.savedValues.ALTITUDE) : "";
        const altInput = el("input", {
            id: altId, name: "ALTITUDE", type: "text",
            value: initAlt(),
            placeholder: altImperial ? "65ft" : "20m",
        });
        // Live feet→metres echo. Altitude is stored in metres, so when the
        // operator types feet we show the converted metres value they'll
        // actually save. Hidden for bare or metres input.
        const altConvert = el("p", { class: "field-help wc-alt-convert" });
        altConvert.hidden = true;
        const refreshAltConvert = () => {
            const v = (altInput.value || "").trim();
            const metres = /ft$/i.test(v) ? altitudeToBareMetres(v) : null;
            if (metres) {
                altConvert.textContent = "= " + metres + " m";
                altConvert.hidden = false;
            } else {
                altConvert.hidden = true;
            }
        };
        refreshAltConvert();
        altInput.addEventListener("input", refreshAltConvert);
        // Help leads with the viewer's locale unit, then offers the other.
        const altHelp = el("p", { class: "field-help" },
            "Antenna height above sea level. Enter ",
            ...(altImperial
                ? ["feet (e.g. ", el("code", {}, "65ft"), ") or metres (e.g. ", el("code", {}, "20m"), ")."]
                : ["metres (e.g. ", el("code", {}, "20m"), ") or feet (e.g. ", el("code", {}, "65ft"), ")."]),
        );

        // --- MLAT name + privacy ---
        const mlatUserId = fieldId("MLAT_USER");
        // Placeholder hints the daemon's per-device fallback name so the
        // operator sees what they'll appear as if they leave this blank.
        // Mirrors feed/scripts/airplanes-mlat.sh: "Anonymous-<first 8 of the
        // feeder id>", or plain "Anonymous" when the feeder id isn't a
        // canonical UUID (file missing / malformed on the device).
        const feederId = (dashboardCtx && dashboardCtx.lastIdentity && dashboardCtx.lastIdentity.feeder_id) || "";
        const mlatAnonFallback = isCanonicalFeederUUID(feederId)
            ? "Anonymous-" + normalizedFeederUUID(feederId).slice(0, 8)
            : "Anonymous";
        const mlatUserInput = el("input", {
            id: mlatUserId, name: "MLAT_USER", type: "text",
            value: configState.savedValues.MLAT_USER || "",
            placeholder: mlatAnonFallback,
        });
        const mlatUserError = el("p", {
            class: "field-help wc-field-error", hidden: true, role: "alert",
        }, "Letters, digits, underscore, hyphen — up to 64 characters.");
        const mlatUserShouldShowError = (v) =>
            (v || "").trim() !== "" && !isValidMlatUser(v);
        mlatUserInput.addEventListener("input", () =>
            refreshFieldError(mlatUserInput, mlatUserError, mlatUserShouldShowError));

        const mlatPrivateId = fieldId("MLAT_PRIVATE");
        const mlatPrivateInput = el("input", {
            id: mlatPrivateId, type: "checkbox", name: "MLAT_PRIVATE",
        });
        if ((configState.savedValues.MLAT_PRIVATE || "false") === "true") mlatPrivateInput.checked = true;

        // Verify-on-map: a compact neutral link under the longitude input
        // that opens the entered position on OpenStreetMap (new tab) so the
        // operator can confirm the pin lands on their actual antenna site.
        // Shown only when MLAT is on and both coordinates are valid and
        // precise (the position half of the save gate); see updateVerifyBtn.
        const verifyBtn = el("a", {
            class: "wc-help-link",
            target: "_blank", rel: "noopener noreferrer",
        }, "Verify on map");
        verifyBtn.hidden = true;

        // The reveal block holds everything that only matters once MLAT is
        // on. Hidden (and irrelevant to dirty/save) until Enable MLAT is
        // checked.
        const mlatReveal = el("div", { class: "mlat-reveal" },
            el("div", { class: "field" },
                el("label", { for: latId }, "Latitude"),
                el("p", { class: "field-help" },
                    "Decimal degrees, e.g. ",
                    el("code", {}, "51.5074"),
                    ". At least 4 decimals (MLAT needs precise coordinates).",
                ),
                latInput,
            ),
            el("div", { class: "field" },
                el("label", { for: lonId }, "Longitude"),
                el("p", { class: "field-help" },
                    "Decimal degrees, East-positive, e.g. ",
                    el("code", {}, "-0.1278"),
                    ". At least 4 decimals.",
                ),
                lonInput,
                verifyBtn,
            ),
            el("div", { class: "field" },
                el("label", { for: altId }, "Altitude"),
                altHelp,
                altInput,
                altConvert,
                el("a", {
                    class: "wc-help-link",
                    href: "https://www.freemaptools.com/elevation-finder.htm",
                    target: "_blank", rel: "noopener noreferrer",
                }, "Find elevation"),
            ),
            el("div", { class: "field" },
                el("label", { for: mlatUserId }, "MLAT name"),
                el("p", { class: "field-help" },
                    "Shown on the MLAT map and in statistics.",
                ),
                mlatUserInput,
                mlatUserError,
            ),
            el("div", { class: "field mlat-privacy-field" },
                el("label", { for: mlatPrivateId }, mlatPrivateInput, " Hide on the MLAT feeder map"),
                el("p", { class: "field-help" },
                    "The MLAT map shows approximated feeder positions and their synchronization peers. Exact coordinates are never shown.",
                ),
            ),
        );

        mlatG.body.appendChild(el("div", { class: "field" },
            el("label", { for: mlatId }, mlatInput, " Enable MLAT"),
        ));
        // Description stays visible whether MLAT is on or off so the
        // operator can read what MLAT does before deciding to enable it.
        mlatG.body.appendChild(el("p", { class: "help" },
            "MLAT triangulates aircraft positions across feeders, providing better accuracy than aircraft-provided GPS data.",
        ));
        mlatG.body.appendChild(mlatReveal);

        // Visibility is driven purely by the checkbox; collapsing also
        // resets the hidden inputs to their saved values and clears any
        // stale error so a half-typed value left behind never lurks as a
        // hidden dirty value when MLAT is re-enabled.
        const updateMlatReveal = () => {
            mlatReveal.hidden = !mlatInput.checked;
            if (!mlatInput.checked) {
                latInput.value = initLat();
                lonInput.value = initLon();
                altInput.value = initAlt();
                mlatUserInput.value = configState.savedValues.MLAT_USER || "";
                refreshAltConvert();
                refreshFieldError(mlatUserInput, mlatUserError, mlatUserShouldShowError);
            }
        };
        mlatInput.addEventListener("change", updateMlatReveal);
        updateMlatReveal();

        const updateVerifyBtn = () => {
            const lat = latInput.value.trim();
            const lon = lonInput.value.trim();
            const ok = mlatInput.checked
                && isValidLatitude(lat) && isValidLongitude(lon)
                && hasLatLonPrecision(lat) && hasLatLonPrecision(lon);
            verifyBtn.hidden = !ok;
            if (ok) {
                const la = encodeURIComponent(lat);
                const lo = encodeURIComponent(lon);
                // ?mlat/mlon drops the marker pin; #map=zoom/lat/lon frames
                // it at building zoom so the operator can eyeball the spot.
                verifyBtn.href = "https://www.openstreetmap.org/?mlat=" + la
                    + "&mlon=" + lo + "#map=18/" + la + "/" + lo;
            } else {
                verifyBtn.removeAttribute("href");
            }
        };
        mlatG.form.addEventListener("input", updateVerifyBtn);
        mlatG.form.addEventListener("change", updateVerifyBtn);
        updateVerifyBtn();

        const mlatGroup = mountGroup({
            name: "mlat",
            formEl: mlatG.form,
            footerEl: mlatG.footer,
            keys: ["MLAT_ENABLED", "LATITUDE", "LONGITUDE", "ALTITUDE", "MLAT_USER", "MLAT_PRIVATE"],
            // When MLAT is off the position/name/privacy inputs are hidden
            // and irrelevant — emit only MLAT_ENABLED so abandoned edits in
            // the collapsed reveal never count as dirty.
            readInputs: () => {
                const out = { MLAT_ENABLED: mlatInput.checked ? "true" : "false" };
                if (mlatInput.checked) {
                    out.LATITUDE = latInput.value;
                    out.LONGITUDE = lonInput.value;
                    out.ALTITUDE = altInput.value;
                    out.MLAT_USER = mlatUserInput.value;
                    out.MLAT_PRIVATE = mlatPrivateInput.checked ? "true" : "false";
                }
                return out;
            },
            // Enabling MLAT requires a valid, precise position and a valid
            // name; until then Save stays visible-but-disabled. Disabling
            // MLAT is always allowed (the user must be able to turn it off
            // even with bad on-disk geo). Altitude must be non-empty:
            // isValidAltitude("") is true (tombstone acceptance), but the
            // daemon refuses MLAT with ALTITUDE empty (altitude_empty), so
            // the gate demands an actual value before enabling. The
            // previewLatLonSet check (passed the live inputs, with no
            // GEO_CONFIGURED key so it takes the derive path) rejects a
            // both-axes-zero pair the way the daemon does — without it the UI
            // would enable Save for 0,0 and let the server reject it.
            isValid: () => {
                if (!mlatInput.checked) return true;
                return isValidLatitude(latInput.value)
                    && isValidLongitude(lonInput.value)
                    && hasLatLonPrecision(latInput.value)
                    && hasLatLonPrecision(lonInput.value)
                    && previewLatLonSet({ LATITUDE: latInput.value, LONGITUDE: lonInput.value })
                    && altInput.value.trim() !== ""
                    && isValidAltitude(altInput.value)
                    && isValidMlatUser(mlatUserInput.value);
            },
            payload: () => {
                const out = {};
                const enabled = mlatInput.checked ? "true" : "false";
                if (enabled !== (configState.savedValues.MLAT_ENABLED || "false")) {
                    out.MLAT_ENABLED = enabled;
                }
                // Disabling MLAT touches nothing but the toggle.
                if (!mlatInput.checked) return out;
                // lat+lon travel as a pair so the backend re-derives
                // GEO_CONFIGURED from both axes; altitude/name/privacy go
                // only when they actually changed.
                const latDirty = !sameValue("LATITUDE", latInput.value, configState.savedValues.LATITUDE);
                const lonDirty = !sameValue("LONGITUDE", lonInput.value, configState.savedValues.LONGITUDE);
                if (latDirty || lonDirty) {
                    out.LATITUDE = latInput.value.trim();
                    out.LONGITUDE = lonInput.value.trim();
                }
                if (!sameValue("ALTITUDE", altInput.value, configState.savedValues.ALTITUDE)) {
                    out.ALTITUDE = altInput.value.trim();
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
            onSavedHook: () => {
                // Rebase inputs to the canonical saved values (apl-feed
                // trims, and canonicalises altitude ft→m) so a "400ft" /
                // "bob " save doesn't re-flag dirty against "121.92" / "bob".
                latInput.value = initLat();
                lonInput.value = initLon();
                altInput.value = initAlt();
                mlatUserInput.value = configState.savedValues.MLAT_USER || "";
                refreshAltConvert();
                refreshFieldError(mlatUserInput, mlatUserError, mlatUserShouldShowError);
                updateMlatReveal();
                updateVerifyBtn();
                mlatGroup.recheck();
            },
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
        const gainError = el("p", {
            class: "field-help wc-field-error", hidden: true, role: "alert",
        }, "auto, min, max, or a number between 0 and 60.");
        const gainShouldShowError = (v) => !isValidGain(v);
        gainInput.addEventListener("input", () =>
            refreshFieldError(gainInput, gainError, gainShouldShowError));

        const gainEditor = el("div", {}, gainInput, gainError);
        const gainEdit = buildEditableField({
            summarise: () => configState.savedValues.GAIN || "auto",
            inputsWrapper: gainEditor,
            resetInputs: () => {
                gainInput.value = configState.savedValues.GAIN || "";
                refreshFieldError(gainInput, gainError, gainShouldShowError);
            },
            focusInput: gainInput,
            footerEl: gainG.footer,
            afterCancel: () => { gainGroup.recheck(); },
        });
        gainG.body.appendChild(el("div", { class: "field" },
            el("label", { for: gainId }, "Gain"),
            gainEdit.summaryEl,
            gainEditor,
        ));
        const gainGroup = mountGroup({
            name: "gain",
            formEl: gainG.form,
            footerEl: gainG.footer,
            keys: ["GAIN"],
            readInputs: () => ({ GAIN: gainInput.value }),
            isValid: () => isValidGain(gainInput.value),
            payload: () => ({ GAIN: gainInput.value.trim() }),
            onSavedHook: () => {
                gainInput.value = configState.savedValues.GAIN || "";
                refreshFieldError(gainInput, gainError, gainShouldShowError);
                gainEdit.refreshSummary();
                gainEdit.setEditing(false);
                gainGroup.recheck();
            },
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

        const sdrSerialError = el("p", {
            class: "field-help wc-field-error", hidden: true, role: "alert",
        }, "Letters, digits, underscore, hyphen — up to 32 characters.");
        const sdrSerialShouldShowError = (v) =>
            (v || "").trim() !== "" && !isValidDump978Serial(v);
        sdrSerialInput.addEventListener("input", () =>
            refreshFieldError(sdrSerialInput, sdrSerialError, sdrSerialShouldShowError));

        const dump978GainError = el("p", {
            class: "field-help wc-field-error", hidden: true, role: "alert",
        }, "A number between 0 and 60.");
        const dump978GainShouldShowError = (v) => !isValidDump978Gain(v);
        dump978GainInput.addEventListener("input", () =>
            refreshFieldError(dump978GainInput, dump978GainError, dump978GainShouldShowError));

        const resetUatInputs = () => {
            sdrSerialInput.value = configState.savedValues.DUMP978_SDR_SERIAL || "978";
            dump978GainInput.value = configState.savedValues.DUMP978_GAIN || "42.1";
            refreshFieldError(sdrSerialInput, sdrSerialError, sdrSerialShouldShowError);
            refreshFieldError(dump978GainInput, dump978GainError, dump978GainShouldShowError);
        };

        const uatEditor = el("div", {},
            el("div", { class: "field" },
                el("label", { for: sdrSerialId }, "978 SDR serial"),
                sdrSerialInput,
                sdrSerialError,
            ),
            el("div", { class: "field" },
                el("label", { for: dump978GainId }, "978 gain"),
                dump978GainInput,
                dump978GainError,
            ),
        );
        const uatEdit = buildEditableField({
            summarise: () => {
                const ser = configState.savedValues.DUMP978_SDR_SERIAL || "978";
                const g = configState.savedValues.DUMP978_GAIN || "42.1";
                return "serial " + ser + " · gain " + g;
            },
            inputsWrapper: uatEditor,
            resetInputs: resetUatInputs,
            focusInput: sdrSerialInput,
            footerEl: uatG.footer,
            afterCancel: () => { uatGroup.recheck(); },
        });

        const uatSub = el("div", { class: "dump978-sub" },
            uatEdit.summaryEl,
            uatEditor,
        );
        uatSub.hidden = !uatInput.checked;
        uatInput.addEventListener("change", () => {
            uatSub.hidden = !uatInput.checked;
            if (!uatInput.checked) {
                uatEdit.setEditing(false);
                // Reset stale inputs + clear any error display so a
                // half-typed invalid value left behind doesn't lurk as
                // a hidden dirty value when UAT is re-enabled.
                resetUatInputs();
            }
        });

        uatG.body.appendChild(el("div", { class: "field" },
            el("label", { for: uatId }, uatInput, " Enable 978 UAT"),
        ));
        uatG.body.appendChild(uatSub);

        const uatGroup = mountGroup({
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
            isValid: () => {
                if (!uatInput.checked) return true;
                return isValidDump978Serial(sdrSerialInput.value)
                    && isValidDump978Gain(dump978GainInput.value);
            },
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
            onSavedHook: () => {
                sdrSerialInput.value = configState.savedValues.DUMP978_SDR_SERIAL || "978";
                dump978GainInput.value = configState.savedValues.DUMP978_GAIN || "42.1";
                refreshFieldError(sdrSerialInput, sdrSerialError, sdrSerialShouldShowError);
                refreshFieldError(dump978GainInput, dump978GainError, dump978GainShouldShowError);
                uatEdit.refreshSummary();
                uatEdit.setEditing(false);
                uatGroup.recheck();
            },
        });

        // Assemble
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
                (resp && resp.payload && resp.payload.error) || "could not load management settings"));
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
        const body = el("div", { class: "management-body" });
        const form = el("form", { class: "config-form" }, body, footer);

        body.appendChild(el("div", { class: "field" },
            el("label", { for: reportId }, reportInput, " Diagnostics & stats"),
        ));
        body.appendChild(el("p", { class: "help" },
            "Sends CPU, memory, disk and temperature readings so you can see this feeder's status on your dashboard ",
            "at airplanes.live and get notified when something goes wrong. Off means no dashboard or alerts for this feeder.",
        ));
        body.appendChild(el("div", { class: "field" },
            el("label", { for: remoteId }, remoteInput, " Remote configuration"),
        ));
        body.appendChild(el("p", { class: "help" },
            "Lets you change position, altitude and MLAT name from your account at airplanes.live, ",
            "without opening this page. You can still edit everything here either way.",
        ));

        parent.appendChild(form);

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

    // ORCHESTRATOR_POLL_INTERVAL_MS is the cadence the SPA polls
    // /api/orchestrator/state at after kicking off an orchestrator run.
    // 2s matches the orchestrator's per-step granularity (apt + feed +
    // webconfig + runtime — each step is typically tens of seconds to
    // minutes, so 2s is plenty of resolution without flooding the
    // device).
    const ORCHESTRATOR_POLL_INTERVAL_MS = 2000;

    // ORCHESTRATOR_STALE_GRACE_POLLS bounds how many terminal-step
    // polls the SPA tolerates before accepting the terminal state as
    // authoritative even though no non-terminal step has been seen.
    // 5 polls * 2 s = 10 s. The orchestrator writes its first state
    // file within a few hundred milliseconds of starting (it's a
    // single tmp+rename right after parsing argv); a terminal step
    // that survives this grace window most likely means the orchestrator
    // started, failed during early init, and wrote `failed` before any
    // non-terminal step — or never started at all. Either is a real
    // outcome the user must see, not a perpetual "Starting…".
    const ORCHESTRATOR_STALE_GRACE_POLLS = 5;

    // Steps that mean the orchestrator is no longer running. The poller
    // stops on any of these. "idle" appears before the first run on a
    // post-boot device (the state file lives on tmpfs); "done" and
    // "failed" are the terminal markers the orchestrator writes;
    // "unavailable" appears if the capability gate flips off mid-run
    // (image-side teardown during an active orchestrator — pathological,
    // but treat it as a terminal stop so the poller doesn't spin).
    const ORCHESTRATOR_TERMINAL_STEPS = new Set(["done", "failed", "idle", "unavailable"]);

    // orchestratorProgress renders the polling progress view after the
    // user clicks "Update System". It polls /api/orchestrator/state at
    // ORCHESTRATOR_POLL_INTERVAL_MS and stops once a terminal step is
    // reported, ignoring any terminal step that pre-dates the current
    // click (left over from a prior run — the state file lives on
    // /run/ which survives the orchestrator's process exit). The card
    // shows step + status + (when present) the error string the
    // orchestrator wrote, plus an apt_irreversible notice the
    // orchestrator surfaces once the apt phase has run.
    function orchestratorProgress() {
        const stepEl = el("p", { class: "muted" }, "Starting…");
        const statusEl = el("p", { class: "muted" }, "");
        const errorEl = el("div", { class: "wc-flash wc-flash--warn", role: "alert" }, "");
        errorEl.hidden = true;
        const aptNoteEl = el("p", { class: "muted" }, "");
        aptNoteEl.hidden = true;
        render(
            el("section", { class: "wc-card" },
                el("h2", {}, "Update system"),
                stepEl,
                statusEl,
                errorEl,
                aptNoteEl,
            ),
        );

        // sawNonTerminal flips true the first time the poller observes
        // a non-terminal step. Until then, a terminal step is treated
        // as leftover state from a prior run — the new orchestrator
        // hasn't reached its first state-file write yet — so we keep
        // polling rather than declare "done" on a stale marker.
        // staleTerminalPolls bounds how long we'll keep "starting"
        // before accepting a terminal step (see
        // ORCHESTRATOR_STALE_GRACE_POLLS rationale above).
        let sawNonTerminal = false;
        let staleTerminalPolls = 0;
        let cancelled = false;
        let pollTimer = null;
        // Local AbortController so a poller-issued fetch can be cancelled
        // without depending on getJSON's writeback to the global
        // activeAbort (which only carries the most recent in-flight
        // request — not this view's lifecycle). The wrapper around
        // activeAbort below ties the global teardown call to our local
        // cancel flag so a navigate() away kills both.
        const localCtrl = new AbortController();
        const prevAbort = activeAbort;
        activeAbort = {
            abort: () => {
                cancelled = true;
                if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
                try { localCtrl.abort(); } catch (_) {}
                if (prevAbort && typeof prevAbort.abort === "function") prevAbort.abort();
            },
        };

        async function pollOnce() {
            if (cancelled) return;
            // Bypass getJSON's global-abort wiring so a state poll
            // doesn't blow away an unrelated activeAbort. Inline the
            // small fetch + JSON parse the helper does.
            let r;
            try {
                const resp = await fetch("/api/orchestrator/state", {
                    method: "GET",
                    credentials: "same-origin",
                    signal: localCtrl.signal,
                });
                let payload = null;
                try { payload = await resp.json(); } catch (_) {}
                r = { ok: resp.ok, status: resp.status, payload: payload || {} };
            } catch (e) {
                if (cancelled) return;
                // Network glitch — retry. Don't surface to the user
                // unless we burn through many in a row (out of scope
                // for this view; a transient blip during an update
                // resolves itself).
                pollTimer = setTimeout(pollOnce, ORCHESTRATOR_POLL_INTERVAL_MS);
                return;
            }
            if (cancelled) return;
            if (handleAuthFailure(r)) return;
            const p = r && r.payload;
            const step = (p && p.step) || "unknown";
            const status = (p && p.status) || "";
            const err = (p && p.error) || "";
            const aptIrr = !!(p && p.apt_irreversible);
            const isTerminal = ORCHESTRATOR_TERMINAL_STEPS.has(step);

            if (!sawNonTerminal && isTerminal) {
                // Either the orchestrator hasn't written its first
                // state yet, or this is leftover state from a prior
                // run. Either way: keep polling, but only for a bounded
                // window — past ORCHESTRATOR_STALE_GRACE_POLLS, accept
                // the terminal step as authoritative so an orchestrator
                // that fails during early init (and writes `failed`
                // before any non-terminal step) doesn't leave the
                // user stuck on "Starting…".
                staleTerminalPolls += 1;
                if (staleTerminalPolls <= ORCHESTRATOR_STALE_GRACE_POLLS) {
                    stepEl.textContent = "Starting…";
                    statusEl.textContent = "";
                    errorEl.hidden = true;
                    aptNoteEl.hidden = true;
                    pollTimer = setTimeout(pollOnce, ORCHESTRATOR_POLL_INTERVAL_MS);
                    return;
                }
                // Fall through and render the terminal step.
            }
            if (!isTerminal) {
                sawNonTerminal = true;
            }

            stepEl.textContent = "Step: " + step;
            statusEl.textContent = status ? ("Status: " + status) : "";
            if (err) {
                errorEl.textContent = err;
                errorEl.hidden = false;
            } else {
                errorEl.hidden = true;
            }
            if (aptIrr) {
                aptNoteEl.textContent = "Note: the apt package upgrade has run and is not rolled back automatically; webconfig and runtime steps can still roll back independently.";
                aptNoteEl.hidden = false;
            }
            if (isTerminal) {
                // Terminal step reached for the live run. Stop polling
                // and keep the final state on screen.
                pollTimer = null;
                return;
            }
            pollTimer = setTimeout(pollOnce, ORCHESTRATOR_POLL_INTERVAL_MS);
        }
        pollOnce();
    }

    function buildUpdatesCard() {
        const updateBtn = el("button", {
            type: "button", class: "wc-btn-primary",
            onclick: async () => {
                updateBtn.disabled = true;
                const r = await postJSON("/api/orchestrator/start", {});
                updateBtn.disabled = false;
                if (handleAuthFailure(r)) return;
                if (r.status === 503) {
                    alert("System update is currently unavailable on this device.");
                    return;
                }
                if (!r.ok) {
                    alert((r.payload && r.payload.error) || (r.payload && r.payload.reason) || "update failed");
                    return;
                }
                navigate(() => orchestratorProgress(), { title: "Update System", showBack: true });
            },
        }, "Update System");

        const updateLogBtn = el("button", {
            type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(() => logViewer("update-orchestrator"), { title: "Update System log", showBack: true }),
        }, "Update System log");

        return el("section", { class: "wc-card" },
            el("h2", {}, "Updates"),
            el("div", { class: "wc-action-grid" },
                updateBtn, updateLogBtn,
            ),
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

        // Consume any flash a mutation queued before its re-render NOW, before
        // the failure early-return below. If we deferred this past a reload
        // that 500s, the stale message would survive and later surface on an
        // unrelated panel (the dashboard also consumes pendingFlash).
        const pf = consumePendingFlash();

        // A 500 / network failure on either fetch must not silently degrade to
        // an empty "No saved networks" view — that hides the real problem and
        // makes the current-network block below misleading. Surface it (with
        // any queued success message, which is still accurate).
        if (!list.ok || !status.ok) {
            render(el("section", { class: "wc-card" },
                el("h2", {}, "Wi-Fi networks"),
                pf && pf.text ? el("div", { class: "wifi-flash-" + (pf.level || "ok"), role: "status" }, pf.text) : null,
                el("p", { class: "error", role: "alert" }, "Could not load Wi-Fi state."),
                el("div", { class: "wifi-add-row" },
                    el("button", { type: "button", class: "wc-btn-primary wifi-add-btn", onclick: () => wifiPanel() }, "Retry")),
            ));
            return;
        }

        const listPayload = (list && list.payload) || {};
        const statusPayload = (status && status.payload) || {};
        const networks = listPayload.networks || [];
        const nmAvailable = !!listPayload.networkmanager_available;
        const activeConn = listPayload.active_connection || null;
        const nonWifiUplinks = statusPayload.non_wifi_uplinks || [];
        const hasEthernet = nonWifiUplinks.length > 0;

        // Match the active connection to a listed network by UUID (stable),
        // falling back to the active flag — never by SSID, since duplicate
        // SSIDs are common and would mis-identify the row. `currentNet` is the
        // real listed entry or null (e.g. an active /run netplan profile that
        // apl-wifi doesn't enumerate). Edit/Remove are offered only for a real
        // managed entry, so a null/foreign current can't issue a request
        // against /api/wifi/undefined.
        let currentNet = null;
        if (activeConn && activeConn.uuid) currentNet = networks.find(n => n.uuid === activeConn.uuid) || null;
        if (!currentNet) currentNet = networks.find(n => n.active) || null;
        const currentSsid = (currentNet && currentNet.ssid) || (activeConn && activeConn.ssid) || "";
        const currentDevice = (activeConn && activeConn.device) || "";
        const hasCurrentWifi = !!(currentNet || (activeConn && (activeConn.uuid || activeConn.ssid)));
        const currentManaged = !!(currentNet && currentNet.managed && currentNet.id);

        // Flash slots. NetworkManager-unavailable is a persistent condition, so
        // it gets its own banner: a transient success/error flash must not
        // clobber it (and vice versa).
        const nmWarn = nmAvailable ? null : el("div", { class: "wifi-flash-warn", role: "status" },
            "NetworkManager is not available on this feeder — Wi-Fi changes won't take effect.");
        const flashEl = el("div", { class: "wifi-flash" });
        function setFlash(msg, kind) {
            flashEl.replaceChildren();
            if (!msg) return;
            flashEl.appendChild(el("div", { class: "wifi-flash-" + (kind || "ok"), role: kind === "error" ? "alert" : "status" }, msg));
        }
        // Paint the flash consumed above the failure branch (a mutation queues
        // it before re-rendering; the flashEl it wrote to was destroyed by the
        // re-render, so it's replayed here).
        if (pf && pf.text) setFlash(pf.text, pf.level || "ok");

        const formHost = el("div", {});

        function badge(text, title, warn) {
            return el("span", { class: warn ? "wifi-badge wifi-badge-warn" : "wifi-badge", title: title }, text);
        }

        // --- current-network block (top) ---
        const heroHost = el("div", { class: "wifi-current" });
        function renderHero() {
            heroHost.replaceChildren();
            if (!hasCurrentWifi) {
                if (hasEthernet) {
                    const e = nonWifiUplinks[0];
                    heroHost.appendChild(el("p", { class: "muted" },
                        "Ethernet only — " + (e.device || "") + (e.ipv4 ? " (" + e.ipv4 + ")" : "")));
                } else {
                    heroHost.appendChild(el("p", { class: "muted" }, "No active uplink detected."));
                }
                return;
            }
            heroHost.appendChild(el("p", { class: "wifi-current__head" },
                el("span", { class: "wifi-dot-active", "aria-hidden": "true", title: "Connected" }, ""),
                " ",
                currentSsid ? el("strong", {}, currentSsid) : el("strong", { class: "muted" }, "(SSID unknown)"),
                currentDevice ? el("span", { class: "muted" }, " on " + currentDevice) : null,
                currentNet && currentNet.first_run_profile ? badge("first-run", "Set during initial setup") : null,
                currentNet && !currentNet.managed ? badge("foreign", "Created outside webconfig — edit via SSH", true) : null,
                currentNet && currentNet.hidden ? badge("hidden", "Hidden network") : null,
            ));

            // Only state facts we actually know — a foreign/unlisted active
            // connection has no keyfile to read priority or psk from.
            if (currentNet) {
                heroHost.appendChild(el("p", { class: "muted wifi-current__meta" },
                    "Priority " + (currentNet.priority || 0) + " · " + (currentNet.has_psk ? "password saved" : "open network")));
            }

            if (currentManaged) {
                // The last-network lock: you can't remove your only network. Any
                // other network — managed OR foreign — is a usable fallback, which
                // matches the server's delete guard exactly.
                const noFallback = !networks.some(n => !(currentNet && n.id === currentNet.id));
                const editBtn = el("button", { type: "button", class: "wc-btn-ghost", onclick: () => showForm(currentNet) }, "Edit");
                const removeBtn = el("button", {
                    type: "button", class: "wc-btn-danger",
                    disabled: noFallback ? "" : null,
                    title: noFallback ? "Add another network before you can remove this one" : null,
                    onclick: () => deleteNetwork(currentNet),
                }, "Remove");
                heroHost.appendChild(el("div", { class: "wifi-btn-row" }, editBtn, removeBtn));
                if (noFallback) {
                    heroHost.appendChild(el("p", { class: "muted wifi-current__note" },
                        "Add another network before you can remove this one."));
                }
            } else if (currentNet && !currentNet.managed) {
                // Active but foreign (e.g. an rpi-imager network set before first
                // boot). Offer Adopt so it can be managed here without SSH.
                const adoptBtn = el("button", {
                    type: "button", class: "wc-btn-primary",
                    disabled: nmAvailable ? null : "",
                    title: nmAvailable ? null : "NetworkManager unavailable — cannot adopt",
                    onclick: () => adoptNetwork(currentNet),
                }, "Adopt");
                heroHost.appendChild(el("div", { class: "wifi-btn-row" }, adoptBtn));
                heroHost.appendChild(el("p", { class: "muted wifi-current__note" },
                    "Set up before first boot. Adopt it to edit or remove it here."));
            } else if (currentNet) {
                heroHost.appendChild(el("p", { class: "muted wifi-current__note" },
                    "Managed outside webconfig — edit via SSH."));
            }
        }

        const addBtn = el("button", { type: "button", class: "wc-btn-primary wifi-add-btn" }, "Add network");
        const tableHost = el("div", {});

        function renderTable() {
            tableHost.replaceChildren();
            // The current network is shown in the block above; the table lists
            // the rest. Exclude by id identity (never by SSID).
            const others = networks.filter(n => !(currentNet && n.id === currentNet.id));
            tableHost.appendChild(el("h3", { class: "wifi-others-title" },
                hasCurrentWifi ? "Other networks" : "Saved networks"));
            if (others.length === 0) {
                tableHost.appendChild(el("p", { class: "muted" },
                    hasCurrentWifi ? "No other saved networks." : "No saved networks."));
                return;
            }
            const rows = others.map(n => {
                const foreignNote = "Created outside webconfig — use Adopt to manage it here";
                // Adopt: only for foreign rows. Imports the netplan/flash-time
                // profile into a managed keyfile so Edit/Delete light up.
                const adoptBtn = n.managed ? null : el("button", {
                    type: "button", class: "wc-btn-primary",
                    disabled: nmAvailable ? null : "",
                    title: nmAvailable
                        ? "Import this flash-time network so you can edit and remove it here"
                        : "NetworkManager unavailable — cannot adopt",
                    onclick: () => adoptNetwork(n),
                }, "Adopt");
                const editBtn = el("button", {
                    type: "button", class: "wc-btn-ghost",
                    disabled: n.managed ? null : "",
                    title: n.managed ? null : foreignNote,
                    onclick: () => showForm(n),
                }, "Edit");
                const activateBtn = el("button", {
                    type: "button", class: "wc-btn-ghost",
                    // Foreign profiles can be activated too — it's a runtime-only
                    // nmcli up, no keyfile change. Only disable when NM is down or
                    // the network is already active.
                    disabled: (!nmAvailable || n.active) ? "" : null,
                    title: !nmAvailable ? "NetworkManager unavailable — cannot activate"
                        : (n.active ? "Already active" : null),
                    onclick: () => activateNetwork(n),
                }, "Activate");
                const deleteBtn = el("button", {
                    type: "button", class: "wc-btn-danger",
                    disabled: n.managed ? null : "",
                    title: n.managed ? null : foreignNote,
                    onclick: () => deleteNetwork(n),
                }, "Delete");
                return el("tr", {},
                    el("td", {},
                        n.active ? el("span", { class: "wifi-dot-active", "aria-hidden": "true", title: "Active" }, "") : el("span", { class: "wifi-dot-idle", "aria-hidden": "true" }, ""),
                        " ",
                        el("strong", {}, n.ssid || "(unknown)"),
                        n.first_run_profile ? badge("first-run", "Set during initial setup") : null,
                        n.managed ? null : badge("foreign", foreignNote, true),
                        n.hidden ? badge("hidden", "Hidden network") : null,
                    ),
                    el("td", { class: "wifi-priority" }, String(n.priority || 0)),
                    el("td", { class: "wifi-actions" }, el("div", { class: "wifi-btn-row" }, adoptBtn, editBtn, activateBtn, deleteBtn)),
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
            // Any other network (managed or foreign) is a usable fallback, so the
            // last-network confirm only fires when this is the only one left.
            const remaining = networks.filter(m => m.id !== n.id).length;
            const needForceLast = remaining === 0;
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
            pendingFlash = { text: "Deleted " + (n.ssid || n.id), level: "ok" };
            await wifiPanel();
        }

        async function activateNetwork(n) {
            const r = await postJSON("/api/wifi/" + encodeURIComponent(n.id) + "/activate", {});
            if (handleAuthFailure(r)) return;
            if (!r.ok) {
                setFlash((r.payload && (r.payload.message || r.payload.reason || r.payload.nm_reason || r.payload.error)) || "activate failed", "error");
                return;
            }
            pendingFlash = { text: "Activating " + (n.ssid || n.id) + "…", level: "ok" };
            await wifiPanel();
        }

        // Adopt a foreign (flash-time) network into a managed keyfile so it can
        // be edited/removed here. The server copies the SSID/password, writes the
        // keyfile, and drops the original netplan profile.
        async function adoptNetwork(n) {
            if (!confirm("Import \"" + (n.ssid || n.id) + "\" so you can manage it here?")) return;
            const r = await postJSON("/api/wifi/" + encodeURIComponent(n.id) + "/adopt", {});
            if (handleAuthFailure(r)) return;
            if (!r.ok) {
                setFlash((r.payload && (r.payload.message || r.payload.reason || r.payload.nm_reason || r.payload.error)) || "adopt failed", "error");
                return;
            }
            pendingFlash = { text: "Now managing " + (n.ssid || n.id), level: "ok" };
            await wifiPanel();
        }

        function showForm(existing) {
            const isEdit = !!existing;
            // novalidate: bypass native HTML5 validation so the inline-error
            // UX owns the field-level rules end-to-end. SSID's `required`
            // attribute is dropped for the same reason — pre-disable governs
            // whether submit fires. Wi-Fi validators are intentionally
            // no-trim (SSIDs and PSKs may carry whitespace), so error
            // predicates use `v !== ""` rather than `v.trim()`.
            const ssid = el("input", { type: "text", value: existing ? (existing.ssid || "") : "" });
            const psk = el("input", {
                type: "password", autocomplete: "new-password", class: "wifi-pw-input",
                placeholder: isEdit && existing.has_psk ? "(unchanged — leave blank to keep)" : "8-63 chars or 64-hex",
            });
            // Reveal toggle. aria-pressed is set as a string both here and in
            // the handler because el() drops falsey attrs (a boolean false
            // would never reach the DOM).
            const pwToggle = el("button", {
                type: "button", class: "wifi-pw-toggle",
                "aria-label": "Show password", "aria-pressed": "false",
            }, "Show");
            pwToggle.onclick = () => {
                const reveal = psk.type === "password";
                psk.type = reveal ? "text" : "password";
                pwToggle.textContent = reveal ? "Hide" : "Show";
                pwToggle.setAttribute("aria-pressed", reveal ? "true" : "false");
                pwToggle.setAttribute("aria-label", reveal ? "Hide password" : "Show password");
            };
            const pwField = el("div", { class: "wifi-pw-field" }, psk, pwToggle);
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

            // Per-field inline errors (mirror the config-tile pattern).
            const ssidError = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" },
                "1-32 bytes, no control characters.");
            const pskError = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" },
                "8-63 printable ASCII chars or 64 hex chars.");
            const priorityError = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" },
                "Integer 0-999, no leading zeros.");

            // PSK empty is valid in BOTH add mode (open network) and edit
            // mode (leave-unchanged), per the existing payload-build logic.
            // The error shows only when PSK is non-empty AND fails the rule.
            const ssidShouldShowError = (v) => v !== "" && !isValidWifiSSID(v);
            const pskShouldShowError = (v) => v !== "" && !isValidWifiPSK(v);
            // Priority is `<input type="number">`. The browser exposes
            // `validity.badInput === true` when the user typed non-numeric
            // text (in which case `.value` is "" but the field is NOT empty
            // in the way regex-check would expect). Surface that as an
            // error and as a disabled-submit reason.
            const priorityShouldShowError = (v) => {
                if (priority.validity && priority.validity.badInput) return true;
                return v !== "" && !isValidWifiPriority(v);
            };

            // busy tracks in-flight save so the post-fetch state restore
            // can route through recheck() instead of unconditionally
            // re-enabling submit on an invalid form.
            let busy = false;
            const recheck = () => {
                if (busy) { submit.disabled = true; return; }
                if (!isValidWifiSSID(ssid.value)) { submit.disabled = true; return; }
                if (psk.value !== "" && !isValidWifiPSK(psk.value)) { submit.disabled = true; return; }
                if (priority.validity && priority.validity.badInput) { submit.disabled = true; return; }
                if (!isValidWifiPriority(priority.value || "0")) { submit.disabled = true; return; }
                submit.disabled = false;
            };
            ssid.addEventListener("input", () => {
                refreshFieldError(ssid, ssidError, ssidShouldShowError);
                recheck();
            });
            psk.addEventListener("input", () => {
                refreshFieldError(psk, pskError, pskShouldShowError);
                recheck();
            });
            priority.addEventListener("input", () => {
                refreshFieldError(priority, priorityError, priorityShouldShowError);
                recheck();
            });

            const form = el("form", {
                class: "wifi-form",
                novalidate: true,
                onsubmit: async (e) => {
                    e.preventDefault();
                    inlineErr.textContent = "";
                    const ssidVal = ssid.value;
                    const pskVal = psk.value;
                    // Defense-in-depth: pre-disable should have prevented
                    // reaching here with invalid values, but keep the guard
                    // for programmatic submits and any browser quirks.
                    if (!isValidWifiSSID(ssidVal)) {
                        inlineErr.textContent = "SSID must be 1-32 bytes, no control characters.";
                        recheck();
                        return;
                    }
                    if (pskVal !== "" && !isValidWifiPSK(pskVal)) {
                        inlineErr.textContent = "Password must be 8-63 printable ASCII chars or 64 hex chars.";
                        recheck();
                        return;
                    }
                    const priVal = priority.value || "0";
                    if ((priority.validity && priority.validity.badInput) || !isValidWifiPriority(priVal)) {
                        inlineErr.textContent = "Priority must be an integer 0-999.";
                        recheck();
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

                    busy = true;
                    recheck();
                    submit.textContent = testBox.checked ? "Testing (up to 30s)…" : "Saving…";
                    const url = isEdit ? "/api/wifi/" + encodeURIComponent(existing.id) : "/api/wifi";
                    const r = isEdit ? await putJSON(url, body) : await postJSON(url, body);
                    busy = false;
                    submit.textContent = isEdit ? "Save changes" : "Add network";
                    recheck();
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
                    pendingFlash = { text: isEdit ? "Updated " + ssidVal : "Added " + ssidVal, level: "ok" };
                    await wifiPanel();
                },
            },
                el("h3", {}, isEdit ? "Edit " + (existing.ssid || existing.id) : "Add network"),
                el("div", { class: "field" }, el("label", {}, "SSID"), ssid, ssidError),
                el("div", { class: "field" }, el("label", {}, "Password"), pwField, pskError),
                el("div", { class: "field-row" },
                    el("label", {}, hidden, " Hidden network"),
                    el("label", {}, "Priority ", priority),
                ),
                priorityError,
                el("p", { class: "field-help" },
                    "Higher number wins — the feeder joins the highest-priority network it can reach first. Default 0."),
                el("div", { class: "field" },
                    el("label", {}, testBox, " Test connection before saving"),
                    el("p", { class: "muted" }, "Tries to join now; rolls back on failure. Connecting to a new network may briefly drop this page if the feeder is on Wi-Fi."),
                ),
                inlineErr,
                el("div", { class: "actions" }, submit, cancel),
            );
            formHost.replaceChildren(form);
            // Initial sync — sets submit's disabled state for the seed values.
            recheck();
            ssid.focus();
        }

        addBtn.onclick = () => showForm(null);

        renderHero();
        renderTable();

        render(
            el("section", { class: "wc-card" },
                el("h2", {}, "Wi-Fi networks"),
                nmWarn,
                flashEl,
                heroHost,
                el("div", { class: "wifi-add-row" }, addBtn),
                formHost,
                tableHost,
            ),
        );
    }

    // ===== System metrics panel (pi_health subpage) =====

    function formatUptime(seconds) {
        if (!seconds || seconds <= 0) return null;
        const d = Math.floor(seconds / 86400);
        const h = Math.floor((seconds % 86400) / 3600);
        const m = Math.floor((seconds % 3600) / 60);
        if (d > 0) return d + " d " + h + " h";
        if (h > 0) return h + " h " + m + " m";
        return m + " m";
    }

    function metricCell(label, value, opts) {
        const o = opts || {};
        const cls = "wc-metrics__cell" + (o.full ? " wc-metrics__cell--full" : "");
        return el("div", { class: cls },
            el("dt", {}, label),
            el("dd", {}, value),
        );
    }

    async function piHealthPanel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading metrics…")));
        const r = await getJSON("/api/status");
        if (handleAuthFailure(r)) return;
        if (!r.ok) {
            render(el("div", { class: "wc-card" },
                el("h2", {}, "System metrics"),
                el("p", { class: "error", role: "alert" },
                    (r.payload && r.payload.error) || "Could not load system metrics."),
            ));
            return;
        }

        const p = r.payload || {};
        const sys = p.system || null;
        const thr = p.pi_throttle || null;     // present only when Pi + throttle probed
        const hh = p.hardware_health || null;   // local-only severity/summary/is_raspberry_pi rollup
        const tempUnit = p.temp_unit === "F" ? "F" : "C";

        // Per-sub-probe success is carried by value presence: the server emits
        // omitempty pointers, so a present number/bool means that probe ran.
        const tempProbed = !!sys && typeof sys.cpu_temp_c === "number";
        const memProbed = !!sys && typeof sys.mem_avail_pct === "number";
        const diskProbed = !!sys && typeof sys.disk_free_pct === "number";
        const timeProbed = !!sys && typeof sys.ntp_synchronized === "boolean";
        const uptimeProbed = !!sys && typeof sys.uptime_s === "number";
        const throttleProbed = thr !== null;
        const psuProbed = !!thr && typeof thr.psu_max_current_ma === "number";
        const anyProbed = tempProbed || memProbed || diskProbed || timeProbed
            || uptimeProbed || throttleProbed || psuProbed;
        if (!anyProbed) {
            render(el("section", { class: "wc-card" },
                el("h2", {}, "System metrics"),
                el("p", { class: "muted" },
                    hh && hh.is_raspberry_pi === false
                        ? "This device isn't a Raspberry Pi — no hardware metrics to show."
                        : "No metrics available — the hardware probes did not return data."),
            ));
            return;
        }

        const severity = (hh && hh.severity) || "na";
        const summaryBanner = el("div", { class: "wc-metrics__summary" },
            el("span", { class: "wc-metrics__summary-dot wc-metrics__summary-dot--" + severity }),
            el("span", {}, (hh && hh.summary) || "Status unknown"),
        );

        const cells = [];

        if (tempProbed) {
            const c = sys.cpu_temp_c;
            const val = tempUnit === "F"
                ? (c * 9 / 5 + 32).toFixed(1) + " °F"
                : c.toFixed(1) + " °C";
            cells.push(metricCell("CPU temperature", val));
        }
        if (memProbed) {
            cells.push(metricCell("Memory free", sys.mem_avail_pct.toFixed(0) + " %"));
        }
        if (diskProbed) {
            cells.push(metricCell("Disk free", sys.disk_free_pct.toFixed(0) + " %"));
        }
        if (uptimeProbed) {
            const upStr = formatUptime(sys.uptime_s);
            if (upStr) {
                cells.push(metricCell("Uptime", upStr));
            }
        }
        if (timeProbed) {
            cells.push(metricCell("NTP synchronised", sys.ntp_synchronized ? "Yes" : "No"));
        }
        if (psuProbed) {
            const maxMA = thr.psu_max_current_ma || 0;
            const expMA = thr.psu_expected_ma || 0;
            const val = expMA > 0
                ? maxMA + " / " + expMA + " mA"
                : maxMA + " mA";
            cells.push(metricCell("PSU current capability", val));
        }
        if (throttleProbed) {
            const flags = [];
            const addFlag = (label, now, ever) => {
                if (!now && !ever) return;
                flags.push(el("span", { class: "wc-metrics__flag" + (now ? " wc-metrics__flag--now" : "") },
                    label + (now ? "" : " (since boot)")));
            };
            addFlag("Under-voltage", thr.undervoltage_now, thr.undervoltage_ever);
            addFlag("Throttled", thr.throttled_now, thr.throttled_ever);
            addFlag("Frequency capped", thr.freq_capped_now, thr.freq_capped_ever);
            addFlag("Soft temp limit", thr.soft_temp_limit_now, thr.soft_temp_limit_ever);
            const body = flags.length > 0
                ? el("div", {}, ...flags)
                : el("span", {}, "None reported");
            cells.push(metricCell("Throttling", body, { full: true }));
        }

        render(el("section", { class: "wc-card" },
            el("h2", {}, "System metrics"),
            summaryBanner,
            el("dl", { class: "wc-metrics" }, ...cells),
            el("p", { class: "muted",
                style: "margin-top: 1.25rem; font-size: 0.85rem;" },
                "These are the same readings the feeder reports to airplanes.live when diagnostics are enabled."),
        ));
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

    // ===== Third-party aggregators =====

    // Vendor data-sharing links come from the (code-owned) adapter descriptor,
    // but we still allowlist the host — only an https link to the vendor's own
    // domain renders, never arbitrary JSON.
    const AGGREGATOR_CLAIM_HOSTS = ["flightradar24.com", "flightaware.com"];
    function vendorClaimHref(url) {
        try {
            const u = new URL(url);
            if (u.protocol !== "https:") return null;
            const host = u.hostname;
            const ok = AGGREGATOR_CLAIM_HOSTS.some(h => host === h || host.endsWith("." + h));
            return ok ? u.toString() : null;
        } catch (_) { return null; }
    }

    // Advisory client-side validators — fast inline feedback only. apl-aggregator
    // re-validates everything and is the authority; these mirror its rules so the
    // form can disable submit and hint before the round-trip.
    const aggEmailRE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    function isValidAggEmail(v) { return aggEmailRE.test(String(v == null ? "" : v).trim()); }
    const fr24KeyRE = /^[A-Za-z0-9]{6,40}$/;
    function isValidFr24Key(v) { return fr24KeyRE.test(String(v == null ? "" : v).trim()); }

    const feederIdRE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
    function isValidFeederId(v) { return feederIdRE.test(String(v == null ? "" : v).trim()); }

    // Adapters with a bespoke config panel, and the display name used where only
    // the id is in hand (the enable-progress view). Keep in sync with the .desc
    // files and the panel dispatcher below.
    const ADAPTER_NAMES = { fr24: "Flightradar24", piaware: "FlightAware" };
    function adapterManageable(id) { return id === "fr24" || id === "piaware"; }
    function adapterPanel(id) { return id === "piaware" ? piawarePanel() : fr24Panel(); }

    // AGG_STATE_BADGE maps an adapter `state` to [label, severity-suffix].
    const AGG_STATE_BADGE = {
        running:             ["Feeding",            "ok"],
        installing:          ["Installing…",        "warn"],
        stopped:             ["Off",                "na"],
        not_installed:       ["Not set up",         "na"],
        failed:              ["Setup failed",       "err"],
        decoder_unavailable: ["Decoder not ready",  "warn"],
        network_unavailable: ["Network unavailable", "warn"],
        unavailable:         ["Unavailable",        "na"],
    };
    function aggStateBadge(state) {
        const pair = AGG_STATE_BADGE[state] || [state || "—", "na"];
        return el("span", { class: "wc-agg-badge wc-agg-badge--" + pair[1] }, pair[0]);
    }

    // classifyAggregators projects /api/aggregators into a single dashboard-tile
    // {dot, meta}. installing/failed take precedence; otherwise it summarises
    // which adapters are feeding.
    function classifyAggregators(payload) {
        const adapters = (payload && payload.aggregators) || [];
        if (!adapters.length) return { dot: "na", meta: "—" };
        if (adapters.some(a => a.state === "installing")) return { dot: "warn", meta: "installing…" };
        if (adapters.some(a => a.state === "failed")) return { dot: "err", meta: "setup failed" };
        const feeding = adapters.filter(a => a.enabled).map(a => a.display_name || a.id);
        if (feeding.length) return { dot: "ok", meta: feeding.join(", ") };
        return { dot: "na", meta: "off" };
    }

    function buildAggregatorTile() {
        const iconNode = el("span", { class: "wc-tile__icon" }, svgIcon(AGGREGATOR_ICON));
        const titleEl  = el("span", { class: "wc-tile__title" }, "Aggregators");
        const metaEl   = el("span", { class: "wc-tile__meta" }, "—");
        const dotEl    = el("span", { class: "wc-tile__dot wc-tile__dot--na" });
        const chev     = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "›");
        const root = el("button", {
            type: "button",
            class: "wc-tile wc-tile--nav",
            "data-state": "unknown",
            onclick: () => navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true }),
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
            dotEl,
        );
        return { root, titleEl, metaEl, dotEl };
    }

    function updateAggregatorTile(tile, resp) {
        if (!tile) return;
        const c = (resp && resp.ok) ? classifyAggregators(resp.payload) : { dot: "na", meta: "—" };
        tile.dotEl.className = "wc-tile__dot wc-tile__dot--" + c.dot;
        tile.metaEl.textContent = c.meta;
        tile.root.title = c.meta || "";
        tile.root.setAttribute("data-state", c.dot);
    }

    // CAVEAT preamble. Org-voice wording is the project owner's call — this is a
    // clear, accurate placeholder, not final copy. TODO: finalize wording.
    const AGG_CAVEAT_TEXT =
        "airplanes.live shares all aircraft data openly. Other networks may filter or hide " +
        "aircraft and can restrict how your data is used. Feeding a third-party network is " +
        "optional: it sends your receiver's data to that provider under their own terms, and " +
        "does not change what you feed to airplanes.live.";

    async function aggregatorsPanel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading aggregators…")));
        const resp = await getJSON("/api/aggregators");
        if (handleAuthFailure(resp)) return;
        const pf = consumePendingFlash();
        if (!resp.ok) {
            render(el("section", { class: "wc-card" },
                el("h2", {}, "Third-party aggregators"),
                pf && pf.text ? el("div", { class: "wc-flash wc-flash--" + (pf.level || "ok") }, pf.text) : null,
                el("p", { class: "error", role: "alert" }, "Could not load aggregators."),
                el("button", { type: "button", class: "wc-btn-primary", onclick: () => aggregatorsPanel() }, "Retry"),
            ));
            return;
        }
        const adapters = (resp.payload && resp.payload.aggregators) || [];

        const flashEl = el("div", {});
        if (pf && pf.text) flashEl.appendChild(el("div", { class: "wc-flash wc-flash--" + (pf.level || "ok") }, pf.text));

        const list = el("div", { class: "wc-agg-list" });
        if (!adapters.length) {
            list.appendChild(el("p", { class: "muted" }, "No third-party aggregators are available on this image."));
        }
        for (const a of adapters) list.appendChild(buildAggregatorRow(a));

        render(el("section", { class: "wc-card" },
            el("h2", {}, "Third-party aggregators"),
            el("p", { class: "wc-agg-caveat" }, AGG_CAVEAT_TEXT),
            flashEl,
            list,
            buildAggregatorBackupCard(),
        ));
    }

    function buildAggregatorRow(a) {
        const manageable = adapterManageable(a.id);
        const actions = el("div", { class: "wc-agg-row__actions" });
        // A configured/enabled adapter stays manageable even if it later reports
        // unavailable, so Disable/Remove are never stranded.
        if (manageable && (a.available || a.enabled || a.configured)) {
            actions.appendChild(el("button", {
                type: "button", class: "wc-btn-ghost",
                onclick: () => navigate(() => adapterPanel(a.id), { title: a.display_name || a.id, showBack: true }),
            }, a.enabled || a.configured ? "Manage" : "Set up"));
        }
        const reason = (!a.available && a.unavailable_reason)
            ? el("p", { class: "muted wc-agg-row__reason" }, a.unavailable_reason) : null;
        // Installed version (the build actually running) + a drift hint when the
        // overlay pins a newer one than is installed — it applies on the next
        // system update (or a reconcile failed and will retry).
        const version = a.version
            ? el("span", { class: "wc-agg-row__version muted" }, "v" + a.version) : null;
        const drift = a.version_drift
            ? el("p", { class: "muted wc-agg-row__reason" }, a.desired_version
                ? "Update to v" + a.desired_version + " pending — applies on the next system update."
                : "An update is pending — it applies on the next system update.")
            : null;
        return el("div", { class: "wc-agg-row" },
            el("div", { class: "wc-agg-row__head" },
                el("span", { class: "wc-agg-row__name" }, a.display_name || a.id),
                aggStateBadge(a.state),
                version,
            ),
            reason,
            drift,
            actions.childNodes.length ? actions : null,
        );
    }

    function buildAggregatorBackupCard() {
        const msg = el("div", {});
        const setMsg = (text, kind) => msg.replaceChildren(text ? el("div", { class: "wc-flash wc-flash--" + (kind || "ok") }, text) : el("span"));

        const exportBtn = el("button", { type: "button", class: "wc-btn-ghost" }, "Download backup");
        exportBtn.onclick = async () => {
            setMsg("");
            const r = await postJSON("/api/aggregators/export", {});
            if (handleAuthFailure(r)) return;
            if (!r.ok) { setMsg((r.payload && (r.payload.message || r.payload.error)) || "Backup failed.", "warn"); return; }
            try {
                const blob = new Blob([JSON.stringify(r.payload, null, 2)], { type: "application/json" });
                const url = URL.createObjectURL(blob);
                const a = el("a", { href: url, download: "airplanes-aggregators-backup.json" });
                document.body.appendChild(a); a.click(); a.remove();
                setTimeout(() => URL.revokeObjectURL(url), 1000);
                setMsg("Backup downloaded — keep it private.", "ok");
            } catch (_) { setMsg("Could not start the download.", "warn"); }
        };

        const fileInput = el("input", { type: "file", accept: "application/json,.json", hidden: true });
        const importBtn = el("button", { type: "button", class: "wc-btn-ghost" }, "Restore from backup");
        importBtn.onclick = () => fileInput.click();
        fileInput.onchange = async () => {
            const file = fileInput.files && fileInput.files[0];
            fileInput.value = "";
            if (!file) return;
            // Guard before reading/parsing so a huge file can't freeze the
            // browser (the server caps the import body at 64 KiB anyway).
            if (file.size > 65536) { setMsg("That backup file is too large to be a valid aggregator backup.", "warn"); return; }
            setMsg("Restoring…");
            let parsed;
            try { parsed = JSON.parse(await file.text()); }
            catch (_) { setMsg("That file isn't a valid backup.", "warn"); return; }
            const r = await postJSON("/api/aggregators/import", parsed);
            if (handleAuthFailure(r)) return;
            if (!r.ok) {
                setMsg((r.payload && (r.payload.message || r.payload.error)) ||
                    "Restore failed. Stop an enabled adapter before restoring.", "warn");
                return;
            }
            pendingFlash = { text: "Identities restored. Set up an adapter to start feeding.", level: "ok" };
            await aggregatorsPanel();
        };

        return el("section", { class: "wc-card wc-agg-backup" },
            el("h3", {}, "Back up & restore"),
            el("p", { class: "muted" },
                "Download your aggregator sign-in details (including sharing keys) so you can restore them after a reflash. The file is sensitive — keep it private."),
            el("div", { class: "wc-agg-row__actions" }, exportBtn, importBtn, fileInput),
            msg,
        );
    }

    // aggEnableErrorMessage turns a failed enable/disable/reset response (or a
    // failed progress overlay) into operator-facing guidance.
    function aggEnableErrorMessage(r) {
        const p = (r && r.payload) || {};
        const code = p.error_code || "";
        if (code === "decoder_unavailable") return "The local decoder isn't ready yet. Make sure the receiver is running, then try again.";
        if (code === "lock_timeout") return "Another aggregator operation is in progress. Try again in a moment.";
        if (code === "rejected" && /lat|lon|alt|latitude|longitude|altitude|location/i.test(p.message || "")) {
            return "Set your feeder location on the dashboard before setting up an aggregator.";
        }
        if (code === "acquire_failed") return "Could not download or install the third-party feeder. Check the feeder's internet connection and try again.";
        if (code === "signup_failed") return "Flightradar24 sign-up didn't return a sharing key. Try again, or paste an existing key.";
        return p.message || p.error || "Operation failed.";
    }

    async function fr24Panel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading…")));
        const resp = await getJSON("/api/aggregators");
        if (handleAuthFailure(resp)) return;
        if (!resp.ok) {
            render(el("section", { class: "wc-card" }, el("h2", {}, "Flightradar24"),
                el("p", { class: "error", role: "alert" }, "Could not load status."),
                el("button", { type: "button", class: "wc-btn-primary", onclick: () => fr24Panel() }, "Retry")));
            return;
        }
        const a = ((resp.payload && resp.payload.aggregators) || []).find(x => x.id === "fr24");
        if (!a) {
            render(el("section", { class: "wc-card" }, el("h2", {}, "Flightradar24"),
                el("p", { class: "muted" }, "Flightradar24 is not available on this image.")));
            return;
        }
        // If an enable is already in flight, jump straight to the progress view.
        if (a.state === "installing" && a.enable && a.enable.request_id) {
            return aggregatorEnableProgress("fr24", a.enable.request_id);
        }
        renderFr24Form(a);
    }

    function renderFr24Form(a) {
        const configured = !!(a.configured || a.enabled);
        const claimUrl = vendorClaimHref(a.claim_url);

        const inlineErr = el("p", { class: "error", role: "alert" });
        const email = el("input", { type: "email", autocomplete: "email",
            placeholder: configured ? "(leave blank to keep current)" : "you@example.com" });
        const key = el("input", { type: "text", autocomplete: "off", spellcheck: "false",
            placeholder: configured ? "(leave blank to keep current)" : "optional — paste an existing sharing key" });
        const emailErr = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" }, "Enter a valid email address.");
        const keyErr = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" }, "Sharing keys are 6–40 letters and digits.");

        const submitLabel = a.enabled ? "Replace identity" : (configured ? "Re-run setup" : "Set up Flightradar24");
        const submit = el("button", { type: "submit", class: "wc-btn-primary" }, submitLabel);

        const recheck = () => {
            let bad = false;
            if (email.value.trim() && !isValidAggEmail(email.value)) bad = true;
            if (!configured && !email.value.trim()) bad = true; // first-time setup needs an email
            if (key.value.trim() && !isValidFr24Key(key.value)) bad = true;
            submit.disabled = bad;
        };
        email.addEventListener("input", () => { refreshFieldError(email, emailErr, v => v.trim() !== "" && !isValidAggEmail(v)); recheck(); });
        key.addEventListener("input", () => { refreshFieldError(key, keyErr, v => v.trim() !== "" && !isValidFr24Key(v)); recheck(); });

        const form = el("form", { class: "wc-agg-form", novalidate: true,
            onsubmit: async (e) => {
                e.preventDefault();
                inlineErr.textContent = "";
                const fields = {};
                const ev = email.value.trim(), kv = key.value.trim();
                if (ev) fields.email = ev;            // omit when blank → helper keeps stored email
                if (kv) fields.sharing_key = kv;       // omit when blank → keeps stored key / signs up
                if (!configured && !fields.email) { inlineErr.textContent = "An email address is required to set up Flightradar24."; return; }
                if (fields.email && !isValidAggEmail(fields.email)) { inlineErr.textContent = "Enter a valid email address."; return; }
                if (fields.sharing_key && !isValidFr24Key(fields.sharing_key)) { inlineErr.textContent = "Sharing keys are 6–40 letters and digits."; return; }
                submit.disabled = true; submit.textContent = "Submitting…";
                const r = await postJSON("/api/aggregators/fr24/enable", { fields });
                if (handleAuthFailure(r)) return;
                if (r.status === 202) {
                    navigate(() => aggregatorEnableProgress("fr24", r.payload && r.payload.request_id),
                        { title: "Setting up Flightradar24", showBack: true });
                    return;
                }
                submit.disabled = false; submit.textContent = submitLabel;
                inlineErr.textContent = aggEnableErrorMessage(r);
            },
        },
            el("h3", {}, "Sign-in details"),
            el("div", { class: "field" }, el("label", {}, "Email address"), email, emailErr),
            el("div", { class: "field" }, el("label", {}, "Sharing key (optional)"), key, keyErr,
                el("p", { class: "field-help" }, "Leave blank to create a new Flightradar24 sharing key from your email. Paste an existing key to reuse a Flightradar24 account.")),
            inlineErr,
            el("div", { class: "wc-agg-row__actions" }, submit),
        );
        recheck();

        const actions = el("div", { class: "wc-agg-row__actions" });
        if (a.enabled) {
            const disableBtn = el("button", { type: "button", class: "wc-btn-ghost" }, "Stop feeding");
            disableBtn.onclick = async () => {
                disableBtn.disabled = true;
                const r = await postJSON("/api/aggregators/fr24/disable", {});
                if (handleAuthFailure(r)) return;
                if (!r.ok) { disableBtn.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r); return; }
                pendingFlash = { text: "Stopped feeding Flightradar24.", level: "ok" };
                navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
            };
            actions.appendChild(disableBtn);
        }
        if (configured) {
            const resetBtn = el("button", { type: "button", class: "wc-btn-danger" }, "Remove");
            resetBtn.onclick = async () => {
                if (!confirm("Remove Flightradar24? This stops feeding and deletes the installed feeder and its stored sharing key.")) return;
                resetBtn.disabled = true;
                const r = await postJSON("/api/aggregators/fr24/reset", {});
                if (handleAuthFailure(r)) return;
                if (!r.ok) { resetBtn.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r); return; }
                pendingFlash = { text: "Removed Flightradar24.", level: "ok" };
                navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
            };
            actions.appendChild(resetBtn);
        }

        const links = el("p", { class: "wc-agg-links" });
        if (claimUrl) links.appendChild(el("a", { href: claimUrl, target: "_blank", rel: "noopener noreferrer" }, "Flightradar24 data-sharing page"));
        if (a.enabled || a.state === "running" || configured) {
            if (links.childNodes.length) links.appendChild(el("span", { class: "muted" }, " · "));
            links.appendChild(el("a", { href: "#", role: "button",
                onclick: (e) => { e.preventDefault(); navigate(() => logViewer("fr24"), { title: "Logs · fr24", showBack: true }); } }, "View logs"));
        }

        render(el("section", { class: "wc-card" },
            el("h2", { class: "wc-card-head" }, el("span", {}, "Flightradar24"), aggStateBadge(a.state)),
            el("p", { class: "muted" }, a.enabled
                ? "Feeding Flightradar24 alongside airplanes.live."
                : "Feed Flightradar24 alongside airplanes.live. Your feeder location is used automatically."),
            form,
            actions.childNodes.length ? actions : null,
            links.childNodes.length ? links : null,
        ));
    }

    async function piawarePanel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading…")));
        const resp = await getJSON("/api/aggregators");
        if (handleAuthFailure(resp)) return;
        if (!resp.ok) {
            render(el("section", { class: "wc-card" }, el("h2", {}, "FlightAware"),
                el("p", { class: "error", role: "alert" }, "Could not load status."),
                el("button", { type: "button", class: "wc-btn-primary", onclick: () => piawarePanel() }, "Retry")));
            return;
        }
        const a = ((resp.payload && resp.payload.aggregators) || []).find(x => x.id === "piaware");
        if (!a) {
            render(el("section", { class: "wc-card" }, el("h2", {}, "FlightAware"),
                el("p", { class: "muted" }, "FlightAware is not available on this image.")));
            return;
        }
        if (a.state === "installing" && a.enable && a.enable.request_id) {
            return aggregatorEnableProgress("piaware", a.enable.request_id);
        }
        renderPiawareForm(a);
    }

    function renderPiawareForm(a) {
        const configured = !!(a.configured || a.enabled);
        const claimUrl = vendorClaimHref(a.claim_url);
        const inlineErr = el("p", { class: "error", role: "alert" });

        const feederId = el("input", { type: "text", autocomplete: "off", spellcheck: "false",
            placeholder: "optional — paste a Feeder ID to reclaim an existing feeder" });
        const feederErr = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" },
            "A Feeder ID looks like 00000000-0000-0000-0000-000000000000.");

        const mlatOn = a.configured_mlat_enabled != null ? !!a.configured_mlat_enabled : !!a.mlat_default;
        const mlat = el("input", { type: "checkbox" });
        mlat.checked = mlatOn;

        const submitLabel = a.enabled ? "Update settings" : (configured ? "Re-run setup" : "Set up FlightAware");
        const submit = el("button", { type: "submit", class: "wc-btn-primary" }, submitLabel);

        const recheck = () => { submit.disabled = feederId.value.trim() !== "" && !isValidFeederId(feederId.value); };
        feederId.addEventListener("input", () => { refreshFieldError(feederId, feederErr, v => v.trim() !== "" && !isValidFeederId(v)); recheck(); });

        const form = el("form", { class: "wc-agg-form", novalidate: true,
            onsubmit: async (e) => {
                e.preventDefault();
                inlineErr.textContent = "";
                const fields = {};
                const fv = feederId.value.trim();
                if (fv) {
                    if (!isValidFeederId(fv)) { inlineErr.textContent = "Enter a valid Feeder ID, or leave it blank."; return; }
                    fields.feeder_id = fv;       // omit when blank → new feeder is provisioned
                }
                submit.disabled = true; submit.textContent = "Submitting…";
                const r = await postJSON("/api/aggregators/piaware/enable", { fields, mlat_enabled: mlat.checked });
                if (handleAuthFailure(r)) return;
                if (r.status === 202) {
                    navigate(() => aggregatorEnableProgress("piaware", r.payload && r.payload.request_id),
                        { title: "Setting up FlightAware", showBack: true });
                    return;
                }
                submit.disabled = false; submit.textContent = submitLabel;
                inlineErr.textContent = aggEnableErrorMessage(r);
            },
        },
            el("h3", {}, "FlightAware settings"),
            el("div", { class: "field" },
                el("label", { class: "wc-checkbox" }, mlat, el("span", {}, "Participate in MLAT (multilateration)")),
                el("p", { class: "field-help" }, "MLAT starts once you claim this feeder and set its location on FlightAware.")),
            el("div", { class: "field" }, el("label", {}, "Feeder ID (optional)"), feederId, feederErr,
                el("p", { class: "field-help" }, "Leave blank to register a new FlightAware feeder. Paste an existing Feeder ID to reclaim a feeder you already own.")),
            inlineErr,
            el("div", { class: "wc-agg-row__actions" }, submit),
        );
        recheck();

        const actions = el("div", { class: "wc-agg-row__actions" });
        if (a.enabled) {
            const disableBtn = el("button", { type: "button", class: "wc-btn-ghost" }, "Stop feeding");
            disableBtn.onclick = async () => {
                disableBtn.disabled = true;
                const r = await postJSON("/api/aggregators/piaware/disable", {});
                if (handleAuthFailure(r)) return;
                if (!r.ok) { disableBtn.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r); return; }
                pendingFlash = { text: "Stopped feeding FlightAware.", level: "ok" };
                navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
            };
            actions.appendChild(disableBtn);
        }
        if (configured) {
            const resetBtn = el("button", { type: "button", class: "wc-btn-danger" }, "Remove");
            resetBtn.onclick = async () => {
                if (!confirm("Remove FlightAware? This stops feeding, uninstalls piaware, and forgets this feeder's identity. Download a backup first if you want to keep its Feeder ID.")) return;
                resetBtn.disabled = true;
                const r = await postJSON("/api/aggregators/piaware/reset", {});
                if (handleAuthFailure(r)) return;
                if (!r.ok) { resetBtn.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r); return; }
                pendingFlash = { text: "Removed FlightAware.", level: "ok" };
                navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
            };
            actions.appendChild(resetBtn);
        }

        const links = el("p", { class: "wc-agg-links" });
        if (claimUrl) links.appendChild(el("a", { href: claimUrl, target: "_blank", rel: "noopener noreferrer" }, "Claim this feeder on FlightAware"));
        if (a.enabled || a.state === "running" || configured) {
            if (links.childNodes.length) links.appendChild(el("span", { class: "muted" }, " · "));
            links.appendChild(el("a", { href: "#", role: "button",
                onclick: (e) => { e.preventDefault(); navigate(() => logViewer("piaware"), { title: "Logs · piaware", showBack: true }); } }, "View logs"));
        }

        render(el("section", { class: "wc-card" },
            el("h2", { class: "wc-card-head" }, el("span", {}, "FlightAware"), aggStateBadge(a.state)),
            el("p", { class: "muted" }, a.enabled
                ? "Feeding FlightAware alongside airplanes.live. Claim this feeder on FlightAware to see your statistics and enable MLAT."
                : "Feed FlightAware (piaware) alongside airplanes.live. After setup, claim the feeder on FlightAware to enable MLAT and see your statistics."),
            form,
            actions.childNodes.length ? actions : null,
            links.childNodes.length ? links : null,
        ));
    }

    const AGG_ENABLE_POLL_MS = 2000;
    const AGG_ENABLE_GRACE_POLLS = 5;
    // aggregatorEnableProgress polls /api/aggregators after a 202 and shows the
    // detached worker's progress. It matches the worker's request_id against the
    // one the 202 returned, so a stale overlay from a prior failed run can't make
    // this run look failed. Mirrors orchestratorProgress's lifecycle teardown.
    function aggregatorEnableProgress(id, requestId) {
        const name = ADAPTER_NAMES[id] || id;
        const stepEl = el("p", { class: "muted" }, "Starting…");
        const errEl = el("div", { class: "wc-flash wc-flash--warn", role: "alert" });
        errEl.hidden = true;
        const actions = el("div", { class: "wc-agg-row__actions" });
        render(el("section", { class: "wc-card" },
            el("h2", {}, "Setting up " + name),
            el("p", { class: "muted" }, "Installing and configuring the " + name + " feeder. This can take a minute — you can leave this page; it keeps running."),
            stepEl, errEl, actions,
        ));

        let cancelled = false, sawMine = false, stale = 0, pollTimer = null;
        const localCtrl = new AbortController();
        const prevAbort = activeAbort;
        activeAbort = { abort: () => {
            cancelled = true;
            if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
            try { localCtrl.abort(); } catch (_) {}
            if (prevAbort && typeof prevAbort.abort === "function") prevAbort.abort();
        }};

        const finishOk = () => {
            pollTimer = null;
            pendingFlash = { text: name + " is now feeding.", level: "ok" };
            navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
        };
        const finishFail = (msg) => {
            pollTimer = null;
            stepEl.textContent = "Setup failed.";
            errEl.textContent = msg || (name + " setup failed.");
            errEl.hidden = false;
            actions.replaceChildren(el("button", { type: "button", class: "wc-btn-primary",
                onclick: () => navigate(() => adapterPanel(id), { title: name, showBack: true }) }, "Back to setup"));
        };

        async function pollOnce() {
            if (cancelled) return;
            let r;
            try {
                const resp = await fetch("/api/aggregators", { method: "GET", credentials: "same-origin", headers: { Accept: "application/json" }, signal: localCtrl.signal });
                let payload = null; try { payload = await resp.json(); } catch (_) {}
                r = { ok: resp.ok, status: resp.status, payload: payload || {} };
            } catch (e) {
                if (cancelled) return;
                pollTimer = setTimeout(pollOnce, AGG_ENABLE_POLL_MS);
                return;
            }
            if (cancelled) return;
            if (handleAuthFailure(r)) return;
            const a = ((r.payload && r.payload.aggregators) || []).find(x => x.id === id);
            const ov = a && a.enable;
            const mine = !!(ov && ov.request_id === requestId);
            if (mine && ov.status === "done") return finishOk();
            if (mine && ov.status === "failed") return finishFail(aggEnableErrorMessage({ payload: ov }));
            if (mine && (ov.status === "running" || ov.status === "starting")) {
                sawMine = true;
                stepEl.textContent = "Working… (" + (ov.step || "starting") + ")";
                pollTimer = setTimeout(pollOnce, AGG_ENABLE_POLL_MS);
                return;
            }
            // No overlay for OUR run yet (launch latency) or a prior run's
            // overlay. Keep showing "Starting…" within the grace window.
            if (!sawMine && stale < AGG_ENABLE_GRACE_POLLS) {
                stale += 1;
                stepEl.textContent = "Starting…";
                pollTimer = setTimeout(pollOnce, AGG_ENABLE_POLL_MS);
                return;
            }
            // We never matched THIS request's overlay (it was superseded, cleared,
            // or never appeared). Do NOT infer success/failure from the adapter's
            // unscoped state — for a re-enable of an already-running adapter that
            // would be wrong. Stop and send the user to the panel, where the
            // reconciled status is authoritative.
            pollTimer = null;
            stepEl.textContent = "Still working — this is taking longer than expected.";
            errEl.hidden = true;
            actions.replaceChildren(el("button", {
                type: "button", class: "wc-btn-primary",
                onclick: () => navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true }),
            }, "Check status"));
        }
        pollOnce();
    }

    async function dashboard() {
        const heroEl = buildHero();
        const tileGrid = buildTileGrid();
        const identityBody = buildIdentityCardBody();
        const privacyBody = buildPrivacyCardBody();
        const configBody = el("div", {}, el("p", { class: "muted" }, "loading…"));

        // Claim-status indicator in the Identity card header: a colored dot
        // + label sourced from /api/claim/status. Built here — outside
        // identityBody, which re-renders on identity change — so its element
        // refs stay stable and only their color/text update.
        const claimDot = el("span", { class: "wc-claim-dot wc-claim-dot--na", title: "Checking claim status…" });
        const claimLabel = el("span", { class: "wc-claim-status__label" }, "Checking…");

        const identityCard = el("section", { class: "wc-card" },
            el("h2", { class: "wc-card-head" },
                el("span", {}, "Identity"),
                el("span", { class: "wc-claim-status", "aria-live": "polite" }, claimDot, claimLabel),
            ),
            identityBody);
        const privacyCard = el("section", { class: "wc-card management-card" }, el("h2", {}, "Management"), privacyBody);
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
            buildUpdatesCard(),
        );
        render.apply(null, renderArgs);

        const ctx = {
            heroEl, tileGrid, identityBody, privacyBody, configBody, rebootBanner,
            claimDot, claimLabel,
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
        const [identity, status, config, aggregators] = await Promise.all([
            getJSON("/api/identity"),
            getJSON("/api/status"),
            getJSON("/api/config"),
            getJSON("/api/aggregators"),
        ]);
        if (ctx !== dashboardCtx) return;
        if (handleAuthFailure(identity) || handleAuthFailure(status) || handleAuthFailure(config)) return;
        updateAggregatorTile(tileGrid.aggregators, aggregators);

        const cfgValues = normaliseSavedValues((config && config.payload && config.payload.values) || {});
        ctx.configValues = cfgValues;
        configState.savedValues = cfgValues;
        ctx.lastIdentity = identity && identity.payload ? identity.payload : null;

        renderIdentityCard(identityBody, identity);
        refreshClaimDot(ctx);
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
            const [identity, status, aggregators] = await Promise.all([
                getJSON("/api/identity"),
                getJSON("/api/status"),
                getJSON("/api/aggregators"),
            ]);
            if (ctx !== dashboardCtx) return;
            if (handleAuthFailure(identity) || handleAuthFailure(status)) return;

            // See the initial-render call site above.
            updateMsgRateFromStatus(status);
            updateHero(ctx.heroEl, status, ctx.configValues);
            updateTiles(ctx.tileGrid, status, ctx.configValues);
            updateAggregatorTile(ctx.tileGrid.aggregators, aggregators);
            updateAppTiles(ctx.tileGrid);
            updateRebootBanner(ctx.rebootBanner, !!(status && status.payload && status.payload.reboot_required));

            const idPayload = identity && identity.payload ? identity.payload : null;
            if (idPayload && identityChanged(ctx.lastIdentity, idPayload)) {
                renderIdentityCard(ctx.identityBody, identity);
                ctx.lastIdentity = idPayload;
            }
            // Fire-and-forget: a claim probe can be slow (network), so don't
            // let it hold pollInFlight and stall the live tiles. It self-
            // guards on ctx and the server-side cache bounds real probes.
            refreshClaimDot(ctx);
        } finally {
            pollInFlight = false;
        }
    }

    // CLAIM_DOT maps a claim-status verdict to [dotClass, label, tooltip]
    // for the Identity-card indicator. Unlisted / transient verdicts
    // (unreachable, rate_limited, unavailable) fall back to the neutral
    // "na" dot rather than an alarming red.
    const CLAIM_DOT = {
        claimed:             ["ok",   "Claimed",        "Claimed — linked to an airplanes.live account"],
        unclaimed:           ["info", "Registered",     "Registered with airplanes.live — not yet claimed by an account"],
        unregistered:        ["warn", "Not registered", "Not registered with airplanes.live yet"],
        no_identity:         ["warn", "Not registered", "No feeder identity yet"],
        secret_mismatch:     ["err",  "Action needed",  "Claim secret didn’t authenticate — re-register from this page"],
        server_unregistered: ["err",  "Action needed",  "airplanes.live has no record of this feeder’s secret — re-register"],
        secret_invalid:      ["err",  "Action needed",  "The local claim secret is malformed — re-register"],
        blocked:             ["err",  "Blocked",        "This feeder is blocked by an administrator"],
    };

    function applyClaimDot(ctx, cls, label, title) {
        if (!ctx.claimDot) return;
        ctx.claimDot.className = "wc-claim-dot wc-claim-dot--" + cls;
        ctx.claimDot.title = title;
        ctx.claimLabel.textContent = label;
    }

    // refreshClaimDot paints the Identity-card dot from the cached claim
    // verdict. Self-guards against a torn-down dashboard and never
    // redirects on auth failure — the main status poll owns that.
    async function refreshClaimDot(ctx) {
        if (!ctx || !ctx.claimDot) return;
        let r;
        try {
            r = await getJSON("/api/claim/status");
        } catch (_) {
            if (ctx === dashboardCtx) applyClaimDot(ctx, "na", "Status unknown", "Couldn’t reach the claim-status check");
            return;
        }
        if (ctx !== dashboardCtx) return;
        if (!r || !r.ok || !r.payload || !r.payload.result) {
            applyClaimDot(ctx, "na", "Status unknown", "Couldn’t load claim status");
            return;
        }
        const view = CLAIM_DOT[r.payload.result] || ["na", "Status unknown", "Couldn’t determine claim status right now"];
        applyClaimDot(ctx, view[0], view[1], view[2]);
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
            onclick: () => navigateDashboard(),
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

    // LOG_TS_RE matches the journalctl --output=short timestamp prefix
    // that the backend forwards. The day field is space-padded for
    // single digits (e.g. "May  9 14:23:45"). Lines that don't match
    // (boot separators, client-side status markers like "[stream
    // closed]") are rendered without a muted timestamp prefix.
    const LOG_TS_RE = /^([A-Z][a-z]{2} [ 0-9][0-9] [0-9]{2}:[0-9]{2}:[0-9]{2}) (.*)$/;

    // appendLogLine renders a single log line into the given <pre>, wrapping
    // the timestamp prefix (when present) in a muted span so the message
    // column is visually dominant. Each line ends in an explicit "\n" text
    // node so the <pre>'s native text flow handles wrapping and copy-paste.
    function appendLogLine(pre, line) {
        const m = LOG_TS_RE.exec(line);
        if (m) {
            pre.appendChild(el("span", { class: "log-ts" }, m[1]));
            pre.appendChild(document.createTextNode(" "));
            pre.appendChild(el("span", { class: "log-msg" }, m[2]));
        } else {
            pre.appendChild(el("span", { class: "log-msg" }, line));
        }
        pre.appendChild(document.createTextNode("\n"));
        pre.scrollTop = pre.scrollHeight;
    }

    // streamLog wires an SSE stream from /api/log/<slug> into a fresh
    // <pre>, with a "(no log entries yet)" placeholder that's cleared on
    // the first arriving line. Returns the <pre> for the caller to embed
    // in its own panel layout. The EventSource is installed as
    // activeStream so navigate() tears it down on the next view change.
    function streamLog(slug) {
        const pre = el("pre", { class: "log-output" });
        const placeholder = el("span", { class: "muted" }, "(no log entries yet)");
        pre.appendChild(placeholder);
        const clearPlaceholder = () => {
            if (placeholder.parentNode === pre) pre.removeChild(placeholder);
        };
        const es = new EventSource("/api/log/" + encodeURIComponent(slug));
        activeStream = es;
        es.onmessage = (ev) => {
            clearPlaceholder();
            appendLogLine(pre, ev.data);
        };
        es.onerror = () => {
            clearPlaceholder();
            appendLogLine(pre, "[stream closed]");
            try { es.close(); } catch (_) {}
            if (activeStream === es) activeStream = null;
        };
        return pre;
    }

    function logViewer(slug) {
        const unit = LOG_SLUG_TO_UNIT[slug] || slug;
        const pre = streamLog(slug);
        render(
            el("section", { class: "wc-card" },
                el("h2", {}, "journalctl -u " + unit),
                el("p", { class: "muted" }, "Streaming live; close this view (use the Dashboard button above) to disconnect."),
                pre,
            ),
        );
    }

    // claimActivityPanel renders the journal stream for
    // airplanes-claim.service alongside a "Claim status" card sourced from
    // GET /api/claim/status — the real account-claim verdict (registered
    // with the backend vs. claimed by a user account), not a guess from the
    // local secret file. That probe is a rate-limited network round-trip
    // cached server-side, so this panel does NOT poll it on a tight loop: it
    // reads once on open, re-checks on a slow jittered ~50s cadence only
    // while the verdict is "unclaimed" (the state where the operator is
    // waiting for a claim to land), and stops once a terminal verdict
    // arrives. A "Check now" button forces an immediate refresh.
    //
    // Lifecycle: navigate() clears statusTimer on leaving the panel. The
    // panel stores its current (re)scheduled handle in both myTimer and
    // statusTimer; every async resolver bails when statusTimer no longer
    // equals myTimer (panel torn down or replaced). pollInFlight guards
    // against an overlapping in-flight probe.
    function claimActivityPanel() {
        const statusCard = el("section", { class: "wc-card claim-status" },
            el("p", { class: "muted" }, "Loading claim status…"));

        let pollInFlight = false;
        // myTimer holds the panel's current (re)scheduled handle, mirrored
        // into the global statusTimer. The post-await `statusTimer !==
        // myTimer` guard lets a stale resolver bail after navigate() tears
        // the panel down (or after a reschedule supersedes it).
        let myTimer = null;
        // Slow re-check cadence while waiting for a claim to land; jittered
        // so feeders behind one IP don't align their probes.
        const RECHECK_MS = 50000;

        const replaceCard = (...nodes) => {
            while (statusCard.firstChild) statusCard.removeChild(statusCard.firstChild);
            for (const n of nodes) statusCard.appendChild(n);
        };

        const checkBtn = el("button", {
            class: "wc-btn-ghost",
            type: "button",
            onclick: () => fetchClaim(true),
        }, "Check now");

        const checkedAgo = (iso) => {
            if (!iso) return "";
            const t = new Date(iso);
            if (Number.isNaN(t.getTime())) return "";
            const secs = Math.max(0, Math.round((Date.now() - t.getTime()) / 1000));
            if (secs < 45) return "checked just now";
            if (secs < 5400) return "checked " + Math.round(secs / 60) + "m ago";
            return "checked " + Math.round(secs / 3600) + "h ago";
        };

        // Map a verdict to a heading + guidance. The claim-page link lives
        // on the dashboard Identity card ("Show claim secret"); this panel
        // describes the state and points back there rather than duplicating
        // a URL it doesn't carry.
        const verdictView = (r) => {
            switch (r.result) {
                case "claimed":
                    return { h: "Claimed ✓", muted: r.version ? "Linked to an airplanes.live account · v" + r.version : "Linked to an airplanes.live account." };
                case "unclaimed":
                    return { h: "Registered — not yet claimed", muted: "Use “Show claim secret” on the dashboard to claim this feeder to your account." };
                case "unregistered":
                case "no_identity":
                    return { h: "Not yet registered", muted: "Press Register on the dashboard; the feeder also retries on reboot." };
                case "secret_mismatch":
                case "server_unregistered":
                case "secret_invalid":
                    return { h: "Needs attention", muted: "This feeder’s claim secret didn’t authenticate. Re-register from the dashboard." };
                case "blocked":
                    return { h: "Blocked", muted: "This feeder is blocked by an administrator." };
                case "rate_limited":
                    return { h: "Status check rate-limited", muted: "airplanes.live is throttling checks; try again shortly." };
                case "unreachable":
                    return { h: "Couldn’t reach airplanes.live", muted: "The feeder couldn’t contact the server to check claim status." };
                default:
                    return { h: "Claim status unavailable", muted: "Could not determine claim status right now." };
            }
        };

        const renderVerdict = (r) => {
            const v = verdictView(r);
            const nodes = [el("h2", {}, v.h)];
            if (v.muted) nodes.push(el("p", { class: "muted" }, v.muted));
            const meta = [];
            const stamp = checkedAgo(r.checked_at);
            if (stamp) meta.push(stamp);
            if (r.stale) meta.push("couldn’t refresh — showing last known");
            if (meta.length) nodes.push(el("p", { class: "muted" }, meta.join(" · ")));
            nodes.push(el("div", { class: "wc-action-grid" }, checkBtn));
            replaceCard(...nodes);
        };

        // nextDelayMs maps a verdict to the auto-recheck delay; 0 = stop.
        // Pending states keep polling — fast for "unregistered" (a just-
        // clicked Register is about to land a secret locally), slow for
        // "unclaimed" (waiting on a website action); transient failures
        // retry; settled verdicts stop and rely on "Check now".
        const nextDelayMs = (r) => {
            switch (r.result) {
                case "unregistered": return 6000;
                case "unclaimed":    return RECHECK_MS;
                case "rate_limited": return Math.max(7000, (r.retry_after_seconds || 20) * 1000);
                case "unreachable":
                case "error":
                case "unavailable":  return 20000;
                default:             return 0;
            }
        };

        const scheduleNext = (r) => {
            if (statusTimer !== myTimer) return;   // panel torn down
            const delay = nextDelayMs(r);
            if (delay <= 0) return;                // settled — wait for "Check now"
            const handle = setTimeout(() => {
                // Bail if a newer fetch/schedule superseded us or the panel
                // was torn down — stops stale timers from multiplying.
                if (statusTimer !== handle) return;
                fetchClaim(false);
            }, delay + Math.floor(Math.random() * 5000));
            myTimer = handle;
            statusTimer = handle;
        };

        async function fetchClaim(force) {
            if (pollInFlight) return;
            // Cancel any pending auto-recheck — this fetch supersedes it.
            if (myTimer) clearTimeout(myTimer);
            pollInFlight = true;
            checkBtn.disabled = true;
            let r;
            try {
                r = await getJSON("/api/claim/status" + (force ? "?max_age=0" : ""));
            } finally {
                pollInFlight = false;
            }
            if (statusTimer !== myTimer) return;   // torn down mid-flight
            checkBtn.disabled = false;
            if (handleAuthFailure(r)) return;
            if (!r.ok || !r.payload || !r.payload.result) {
                replaceCard(
                    el("p", { class: "error", role: "alert" },
                        (r.payload && r.payload.error) || "Could not load claim status."),
                    el("div", { class: "wc-action-grid" }, checkBtn),
                );
                scheduleNext({ result: "unavailable" });   // retry the transient failure
                return;
            }
            renderVerdict(r.payload);
            scheduleNext(r.payload);
        }

        const logPre = streamLog("claim");
        render(
            el("div", {},
                statusCard,
                el("section", { class: "wc-card" },
                    el("h2", {}, "journalctl -u airplanes-claim.service"),
                    el("p", { class: "muted" }, "Streaming live; close this view (use the Dashboard button above) to disconnect."),
                    logPre,
                ),
            ),
        );

        // Establish the liveness token before the first fetch so the
        // post-await `statusTimer !== myTimer` guard holds for it too.
        myTimer = setTimeout(() => {}, 0);
        statusTimer = myTimer;
        fetchClaim(false);
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
    if (userMenu) {
        userMenu.addEventListener("toggle", () => {
            if (userMenu.open) openUserMenuHooks();
            else closeUserMenu();
        });
    }
    if (userMenuChangePw) userMenuChangePw.addEventListener("click", () => {
        closeUserMenu();
        navigate(changePasswordPanel, { title: "Change password", showBack: true });
    });
    if (userMenuLogout) userMenuLogout.addEventListener("click", async () => {
        closeUserMenu();
        await postJSON("/api/auth/logout", {});
        await boot();
    });
    if (userMenuReboot) userMenuReboot.addEventListener("click", () => {
        closeUserMenu();
        requestReboot(null, true);
    });
    if (userMenuPoweroff) userMenuPoweroff.addEventListener("click", () => {
        closeUserMenu();
        navigate(confirmPowerOffPanel, { title: "Power off", showBack: true });
    });
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
