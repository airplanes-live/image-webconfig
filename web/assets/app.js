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
    const userMenuAggregators = document.getElementById("wc-user-aggregators");
    const userMenuBackup = document.getElementById("wc-user-backup");
    const userMenuSSH = document.getElementById("wc-user-ssh");
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

    // Wrap a password <input> with a Show/Hide reveal toggle and return the
    // wrapping element (the input itself is unchanged, so callers keep their
    // reference for .value/.focus()). The toggle flips input.type between
    // password and text. aria-pressed is set as a string because el() drops
    // falsey attrs. type="button" so it never submits the enclosing form.
    // Named pwReveal (not pwField) to avoid colliding with the local pwField
    // identifiers in the SSH forms.
    function pwReveal(input) {
        input.classList.add("wc-pw-input");
        const toggle = el("button", {
            type: "button", class: "wc-pw-toggle",
            "aria-label": "Show password", "aria-pressed": "false",
        }, "Show");
        toggle.onclick = () => {
            const reveal = input.type === "password";
            input.type = reveal ? "text" : "password";
            toggle.textContent = reveal ? "Hide" : "Show";
            toggle.setAttribute("aria-pressed", reveal ? "true" : "false");
            toggle.setAttribute("aria-label", reveal ? "Hide password" : "Show password");
        };
        return el("div", { class: "wc-pw-field" }, input, toggle);
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

    // safeClaimHref gates what may be rendered as a clickable href. The
    // claim page follows feed.env's APL_FEED_WEBSITE_URL, so the value is
    // config-derived data: require a plain web scheme so a javascript: or
    // data: URL in feed.env can never become a live link. Anything else
    // degrades to non-clickable text.
    function safeClaimHref(url) {
        try {
            const u = new URL(url);
            if (u.protocol !== "https:" && u.protocol !== "http:") return null;
            return u.toString();
        } catch (_) { return null; }
    }

    // claimHrefWithFeederId builds the "Claim this feeder" href: the
    // safeClaimHref-validated claim page plus a feeder_id query parameter
    // the website uses to prefill its claim form. A feeder id that fails
    // isValidFeederId is left off — a bad prefill link would land the user
    // on an inexplicably empty form, while the bare claim page still works.
    function claimHrefWithFeederId(claimPage, feederId) {
        const safe = safeClaimHref(claimPage);
        if (!safe) return null;
        if (!isValidFeederId(feederId)) return safe;
        const u = new URL(safe);
        u.searchParams.set("feeder_id", String(feederId).trim());
        return u.toString();
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

    // gainIsAdaptive reports whether the (saved) configured gain leaves the
    // value up to readsb — auto/min/max, or empty (the default is auto). For a
    // pinned numeric gain the configured value IS the effective value, so we
    // don't surface a redundant "currently X dB". Explicit allowlist, not
    // "not a number", so a hand-edited junk value hides rather than shows.
    function gainIsAdaptive(v) {
        const s = (v == null ? "" : String(v)).trim();
        return s === "" || s === "auto" || s === "min" || s === "max";
    }

    // GAIN_STALE_S: hide the effective gain once readsb's stats.json is older
    // than this. readsb rewrites it every ~10s, so a larger gap means a wedged
    // or stopped decoder and a frozen reading we shouldn't trust.
    const GAIN_STALE_S = 90;

    // effectiveGainDb pulls readsb's live effective gain from /api/status's
    // readsb_stats, or null when absent, non-numeric, or too stale to trust.
    function effectiveGainDb(payload) {
        const rs = payload && payload.readsb_stats;
        if (!rs || typeof rs.gain_db !== "number") return null;
        if (typeof rs.age_sec === "number" && rs.age_sec >= GAIN_STALE_S) return null;
        return rs.gain_db;
    }

    // isValidReadsbSdrSerial mirrors valid_readsb_sdr_serial. Empty is
    // valid (single-SDR default: readsb opens the first stick, no --device).
    const sdrSerialRE = /^[0-9A-Za-z_-]{1,32}$/;
    function isValidReadsbSdrSerial(v) {
        const s = (v == null ? "" : String(v)).trim();
        if (s === "") return true;
        return sdrSerialRE.test(s);
    }

    // isValidDump978Serial mirrors valid_dump978_serial. Empty is valid
    // (treated as "no SDR serial selected").
    function isValidDump978Serial(v) {
        const s = (v == null ? "" : String(v)).trim();
        if (s === "") return true;
        return sdrSerialRE.test(s);
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

    // Third-party aggregator field validators — advisory client-side hints that
    // mirror apl-aggregator's bash twins (_valid_email / _valid_fr24_key /
    // _valid_feeder_id). The helper re-validates everything and is the authority.
    const aggEmailRE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    function isValidAggEmail(v) { return aggEmailRE.test(String(v == null ? "" : v).trim()); }
    const fr24KeyRE = /^[A-Za-z0-9]{6,40}$/;
    function isValidFr24Key(v) { return fr24KeyRE.test(String(v == null ? "" : v).trim()); }
    const feederIdRE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
    function isValidFeederId(v) { return feederIdRE.test(String(v == null ? "" : v).trim()); }
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
    // (MLAT_USER, DUMP978_SDR_SERIAL) hide their error on empty input;
    // empty-invalid fields (GAIN, DUMP978_GAIN) surface the error even on
    // empty.
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
                // The forwarder has no live 1090 input when the decoder
                // self-disabled for an absent pinned SDR — render idle rather
                // than the green "feeding" the active forwarder alone implies.
                const readsbDecision = payload.readsb_decision || null;
                if (readsbDecision && readsbDecision.state === "disabled") {
                    return { dot: "warn", meta: "idle — no 1090 SDR" };
                }
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
                // Decision-first: an "active" readsb may be sleeping in the
                // pinned-SDR-absent self-disable (state file says
                // disabled/no_hardware). Consult the published decision before
                // the active-state count shortcut, mirroring the mlat/978 tiles.
                const readsbDecision = payload.readsb_decision || null;
                if (readsbDecision && readsbDecision.state === "disabled") {
                    // Any disabled decision is non-green; no_hardware (a pinned
                    // 1090 SDR that isn't present) gets the specific message.
                    return readsbDecision.reason === "no_hardware"
                        ? { dot: "warn", meta: "1090 SDR not detected" }
                        : { dot: "warn", meta: "decoder disabled" };
                }
                if (state === "active") {
                    const n = feed && typeof feed.aircraft_count === "number" ? feed.aircraft_count : null;
                    // lastMsgRate is computed once per poll by the dashboard
                    // loop; reading the cached value avoids double-calling
                    // computeMsgRate, which would burn its own baseline.
                    let parts = [];
                    if (n !== null) parts.push(n + " aircraft");
                    if (lastMsgRate !== null) parts.push(lastMsgRate.toFixed(0) + " msg/s");
                    else if (n !== null) parts.push("rate pending");
                    // Effective gain, shown only under adaptive config
                    // (auto/min/max) — a pinned numeric gain is already the
                    // configured value, so it would be redundant here.
                    const gainDb = effectiveGainDb(payload);
                    if (gainDb !== null && gainIsAdaptive(configValues.GAIN)) {
                        parts.push(gainDb.toFixed(1) + " dB");
                    }
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
        // A self-disabled readsb (pinned 1090 SDR absent) is "active" but not
        // decoding, so it does not count as feeding either.
        const readsbDecision = payload.readsb_decision || null;
        const readsbDecoding = readsbState === "active"
            && !(readsbDecision && readsbDecision.state === "disabled");
        if (feedState !== "active" || !readsbDecoding) {
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
        const heroReadsbDecision = payload.readsb_decision || null;
        const heroReadsbDisabled = heroReadsbDecision && heroReadsbDecision.state === "disabled";
        if (heroReadsbDisabled) {
            // The decoder self-disabled (pinned 1090 SDR absent): not feeding,
            // regardless of the forwarder still being active.
            heroEl.titleEl.textContent = "Not feeding · 1090 SDR not detected";
        } else if (services["airplanes-feed.service"] === "active" && aircraft !== null) {
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

    // setTileStatus paints a tile's left status ribbon by toggling the
    // wc-tile--status-* class on the root. It replaces the per-tile status
    // dot: the ribbon colour carries the same ok/warn/err/na signal the dot
    // used to, and dropping the dot frees the right edge (chevron / link
    // icon no longer crowd against a coloured dot).
    const TILE_STATUSES = ["ok", "warn", "err", "na"];
    function setTileStatus(root, status) {
        const s = TILE_STATUSES.indexOf(status) === -1 ? "na" : status;
        for (const x of TILE_STATUSES) {
            root.classList.toggle("wc-tile--status-" + x, x === s);
        }
    }

    function buildTile(unit) {
        const slug = UNIT_TO_LOG_SLUG[unit];
        const iconPath = SERVICE_ICONS[unit];
        const iconNode = el("span", { class: "wc-tile__icon" }, iconPath ? svgIcon(iconPath) : null);

        const title = unit.replace(/\.service$/, "");
        const titleEl = el("span", { class: "wc-tile__title" }, title);
        const metaEl = el("span", { class: "wc-tile__meta" }, "—");

        const tag = slug ? "a" : "div";
        const attrs = { class: "wc-tile wc-tile--service wc-tile--status-na", "data-state": "unknown" };
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
        );
        return { root, titleEl, metaEl };
    }

    function updateTile(tile, unit, payload) {
        const services = payload.services || {};
        const state = services[unit] || "unknown";
        const c = classifyService(unit, state, payload);
        setTileStatus(tile.root, c.dot);
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
        const chev     = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "›");
        const root = el("button", {
            type: "button",
            class: "wc-tile wc-tile--hardware wc-tile--nav wc-tile--status-na",
            "data-state": "unknown",
            onclick: () => navigate(piHealthPanel, { title: "System metrics", showBack: true }),
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
        );
        return { root, titleEl, metaEl };
    }

    function updateHardwareTile(tile, payload) {
        const hh = (payload && payload.hardware_health) || null;
        if (!hh) {
            setTileStatus(tile.root, "na");
            tile.metaEl.textContent = "—";
            tile.root.title = "";
            tile.root.setAttribute("data-state", "unknown");
            return;
        }
        const sev = hh.severity || "na";
        const summary = hh.summary || "—";
        setTileStatus(tile.root, sev);
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
        // Signal first: the meta line truncates on the right, and a long
        // SSID would otherwise push the percentage out of view.
        return { dot, meta: pct + "% · " + displaySSID };
    }

    function buildWifiTile() {
        const iconNode = el("span", { class: "wc-tile__icon" }, svgIcon(WIFI_ICON));
        const titleEl  = el("span", { class: "wc-tile__title" }, "Wi-Fi");
        const metaEl   = el("span", { class: "wc-tile__meta" }, "—");
        const chev     = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "›");
        const root = el("button", {
            type: "button",
            class: "wc-tile wc-tile--wifi wc-tile--nav wc-tile--status-na",
            "data-state": "unknown",
            onclick: () => navigate(wifiPanel, { title: "Wi-Fi networks", showBack: true }),
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
        );
        return { root, titleEl, metaEl };
    }

    function updateWifiTile(tile, payload) {
        const c = classifyWifi(payload && payload.wifi);
        setTileStatus(tile.root, c.dot);
        tile.metaEl.textContent = c.meta;
        tile.root.title = c.meta || "";
        tile.root.setAttribute("data-state", c.dot);
    }

    function buildTileGrid() {
        const tiles = {};
        const appTiles = {};

        // Row 1: hardware + Wi-Fi.
        const hardware = buildHardwareTile();
        const wifi = buildWifiTile();
        const row1 = el("div", { class: "wc-grid--tiles" }, hardware.root, wifi.root);

        // Per-aggregator tiles, populated from /api/aggregators. Hidden
        // until at least one configured/active adapter exists.
        const aggTiles = el("div", { class: "wc-grid--tiles wc-agg-tiles", hidden: true });

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

        const root = el("div", { class: "wc-tiles-stack" }, row1, row2, row3, apps, aggTiles);
        return { root, tiles, appTiles, hardware, wifi, aggTiles };
    }

    function buildAppTile(app) {
        const iconPath = APP_ICONS[app.id];
        const iconNode = el("span", { class: "wc-tile__icon" }, iconPath ? svgIcon(iconPath) : null);

        const titleEl = el("span", { class: "wc-tile__title" }, app.label);
        const metaEl = el("span", { class: "wc-tile__meta" }, app.meta || "—");
        const chev = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "↗");

        const root = el("a", {
            class: "wc-tile wc-tile--app wc-tile--status-na",
            "data-state": "unknown",
            href: app.href,
            target: "_blank",
            rel: "noopener noreferrer",
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
        );
        return { root, titleEl, metaEl, href: app.href };
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
            setTileStatus(tile.root, ok ? "ok" : "err");
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

    // updateEffectiveGain refreshes the "Currently X dB" line under the Gain
    // config field. The config card is rendered once and persists across status
    // polls, so this is driven from the poll rather than a config re-render.
    // Scoped to ctx.configBody (not a global query). Shown only when readsb is
    // active, the saved gain is adaptive, and a fresh reading exists.
    function updateEffectiveGain(ctx, status) {
        const host = ctx && ctx.configBody;
        if (!host) return;
        const line = host.querySelector('[data-role="gain-effective"]');
        if (!line) return;
        const payload = (status && status.payload) || {};
        const readsbActive = (payload.services || {})["readsb.service"] === "active";
        const gainDb = effectiveGainDb(payload);
        if (readsbActive && gainIsAdaptive(configState.savedValues.GAIN) && gainDb !== null) {
            line.textContent = "Currently " + gainDb.toFixed(1) + " dB.";
            line.hidden = false;
        } else {
            line.textContent = "";
            line.hidden = true;
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
            // Before first-run assigns a UUID, a freshly-flashed
            // replacement device recovers its identity from a saved backup
            // via the combined Backup & restore page.
            parent.appendChild(el("div", { class: "wc-action-grid" },
                el("button", {
                    class: "wc-btn-ghost",
                    type: "button",
                    onclick: () => navigate(backupPanel, { title: "Backup & restore", showBack: true }),
                }, "Backup & restore"),
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
            // Row 1: Register now + Claim activity. Row 2: a recovery path
            // to the combined Backup & restore page (col 2 stays empty so
            // column alignment matches the secret-present view).
            parent.appendChild(el("div", { class: "wc-action-grid" },
                registerBtn, claimLog,
                el("button", {
                    class: "wc-btn-ghost",
                    type: "button",
                    onclick: () => navigate(backupPanel, { title: "Backup & restore", showBack: true }),
                }, "Backup & restore"),
                el("span", { class: "wc-action-grid__spacer", "aria-hidden": "true" }),
            ));
            parent.appendChild(registerErr);
            return;
        }

        // doReveal fetches the full secret and renders the revealed view via
        // showRevealed (optionally with a status note, e.g. after a
        // rotation). Factored out so "Rotate secret" can re-render with the
        // freshly-rotated secret in place. doReveal/showRevealed/rotateSecret
        // are function declarations so they hoist above reveal's onclick.
        const reveal = el("button", {
            class: "wc-btn-ghost",
            type: "button",
            onclick: async () => {
                reveal.disabled = true;
                await doReveal();
                reveal.disabled = false;
            },
        }, "Show claim secret");

        async function doReveal(note) {
            const r = await postJSON("/api/identity/secret", {});
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
            showRevealed(r.payload, note);
        }

        function showRevealed(payload, note) {
            const safe = claimHrefWithFeederId(payload.claim_page, payload.feeder_id);
            const linkOrText = safe
                ? el("a", { href: safe, target: "_blank", rel: "noopener noreferrer" }, "Claim this feeder")
                : el("span", { class: "muted" }, payload.claim_page || "");
            const rotateMsg = el("p", { class: "muted", role: "status" }, note || "");
            const rotateBtn = el("button", {
                class: "wc-btn-ghost",
                type: "button",
                onclick: () => rotateSecret(rotateBtn, rotateMsg),
            }, "Rotate secret");
            parent.replaceChildren(
                el("p", { class: "wc-copy-row" },
                    el("strong", {}, "Feeder ID: "),
                    el("span", { class: "wc-copy-val" }, payload.feeder_id),
                    copyButton(payload.feeder_id, "feeder ID")),
                el("p", { class: "wc-copy-row" },
                    el("strong", {}, "Claim secret: "),
                    el("code", { class: "wc-copy-val" }, payload.claim_secret),
                    copyButton(payload.claim_secret, "claim secret")),
                el("p", {}, linkOrText),
                el("div", { class: "wc-action-grid" }, claimLog, rotateBtn),
                rotateMsg,
            );
        }

        // rotateSecret drives POST /api/claim/rotate. Confirm-only — the
        // reveal already exposed the secret to this session. On success it
        // re-reveals the new secret and repaints the claim dot. "pending"
        // (the feed helper kept an interrupted rotation, which a retry
        // resumes) and "unknown" (the call couldn't be confirmed) both leave
        // the button enabled so the user can try again.
        async function rotateSecret(btn, msgEl) {
            if (!confirm("Rotate the claim secret? This replaces it with a new one. "
                + "The old secret stops working immediately, including any backup that holds it.")) {
                return;
            }
            btn.disabled = true;
            msgEl.className = "muted";
            msgEl.textContent = "Rotating…";
            const r = await postJSON("/api/claim/rotate", {});
            if (handleAuthFailure(r)) return;
            const status = r.payload && r.payload.status;
            if (r.ok && status === "rotated") {
                refreshClaimDot(dashboardCtx);
                await doReveal("Claim secret rotated.");
                return;
            }
            btn.disabled = false;
            msgEl.className = "error";
            if (status === "pending") {
                msgEl.textContent = (r.payload && r.payload.message)
                    || "Rotation didn’t finish — try again to resume it.";
            } else if (status === "unknown") {
                msgEl.textContent = (r.payload && r.payload.message)
                    || "Couldn’t confirm the rotation — check the claim status, then try again.";
            } else {
                msgEl.textContent = (r.payload && (r.payload.message || r.payload.error)) || "Rotation failed.";
            }
        }

        // Show claim secret + Claim activity. Identity backup/restore now
        // lives in the combined Backup & restore page; "Rotate secret"
        // appears in the revealed view above.
        parent.appendChild(el("div", { class: "wc-action-grid" },
            reveal, claimLog,
        ));
    }

    // feederUUIDRE matches a canonical feeder ID: 8-4-4-4-12 lowercase hex
    // (mirrors isCanonicalUUID in internal/server/handlers.go). Used by the
    // MLAT anonymous-username placeholder to decide whether a real feeder ID
    // is available to derive the fallback name from.
    const feederUUIDRE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

    function normalizedFeederUUID(v) {
        return String(v == null ? "" : v).trim().toLowerCase();
    }
    function isCanonicalFeederUUID(v) {
        return feederUUIDRE.test(normalizedFeederUUID(v));
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
        // Success confirmation. The Save button is dirty-state (it vanishes
        // the moment a save lands), so without this a clean save is silent —
        // indistinguishable from nothing having happened. role="status" makes
        // it a polite live region; it stays until the group is edited again.
        const saved = el("p", { class: "config-fieldset__saved", role: "status" });
        saved.hidden = true;
        footerEl.appendChild(btn);
        footerEl.appendChild(err);
        footerEl.appendChild(pending);
        footerEl.appendChild(saved);

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
                saved.hidden = true;
                saved.textContent = "";
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
            saved.hidden = true;
            saved.textContent = "";
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
            const refreshOk = refreshed.ok;
            if (refreshOk) {
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
            // Only claim success when the authoritative refresh also landed:
            // a failed refresh leaves savedValues stale, so the group can
            // still read dirty — "Saved." next to a re-shown Save button
            // would contradict itself.
            if (pendingRestart.length === 0 && refreshOk) {
                saved.hidden = false;
                saved.textContent = "Saved.";
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
        // Live "Currently X dB" line — the effective gain readsb settled on.
        // Refreshed by updateEffectiveGain() from the status poll (the config
        // card persists across polls); shown only under adaptive gain.
        const gainEffective = el("p", {
            class: "field-help wc-gain-effective",
            "data-role": "gain-effective",
            hidden: true,
        });
        gainG.body.appendChild(gainEffective);
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
                // Reflect the new gain mode immediately (e.g. switching to a
                // pinned number must hide "Currently X dB" without waiting for
                // the next poll, which is paused while the card has focus).
                if (dashboardCtx) updateEffectiveGain(dashboardCtx, dashboardCtx.lastStatus);
            },
        });

        // ===== 1090 SDR serial =====
        // Pins readsb to a specific RTL-SDR by EEPROM serial so dual-SDR
        // feeders survive USB re-enumeration. Detected devices come from
        // GET /api/sdr (best-effort: enumeration failure degrades to the
        // custom free-text path, never blocks saving).
        const sdrG = buildGroup("1090 SDR");
        const SDR_CUSTOM = "__custom__";
        const sdr1090Id = fieldId("READSB_SDR_SERIAL");
        const sdr1090Select = el("select", { id: sdr1090Id, name: "READSB_SDR_SERIAL" });
        const sdr1090Custom = el("input", {
            type: "text", placeholder: "serial",
            "aria-label": "Custom 1090 SDR serial",
        });
        const sdr1090CustomField = el("div", { class: "field" }, sdr1090Custom);
        // sdrDevices is shared with the 978 serial datalist below.
        let sdrDevices = [];

        sdrG.body.appendChild(el("p", { class: "help" },
            "With two SDRs (1090 + 978), USB enumeration order can change ",
            "across reboots and readsb may open the wrong stick. Pinning by ",
            "serial makes the assignment deterministic. Leave on automatic ",
            "for a single SDR.",
        ));

        const sdr1090Error = el("p", {
            class: "field-help wc-field-error", hidden: true, role: "alert",
        }, "Letters, digits, underscore, hyphen — up to 32 characters.");
        const sdr1090Warn = el("p", {
            class: "field-help wc-field-warn", hidden: true,
        });

        // The currently-effective serial: the select's value, or the
        // custom text input when "Custom serial…" is chosen.
        const sdr1090Value = () =>
            sdr1090Select.value === SDR_CUSTOM ? sdr1090Custom.value : sdr1090Select.value;

        // rebuildSdr1090Options repopulates the select from the saved value
        // plus the detected-device list: Automatic (empty), each detected
        // serial, the saved value as "(not detected)" when absent, and the
        // custom escape hatch. Detected serials that fail the charset rule
        // are listed disabled — offering them would build an unsaveable form.
        function rebuildSdr1090Options() {
            const saved = (configState.savedValues.READSB_SDR_SERIAL || "").trim();
            // Preserve the user's in-progress choice across a rebuild (the
            // /api/sdr fetch can resolve mid-edit): keep the OPTION value —
            // custom mode survives as custom mode, the typed text stays in
            // the untouched custom input.
            const keep = sdr1090Select.value;
            sdr1090Select.textContent = "";
            sdr1090Select.appendChild(el("option", { value: "" }, "Automatic (single SDR)"));
            const seen = new Set();
            for (const d of sdrDevices) {
                if (seen.has(d.serial)) continue;
                seen.add(d.serial);
                let label = d.serial;
                if (d.product) label += " — " + d.product;
                if (d.duplicate) label += " (duplicate serial)";
                const opts = { value: d.serial };
                // Raw charset test, not the (trimming) validator: a
                // whitespace-padded EEPROM serial must be offered disabled
                // verbatim, never silently trimmed into a value that no
                // longer matches the device.
                if (!sdrSerialRE.test(d.serial)) {
                    opts.disabled = true;
                    label += " — unsupported serial, reflash with rtl_eeprom";
                }
                sdr1090Select.appendChild(el("option", opts, label));
            }
            if (saved !== "" && !seen.has(saved)) {
                sdr1090Select.appendChild(el("option", { value: saved }, saved + " (not detected)"));
            }
            sdr1090Select.appendChild(el("option", { value: SDR_CUSTOM }, "Custom serial…"));
            // Restore the previous selection if it still exists; fall back
            // to the saved value (always present via the branches above).
            const values = Array.from(sdr1090Select.options).map((o) => o.value);
            sdr1090Select.value = values.includes(keep) ? keep : saved;
            refreshSdr1090Editor();
        }

        // refreshSdr1090Editor recomputes custom-input visibility, the
        // error line, and the advisory warning line.
        function refreshSdr1090Editor() {
            sdr1090CustomField.hidden = sdr1090Select.value !== SDR_CUSTOM;
            const v = (sdr1090Value() || "").trim();
            const invalid = v !== "" && !isValidReadsbSdrSerial(v);
            sdr1090Error.hidden = !invalid;
            sdr1090Warn.hidden = true;
            if (invalid) return;
            const warnings = [];
            if (sdrDevices.some((d) => d.duplicate)) {
                warnings.push("Two or more sticks share a serial — pinning by a duplicated serial is unreliable. Assign unique serials with rtl_eeprom.");
            }
            if (v === "" && sdrDevices.length >= 2) {
                warnings.push("Multiple SDRs detected but 1090 is not pinned — after a reboot readsb may grab the wrong stick.");
            }
            const uat978 = (configState.savedValues.UAT_INPUT || "") !== ""
                ? (configState.savedValues.DUMP978_SDR_SERIAL || "978").trim() : "";
            if (v !== "" && uat978 !== "" && v === uat978) {
                warnings.push("This is also the 978 SDR serial — both decoders would fight over one stick.");
            }
            // readsb's device search treats a fully-numeric string below the
            // device count as an index, not a serial (strtol base 0, so
            // "00000001" parses as 1). Warn — pinning by such a serial does
            // not do what it looks like.
            const n = Number(v);
            if (v !== "" && Number.isInteger(n) && n >= 0 && n < sdrDevices.length) {
                warnings.push("readsb interprets \"" + v + "\" as a device index, not a serial. Use a longer serial (rtl_eeprom) for a reliable pin.");
            }
            if (warnings.length > 0) {
                sdr1090Warn.textContent = warnings.join(" ");
                sdr1090Warn.hidden = false;
            }
        }
        sdr1090Select.addEventListener("change", refreshSdr1090Editor);
        sdr1090Custom.addEventListener("input", refreshSdr1090Editor);

        const sdr1090Editor = el("div", {},
            sdr1090Select,
            sdr1090CustomField,
            sdr1090Error,
            sdr1090Warn,
        );
        const sdr1090Edit = buildEditableField({
            summarise: () => (configState.savedValues.READSB_SDR_SERIAL || "").trim() || "automatic",
            inputsWrapper: sdr1090Editor,
            resetInputs: () => {
                sdr1090Custom.value = "";
                rebuildSdr1090Options();
            },
            focusInput: sdr1090Select,
            footerEl: sdrG.footer,
            afterCancel: () => { sdrGroup.recheck(); },
        });
        sdrG.body.appendChild(el("div", { class: "field" },
            el("label", { for: sdr1090Id }, "1090 SDR serial"),
            sdr1090Edit.summaryEl,
            sdr1090Editor,
        ));
        const sdrGroup = mountGroup({
            name: "sdr1090",
            formEl: sdrG.form,
            footerEl: sdrG.footer,
            keys: ["READSB_SDR_SERIAL"],
            readInputs: () => ({ READSB_SDR_SERIAL: sdr1090Value() }),
            isValid: () => isValidReadsbSdrSerial(sdr1090Value()),
            payload: () => ({ READSB_SDR_SERIAL: sdr1090Value().trim() }),
            onSavedHook: () => {
                sdr1090Custom.value = "";
                rebuildSdr1090Options();
                sdr1090Edit.refreshSummary();
                sdr1090Edit.setEditing(false);
                sdrGroup.recheck();
            },
        });
        rebuildSdr1090Options();

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

        // Detected-serial suggestions for the 978 field — same /api/sdr
        // data as the 1090 picker; the field stays free-text.
        const sdr978ListId = fieldId("DUMP978_SDR_SERIAL_LIST");
        const sdr978Datalist = el("datalist", { id: sdr978ListId });
        sdrSerialInput.setAttribute("list", sdr978ListId);

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
                sdr978Datalist,
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
                // The 1090 group's same-stick warning compares against the
                // 978 serial just saved — recompute it so an open 1090
                // editor doesn't show a stale verdict.
                refreshSdr1090Editor();
            },
        });

        // Assemble
        parent.appendChild(mlatG.fieldset);
        parent.appendChild(gainG.fieldset);
        parent.appendChild(sdrG.fieldset);
        parent.appendChild(uatG.fieldset);

        // Best-effort SDR enumeration: populate the 1090 picker and the
        // 978 datalist once the device list arrives. Any failure leaves
        // the base options (automatic / saved / custom) in place — the
        // form never depends on this endpoint.
        getJSON("/api/sdr").then((r) => {
            if (!r.ok || !Array.isArray(r.payload.devices)) return;
            sdrDevices = r.payload.devices.filter(
                (d) => d && typeof d.serial === "string" && d.serial !== "");
            rebuildSdr1090Options();
            sdr978Datalist.textContent = "";
            const seen = new Set();
            for (const d of sdrDevices) {
                if (seen.has(d.serial)) continue;
                seen.add(d.serial);
                // Don't suggest serials the validator would reject — a
                // selectable suggestion must never make the field unsaveable.
                if (!sdrSerialRE.test(d.serial)) continue;
                sdr978Datalist.appendChild(el("option", { value: d.serial }));
            }
        });
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

    // ORCHESTRATOR_STALE_GRACE_POLLS bounds how many terminal-state
    // polls the SPA tolerates before accepting the terminal state as
    // authoritative even though no non-terminal state has been seen.
    // 5 polls * 2 s = 10 s. The orchestrator writes its first state
    // file within a few hundred milliseconds of starting (it's a
    // single tmp+rename right after parsing argv); a terminal state
    // that survives this grace window most likely means the orchestrator
    // started and failed before this poller saw a non-terminal write —
    // or never started at all. Either is a real outcome the user must
    // see, not a perpetual "Starting…". Two accepted consequences: a run
    // that fails within the first poll interval is held at "Starting…"
    // for up to this window (indistinguishable from stale state), and an
    // orchestrator that dies before its very first state write (e.g.
    // flock contention aborts it pre-init) leaves the previous run's
    // terminal state to be rendered after the window — the user sees the
    // prior outcome and can retry.
    const ORCHESTRATOR_STALE_GRACE_POLLS = 5;

    // Step values that on their own mean the orchestrator is no longer
    // running. "idle" appears before the first run on a post-boot device
    // (the state file lives on tmpfs); "done" is the success marker the
    // orchestrator writes; "unavailable" appears if the capability gate
    // flips off mid-run (image-side teardown during an active
    // orchestrator — pathological, but treat it as a terminal stop so
    // the poller doesn't spin). Failure is status-coded, not step-coded:
    // the orchestrator writes status "failed" with `step` keeping the
    // name of the step that failed, so the poller's terminal check is
    // this set OR status === "failed". ("failed" stays in the set for
    // symmetry with the server's step constants; the orchestrator never
    // writes it as a step value.)
    const ORCHESTRATOR_TERMINAL_STEP_VALUES = new Set(["done", "failed", "idle", "unavailable"]);

    // ORCHESTRATOR_GATEWAY_ERROR_MAX_POLLS bounds how many consecutive
    // 502/503/504 polls are absorbed silently. The orchestrator restarts
    // webconfig itself mid-run, so lighttpd answers for the backend for a
    // stretch; sessions persist across that restart, so the right move is
    // to keep the last rendered state and retry. 90 polls * 2 s = 3 min —
    // far past any normal restart — after which a warning surfaces (the
    // poll keeps going; a recovering backend clears it).
    const ORCHESTRATOR_GATEWAY_ERROR_MAX_POLLS = 90;

    // orchestratorProgress renders the polling progress view after the
    // user clicks "Update System". It polls /api/orchestrator/state at
    // ORCHESTRATOR_POLL_INTERVAL_MS and stops once a terminal state is
    // reported, ignoring any terminal state that pre-dates the current
    // click (left over from a prior run — the state file lives on
    // /run/ which survives the orchestrator's process exit). The card
    // shows step + status + (when present) the error string the
    // orchestrator wrote, plus an apt_irreversible notice when a failed
    // run left the apt phase's package changes in place.
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
        // a non-terminal state. Until then, a terminal state is treated
        // as leftover state from a prior run — the new orchestrator
        // hasn't reached its first state-file write yet — so we keep
        // polling rather than declare "done" on a stale marker.
        // staleTerminalPolls bounds how long we'll keep "starting"
        // before accepting a terminal state (see
        // ORCHESTRATOR_STALE_GRACE_POLLS rationale above).
        let sawNonTerminal = false;
        let staleTerminalPolls = 0;
        let gatewayErrorPolls = 0;
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
            // lighttpd answers 502/503/504 for the backend while the
            // update restarts webconfig itself. Sessions survive that
            // restart, so treat the gateway-error stretch like a network
            // blip: keep the last rendered state and retry. Bounded so a
            // backend that never comes back surfaces a warning instead
            // of polling silently forever.
            if (r.status === 502 || r.status === 503 || r.status === 504) {
                gatewayErrorPolls += 1;
                if (gatewayErrorPolls > ORCHESTRATOR_GATEWAY_ERROR_MAX_POLLS) {
                    errorEl.textContent = "Cannot reach the feeder — the update may still be running. This page keeps retrying.";
                    errorEl.hidden = false;
                }
                pollTimer = setTimeout(pollOnce, ORCHESTRATOR_POLL_INTERVAL_MS);
                return;
            }
            gatewayErrorPolls = 0;
            if (handleAuthFailure(r)) return;
            const p = r && r.payload;
            const step = (p && p.step) || "unknown";
            const status = (p && p.status) || "";
            const err = (p && p.error) || "";
            const aptIrr = !!(p && p.apt_irreversible);
            const isTerminal = ORCHESTRATOR_TERMINAL_STEP_VALUES.has(step)
                || status === "failed";

            if (!sawNonTerminal && isTerminal) {
                // Either the orchestrator hasn't written its first
                // state yet, or this is leftover state from a prior
                // run. Either way: keep polling, but only for a bounded
                // window — past ORCHESTRATOR_STALE_GRACE_POLLS, accept
                // the terminal state as authoritative so an orchestrator
                // that fails before this poller sees a non-terminal
                // write doesn't leave the user stuck on "Starting…".
                staleTerminalPolls += 1;
                if (staleTerminalPolls <= ORCHESTRATOR_STALE_GRACE_POLLS) {
                    stepEl.textContent = "Starting…";
                    statusEl.textContent = "";
                    errorEl.hidden = true;
                    aptNoteEl.hidden = true;
                    pollTimer = setTimeout(pollOnce, ORCHESTRATOR_POLL_INTERVAL_MS);
                    return;
                }
                // Fall through and render the terminal state.
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
            // The apt phase has no rollback, but that only matters when
            // the run actually failed: the user may assume "failed" means
            // the device reverted entirely, and the note corrects that.
            // On a successful run nothing rolled back, so the warning
            // would be noise. Failure is encoded as status === "failed";
            // the step field keeps the step name, so it covers both a
            // failure inside the apt step and a later step failing after
            // apt already mutated the system.
            if (aptIrr && status === "failed") {
                aptNoteEl.textContent = "Note: the apt package upgrade has run and is not rolled back automatically; webconfig and runtime steps can still roll back independently.";
                aptNoteEl.hidden = false;
            } else {
                aptNoteEl.hidden = true;
            }
            if (isTerminal) {
                // Terminal state reached for the live run. Stop polling
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
            const ssid = el("input", { id: "wifi-ssid", name: "ssid", type: "text", value: existing ? (existing.ssid || "") : "" });
            const psk = el("input", {
                id: "wifi-psk", name: "psk",
                type: "password", autocomplete: "new-password",
                placeholder: isEdit && existing.has_psk ? "(unchanged — leave blank to keep)" : "8-63 chars or 64-hex",
            });
            const pwField = pwReveal(psk);
            const hidden = el("input", { id: "wifi-hidden", name: "hidden", type: "checkbox" });
            if (existing && existing.hidden) hidden.checked = true;
            const priority = el("input", {
                id: "wifi-priority", name: "priority",
                type: "number", min: "0", max: "999",
                value: String(existing ? (existing.priority || 0) : 0),
            });
            const testBox = el("input", { id: "wifi-test-connection", name: "test_connection", type: "checkbox", checked: "" });
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
                el("div", { class: "field" }, el("label", { for: "wifi-ssid" }, "SSID"), ssid, ssidError),
                el("div", { class: "field" }, el("label", { for: "wifi-psk" }, "Password"), pwField, pskError),
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
            el("p", { class: "muted wc-metrics__note" },
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

    // The aggregator field validators (isValidAggEmail / isValidFr24Key /
    // isValidFeederId) live in the @validator-parity block above, parity-tested
    // against apl-aggregator's bash twins.

    // Adapters with a bespoke config panel, and the display name used where only
    // the id is in hand (the enable-progress view). Keep in sync with the .desc
    // files and the panel dispatcher below.
    const ADAPTER_NAMES = { fr24: "Flightradar24", piaware: "FlightAware" };
    function adapterManageable(id) { return id === "fr24" || id === "piaware"; }
    function adapterPanel(id) { return id === "piaware" ? piawarePanel() : fr24Panel(); }

    // AGG_STATE_BADGE maps an adapter `state` to [label, severity-suffix].
    const AGG_STATE_BADGE = {
        running:             ["Feeding",            "ok"],
        not_feeding:         ["Not feeding",        "err"],
        checking:            ["Checking…",          "warn"],
        installing:          ["Installing…",        "warn"],
        stopped:             ["Off",                "na"],
        not_installed:       ["Not set up",         "na"],
        configured_off:      ["Ready to enable",    "na"],
        failed:              ["Setup failed",       "err"],
        decoder_unavailable: ["Decoder not ready",  "warn"],
        network_unavailable: ["Network unavailable", "warn"],
        unavailable:         ["Unavailable",        "na"],
        unmanaged:           ["Unmanaged",          "na"],
    };
    function aggStateBadge(state) {
        const pair = AGG_STATE_BADGE[state] || [state || "—", "na"];
        return el("span", { class: "wc-agg-badge wc-agg-badge--" + pair[1] }, pair[0]);
    }

    // aggDisplayState collapses an adapter to the state its badge should show. An
    // externally-installed (unmanaged) adapter reads "unmanaged" — EXCEPT while one
    // of our own mutations is in flight or has just failed, which stays visible so a
    // conflicting managed copy can still be cleaned up.
    function aggDisplayState(a) {
        const s = a.state;
        if (s === "installing" || s === "removing" || s === "applying" || s === "failed") return s;
        if (a.external_install) return "unmanaged";
        // A restored (or otherwise saved-but-disabled) identity reports
        // not_installed with configured=true: credentials are on disk, the vendor
        // binary just isn't enabled yet. Distinguish it from a never-touched
        // adapter so a restore doesn't read as "Not set up" / a no-op.
        if (s === "not_installed" && a.configured) return "configured_off";
        // A running service is only truly "Feeding" if the vendor's own feed-health
        // probe confirms it. `state` is process lifecycle (systemd-active), which
        // stays "running" even when the upstream feed is rejected — so a not_feeding
        // verdict shows red and an unknown/unconfirmed one shows amber, never green.
        // Absent feed_health (adapter without a probe, or an older release) keeps
        // "running", so those tiles are unchanged.
        if (s === "running") {
            if (a.feed_health === "not_feeding") return "not_feeding";
            if (a.feed_health === "unknown") return "checking";
        }
        return s;
    }

    // buildAdapterTile renders one configured/active adapter as a nav tile that
    // opens its manage page. The state label + dot come from AGG_STATE_BADGE.
    function buildAdapterTile(a) {
        const ds = aggDisplayState(a);
        const pair = AGG_STATE_BADGE[ds] || [ds || "—", "na"];
        const iconNode = el("span", { class: "wc-tile__icon" }, svgIcon(AGGREGATOR_ICON));
        const titleEl  = el("span", { class: "wc-tile__title" }, a.display_name || a.id);
        const metaEl   = el("span", { class: "wc-tile__meta" }, a.reconcile_error ? pair[0] + " · Update failed" : pair[0]);
        const chev     = el("span", { class: "wc-tile__chev", "aria-hidden": "true" }, "›");
        const root = el("button", {
            type: "button",
            class: "wc-tile wc-tile--nav wc-tile--status-" + pair[1],
            "data-state": pair[1],
            title: pair[0],
            onclick: () => navigate(() => adapterPanel(a.id), { title: a.display_name || a.id, showBack: true }),
        },
            iconNode,
            el("span", { class: "wc-tile__body" }, titleEl, metaEl),
            chev,
        );
        return root;
    }

    // renderAggregatorTiles repopulates the dashboard's per-aggregator tile row
    // from /api/aggregators. Only configured/active adapters get a tile; an
    // ids+states signature short-circuits the rebuild so a steady poll doesn't
    // flicker the row.
    function renderAggregatorTiles(container, resp) {
        if (!container) return;
        const all = (resp && resp.ok && resp.payload && resp.payload.aggregators) || [];
        const adapters = all.filter(a =>
            a.configured || a.enabled || a.external_install ||
            a.state === "installing" || a.state === "removing" || a.state === "failed");
        // Signature covers everything a tile renders + membership flags, so a
        // change to any of them re-renders rather than going stale behind state.
        const sig = adapters.map(a =>
            [a.id, a.state || "", a.display_name || "", a.configured ? 1 : 0, a.enabled ? 1 : 0,
             a.external_install ? 1 : 0, a.feed_health || "",
             (a.reconcile_error && a.reconcile_error.error_code) || ""].join(":")
        ).join("|");
        if (container.__sig === sig) return;
        container.__sig = sig;
        container.replaceChildren(...adapters.map(buildAdapterTile));
        container.hidden = adapters.length === 0;
    }

    // CAVEAT preamble. Org-voice wording is the project owner's call — this is a
    // clear, accurate placeholder, not final copy. TODO: finalize wording.
    const AGG_CAVEAT_TEXT =
        "airplanes.live shares all aircraft data openly and never filters or restricts it. " +
        "Other networks may filter or hide aircraft and set their own terms — feeding them is " +
        "your choice.";

    // Intro shown above the caveat: what this section is for.
    const AGG_INTRO_TEXT =
        "Feed your receiver's data to other tracking networks in addition to airplanes.live. " +
        "Adding a network here does not change or reduce what you feed to airplanes.live.";

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
            el("p", { class: "wc-agg-intro" }, AGG_INTRO_TEXT),
            el("p", { class: "wc-agg-caveat" }, AGG_CAVEAT_TEXT),
            flashEl,
            list,
        ));
    }

    function buildAggregatorRow(a) {
        const manageable = adapterManageable(a.id);
        const ds = aggDisplayState(a);
        const pair = AGG_STATE_BADGE[ds] || [ds || "—", "na"];
        // A failed auto-update is sticky and easy to miss on the manage page, so
        // surface it on the row too; a pending (self-healing) drift is not noisy here.
        let status = a.version ? pair[0] + " · v" + a.version : pair[0];
        if (a.reconcile_error) status += " · Update failed";

        const icon = el("span", { class: "wc-agg-row__icon" }, svgIcon(AGGREGATOR_ICON));
        const body = el("div", { class: "wc-agg-row__body" },
            el("span", { class: "wc-agg-row__name" }, a.display_name || a.id),
            el("span", { class: "wc-agg-row__status" }, status),
        );
        const dot = el("span", { class: "wc-tile__dot wc-tile__dot--" + pair[1] });

        if (manageable) {
            const chev = el("span", { class: "wc-agg-row__chev", "aria-hidden": "true" }, "›");
            return el("button", {
                type: "button",
                class: "wc-agg-row wc-agg-row--clickable",
                onclick: () => navigate(() => adapterPanel(a.id), { title: a.display_name || a.id, showBack: true }),
            }, icon, body, dot, chev);
        }
        return el("div", { class: "wc-agg-row" }, icon, body, dot);
    }

    // ===== Combined device backup / restore =====

    // The five sections a combined backup carries, in display order. Restore
    // applies them in a slightly different order server-side, but the checklist
    // is keyed by section so arrival order doesn't matter.
    const BACKUP_SECTIONS = [
        { key: "settings", label: "Feeder settings" },
        { key: "identity", label: "Feeder identity" },
        { key: "aggregators", label: "Third-party aggregators" },
        { key: "wifi", label: "Saved Wi-Fi networks" },
        { key: "password", label: "Admin password" },
    ];

    // buildSectionCheckboxes renders a vertical checkbox group, one per entry in
    // `sections` (a subset of BACKUP_SECTIONS). opts.checked(key) sets the
    // initial state (default checked); opts.locked(key) forces a box checked +
    // disabled (e.g. the password section on first-run restore, where it is
    // mandatory); opts.onChange fires after any toggle. selected() returns the
    // checked keys in BACKUP_SECTIONS order.
    function buildSectionCheckboxes(sections, opts) {
        opts = opts || {};
        const boxes = {};
        const node = el("div", { class: "wc-backup-sections" });
        for (const s of sections) {
            const locked = opts.locked ? !!opts.locked(s.key) : false;
            const input = el("input", { type: "checkbox", name: "section-" + s.key });
            input.checked = locked || (opts.checked ? !!opts.checked(s.key) : true);
            input.disabled = locked;
            if (opts.onChange) input.onchange = opts.onChange;
            boxes[s.key] = input;
            node.appendChild(el("label", { class: "wc-checkbox" }, input, el("span", {}, s.label)));
        }
        const selected = () => sections.map((s) => s.key).filter((k) => boxes[k].checked);
        return { node, selected };
    }

    // restrictSections returns a shallow clone of a parsed backup envelope whose
    // `sections` map is narrowed to `keys` — what the user ticked. The server
    // restore loop already skips absent sections, so sending only the selected
    // ones is all that "restore just these" needs.
    function restrictSections(env, keys) {
        const want = new Set(keys);
        const sections = {};
        for (const k of Object.keys(env.sections || {})) {
            if (want.has(k)) sections[k] = env.sections[k];
        }
        return Object.assign({}, env, { sections: sections });
    }

    // buildRestoreChecklist renders one live status row per section. Pass the
    // subset actually being restored so deselected sections don't sit stuck on
    // "waiting…"; defaults to every section for callers that restore all.
    function buildRestoreChecklist(sections) {
        sections = sections || BACKUP_SECTIONS;
        const rows = {};
        const container = el("div", { class: "wc-agg-status", role: "status", "aria-live": "polite" });
        for (const s of sections) {
            const dot = el("span", { class: "wc-tile__dot wc-tile__dot--na" });
            const state = el("span", { class: "wc-agg-status__value" }, "waiting…");
            rows[s.key] = { dot, state };
            container.appendChild(el("div", { class: "wc-agg-status__row" },
                dot, el("span", { class: "wc-agg-status__label" }, s.label), state));
        }
        const setStatus = (key, status, reason) => {
            const r = rows[key];
            if (!r) return;
            let sev = "na", text = status;
            if (status === "applied") { sev = "ok"; text = "restored" + (reason ? " — " + reason : ""); }
            else if (status === "skipped") { sev = "na"; text = "skipped" + (reason ? " — " + reason : ""); }
            else if (status === "failed") { sev = "err"; text = "failed" + (reason ? " — " + reason : ""); }
            r.dot.className = "wc-tile__dot wc-tile__dot--" + sev;
            r.state.textContent = text;
        };
        return { node: container, setStatus };
    }

    // streamRestore POSTs a backup envelope and reads the NDJSON restore stream,
    // calling onEvent(ev) per event so the checklist updates live. A pre-stream
    // rejection (4xx) is a normal JSON error body, not NDJSON. Returns
    // {ok, status, summary, error}. Deliberately uses its own fetch (not
    // sendJSON, which buffers + parses one JSON value) and does NOT register an
    // AbortController: the server finishes the restore under its own background
    // context regardless, so navigating away must not interrupt a write in
    // flight.
    async function streamRestore(path, body, onEvent) {
        let resp;
        try {
            resp = await fetch(path, {
                method: "POST",
                credentials: "same-origin",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(body),
            });
        } catch (_) {
            return { ok: false, status: 0, error: "network error" };
        }
        if (!resp.ok) {
            let payload = null;
            try { payload = await resp.json(); } catch (_) {}
            return { ok: false, status: resp.status, error: (payload && payload.error) || "Restore failed." };
        }
        if (!resp.body || typeof resp.body.getReader !== "function") {
            return { ok: false, status: resp.status, error: "restore stream unavailable" };
        }
        let summary = null;
        const handleLine = (line) => {
            line = line.trim();
            if (!line) return;
            let ev;
            try { ev = JSON.parse(line); } catch (_) { return; }
            if (ev.type === "summary") summary = ev;
            onEvent(ev);
        };
        const reader = resp.body.getReader();
        const decoder = new TextDecoder();
        let buf = "";
        for (;;) {
            let chunk;
            try { chunk = await reader.read(); }
            catch (_) { return { ok: false, status: resp.status, error: "connection lost during restore" }; }
            if (chunk.done) break;
            buf += decoder.decode(chunk.value, { stream: true });
            let nl;
            while ((nl = buf.indexOf("\n")) >= 0) {
                handleLine(buf.slice(0, nl));
                buf = buf.slice(nl + 1);
            }
        }
        buf += decoder.decode(); // flush any trailing multi-byte sequence
        handleLine(buf);
        // A stream that ends without the terminal summary line was truncated
        // (dropped connection, killed server) — report it as a failure rather
        // than letting the caller treat a partial restore as complete.
        if (!summary) {
            return { ok: false, status: resp.status, error: "restore ended unexpectedly — it may be incomplete" };
        }
        return { ok: true, status: resp.status, summary };
    }

    // contentDispositionFilename extracts the quoted filename from a
    // Content-Disposition header. It only needs to understand what our own
    // server emits — `attachment; filename="…"` with sanitized ASCII — and
    // returns null for anything else.
    function contentDispositionFilename(header) {
        const m = /(?:^|;)\s*filename="([^"]*)"/i.exec(header || "");
        return (m && m[1]) || null;
    }

    // fetchBackupExport POSTs the export request and hands back the server's
    // exact bytes as a Blob plus the filename the server chose (from
    // Content-Disposition), so the saved file is byte-identical to what the
    // server produced. Deliberately bypasses sendJSON, which JSON-parses the
    // body — re-serializing client-side would make the file a reconstruction
    // rather than the server's own bytes. A failed export is a normal JSON
    // error body, parsed so callers can show the message.
    async function fetchBackupExport(sectionKeys) {
        const ctrl = new AbortController();
        activeAbort = ctrl;
        let resp;
        try {
            resp = await fetch("/api/backup/export", {
                method: "POST",
                credentials: "same-origin",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ sections: sectionKeys }),
                signal: ctrl.signal,
            });
            if (!resp.ok) {
                let payload = null;
                try { payload = await resp.json(); } catch (_) {}
                return { ok: false, status: resp.status, payload: payload || {} };
            }
            const blob = await resp.blob();
            return {
                ok: true, status: resp.status, blob,
                filename: contentDispositionFilename(resp.headers.get("Content-Disposition")),
            };
        } catch (e) {
            return {
                ok: false, status: 0, payload: { error: "network error" },
                aborted: e && e.name === "AbortError",
            };
        }
    }

    // parseBackupFile reads + validates an uploaded file client-side: size cap
    // (mirrors the server's 256 KiB combinedBackupBodyLimit), JSON parse, and
    // the combined-backup kind. Returns {ok, value, error}.
    async function parseBackupFile(file) {
        if (!file) return { ok: false, error: "" };
        if (file.size > 262144) return { ok: false, error: "That file is too large to be a valid backup." };
        let parsed;
        try { parsed = JSON.parse(await file.text()); }
        catch (_) { return { ok: false, error: "That file isn't a valid backup." }; }
        if (!parsed || parsed.kind !== "airplanes-combined-backup") {
            return { ok: false, error: "That file isn't an airplanes.live feeder backup." };
        }
        return { ok: true, value: parsed };
    }

    function backupPanel() {
        render(el("div", { class: "wc-stack" }, buildBackupExportCard(), buildBackupRestoreCard()));
    }

    function buildBackupExportCard() {
        const msg = el("div", {});
        const setMsg = (text, kind) => msg.replaceChildren(text ? el("div", { class: "wc-flash wc-flash--" + (kind || "ok") }, text) : el("span"));
        const sections = buildSectionCheckboxes(BACKUP_SECTIONS, { onChange: refresh });
        const pwNote = el("p", { class: "muted", hidden: true },
            "A backup without the admin password can't set up a freshly-flashed feeder — it can only be restored onto one that's already set up.");
        const exportBtn = el("button", { type: "button", class: "wc-btn-ghost" }, "Download backup");

        function refresh() {
            const sel = sections.selected();
            exportBtn.disabled = sel.length === 0;
            pwNote.hidden = sel.indexOf("password") !== -1;
        }

        exportBtn.onclick = async () => {
            const sel = sections.selected();
            if (sel.length === 0) return;
            setMsg("");
            exportBtn.disabled = true;
            const r = await fetchBackupExport(sel);
            exportBtn.disabled = false;
            if (handleAuthFailure(r)) return;
            if (!r.ok) { setMsg((r.payload && r.payload.error) || "Backup failed.", "warn"); return; }
            try {
                const url = URL.createObjectURL(r.blob);
                const a = el("a", { href: url, download: r.filename || "airplanes-feeder-backup.json" });
                document.body.appendChild(a); a.click(); a.remove();
                setTimeout(() => URL.revokeObjectURL(url), 1000);
                setMsg("Backup downloaded — keep it private.", "ok");
            } catch (_) { setMsg("Could not start the download.", "warn"); }
        };
        refresh();
        return el("section", { class: "wc-card" },
            el("h2", {}, "Back up this feeder"),
            el("p", { class: "muted" },
                "Download a file with the parts of this feeder you choose — identity, settings, third-party aggregator sign-ins, saved Wi-Fi networks (including passwords), and the admin password. Use it to restore the feeder after reflashing. The file holds every secret it contains — keep it private."),
            sections.node,
            pwNote,
            el("div", { class: "wc-agg-row__actions" }, exportBtn),
            msg,
        );
    }

    function buildBackupRestoreCard() {
        const msg = el("div", {});
        const setMsg = (text, kind) => msg.replaceChildren(text ? el("div", { class: "wc-flash wc-flash--" + (kind || "ok") }, text) : el("span"));
        const area = el("div", { class: "wc-restore-area" });
        const fileInput = el("input", { type: "file", accept: "application/json,.json", hidden: true });
        const importBtn = el("button", { type: "button", class: "wc-btn-ghost" }, "Restore from backup");
        importBtn.onclick = () => fileInput.click();
        fileInput.onchange = async () => {
            const file = fileInput.files && fileInput.files[0];
            fileInput.value = "";
            area.replaceChildren();
            setMsg("");
            const parsed = await parseBackupFile(file);
            if (!parsed.ok) { if (parsed.error) setMsg(parsed.error, "warn"); return; }
            const present = BACKUP_SECTIONS.filter((s) => parsed.value.sections && parsed.value.sections[s.key]);
            if (present.length === 0) { setMsg("That backup has nothing in it to restore.", "warn"); return; }
            showSelection(parsed.value, present);
        };

        // showSelection lets the user pick which of the sections present in the
        // file to apply. The admin password defaults OFF so a routine restore
        // doesn't replace the login or log the user out unless they ask for it.
        function showSelection(env, present) {
            const sections = buildSectionCheckboxes(present, {
                checked: (k) => k !== "password",
                onChange: refresh,
            });
            const restoreBtn = el("button", { type: "button", class: "wc-btn-primary" }, "Restore selected");
            function refresh() { restoreBtn.disabled = sections.selected().length === 0; }

            restoreBtn.onclick = async () => {
                const sel = sections.selected();
                if (sel.length === 0) return;
                const withPassword = sel.indexOf("password") !== -1;
                const prompt = withPassword
                    ? "Restore the selected items? This includes the admin password, so you'll be logged out and must log in with the password from the backup."
                    : "Restore the selected items onto this feeder?";
                if (!confirm(prompt)) return;

                const chosen = present.filter((s) => sel.indexOf(s.key) !== -1);
                const checklist = buildRestoreChecklist(chosen);
                area.replaceChildren(el("h3", {}, "Restoring…"), checklist.node);
                importBtn.disabled = true;
                let failed = 0;
                let res;
                try {
                    res = await streamRestore("/api/backup/restore", restrictSections(env, sel), (ev) => {
                        if (ev.type === "section") {
                            checklist.setStatus(ev.section, ev.status, ev.reason);
                            if (ev.status === "failed") failed++;
                        }
                    });
                } finally {
                    importBtn.disabled = false;
                }
                if (handleAuthFailure(res)) return;
                if (!res.ok) { setMsg(res.error || "Restore failed.", "warn"); return; }
                if (res.summary && res.summary.password_changed) {
                    navigate(() => loginPanel(failed > 0
                        ? "Password restored, but some items couldn't be restored — log in and re-check Backup & restore."
                        : "Password restored — log in with the password from your backup."), {});
                    return;
                }
                if (failed > 0) { setMsg("Restore finished, but some items couldn't be restored — see above.", "warn"); return; }
                setMsg("Restore complete.", "ok");
            };
            refresh();
            area.replaceChildren(
                el("p", { class: "muted" }, "Choose what to restore from this backup:"),
                sections.node,
                el("div", { class: "wc-agg-row__actions" }, restoreBtn),
            );
        }

        return el("section", { class: "wc-card" },
            el("h2", {}, "Restore from a backup"),
            el("p", { class: "muted" },
                "Upload a backup file, then choose what to restore. Saved Wi-Fi networks are added without disturbing the connection you're using now."),
            el("div", { class: "wc-agg-row__actions" }, importBtn, fileInput),
            area,
            msg,
        );
    }

    // setupRestorePanel is the first-run counterpart reached from the password
    // setup screen: it restores a backup onto a freshly-flashed feeder (no
    // password set yet) and auto-logs-in. The backup MUST carry a password
    // section — that's what completes setup.
    function setupRestorePanel() {
        const msg = el("div", {});
        const setMsg = (text, kind) => msg.replaceChildren(text ? el("div", { class: "wc-flash wc-flash--" + (kind || "ok") }, text) : el("span"));
        const area = el("div", { class: "wc-restore-area" });
        const fileInput = el("input", { type: "file", accept: "application/json,.json", hidden: true });
        const importBtn = el("button", { type: "button", class: "wc-btn-primary" }, "Choose backup file");
        importBtn.onclick = () => fileInput.click();
        fileInput.onchange = async () => {
            const file = fileInput.files && fileInput.files[0];
            fileInput.value = "";
            area.replaceChildren();
            setMsg("");
            const parsed = await parseBackupFile(file);
            if (!parsed.ok) { if (parsed.error) setMsg(parsed.error, "warn"); return; }
            if (!(parsed.value.sections && parsed.value.sections.password)) {
                setMsg("This backup has no saved admin password, so it can't complete setup. Use “Set a password instead”.", "warn");
                return;
            }
            const present = BACKUP_SECTIONS.filter((s) => parsed.value.sections[s.key]);
            showSelection(parsed.value, present);
        };

        // showSelection lets the user pick what to restore onto the fresh flash.
        // The admin password is mandatory here — it completes setup and signs
        // the user in — so its box is checked and locked.
        function showSelection(env, present) {
            const sections = buildSectionCheckboxes(present, {
                locked: (k) => k === "password",
                onChange: refresh,
            });
            const restoreBtn = el("button", { type: "button", class: "wc-btn-primary" }, "Restore selected");
            function refresh() { restoreBtn.disabled = sections.selected().length === 0; }

            restoreBtn.onclick = async () => {
                const sel = sections.selected();
                if (sel.length === 0) return;
                const chosen = present.filter((s) => sel.indexOf(s.key) !== -1);
                const checklist = buildRestoreChecklist(chosen);
                area.replaceChildren(el("h3", {}, "Restoring…"), checklist.node);
                importBtn.disabled = true;
                let failed = 0;
                let res;
                try {
                    res = await streamRestore("/api/backup/restore-setup", restrictSections(env, sel), (ev) => {
                        if (ev.type === "section") {
                            checklist.setStatus(ev.section, ev.status, ev.reason);
                            if (ev.status === "failed") failed++;
                        }
                    });
                } finally {
                    importBtn.disabled = false;
                }
                if (!res.ok) { setMsg(res.error || "Restore failed.", "warn"); return; }
                // The device is initialized and we're auto-logged-in; carry a warning
                // to the dashboard if any section failed, then boot() routes there.
                if (failed > 0) {
                    pendingFlash = { text: "Signed in. Some items couldn't be restored — check Backup & restore.", level: "warn" };
                }
                await boot();
            };
            refresh();
            area.replaceChildren(
                el("p", { class: "muted" }, "Choose what to restore. The admin password is always restored so you can sign in."),
                sections.node,
                el("div", { class: "wc-agg-row__actions" }, restoreBtn),
            );
        }

        render(el("section", { class: "wc-card" },
            el("h2", {}, "Restore from backup"),
            el("p", {},
                "Upload a backup from another airplanes.live feeder (or an earlier flash of this one), then choose what to restore. You'll be signed in automatically."),
            el("div", { class: "wc-agg-row__actions" }, importBtn, fileInput),
            area,
            msg,
            el("div", { class: "wc-setup-alt" },
                el("button", { type: "button", class: "wc-btn-ghost", onclick: () => navigate(setupPanel, { title: "First-time setup", showBack: false }) }, "Set a password instead")),
        ));
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

    // aggReconcileErrorMessage turns a recorded reconcile_error — a failed
    // background auto-update applied after a system update — into operator
    // guidance. Kept separate from aggEnableErrorMessage: the user didn't trigger
    // this, so the copy points at Update System / logs, not "try again".
    function aggReconcileErrorMessage(err) {
        const code = (err && err.error_code) || "";
        if (code === "acquire_failed") return "Automatic update failed — check the feeder's internet connection and run Update System again.";
        if (code === "state_error") return "Updated, but the service didn't stay running — view logs.";
        return "The last automatic update didn't finish — run Update System again, or view logs.";
    }

    async function fr24Panel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading…")));
        // Per-adapter detail fetch (not the list): it carries status_detail from
        // the vendor status tool, which is too slow to run on the dashboard poll.
        const [resp, config] = await Promise.all([
            getJSON("/api/aggregators/fr24"),
            getJSON("/api/config"),
        ]);
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
        // If a mutation (enable/reset) is already in flight, resume its progress.
        if (a.enable && a.enable.request_id && (a.state === "installing" || a.state === "removing" || a.state === "applying")) {
            return aggregatorMutationProgress("fr24", a.enable.request_id, a.enable.op || "enable");
        }
        const cfg = (config.payload && config.payload.values) || {};
        renderFr24Form(a, cfg);
    }

    // aggDetailHead: card title with the status chip right after the name
    // (left-aligned), e.g. "FlightAware (Feeding)".
    function aggDetailHead(name, state) {
        return el("h2", { class: "wc-card-head wc-agg-detail-head" },
            el("span", { class: "wc-agg-detail-head__name" }, name),
            aggStateBadge(state));
    }

    // aggFeedLine: the one-line summary under the manage-page heading. Health-
    // aware: "Feeding X." was previously shown for any enabled adapter, which
    // read as success while the vendor was actively rejecting the feed (e.g. a
    // bad FR24 sharing key). Only a confirmed feed_health=feeding says feeding;
    // a confirmed rejection names the likely fix; anything else stays neutral.
    function aggFeedLine(a, name, rejectedHint) {
        if (!a.enabled) return "Set up but not feeding right now.";
        if (a.feed_health === "feeding") return "Feeding " + name + ".";
        if (a.feed_health === "not_feeding") {
            return "Not feeding \u2014 " + name + " is rejecting the feed. " + rejectedHint;
        }
        return "Waiting for " + name + " to confirm the feed\u2026";
    }

    // buildAggStatusBlock: the "Status" card on a manage page — installed
    // version plus the per-adapter status lines the helper surfaces from the
    // vendor status command (piaware-status / fr24feed). Null when empty.
    function aggStatusRow(label, value, sev) {
        return el("div", { class: "wc-agg-status__row" },
            el("span", { class: "wc-tile__dot wc-tile__dot--" + (sev || "na") }),
            el("span", { class: "wc-agg-status__label" }, label),
            el("span", { class: "wc-agg-status__value" }, value));
    }
    const AGG_STATUS_SEV = { ok: 1, warn: 1, err: 1, na: 1 };
    function buildAggStatusBlock(a) {
        const rows = el("div", { class: "wc-agg-status" });
        if (a.version) rows.appendChild(aggStatusRow("Installed version", "v" + a.version, "na"));
        // Auto-update state: a recorded reconcile_error (a failed background
        // update) is sticky and takes precedence; plain version_drift is a pending
        // update that self-heals on the next reconcile.
        if (a.reconcile_error) {
            rows.appendChild(aggStatusRow("Update", "Failed — " + aggReconcileErrorMessage(a.reconcile_error), "err"));
        } else if (a.version_drift) {
            rows.appendChild(aggStatusRow("Update", a.desired_version ? "Pending — updating to v" + a.desired_version : "Pending", "warn"));
        }
        // status_detail is helper-provided JSON forwarded verbatim — guard the
        // shape (non-array, odd severity) rather than trust it.
        const detail = Array.isArray(a.status_detail) ? a.status_detail : [];
        for (const d of detail) {
            if (!d || !d.label) continue;
            const sev = AGG_STATUS_SEV[d.severity] ? d.severity : "na";
            rows.appendChild(aggStatusRow(String(d.label), d.value != null ? String(d.value) : "—", sev));
        }
        if (!rows.childNodes.length) return null;
        return el("section", { class: "wc-card wc-agg-status-card" }, el("h3", {}, "Status"), rows);
    }

    // renderAggExternalManaged: read-only view for an adapter installed OUTSIDE
    // airplanes.live (external_install). The vendor/admin tool owns it, so our
    // enable/disable/logs verbs can't act on it — show what we can read and present
    // the actions disabled rather than letting a click error. Remove stays enabled
    // only when WE also have a copy here (managed_install): the conflict case where
    // reset usefully clears our duplicate and leaves the external install alone.
    function renderAggExternalManaged(a, name) {
        const inlineErr = el("p", { class: "error", role: "alert" });
        const tip = name + " is managed by another tool on this device, not by airplanes.live.";
        // A failed managed mutation (e.g. a Remove of our conflicting copy) lands
        // here once it leaves the in-flight states — surface it instead of swallowing.
        if (a.enable && (a.enable.error_code || a.enable.status === "failed")) {
            inlineErr.textContent = aggEnableErrorMessage({ payload: a.enable });
        }

        const note = a.managed_install
            ? name + " is installed on this device by another tool. An airplanes.live-managed copy " +
              "was also set up but can't run alongside it. Use Remove to clear the airplanes.live copy " +
              "— the other install is left untouched."
            : name + " is already installed on this device by another tool, so airplanes.live can't " +
              "start, stop, or remove it here. To let airplanes.live manage " + name + " instead, remove " +
              "the existing install over SSH, then set it up here.";

        const start = el("button", { type: "button", class: "wc-btn-primary", disabled: "", title: tip }, "Start feeding");

        // Label the button by what it actually deletes: in the conflict case it
        // clears ONLY our managed copy (never the external install), so spell that
        // out rather than a bare "Remove" that reads like it removes their feeder.
        const remove = el("button", { type: "button", class: "wc-btn-danger" },
            a.managed_install ? "Remove airplanes.live copy" : "Remove");
        if (a.managed_install) {
            remove.onclick = async () => {
                if (!confirm("Remove the airplanes.live-managed " + name + " copy? This stops and deletes " +
                    "only the airplanes.live copy; the install added by the other tool is left as-is.")) return;
                remove.disabled = true;
                const r = await postJSON("/api/aggregators/" + a.id + "/reset", {});
                if (handleAuthFailure(r)) return;
                if (r.status === 202) {
                    navigate(() => aggregatorMutationProgress(a.id, r.payload && r.payload.request_id, "reset"),
                        { title: "Removing " + name, showBack: true });
                    return;
                }
                remove.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r);
            };
        } else {
            remove.disabled = true;
            remove.title = tip;
        }

        render(el("section", { class: "wc-card" },
            aggDetailHead(name, aggDisplayState(a)),
            el("p", { class: "muted" }, "Installed on this device by another tool."),
            buildAggStatusBlock(a),
            el("p", { class: "wc-agg-caveat" }, note),
            inlineErr,
            el("div", { class: "wc-agg-row__actions" }, start, remove),
        ));
    }

    function renderFr24Form(a, cfg) {
        if (a.external_install) return renderAggExternalManaged(a, "Flightradar24");
        cfg = cfg || {};
        const configured = !!(a.configured || a.enabled);
        const inlineErr = el("p", { class: "error", role: "alert" });

        const viewLogs = el("button", { type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(() => logViewer("fr24"), { title: "Logs · fr24", showBack: true }) }, "View logs");

        // Already set up: status card + a single action row, no inputs.
        if (configured) {
            const actions = el("div", { class: "wc-agg-row__actions" });
            if (a.enabled) {
                const stop = el("button", { type: "button", class: "wc-btn-ghost" }, "Stop feeding");
                stop.onclick = async () => {
                    stop.disabled = true;
                    const r = await postJSON("/api/aggregators/fr24/disable", {});
                    if (handleAuthFailure(r)) return;
                    if (!r.ok) { stop.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r); return; }
                    pendingFlash = { text: "Stopped feeding Flightradar24.", level: "ok" };
                    navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
                };
                actions.appendChild(stop);
            } else {
                const start = el("button", { type: "button", class: "wc-btn-primary" }, "Start feeding");
                start.onclick = async () => {
                    start.disabled = true;
                    const r = await postJSON("/api/aggregators/fr24/enable", { fields: {} });
                    if (handleAuthFailure(r)) return;
                    if (r.status === 202) {
                        navigate(() => aggregatorEnableProgress("fr24", r.payload && r.payload.request_id),
                            { title: "Starting Flightradar24", showBack: true });
                        return;
                    }
                    start.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r);
                };
                actions.appendChild(start);
            }
            const remove = el("button", { type: "button", class: "wc-btn-danger" }, "Remove");
            remove.onclick = async () => {
                if (!confirm("Remove Flightradar24? This stops feeding and deletes the installed feeder and its stored sharing key.")) return;
                remove.disabled = true;
                const r = await postJSON("/api/aggregators/fr24/reset", {});
                if (handleAuthFailure(r)) return;
                if (r.status === 202) {
                    navigate(() => aggregatorMutationProgress("fr24", r.payload && r.payload.request_id, "reset"),
                        { title: "Removing Flightradar24", showBack: true });
                    return;
                }
                remove.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r);
            };
            actions.appendChild(remove);
            actions.appendChild(viewLogs);

            render(el("section", { class: "wc-card" },
                aggDetailHead("Flightradar24", aggDisplayState(a)),
                el("p", { class: "muted" }, aggFeedLine(a, "Flightradar24", "Check your sharing key.")),
                buildAggStatusBlock(a),
                inlineErr,
                actions,
            ));
            return;
        }

        // Not set up: collect sign-in details + location; the actions row lives
        // inside the form so "Set up" and "View logs" stay on one line.
        const email = el("input", { id: "agg-fr24-email", name: "email", type: "email", autocomplete: "email", placeholder: "you@example.com" });
        const key = el("input", { id: "agg-fr24-key", name: "sharing_key", type: "text", autocomplete: "off", spellcheck: "false",
            placeholder: "optional — paste an existing sharing key" });
        const emailErr = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" }, "Enter a valid email address.");
        const keyErr = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" }, "Sharing keys are 6–40 letters and digits.");

        const lat = el("input", { id: "agg-fr24-lat", name: "lat", type: "text", inputmode: "decimal", autocomplete: "off", spellcheck: "false", value: cfg.LATITUDE || "" });
        const lon = el("input", { id: "agg-fr24-lon", name: "lon", type: "text", inputmode: "decimal", autocomplete: "off", spellcheck: "false", value: cfg.LONGITUDE || "" });
        const alt = el("input", { id: "agg-fr24-alt", name: "alt", type: "text", inputmode: "decimal", autocomplete: "off", spellcheck: "false", value: cfg.ALTITUDE || "" });

        const submitLabel = "Set up Flightradar24";
        const submit = el("button", { type: "submit", class: "wc-btn-primary" }, submitLabel);

        // Range-check the coordinates to match the helper's _valid_geo, so the
        // form never submits values the helper would reject (which would surface
        // a misleading "set your feeder location" error).
        const inRange = (v, lo, hi) => {
            const s = String(v).trim();
            const n = Number(s);
            return s !== "" && Number.isFinite(n) && n >= lo && n <= hi;
        };
        const validGeo = () => inRange(lat.value, -90, 90) && inRange(lon.value, -180, 180) && inRange(alt.value, -500, 12000);
        const recheck = () => {
            let bad = false;
            if (!email.value.trim() || !isValidAggEmail(email.value)) bad = true;
            if (key.value.trim() && !isValidFr24Key(key.value)) bad = true;
            if (!validGeo()) bad = true;
            submit.disabled = bad;
        };
        email.addEventListener("input", () => { refreshFieldError(email, emailErr, v => v.trim() !== "" && !isValidAggEmail(v)); recheck(); });
        key.addEventListener("input", () => { refreshFieldError(key, keyErr, v => v.trim() !== "" && !isValidFr24Key(v)); recheck(); });
        lat.addEventListener("input", recheck);
        lon.addEventListener("input", recheck);
        alt.addEventListener("input", recheck);

        const form = el("form", { class: "wc-agg-form", novalidate: true,
            onsubmit: async (e) => {
                e.preventDefault();
                inlineErr.textContent = "";
                const fields = {};
                const ev = email.value.trim(), kv = key.value.trim();
                if (ev) fields.email = ev;
                if (kv) fields.sharing_key = kv;
                if (!fields.email) { inlineErr.textContent = "An email address is required to set up Flightradar24."; return; }
                if (!isValidAggEmail(fields.email)) { inlineErr.textContent = "Enter a valid email address."; return; }
                if (fields.sharing_key && !isValidFr24Key(fields.sharing_key)) { inlineErr.textContent = "Sharing keys are 6–40 letters and digits."; return; }
                if (!validGeo()) { inlineErr.textContent = "Enter a valid latitude (−90 to 90), longitude (−180 to 180), and altitude (−500 to 12000 m)."; return; }
                submit.disabled = true; submit.textContent = "Submitting…";
                const r = await postJSON("/api/aggregators/fr24/enable", {
                    fields,
                    lat: Number(lat.value),
                    lon: Number(lon.value),
                    alt: Number(alt.value),
                });
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
            el("div", { class: "field" }, el("label", { for: "agg-fr24-email" }, "Email address"), email, emailErr),
            el("div", { class: "field" }, el("label", { for: "agg-fr24-key" }, "Sharing key (optional)"), key, keyErr,
                el("p", { class: "field-help" }, "Leave blank to create a new Flightradar24 sharing key from your email. Paste an existing key to reuse a Flightradar24 account.")),
            el("div", { class: "field" }, el("label", { for: "agg-fr24-lat" }, "Latitude"), lat),
            el("div", { class: "field" }, el("label", { for: "agg-fr24-lon" }, "Longitude"), lon),
            el("div", { class: "field" }, el("label", { for: "agg-fr24-alt" }, "Altitude (m)"), alt,
                el("p", { class: "field-help" }, "Sent to Flightradar24 at sign-up. Prefilled from your feeder location; edit to send a different location.")),
            inlineErr,
            el("div", { class: "wc-agg-row__actions" }, submit, viewLogs),
        );
        recheck();

        render(el("section", { class: "wc-card" },
            aggDetailHead("Flightradar24", aggDisplayState(a)),
            el("p", { class: "muted" }, "Send your receiver's data to Flightradar24."),
            buildAggStatusBlock(a),
            form,
        ));
    }

    async function piawarePanel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading…")));
        // Per-adapter detail fetch (carries vendor status_detail); see fr24Panel.
        const resp = await getJSON("/api/aggregators/piaware");
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
        if (a.enable && a.enable.request_id && (a.state === "installing" || a.state === "removing" || a.state === "applying")) {
            return aggregatorMutationProgress("piaware", a.enable.request_id, a.enable.op || "enable");
        }
        renderPiawareForm(a);
    }

    function renderPiawareForm(a) {
        if (a.external_install) return renderAggExternalManaged(a, "FlightAware");
        const configured = !!(a.configured || a.enabled);
        const inlineErr = el("p", { class: "error", role: "alert" });
        const mlatOn = a.configured_mlat_enabled != null ? !!a.configured_mlat_enabled : !!a.mlat_default;

        const viewLogs = el("button", { type: "button", class: "wc-btn-ghost",
            onclick: () => navigate(() => logViewer("piaware"), { title: "Logs · piaware", showBack: true }) }, "View logs");

        // Already set up: status card + a single action row, no inputs.
        if (configured) {
            const actions = el("div", { class: "wc-agg-row__actions" });
            if (a.enabled) {
                const stop = el("button", { type: "button", class: "wc-btn-ghost" }, "Stop feeding");
                stop.onclick = async () => {
                    stop.disabled = true;
                    const r = await postJSON("/api/aggregators/piaware/disable", {});
                    if (handleAuthFailure(r)) return;
                    if (!r.ok) { stop.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r); return; }
                    pendingFlash = { text: "Stopped feeding FlightAware.", level: "ok" };
                    navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
                };
                actions.appendChild(stop);
            } else {
                const start = el("button", { type: "button", class: "wc-btn-primary" }, "Start feeding");
                start.onclick = async () => {
                    start.disabled = true;
                    const r = await postJSON("/api/aggregators/piaware/enable", { fields: {}, mlat_enabled: mlatOn });
                    if (handleAuthFailure(r)) return;
                    if (r.status === 202) {
                        navigate(() => aggregatorEnableProgress("piaware", r.payload && r.payload.request_id),
                            { title: "Starting FlightAware", showBack: true });
                        return;
                    }
                    start.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r);
                };
                actions.appendChild(start);
            }
            const remove = el("button", { type: "button", class: "wc-btn-danger" }, "Remove");
            remove.onclick = async () => {
                if (!confirm("Remove FlightAware? This stops feeding, uninstalls piaware, and forgets this feeder's identity. Download a backup first if you want to keep its Feeder ID.")) return;
                remove.disabled = true;
                const r = await postJSON("/api/aggregators/piaware/reset", {});
                if (handleAuthFailure(r)) return;
                if (r.status === 202) {
                    navigate(() => aggregatorMutationProgress("piaware", r.payload && r.payload.request_id, "reset"),
                        { title: "Removing FlightAware", showBack: true });
                    return;
                }
                remove.disabled = false; inlineErr.textContent = aggEnableErrorMessage(r);
            };
            actions.appendChild(remove);
            actions.appendChild(viewLogs);

            render(el("section", { class: "wc-card" },
                aggDetailHead("FlightAware", aggDisplayState(a)),
                el("p", { class: "muted" }, aggFeedLine(a, "FlightAware", "Check the service logs.")),
                buildAggStatusBlock(a),
                inlineErr,
                actions,
            ));
            return;
        }

        // Not set up: Feeder ID (optional) + MLAT toggle; actions in the form.
        const feederId = el("input", { id: "agg-piaware-feeder-id", name: "feeder_id", type: "text", autocomplete: "off", spellcheck: "false",
            placeholder: "optional — paste a Feeder ID to reclaim an existing feeder" });
        const feederErr = el("p", { class: "field-help wc-field-error", hidden: true, role: "alert" },
            "A Feeder ID looks like 00000000-0000-0000-0000-000000000000.");

        const mlat = el("input", { id: "agg-piaware-mlat", name: "mlat_enabled", type: "checkbox" });
        mlat.checked = mlatOn;

        const submitLabel = "Set up FlightAware";
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
            el("div", { class: "field" }, el("label", { for: "agg-piaware-feeder-id" }, "Feeder ID (optional)"), feederId, feederErr,
                el("p", { class: "field-help" }, "Leave blank to register a new FlightAware feeder. Paste an existing Feeder ID to reclaim a feeder you already own.")),
            el("div", { class: "field" },
                el("label", { class: "wc-checkbox" }, mlat, el("span", {}, "Participate in MLAT (multilateration)")),
                el("p", { class: "field-help" }, "MLAT antenna location is set on flightaware.com for this feeder — not here.")),
            inlineErr,
            el("div", { class: "wc-agg-row__actions" }, submit, viewLogs),
        );
        recheck();

        render(el("section", { class: "wc-card" },
            aggDetailHead("FlightAware", aggDisplayState(a)),
            el("p", { class: "muted" }, "Send your receiver's data to FlightAware."),
            buildAggStatusBlock(a),
            form,
        ));
    }

    const AGG_ENABLE_POLL_MS = 2000;
    const AGG_ENABLE_GRACE_POLLS = 5;
    // Per-op copy for the shared mutation-progress view. enable and reset both run
    // as detached workers (reset escapes the webconfig sandbox the same way enable
    // does); the SPA polls until the worker's overlay reaches done/failed.
    const AGG_MUTATION_COPY = {
        enable: {
            heading: n => "Setting up " + n,
            blurb:   n => "Installing and configuring the " + n + " feeder. This can take a minute — you can leave this page; it keeps running.",
            working: "Working",
            okFlash: n => n + " is now feeding.",
            failHeading: "Setup failed.",
            failDefault: n => n + " setup failed.",
        },
        reset: {
            heading: n => "Removing " + n,
            blurb:   n => "Removing the " + n + " feeder. This can take a minute — you can leave this page; it keeps running.",
            working: "Removing",
            okFlash: n => "Removed " + n + ".",
            failHeading: "Removal failed.",
            failDefault: n => n + " could not be removed.",
        },
    };
    // aggregatorMutationProgress polls /api/aggregators after a 202 and shows the
    // detached worker's progress. It matches the worker's request_id against the
    // one the 202 returned, so a stale overlay from a prior run can't make this
    // run look done/failed. Mirrors orchestratorProgress's lifecycle teardown.
    function aggregatorMutationProgress(id, requestId, op) {
        const copy = AGG_MUTATION_COPY[op] || AGG_MUTATION_COPY.enable;
        const name = ADAPTER_NAMES[id] || id;
        const stepEl = el("p", { class: "muted" }, "Starting…");
        const errEl = el("div", { class: "wc-flash wc-flash--warn", role: "alert" });
        errEl.hidden = true;
        const actions = el("div", { class: "wc-agg-row__actions" });
        render(el("section", { class: "wc-card" },
            el("h2", {}, copy.heading(name)),
            el("p", { class: "muted" }, copy.blurb(name)),
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
            pendingFlash = { text: copy.okFlash(name), level: "ok" };
            navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
        };
        const finishFail = (msg) => {
            pollTimer = null;
            stepEl.textContent = copy.failHeading;
            errEl.textContent = msg || copy.failDefault(name);
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
                stepEl.textContent = copy.working + "… (" + (ov.step || "starting") + ")";
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
            // unscoped state — for a re-run against an already-running adapter that
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
    // Thin wrapper preserving the enable call sites.
    function aggregatorEnableProgress(id, requestId) {
        return aggregatorMutationProgress(id, requestId, "enable");
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
        renderAggregatorTiles(tileGrid.aggTiles, aggregators);

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
        ctx.lastStatus = status;
        updateEffectiveGain(ctx, status);
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
            ctx.lastStatus = status;
            updateEffectiveGain(ctx, status);
            renderAggregatorTiles(ctx.tileGrid.aggTiles, aggregators);
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
        let view = CLAIM_DOT[r.payload.result] || ["na", "Status unknown", "Couldn’t determine claim status right now"];
        // Refine "Registered" when the server says the feeder can't be
        // claimed until it is seen feeding (newer feed CLI + server).
        if (r.payload.result === "unclaimed" && r.payload.claim_unavailable_reason === "not_seen_feeding") {
            view = r.payload.last_seen_at
                ? ["warn", "Registered", "Registered, but not seen feeding recently — claimable again once it reconnects"]
                : ["warn", "Registered", "Registered — waiting for first data before it can be claimed"];
        } else if (r.payload.result === "unclaimed" && r.payload.claimable === false) {
            view = ["warn", "Registered", "Registered, but not currently claimable"];
        }
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
            el("div", { class: "field" }, el("label", { for: "setup-pw" }, "Password"), pwReveal(pw)),
            el("div", { class: "field" }, el("label", { for: "setup-pw-confirm" }, "Confirm password"), pwReveal(confirmInput)),
            submit,
            el("div", { class: "wc-setup-alt" },
                el("button", {
                    type: "button", class: "wc-btn-ghost",
                    onclick: () => navigate(setupRestorePanel, { title: "Restore from backup", showBack: false }),
                }, "Restore from backup instead")),
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
            el("div", { class: "field" }, el("label", { for: "login-pw" }, "Password"), pwReveal(pw)),
            submit,
            err,
        );
        render(form);
        pw.focus();
    }

    // ===== SSH access (pi account) =====

    // The SSH card manages ONLY the pi account: a per-device password
    // (enable / rotate / disable) and a single webconfig-managed authorized
    // key (set / clear). Every mutating action re-authenticates with the
    // webconfig password, which the server re-verifies against a fresh hash and
    // strips before forwarding to the privileged apl-ssh helper.
    const SSH_IMAGER_NOTE =
        "Set up SSH in Raspberry Pi Imager? Manage that login from your own session.";

    async function sshPanel() {
        render(el("div", { class: "wc-card" }, el("p", { class: "muted" }, "loading SSH access…")));
        const resp = await getJSON("/api/ssh");
        if (handleAuthFailure(resp)) return;
        const pf = consumePendingFlash();
        if (!resp.ok) {
            render(el("section", { class: "wc-card" },
                el("h2", {}, "SSH access"),
                pf && pf.text ? el("div", { class: "wc-flash wc-flash--" + (pf.level || "ok") }, pf.text) : null,
                el("p", { class: "error", role: "alert" }, "Could not load SSH status."),
                el("button", { type: "button", class: "wc-btn-primary", onclick: () => sshPanel() }, "Retry"),
            ));
            return;
        }
        const st = resp.payload || {};
        // password_auth_allowed / password_hash_unlocked may be null (helper
        // couldn't determine). Treat "password enabled" as both being true; any
        // null/false falls back to the enable affordance.
        const passwordOn = st.password_auth_allowed === true && st.password_hash_unlocked === true;
        const keyOn = st.managed_key_present === true;
        const piPresent = st.pi_present === true;

        const flashEl = el("div", {});
        if (pf && pf.text) flashEl.appendChild(el("div", { class: "wc-flash wc-flash--" + (pf.level || "ok") }, pf.text));

        const sections = [];
        if (!piPresent) {
            sections.push(el("p", { class: "error", role: "alert" },
                "The pi account does not exist on this device, so webconfig can't manage SSH access for it."));
        } else {
            sections.push(buildSSHPasswordSection(passwordOn));
            sections.push(buildSSHKeySection(keyOn));
        }

        render(el("section", { class: "wc-card" },
            el("h2", {}, "SSH access"),
            el("p", { class: "muted" },
                "Manage SSH login for the ", el("code", {}, "pi"), " account: a per-device password and a single public key. ",
                "Other accounts are unaffected."),
            el("p", { class: "muted" }, SSH_IMAGER_NOTE),
            flashEl,
            ...sections,
        ));
    }

    // sshReauthForm builds a small inline form: a webconfig-password field, any
    // extra fields (e.g. the new SSH password or the public key), and a submit
    // button that re-auths + POSTs to path. body() returns the request body
    // EXCLUDING current_password (added here). On success it sets a pending
    // flash and re-renders the SSH panel.
    function sshReauthForm(opts) {
        const err = errorEl();
        const pwField = el("input", {
            type: "password", name: "current-password", autocomplete: "current-password",
            required: true, placeholder: "Your webconfig password",
        });
        const username = hiddenUsernameField();
        const submit = el("button", { type: "submit", class: opts.danger ? "wc-btn-danger" : "wc-btn-primary" }, opts.submitLabel);

        const form = el("form", { class: "wc-ssh-action" },
            username,
            ...(opts.extraFields || []),
            el("div", { class: "field" },
                el("label", {}, "Confirm with your webconfig password"), pwReveal(pwField)),
            el("div", { class: "actions" }, submit),
            err,
        );
        form.addEventListener("submit", async (e) => {
            e.preventDefault();
            err.textContent = "";
            if (opts.validate) {
                const v = opts.validate();
                if (v) { err.textContent = v; return; }
            }
            submit.disabled = true;
            const prevLabel = submit.textContent;
            submit.textContent = "Working…";
            const body = Object.assign({ current_password: pwField.value }, opts.body ? opts.body() : {});
            const r = await postJSON(opts.path, body);
            submit.disabled = false;
            submit.textContent = prevLabel;
            if (handleAuthFailure(r)) return;
            if (!r.ok) {
                err.textContent = (r.payload && (r.payload.message || r.payload.error)) || "Operation failed.";
                return;
            }
            pendingFlash = { text: opts.successText, level: "ok" };
            await sshPanel();
        });
        return form;
    }

    function buildSSHPasswordSection(passwordOn) {
        const wrap = el("div", { class: "wc-ssh-section" },
            el("h3", {}, "Password login"),
        );
        const dot = el("span", { class: "wc-tile__dot wc-tile__dot--" + (passwordOn ? "ok" : "na") });
        wrap.appendChild(el("p", { class: "wc-ssh-state" }, dot,
            passwordOn ? " Password login is enabled for pi." : " Password login is disabled."));

        const newPw = el("input", {
            type: "password", name: "new-ssh-password", autocomplete: "new-password",
            required: true, minlength: "12", placeholder: "New pi password (≥12 characters)",
        });
        const pwField = () => el("div", { class: "field" },
            el("label", {}, passwordOn ? "New password for pi" : "Password for pi"), pwReveal(newPw));

        wrap.appendChild(sshReauthForm({
            path: passwordOn ? "/api/ssh/set-password" : "/api/ssh/enable-password",
            submitLabel: passwordOn ? "Rotate password" : "Enable password",
            extraFields: [pwField()],
            validate: () => (newPw.value.length < 12 ? "The pi password must be at least 12 characters." : ""),
            body: () => ({ password: newPw.value }),
            successText: passwordOn ? "pi password rotated." : "Password login enabled for pi.",
        }));

        if (passwordOn) {
            wrap.appendChild(sshReauthForm({
                path: "/api/ssh/disable-password",
                submitLabel: "Disable password login",
                danger: true,
                successText: "Password login disabled for pi.",
            }));
        }
        return wrap;
    }

    function buildSSHKeySection(keyOn) {
        const wrap = el("div", { class: "wc-ssh-section" },
            el("h3", {}, "Public key"),
        );
        const dot = el("span", { class: "wc-tile__dot wc-tile__dot--" + (keyOn ? "ok" : "na") });
        wrap.appendChild(el("p", { class: "wc-ssh-state" }, dot,
            keyOn ? " A webconfig-managed key is set for pi." : " No webconfig-managed key is set."));

        const keyInput = el("textarea", {
            name: "ssh-public-key", rows: "3", autocomplete: "off", spellcheck: "false",
            placeholder: "ssh-ed25519 AAAA… comment",
        });
        const keyField = el("div", { class: "field" },
            el("label", {}, keyOn ? "Replace the managed key" : "Public key"), keyInput);

        wrap.appendChild(sshReauthForm({
            path: "/api/ssh/set-key",
            submitLabel: keyOn ? "Replace key" : "Set key",
            extraFields: [keyField],
            validate: () => (keyInput.value.trim() === "" ? "Paste a public key." : ""),
            body: () => ({ key: keyInput.value.trim() }),
            successText: keyOn ? "Managed key replaced." : "Managed key set for pi.",
        }));

        if (keyOn) {
            wrap.appendChild(sshReauthForm({
                path: "/api/ssh/clear-key",
                submitLabel: "Clear key",
                danger: true,
                successText: "Managed key cleared.",
            }));
        }
        return wrap;
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
            el("div", { class: "field" }, el("label", { for: "change-pw-old" }, "Current password"), pwReveal(oldPw)),
            el("div", { class: "field" }, el("label", { for: "change-pw-new" }, "New password"), pwReveal(newPw)),
            el("div", { class: "field" }, el("label", { for: "change-pw-confirm" }, "Confirm new password"), pwReveal(confirmInput)),
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
                    // The claimability pair (newer feed CLI + server)
                    // refines the unclaimed copy; absent fields fall
                    // through to the original wording.
                    if (r.claim_unavailable_reason === "not_seen_feeding") {
                        if (r.last_seen_at) {
                            return { h: "Registered — not seen feeding recently", muted: "Claiming unlocks a few minutes after the feeder reconnects and data flows. This page re-checks automatically." };
                        }
                        return { h: "Registered — waiting for first data", muted: "Claiming unlocks once the feeder is seen by the server, usually a few minutes after data starts flowing. This page re-checks automatically." };
                    }
                    if (r.claimable === false) {
                        return { h: "Registered — not currently claimable", muted: "Claiming is temporarily unavailable for this feeder." };
                    }
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
            el("div", { class: "wc-stack" },
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
    if (userMenuAggregators) userMenuAggregators.addEventListener("click", () => {
        closeUserMenu();
        navigate(aggregatorsPanel, { title: "Third-party aggregators", showBack: true });
    });
    if (userMenuBackup) userMenuBackup.addEventListener("click", () => {
        closeUserMenu();
        navigate(backupPanel, { title: "Backup & restore", showBack: true });
    });
    if (userMenuSSH) userMenuSSH.addEventListener("click", () => {
        closeUserMenu();
        navigate(sshPanel, { title: "SSH access", showBack: true });
    });
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
