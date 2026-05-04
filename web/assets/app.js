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

    async function dashboard() {
        // Skeleton with placeholder cards; populate as endpoints respond.
        const statusBody = el("p", { class: "muted" }, "loading…");
        const identityBody = el("div", {}, el("p", { class: "muted" }, "loading…"));
        const configBody = el("div", {}, el("p", { class: "muted" }, "loading…"));

        const logout = el("button", {
            class: "secondary",
            onclick: async () => { await postJSON("/api/auth/logout", {}); await boot(); },
        }, "Log out");
        const change = el("button", {
            class: "secondary",
            onclick: () => changePasswordPanel(),
        }, "Change password");
        const refresh = el("button", {
            class: "secondary",
            onclick: () => dashboard(),
        }, "Refresh");

        const updateBtn = el("button", {
            class: "secondary",
            onclick: async () => {
                updateBtn.disabled = true;
                const r = await postJSON("/api/update", {});
                updateBtn.disabled = false;
                if (!r.ok) {
                    alert(r.payload.error || "update failed");
                    return;
                }
                logViewer("update");
            },
        }, "Run update");

        const rebootBtn = el("button", {
            class: "secondary",
            onclick: async () => {
                if (!confirm("Reboot the feeder now?")) return;
                rebootBtn.disabled = true;
                rebootBtn.textContent = "Rebooting…";
                await postJSON("/api/reboot", {});
                render(el("div", { class: "panel" },
                    el("h2", {}, "Rebooting…"),
                    el("p", {}, "The feeder is restarting. This page will go offline for ~30 seconds."),
                ));
            },
        }, "Reboot");

        render(
            el("div", { class: "dashboard-grid" },
                el("div", { class: "dashboard-card" },
                    el("h2", {}, "Identity"),
                    identityBody,
                ),
                el("div", { class: "dashboard-card" },
                    el("h2", {}, "Status"),
                    statusBody,
                ),
                el("div", { class: "dashboard-card" },
                    el("h2", {}, "Configuration"),
                    configBody,
                ),
                el("div", { class: "dashboard-card" },
                    el("h2", {}, "Live logs"),
                    el("p", { class: "muted" }, "Tail journalctl output for any unit:"),
                    el("div", { class: "actions" },
                        ...["feed", "mlat", "readsb", "dump978", "uat", "claim", "update"].map(slug =>
                            el("button", {
                                class: "secondary",
                                onclick: () => logViewer(slug),
                            }, slug),
                        ),
                    ),
                ),
            ),
            el("div", { class: "actions" }, refresh, updateBtn, change, rebootBtn, logout),
        );

        const [identity, status, config] = await Promise.all([
            getJSON("/api/identity"),
            getJSON("/api/status"),
            getJSON("/api/config"),
        ]);
        renderIdentityCard(identityBody, identity);
        renderStatusCard(statusBody, status);
        renderConfigCard(configBody, config);
    }

    function renderIdentityCard(parent, resp) {
        parent.replaceChildren();
        if (!resp.ok) {
            parent.appendChild(el("p", { class: "error" }, resp.payload.error || "could not load identity"));
            return;
        }
        const id = resp.payload || {};
        if (!id.feeder_id) {
            parent.appendChild(el("p", { class: "muted" }, "Feeder ID will be assigned on first run."));
            return;
        }
        parent.appendChild(el("p", {}, el("strong", {}, "Feeder ID: "), id.feeder_id));
        if (!id.claim_secret_present) {
            parent.appendChild(el("p", { class: "muted" }, "No claim secret yet — apl-feed claim register will create one."));
            return;
        }
        const reveal = el("button", {
            class: "secondary",
            onclick: async () => {
                reveal.disabled = true;
                const r = await postJSON("/api/identity/secret", {});
                reveal.disabled = false;
                if (!r.ok) {
                    parent.replaceChildren(el("p", { class: "error" }, r.payload.error || "reveal failed"));
                    return;
                }
                parent.replaceChildren(
                    el("p", {}, el("strong", {}, "Feeder ID: "), r.payload.feeder_id),
                    el("p", {}, el("strong", {}, "Claim secret: "), el("code", {}, r.payload.claim_secret)),
                    el("p", {}, el("a", { href: r.payload.claim_page, target: "_blank", rel: "noopener noreferrer" }, "Claim this feeder")),
                );
            },
        }, "Show claim secret");
        parent.appendChild(reveal);
    }

    function renderStatusCard(parent, resp) {
        parent.replaceChildren();
        if (!resp.ok) {
            parent.appendChild(el("p", { class: "error" }, resp.payload.error || "could not load status"));
            return;
        }
        const services = resp.payload.services || {};
        const list = el("ul", { class: "service-list" });
        for (const [unit, state] of Object.entries(services).sort()) {
            list.appendChild(el("li", {},
                el("span", { class: "service-state state-" + state }, state),
                " ",
                el("code", {}, unit.replace(/\.service$/, "")),
            ));
        }
        parent.appendChild(list);
        const feed = resp.payload.feed;
        if (feed) {
            parent.appendChild(el("p", { class: "muted" },
                feed.aircraft_count + " aircraft, " + feed.messages_counter + " messages decoded",
            ));
        }
    }

    function renderConfigCard(parent, resp) {
        parent.replaceChildren();
        if (!resp.ok) {
            parent.appendChild(el("p", { class: "error" }, resp.payload.error || "could not load config"));
            return;
        }
        const values = resp.payload.values || {};
        const inputs = {};
        const field = (key, label, attrs) => {
            const input = el("input", { name: key, value: values[key] || "", ...(attrs || {}) });
            inputs[key] = input;
            return el("div", { class: "field" },
                el("label", {}, label, " ", el("code", {}, key)),
                input,
            );
        };
        const dump978On = (values["UAT_INPUT"] || "") !== "";
        const uat = el("input", {
            type: "checkbox",
            name: "UAT_INPUT",
            ...(dump978On ? { checked: "" } : {}),
        });
        inputs["UAT_INPUT"] = uat;

        const err = errorEl();
        const submit = el("button", { type: "submit" }, "Save & restart");

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
                    USER: inputs.USER.value.trim(),
                    GAIN: inputs.GAIN.value.trim(),
                    UAT_INPUT: uat.checked ? "127.0.0.1:30978" : "",
                };
                // Drop empty strings except UAT_INPUT (empty is meaningful for
                // disabling 978).
                for (const k of Object.keys(updates)) {
                    if (k !== "UAT_INPUT" && updates[k] === "") {
                        delete updates[k];
                    }
                }
                const r = await postJSON("/api/config", { updates });
                submit.disabled = false;
                submit.textContent = "Save & restart";
                if (!r.ok) {
                    err.textContent = r.payload.error || "save failed";
                    return;
                }
                await dashboard();
            },
        },
            field("LATITUDE", "Latitude", { type: "text", inputmode: "decimal", placeholder: "51.5" }),
            field("LONGITUDE", "Longitude", { type: "text", inputmode: "decimal", placeholder: "-0.1" }),
            field("ALTITUDE", "Altitude", { type: "text", placeholder: "120m" }),
            field("USER", "MLAT name", { type: "text", placeholder: "alice" }),
            field("GAIN", "Gain", { type: "text", placeholder: "auto" }),
            el("div", { class: "field" },
                el("label", {},
                    uat, " Enable 978 UAT", " ", el("code", {}, "UAT_INPUT"),
                ),
            ),
            submit,
            err,
        );
        parent.appendChild(form);

        // Read-only keys (set by feed/install.sh, not editable here).
        const readOnly = Object.keys(values).filter(k =>
            !["LATITUDE", "LONGITUDE", "ALTITUDE", "USER", "GAIN", "UAT_INPUT"].includes(k)
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

    function logViewer(slug) {
        const pre = el("pre", { class: "log-output" });
        const back = el("button", { class: "secondary", onclick: () => dashboard() }, "Back");
        render(
            el("div", { class: "panel" },
                el("h2", {}, "journalctl -u " + slug),
                el("p", { class: "muted" }, "Streaming live; close this view to disconnect."),
                pre,
                back,
            ),
        );
        const es = new EventSource("/api/log/" + encodeURIComponent(slug));
        es.onmessage = (ev) => {
            pre.appendChild(document.createTextNode(ev.data + "\n"));
            pre.scrollTop = pre.scrollHeight;
        };
        es.onerror = () => {
            pre.appendChild(document.createTextNode("[stream closed]\n"));
            es.close();
        };
        // Close the EventSource when navigating away from this view.
        back.addEventListener("click", () => es.close(), { once: true });
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
