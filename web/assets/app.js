// airplanes-webconfig SPA shell. State-aware: detects uninitialized →
// shows setup panel; initialized + no session → login; initialized +
// valid session → dashboard skeleton.
(function () {
    "use strict";

    const app = document.getElementById("app");

    async function postJSON(path, body) {
        const resp = await fetch(path, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            credentials: "same-origin",
            body: JSON.stringify(body),
        });
        let payload = null;
        try { payload = await resp.json(); } catch (_) { /* empty */ }
        return { ok: resp.ok, status: resp.status, payload: payload || {} };
    }

    async function getJSON(path) {
        const resp = await fetch(path, {
            method: "GET",
            credentials: "same-origin",
            headers: { "Accept": "application/json" },
        });
        let payload = null;
        try { payload = await resp.json(); } catch (_) { /* empty */ }
        return { ok: resp.ok, status: resp.status, payload: payload || {} };
    }

    function el(tag, attrs = {}, ...children) {
        const node = document.createElement(tag);
        for (const [k, v] of Object.entries(attrs)) {
            if (k === "class") node.className = v;
            else if (k === "html") node.innerHTML = v;
            else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
            else node.setAttribute(k, v);
        }
        for (const c of children) {
            if (c == null) continue;
            node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
        }
        return node;
    }

    function clear() { app.replaceChildren(); }

    function render(...nodes) { clear(); for (const n of nodes) app.appendChild(n); }

    function errorEl() { return el("div", { class: "error", role: "alert" }); }

    // ---- panels ----

    function setupPanel() {
        const err = errorEl();
        const pw = el("input", { type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const confirm = el("input", { type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const submit = el("button", { type: "submit" }, "Set password");

        const form = el("form", {
            class: "panel",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                if (pw.value !== confirm.value) {
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
                    err.textContent = r.payload.error || "Setup failed.";
                    return;
                }
                await boot();
            },
        },
            el("h2", {}, "Set webconfig password"),
            el("p", {}, "Choose the password used to administer this feeder. Minimum 12 characters."),
            el("div", { class: "field" }, el("label", {}, "Password"), pw),
            el("div", { class: "field" }, el("label", {}, "Confirm password"), confirm),
            submit,
            err,
        );
        render(form);
        pw.focus();
    }

    function loginPanel(initialError) {
        const err = errorEl();
        if (initialError) err.textContent = initialError;
        const pw = el("input", { type: "password", autocomplete: "current-password", required: true });
        const submit = el("button", { type: "submit" }, "Log in");

        const form = el("form", {
            class: "panel",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                submit.disabled = true;
                submit.textContent = "Logging in…";
                const r = await postJSON("/api/auth/login", { password: pw.value });
                submit.disabled = false;
                submit.textContent = "Log in";
                if (!r.ok) {
                    if (r.status === 423) {
                        err.textContent = "Too many failed attempts. Try again later.";
                    } else {
                        err.textContent = r.payload.error || "Login failed.";
                    }
                    pw.value = "";
                    pw.focus();
                    return;
                }
                await boot();
            },
        },
            el("h2", {}, "Log in"),
            el("div", { class: "field" }, el("label", {}, "Password"), pw),
            submit,
            err,
        );
        render(form);
        pw.focus();
    }

    function dashboard() {
        const logout = el("button", {
            class: "secondary",
            onclick: async () => {
                await postJSON("/api/auth/logout", {});
                await boot();
            },
        }, "Log out");

        const change = el("button", {
            class: "secondary",
            onclick: () => changePasswordPanel(),
        }, "Change password");

        render(
            el("div", { class: "dashboard-grid" },
                el("div", { class: "dashboard-card" },
                    el("h2", {}, "Status"),
                    el("p", {}, "Signed in."),
                ),
            ),
            el("div", { class: "actions" }, change, logout),
        );
    }

    function changePasswordPanel() {
        const err = errorEl();
        const oldPw = el("input", { type: "password", autocomplete: "current-password", required: true });
        const newPw = el("input", { type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const confirm = el("input", { type: "password", autocomplete: "new-password", required: true, minlength: "12" });
        const submit = el("button", { type: "submit" }, "Change password");
        const cancel = el("button", { type: "button", class: "secondary", onclick: () => dashboard() }, "Cancel");

        const form = el("form", {
            class: "panel",
            onsubmit: async (e) => {
                e.preventDefault();
                err.textContent = "";
                if (newPw.value !== confirm.value) {
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
                if (!r.ok) {
                    err.textContent = r.payload.error || "Change failed.";
                    return;
                }
                dashboard();
            },
        },
            el("h2", {}, "Change webconfig password"),
            el("div", { class: "field" }, el("label", {}, "Current password"), oldPw),
            el("div", { class: "field" }, el("label", {}, "New password"), newPw),
            el("div", { class: "field" }, el("label", {}, "Confirm new password"), confirm),
            el("div", { class: "actions" }, cancel, submit),
            err,
        );
        render(form);
        oldPw.focus();
    }

    function loadingPanel(msg) {
        render(el("div", { class: "panel" }, el("p", {}, msg)));
    }

    function corruptPanel(msg) {
        render(el("div", { class: "panel" },
            el("h2", {}, "Recovery required"),
            el("p", {}, msg),
            el("p", {}, "Drop /boot/firmware/airplanes-reset-password on the SD card and reboot to start over."),
        ));
    }

    async function boot() {
        loadingPanel("Loading…");
        const stateResp = await getJSON("/api/state");
        if (!stateResp.ok) {
            corruptPanel(stateResp.payload.error || "Server returned an unexpected error.");
            return;
        }
        if (stateResp.payload.state === "uninitialized") {
            setupPanel();
            return;
        }
        // Initialized — check for a valid session.
        const who = await getJSON("/api/auth/whoami");
        if (who.ok) {
            dashboard();
        } else {
            loginPanel();
        }
    }

    boot().catch((e) => corruptPanel("Fatal error: " + (e && e.message || e)));
})();
