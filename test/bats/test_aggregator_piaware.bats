#!/usr/bin/env bats
#
# Unit tests for the piaware (FlightAware) apt-repo adapter in apl-aggregator.
# Every system touchpoint is stubbed via the AGG_* seams: apt-get, dpkg-query,
# piaware-config, gpg, systemctl, systemd-run (runs the enable worker inline),
# and the os-release / apt-path / feeder-id-cache locations. The real apt +
# piaware install is validated end-to-end on a trixie/arm64 feeder, not here.

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

    printf 'VERSION_ID="13"\nVERSION_CODENAME=trixie\n' > "$WORK/os-release"
    export AGG_OS_RELEASE="$WORK/os-release"

    export AGG_KEYRING_DIR="$WORK/keyrings"
    export AGG_APT_SOURCES_DIR="$WORK/sources.list.d"
    export AGG_APT_PREFS_DIR="$WORK/preferences.d"
    export AGG_POLICY_RCD="$WORK/policy-rc.d"
    export AGG_PIAWARE_CONF="$WORK/piaware.conf"
    # Cache file lives under a dedicated dir — teardown rm -rf's its PARENT, so it
    # must not be $WORK itself (production path is /var/cache/piaware/feeder_id).
    mkdir -p "$WORK/cache/piaware"
    export AGG_PIAWARE_FEEDER_ID_FILE="$WORK/cache/piaware/feeder_id"

    export PKG_MARK="$WORK/pkg-installed"
    export APT_LOG="$WORK/apt.log";        : > "$APT_LOG"
    export PIAWARE_CONFIG_LOG="$WORK/piaware-config.log"; : > "$PIAWARE_CONFIG_LOG"
    export APT_SIM_REMOVE=""        # set → `-s install` emits a Remv line
    export APT_SIM_INST_DENY=""     # set → `-s install` emits an extra Inst <pkg>

    # dpkg-query: installed iff the marker file exists.
    DQ="$WORK/dpkg-query"
    cat > "$DQ" <<'EOF'
#!/usr/bin/env bash
[ -f "$PKG_MARK" ] && { printf 'ii '; exit 0; }
exit 1
EOF
    chmod +x "$DQ"; export AGG_DPKG_QUERY="$DQ"

    export SYSTEMCTL_LOG="$WORK/systemctl.log"; : > "$SYSTEMCTL_LOG"
    export ENABLE_UNIT_STATE=inactive
    SC="$WORK/systemctl"
    cat > "$SC" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
if [[ "$1" == is-active ]]; then
    case "$2" in
        *aggregator-enable*) echo "${ENABLE_UNIT_STATE:-inactive}" ;;
        piaware.service) [ -f "$PKG_MARK" ] && echo active || echo inactive ;;
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

    # gpg stub: --dearmor copies stdin→stdout; --list-keys emits the pinned fpr.
    GPG="$WORK/gpg"
    cat > "$GPG" <<'EOF'
#!/usr/bin/env bash
if printf '%s\n' "$@" | grep -q -- '--dearmor'; then cat; exit 0; fi
if printf '%s\n' "$@" | grep -q -- '--list-keys'; then
    echo 'fpr:::::::::5DAC1E3EF2234DD4ED09FA69671AD055972C325B:'; exit 0
fi
exit 0
EOF
    chmod +x "$GPG"; export AGG_GPG="$GPG"

    # apt-get stub: records argv + the no-restart env, simulates install/autoremove,
    # and on a real install marks the package installed + provisions a feeder-id.
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
            echo "Inst piaware (11.0 apt.svc.flightaware.com [arm64])"
            echo "Inst tcl (8.6 Debian:13 [arm64])"
            exit 0
        fi
        : > "$PKG_MARK"
        printf 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\n' > "$AGG_PIAWARE_FEEDER_ID_FILE"
        exit 0 ;;
    autoremove)
        [ "$sim" = 1 ] && { echo "Purg tcl [8.6]"; exit 0; }
        exit 0 ;;
    purge) rm -f "$PKG_MARK"; exit 0 ;;
esac
exit 0
EOF
    chmod +x "$APT"; export AGG_APT_GET="$APT"

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

# --- status ----------------------------------------------------------------

@test "status lists piaware as available + not_installed on trixie/arm64" {
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="piaware")
        | .acquire_method=="apt-repo" and .available==true and .state=="not_installed"
        and .mlat_capable==true and .mlat_default==true'
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

# --- enable (worker run inline by the systemd-run stub) --------------------

@test "enable installs piaware, configures the receiver + MLAT, starts it, persists feeder-id" {
    agg enable '{"id":"piaware","fields":{}}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result=="accepted" and .id=="piaware" and (.request_id|length>0)'
    [ "$(overlay_status)" = "done" ]
    [ -f "$PKG_MARK" ]
    grep -q 'receiver-type other' "$PIAWARE_CONFIG_LOG"
    grep -q 'receiver-host 127.0.0.1' "$PIAWARE_CONFIG_LOG"
    grep -q 'receiver-port 30005' "$PIAWARE_CONFIG_LOG"
    grep -q 'allow-mlat yes' "$PIAWARE_CONFIG_LOG"
    grep -q 'enable --now piaware.service' "$SYSTEMCTL_LOG"
    jq -e '.enabled==true and .mlat_enabled==true and .fields.feeder_id=="aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"' "$AGG_STATE_DIR/piaware.json"
    [ "$(managed '.we_installed')" = "true" ]
    echo "$(managed '.installed_pkgs|join(" ")')" | grep -q piaware
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
    # The provided reclaim id is authoritative — the stale cache id the apt stub
    # wrote must not win.
    jq -e '.fields.feeder_id=="11111111-2222-4333-8444-555555555555"' "$AGG_STATE_DIR/piaware.json"
}

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

# --- enable: synchronous rejections (no worker) ----------------------------

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

@test "reset purges piaware + removes the apt files it created" {
    agg enable '{"id":"piaware","fields":{}}'; [ "$status" -eq 0 ]
    [ -f "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources" ]
    agg reset '{"id":"piaware"}'
    [ "$status" -eq 0 ]
    grep -q 'disable --now piaware.service' "$SYSTEMCTL_LOG"
    grep -q 'purge -y piaware' "$APT_LOG"
    [ ! -f "$PKG_MARK" ]
    [ ! -f "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources" ]
    [ ! -f "$AGG_APT_PREFS_DIR/airplanes-flightaware.pref" ]
    [ ! -f "$AGG_PIAWARE_CONF" ]
    [ ! -f "$AGG_STATE_DIR/piaware.json" ]
    [ ! -f "$AGG_STATE_DIR/piaware.managed.json" ]
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

@test "reset cleans the apt source/pin/keyring + policy-rc.d a FAILED acquire left behind" {
    # An acquire that aborts at the simulate has written the apt files + the
    # install guard but never set we_installed; reset must still remove them so a
    # failed enable can't strand a global policy-rc.d or third-party apt source.
    export APT_SIM_REMOVE=some-package
    agg enable '{"id":"piaware","fields":{}}'; [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "failed" ]
    [ -f "$AGG_POLICY_RCD" ]
    [ -f "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources" ]
    [ "$(jq -r '.we_installed // false' "$AGG_STATE_DIR/piaware.managed.json")" != "true" ]
    unset APT_SIM_REMOVE
    agg reset '{"id":"piaware"}'
    [ "$status" -eq 0 ]
    [ ! -f "$AGG_POLICY_RCD" ]
    [ ! -f "$AGG_APT_SOURCES_DIR/airplanes-flightaware.sources" ]
    [ ! -f "$AGG_APT_PREFS_DIR/airplanes-flightaware.pref" ]
    [ ! -f "$AGG_KEYRING_DIR/airplanes-flightaware-archive-keyring.gpg" ]
    ! grep -q 'purge -y piaware' "$APT_LOG"          # nothing was installed to purge
}
