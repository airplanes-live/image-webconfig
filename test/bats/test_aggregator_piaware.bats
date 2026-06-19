#!/usr/bin/env bats
#
# Unit tests for the piaware (FlightAware) pinned-deb adapter in apl-aggregator.
# piaware installs from a fetched, sha256-verified .deb (no persisted apt source)
# and is held at the descriptor-pinned version. Every system touchpoint is stubbed
# via the AGG_* seams: the .deb fetch, apt-get, apt-mark, dpkg-query, piaware-
# config, systemctl, systemd-run (runs the enable worker inline), and the
# os-release / feeder-id-cache locations. The real apt + piaware install is
# validated end-to-end on a trixie/arm64 feeder, not here.

setup() {
    command -v jq >/dev/null || skip "jq not installed"
    APLAGG="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-aggregator"
    [ -x "$APLAGG" ] || skip "apl-aggregator missing"

    WORK="$(mktemp -d)"
    export AGG_DESC_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes-webconfig/aggregators"
    export AGG_STATE_DIR="$WORK/state"; mkdir -p "$AGG_STATE_DIR"
    export AGG_INSTALL_ROOT="$WORK/install"
    export AGG_LOCK="$WORK/lock"
    export AGG_REQ_DIR="$WORK/req"
    export AGG_ENABLE_STATE="$WORK/enable.state"
    export AGG_DECODER_STATE=up
    export AGG_SELF="$APLAGG"
    export AGG_ENABLE_GRACE=10
    export AGG_DPKG_ARCH=arm64
    export AGG_PIAWARE_ID_WAIT=2
    export AGG_LIVENESS_WAIT=0   # no settle delay in tests

    printf 'VERSION_ID="13"\nVERSION_CODENAME=trixie\n' > "$WORK/os-release"
    export AGG_OS_RELEASE="$WORK/os-release"

    export AGG_KEYRING_DIR="$WORK/keyrings";       mkdir -p "$AGG_KEYRING_DIR"
    export AGG_APT_SOURCES_DIR="$WORK/sources.list.d"; mkdir -p "$AGG_APT_SOURCES_DIR"
    export AGG_APT_PREFS_DIR="$WORK/preferences.d";    mkdir -p "$AGG_APT_PREFS_DIR"
    export AGG_POLICY_RCD="$WORK/policy-rc.d"
    export AGG_PIAWARE_CONF="$WORK/piaware.conf"
    # Cache file lives under a dedicated dir — teardown rm -rf's its PARENT, so it
    # must not be $WORK itself (production path is /var/cache/piaware/feeder_id).
    mkdir -p "$WORK/cache/piaware"
    export AGG_PIAWARE_FEEDER_ID_FILE="$WORK/cache/piaware/feeder_id"

    export PKG_MARK="$WORK/pkg-installed"
    export PKG_VER="$WORK/pkg-version"            # the installed dpkg version
    export HOLD_MARK="$WORK/pkg-held"             # apt-mark hold state
    export APT_LOG="$WORK/apt.log";        : > "$APT_LOG"
    export APT_MARK_LOG="$WORK/apt-mark.log"; : > "$APT_MARK_LOG"
    export PIAWARE_CONFIG_LOG="$WORK/piaware-config.log"; : > "$PIAWARE_CONFIG_LOG"
    export APT_SIM_REMOVE=""        # set → `-s install` emits a Remv line
    export APT_SIM_INST_DENY=""     # set → `-s install` emits an extra Inst <pkg>
    export INSTALL_VERSION="11.0"   # the version the (real) install lands
    export PIAWARE_ACTIVE="active"  # is-active reply for piaware.service when installed

    # Pinned-.deb fetch override: drop a dummy .deb at <dest> (bypasses curl+sha).
    FD="$WORK/fetch-deb"
    printf '#!/usr/bin/env bash\nprintf dummy > "$1"\n' > "$FD"
    chmod +x "$FD"; export AGG_PIAWARE_DEB_OVERRIDE="$FD"

    # dpkg-query: installed iff the marker file exists. Answers both the
    # Status-Abbrev probe (ii) and the Version probe (the recorded version).
    DQ="$WORK/dpkg-query"
    cat > "$DQ" <<'EOF'
#!/usr/bin/env bash
fmt=""
for a in "$@"; do case "$a" in -f=*) fmt="${a#-f=}" ;; esac; done
[ -f "$PKG_MARK" ] || exit 1
# A held+installed package reports 'hi', a normal install 'ii' — the helper must
# treat both as installed (held is exactly the state this feature creates).
abbrev="ii "; [ -f "$HOLD_MARK" ] && abbrev="hi "
case "$fmt" in
    *Status-Abbrev*) printf '%s' "$abbrev" ;;
    *Version*)       printf '%s' "$(cat "$PKG_VER" 2>/dev/null || echo 11.0)" ;;
    *)               printf '%s' "$abbrev" ;;
esac
exit 0
EOF
    chmod +x "$DQ"; export AGG_DPKG_QUERY="$DQ"

    # dpkg-deb -f <deb> <field>: the pinned .deb's control metadata. Version tracks
    # INSTALL_VERSION so a mismatch test trips the pre-install check.
    DD="$WORK/dpkg-deb"
    cat > "$DD" <<'EOF'
#!/usr/bin/env bash
case "$3" in
    Package)      printf piaware ;;
    Version)      printf '%s' "${INSTALL_VERSION:-11.0}" ;;
    Architecture) printf arm64 ;;
esac
exit 0
EOF
    chmod +x "$DD"; export AGG_DPKG_DEB="$DD"

    export SYSTEMCTL_LOG="$WORK/systemctl.log"; : > "$SYSTEMCTL_LOG"
    export ENABLE_UNIT_STATE=inactive
    SC="$WORK/systemctl"
    cat > "$SC" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
if [[ "$1" == is-active ]]; then
    case "$2" in
        *aggregator-enable*) echo "${ENABLE_UNIT_STATE:-inactive}" ;;
        piaware.service)     [ -f "$PKG_MARK" ] && echo "${PIAWARE_ACTIVE:-active}" || echo inactive ;;
        *) echo active ;;
    esac
    exit 0
fi
exit 0
EOF
    chmod +x "$SC"; export AGG_SYSTEMCTL="$SC"

    SR="$WORK/systemd-run"
    printf '#!/usr/bin/env bash\nwhile [[ "$1" == --* ]]; do shift; done\n"$@"\nexit 0\n' > "$SR"
    chmod +x "$SR"; export AGG_SYSTEMD_RUN="$SR"

    # apt-get stub: records argv + the no-restart env, simulates install/autoremove,
    # and on a real install marks the package installed (at INSTALL_VERSION) +
    # provisions a feeder-id.
    APT="$WORK/apt-get"
    cat > "$APT" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$APT_LOG"
printf 'env NEEDRESTART_MODE=%s NEEDRESTART_SUSPEND=%s DEBIAN_FRONTEND=%s\n' \
    "${NEEDRESTART_MODE:-}" "${NEEDRESTART_SUSPEND:-}" "${DEBIAN_FRONTEND:-}" >> "$APT_LOG"
cmd=""; sim=0
for a in "$@"; do
    case "$a" in update|install|purge|autoremove) [ -z "$cmd" ] && cmd="$a" ;; esac
    [ "$a" = "-s" ] && sim=1
done
case "$cmd" in
    update) exit 0 ;;
    install)
        if [ "$sim" = 1 ]; then
            [ -n "$APT_SIM_REMOVE" ] && echo "Remv $APT_SIM_REMOVE [1.0]"
            [ -n "$APT_SIM_INST_DENY" ] && echo "Inst $APT_SIM_INST_DENY (11.0 fa [arm64])"
            echo "Inst piaware (11.0 [arm64])"
            echo "Inst tcl (8.6 Debian:13 [arm64])"
            exit 0
        fi
        : > "$PKG_MARK"
        printf '%s' "${INSTALL_VERSION:-11.0}" > "$PKG_VER"
        printf 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\n' > "$AGG_PIAWARE_FEEDER_ID_FILE"
        exit 0 ;;
    autoremove)
        [ "$sim" = 1 ] && { echo "Purg tcl [8.6]"; exit 0; }
        exit 0 ;;
    purge) rm -f "$PKG_MARK" "$PKG_VER" "$HOLD_MARK"; exit 0 ;;
esac
exit 0
EOF
    chmod +x "$APT"; export AGG_APT_GET="$APT"

    AM="$WORK/apt-mark"
    cat > "$AM" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$APT_MARK_LOG"
case "$1" in hold) : > "$HOLD_MARK" ;; unhold) rm -f "$HOLD_MARK" ;; esac
exit 0
EOF
    chmod +x "$AM"; export AGG_APT_MARK="$AM"

    PC="$WORK/piaware-config"
    cat > "$PC" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$PIAWARE_CONFIG_LOG"
exit 0
EOF
    chmod +x "$PC"; export AGG_PIAWARE_CONFIG="$PC"
}

teardown() { [ -n "${WORK:-}" ] && rm -rf "$WORK"; }

agg() { run bash -c 'printf "%s" "$3" | "$1" "$2" --json' _ "$APLAGG" "$1" "$2"; }
overlay_status() { jq -r '.status // ""' "$AGG_ENABLE_STATE" 2>/dev/null; }
overlay_code() { jq -r '.error_code // ""' "$AGG_ENABLE_STATE" 2>/dev/null; }
managed() { jq -r "$1" "$AGG_STATE_DIR/piaware.managed.json" 2>/dev/null; }

# Seed a managed, installed piaware at <version>, enabled=<true|false>.
seed_managed() {
    local ver="$1" enabled="${2:-true}"
    : > "$PKG_MARK"; printf '%s' "$ver" > "$PKG_VER"; : > "$HOLD_MARK"   # installed + held
    printf '{"we_installed":true,"installed_pkgs":["piaware"],"hold":true}' \
        > "$AGG_STATE_DIR/piaware.managed.json"
    printf '{"schema_version":1,"enabled":%s,"mlat_enabled":true,"installed_version":"%s","fields":{"feeder_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}}' \
        "$enabled" "$ver" > "$AGG_STATE_DIR/piaware.json"
}

# --- status ----------------------------------------------------------------

@test "status lists piaware as pinned-deb, available + not_installed on trixie/arm64" {
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware")
        | .acquire_method=="pinned-deb" and .available==true and .state=="not_installed"
        and .mlat_capable==true and .mlat_default==true and .desired_version=="11.0"'
}

@test "status reports unavailable on an unsupported OS" {
    printf 'VERSION_ID="99"\nVERSION_CODENAME=sid\n' > "$WORK/os-release"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware") | .available==false and .state=="unavailable"'
}

@test "status surfaces a live feeder-id even when state has none" {
    printf 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\n' > "$AGG_PIAWARE_FEEDER_ID_FILE"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware")
        | (.secret_fields_present | index("feeder_id")) != null and .configured==true'
}

@test "status reports the live installed version and flags drift vs the pin" {
    seed_managed "10.0" true
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware")
        | .version=="10.0" and .desired_version=="11.0" and .version_drift==true'
}

@test "status flags a pre-existing unmanaged piaware as external_install" {
    : > "$PKG_MARK"          # package present, but no piaware.managed.json marker
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware")
        | .external_install==true and .managed_install==false'
}

@test "status treats a bare /etc/piaware.conf (no package) as an unmanaged install" {
    printf 'feeder-id deadbeef\n' > "$AGG_PIAWARE_CONF"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware") | .external_install==true'
}

@test "status reports a managed piaware as managed_install, not external" {
    seed_managed "11.0" true
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware")
        | .managed_install==true and .external_install==false'
}

@test "status: an unmanaged package plus our own state reports both external and managed" {
    : > "$PKG_MARK"          # admin package present, no we_installed marker
    # Our own state (e.g. an imported identity) — reset can safely clear just this.
    printf '{"schema_version":1,"enabled":false,"mlat_enabled":false,"fields":{"feeder_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}}' \
        > "$AGG_STATE_DIR/piaware.json"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware")
        | .external_install==true and .managed_install==true'
}

# --- enable (worker run inline by the systemd-run stub) --------------------

@test "enable fetches+installs the pinned .deb, holds it, configures + starts, persists feeder-id" {
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result=="accepted" and .id=="piaware" and (.request_id|length>0)'
    [ "$(overlay_status)" = "done" ]
    [ -f "$PKG_MARK" ]
    grep -q 'allow-downgrades' "$APT_LOG"
    grep -q '^hold piaware' "$APT_MARK_LOG"
    grep -q 'receiver-type other' "$PIAWARE_CONFIG_LOG"
    grep -q 'receiver-port 30005' "$PIAWARE_CONFIG_LOG"
    grep -q 'allow-mlat yes' "$PIAWARE_CONFIG_LOG"
    grep -q 'enable --now piaware.service' "$SYSTEMCTL_LOG"
    jq -e '.enabled==true and .mlat_enabled==true and .installed_version=="11.0"
        and .fields.feeder_id=="aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"' "$AGG_STATE_DIR/piaware.json"
    [ "$(managed '.we_installed')" = "true" ]
    [ "$(managed '.hold')" = "true" ]
    echo "$(managed '.installed_pkgs|join(" ")')" | grep -q piaware
    # No persisted FlightAware apt source under the pinned-deb model.
    [ ! -f "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources" ]
}

@test "enable applies the no-restart apt env (NEEDRESTART/DEBIAN_FRONTEND)" {
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    grep -q 'NEEDRESTART_MODE=l' "$APT_LOG"
    grep -q 'DEBIAN_FRONTEND=noninteractive' "$APT_LOG"
}

@test "enable with mlat_enabled:false sets allow-mlat no" {
    agg enable '{"id":"piaware","mlat_enabled":false,"fields":{}}'
    [ "$status" -eq 0 ]
    grep -q 'allow-mlat no' "$PIAWARE_CONFIG_LOG"
    jq -e '.mlat_enabled==false' "$AGG_STATE_DIR/piaware.json"
}

@test "enable passes a restored feeder-id to piaware-config and persists THAT id, not the cache" {
    agg enable '{"id":"piaware","fields":{"feeder_id":"11111111-2222-4333-8444-555555555555"}}'
    [ "$status" -eq 0 ]
    grep -q 'feeder-id 11111111-2222-4333-8444-555555555555' "$PIAWARE_CONFIG_LOG"
    jq -e '.fields.feeder_id=="11111111-2222-4333-8444-555555555555"' "$AGG_STATE_DIR/piaware.json"
}

@test "enable aborts (acquire_failed) when the .deb sha256 does not match the pin" {
    # Drop the override so the real _download_verify runs; a curl stub writes
    # bytes whose sha256 cannot match the descriptor pin.
    unset AGG_PIAWARE_DEB_OVERRIDE
    mkdir -p "$WORK/bin"
    cat > "$WORK/bin/curl" <<'EOF'
#!/usr/bin/env bash
out=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
[ -n "$out" ] && printf 'not-the-pinned-bytes' > "$out"
exit 0
EOF
    chmod +x "$WORK/bin/curl"
    PATH="$WORK/bin:$PATH" agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "acquire_failed" ]
    [ ! -f "$PKG_MARK" ]
}

@test "enable aborts (pre-install) when the .deb version does not match the pin" {
    export INSTALL_VERSION="9.9"   # the .deb advertises a version != the 11.0 pin
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "acquire_failed" ]
    [ ! -f "$PKG_MARK" ]            # caught before apt ran the .deb
}

@test "enable aborts (acquire_failed) when the apt simulate would remove a package" {
    export APT_SIM_REMOVE=some-package
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]                 # launch accepted; worker fails
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "acquire_failed" ]
    [ ! -f "$PKG_MARK" ]                # real install never ran
}

@test "enable aborts when the apt simulate would install a denied decoder package" {
    export APT_SIM_INST_DENY=dump1090-fa
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "acquire_failed" ]
    [ ! -f "$PKG_MARK" ]
}

@test "enable reports a crash-looping unit as state_error (liveness)" {
    export PIAWARE_ACTIVE=activating   # starts, never reaches active
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "state_error" ]
}

@test "enable sweeps a stale legacy FlightAware apt source before installing" {
    # A dev feeder that ran the retired apt-repo path still has a source on disk.
    printf 'Types: deb\n' > "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources"
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    [ ! -f "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources" ]
}

@test "enable on an already-current managed piaware skips reinstall" {
    seed_managed "11.0" false
    : > "$APT_LOG"
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    ! grep -q 'allow-downgrades' "$APT_LOG"     # no (re)install ran
    grep -q 'enable --now piaware.service' "$SYSTEMCTL_LOG"
}

@test "enable reinstalls a managed piaware that is on the wrong version" {
    seed_managed "10.0" false
    : > "$APT_LOG"
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    grep -q 'allow-downgrades' "$APT_LOG"        # reinstalled to the pin
    jq -e '.installed_version=="11.0"' "$AGG_STATE_DIR/piaware.json"
}

# --- enable: synchronous rejections (no worker) ----------------------------

@test "enable rejects a non-boolean mlat_enabled synchronously" {
    agg enable '{"id":"piaware","mlat_enabled":"false","fields":{}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code=="rejected"'
    [ ! -f "$AGG_ENABLE_STATE" ]
}

@test "enable rejects a non-object fields synchronously" {
    agg enable '{"id":"piaware","fields":"nope"}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code=="rejected"'
}

@test "enable rejects a malformed feeder-id synchronously" {
    agg enable '{"id":"piaware","fields":{"feeder_id":"not-a-uuid"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code=="rejected"'
    [ ! -f "$AGG_ENABLE_STATE" ]
}

@test "enable fails cleanly when the local decoder is unreachable" {
    export AGG_DECODER_STATE=down
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code=="decoder_unavailable"'
}

@test "enable is rejected on an unsupported OS synchronously" {
    printf 'VERSION_ID="99"\nVERSION_CODENAME=sid\n' > "$WORK/os-release"
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code=="rejected"'
}

@test "enable refuses to touch a pre-existing unmanaged piaware" {
    : > "$PKG_MARK"            # piaware installed, but no ownership marker
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 3 ]
    echo "$output" | jq -e '.error_code=="state_error"'
}

# --- set (live reconcile) --------------------------------------------------

@test "set toggles allow-mlat live + try-restart on a managed piaware" {
    agg enable '{"id":"piaware","fields":{}}'; [ "$status" -eq 0 ]
    : > "$PIAWARE_CONFIG_LOG"   # isolate the set's piaware-config call
    agg set '{"id":"piaware","mlat_enabled":false}'
    [ "$status" -eq 0 ]
    grep -q 'allow-mlat no' "$PIAWARE_CONFIG_LOG"
    grep -q 'try-restart piaware.service' "$SYSTEMCTL_LOG"
    jq -e '.mlat_enabled==false' "$AGG_STATE_DIR/piaware.json"
}

@test "set on a not-installed piaware just stores the preference (no piaware-config)" {
    agg set '{"id":"piaware","mlat_enabled":true}'
    [ "$status" -eq 0 ]
    jq -e '.mlat_enabled==true' "$AGG_STATE_DIR/piaware.json"
    [ ! -s "$PIAWARE_CONFIG_LOG" ]
}

# --- reset (ownership-aware teardown) --------------------------------------

@test "reset unholds + purges piaware and clears our state" {
    agg enable '{"id":"piaware","fields":{}}'; [ "$status" -eq 0 ]
    agg reset '{"id":"piaware"}'
    [ "$status" -eq 0 ]
    grep -q 'disable --now piaware.service' "$SYSTEMCTL_LOG"
    grep -q '^unhold piaware' "$APT_MARK_LOG"
    grep -q 'purge -y piaware' "$APT_LOG"
    [ ! -f "$PKG_MARK" ]
    [ ! -f "$AGG_PIAWARE_CONF" ]
    [ ! -f "$AGG_STATE_DIR/piaware.json" ]
    [ ! -f "$AGG_STATE_DIR/piaware.managed.json" ]
}

@test "reset sweeps a leftover legacy FlightAware apt source" {
    agg enable '{"id":"piaware","fields":{}}'; [ "$status" -eq 0 ]
    printf 'Types: deb\n' > "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources"
    printf 'Package: *\n'  > "$AGG_APT_PREFS_DIR/airplanes-flightaware.pref"
    agg reset '{"id":"piaware"}'
    [ "$status" -eq 0 ]
    [ ! -f "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources" ]
    [ ! -f "$AGG_APT_PREFS_DIR/airplanes-flightaware.pref" ]
}

@test "reset leaves a pre-existing unmanaged piaware untouched" {
    : > "$PKG_MARK"                    # installed, no ownership marker
    printf '%s' '{"schema_version":1,"enabled":false,"fields":{}}' > "$AGG_STATE_DIR/piaware.json"
    agg reset '{"id":"piaware"}'
    [ "$status" -eq 0 ]
    [ -f "$PKG_MARK" ]                                          # NOT purged
    ! grep -q 'purge -y piaware' "$APT_LOG"
    ! grep -q 'disable --now piaware.service' "$SYSTEMCTL_LOG"  # NOT stopped
    [ ! -f "$AGG_STATE_DIR/piaware.json" ]                      # our state still cleared
}

@test "reset cleans the policy-rc.d an aborted acquire left behind" {
    # An acquire that aborts at the simulate has set the install guard but never
    # set we_installed; reset must still remove the guard so a stray policy-rc.d
    # can't break later apt operations.
    export APT_SIM_REMOVE=some-package
    agg enable '{"id":"piaware","fields":{}}'; [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "failed" ]
    [ -f "$AGG_POLICY_RCD" ]
    [ "$(jq -r '.we_installed // false' "$AGG_STATE_DIR/piaware.managed.json")" != "true" ]
    unset APT_SIM_REMOVE
    agg reset '{"id":"piaware"}'
    [ "$status" -eq 0 ]
    [ ! -f "$AGG_POLICY_RCD" ]
    ! grep -q 'purge -y piaware' "$APT_LOG"          # nothing was installed to purge
}

# --- reconcile (overlay-update auto-apply) ---------------------------------

@test "reconcile updates a drifted managed piaware to the pin and restarts it" {
    seed_managed "10.0" true
    : > "$APT_LOG"; : > "$SYSTEMCTL_LOG"
    run "$APLAGG" reconcile --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result=="ok"'
    echo "$output" | jq -e '.reconciled[] | select(.id=="piaware")
        | .action=="update" and .from=="10.0" and .to=="11.0" and .status=="ok"'
    grep -q 'allow-downgrades' "$APT_LOG"
    grep -q 'allow-change-held-packages' "$APT_LOG"
    grep -q 'try-restart piaware.service' "$SYSTEMCTL_LOG"
    jq -e '.installed_version=="11.0"' "$AGG_STATE_DIR/piaware.json"
}

@test "reconcile reports failed when the unit does not come up after the update" {
    seed_managed "10.0" true
    export PIAWARE_ACTIVE=activating   # installs to the pin but never reaches active
    run "$APLAGG" reconcile --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.reconciled[] | select(.id=="piaware") | .status=="failed"'
    # The new version did land (it installed); the failure is the restart.
    jq -e '.installed_version=="11.0" and (.reconcile_error|type=="object")' "$AGG_STATE_DIR/piaware.json"
}

@test "reconcile only try-restarts a managed piaware already on the pin" {
    seed_managed "11.0" true
    : > "$APT_LOG"; : > "$SYSTEMCTL_LOG"
    run "$APLAGG" reconcile --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.reconciled[] | select(.id=="piaware") | .action=="restart" and .status=="ok"'
    ! grep -q 'allow-downgrades' "$APT_LOG"      # no reinstall
    grep -q 'try-restart piaware.service' "$SYSTEMCTL_LOG"
}

@test "reconcile skips an adapter that is not installed" {
    run "$APLAGG" reconcile --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '[.reconciled[] | select(.id=="piaware")] | length == 0'
}

@test "reconcile is fail-soft and records drift when re-acquire fails" {
    seed_managed "10.0" true
    export INSTALL_VERSION="9.9"     # the new install lands the wrong version
    run "$APLAGG" reconcile --json
    [ "$status" -eq 0 ]              # the loop never aborts
    echo "$output" | jq -e '.reconciled[] | select(.id=="piaware") | .status=="failed"'
    # Old version stays installed and is stamped as a reconcile error → still drift.
    jq -e '.installed_version=="10.0" and (.reconcile_error|type=="object")' "$AGG_STATE_DIR/piaware.json"
}
