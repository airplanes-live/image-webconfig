#!/usr/bin/env bats
#
# Unit tests for `apl-aggregator status|list` — the read-only status report.
# systemctl is stubbed via AGG_SYSTEMCTL (driven by SVC_STATE); the local
# decoder probe is bypassed via AGG_DECODER_STATE. The real shipped fr24.desc
# descriptor is exercised so the descriptor and helper stay in sync.

setup() {
    command -v jq >/dev/null || skip "jq not installed"

    APLAGG="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-aggregator"
    [ -x "$APLAGG" ] || skip "apl-aggregator missing"

    WORK="$(mktemp -d)"
    export AGG_STATE_DIR="$WORK/state"
    export AGG_INSTALL_ROOT="$WORK/install"
    export AGG_ENABLE_STATE="$WORK/enable.state"
    export AGG_REQ_DIR="$WORK/req"
    mkdir -p "$AGG_STATE_DIR" "$AGG_INSTALL_ROOT"

    # Exercise the real shipped descriptors.
    export AGG_DESC_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes-webconfig/aggregators"

    # Stub systemctl: `is-active <unit>` prints $SVC_STATE (default inactive).
    SYSTEMCTL="$WORK/fake-systemctl"
    cat > "$SYSTEMCTL" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${SVC_STATE-inactive}"
[[ "${SVC_STATE-inactive}" == "active" ]] && exit 0 || exit 3
EOF
    chmod +x "$SYSTEMCTL"
    export AGG_SYSTEMCTL="$SYSTEMCTL"

    # dpkg-query stub: a package reads installed iff $AGG_TEST_DPKG_DIR/<pkg>
    # exists. Default-clean so fr24's external-install package probe is
    # deterministic on any host (the build box may or may not have fr24feed).
    export AGG_TEST_DPKG_DIR="$WORK/dpkg"; mkdir -p "$AGG_TEST_DPKG_DIR"
    DQ="$WORK/fake-dpkg-query"
    cat > "$DQ" <<'EOF'
#!/usr/bin/env bash
pkg="${@: -1}"
[ -f "$AGG_TEST_DPKG_DIR/$pkg" ] || exit 1
printf 'ii '
exit 0
EOF
    chmod +x "$DQ"; export AGG_DPKG_QUERY="$DQ"

    # Point fr24's external-install signals at temp paths so a real vendor
    # fr24feed on the build host can't leak into these tests; default-absent.
    mkdir -p "$WORK/fr24"
    export AGG_FR24_SYSTEM_BIN="$WORK/fr24/usr-bin-fr24feed"
    export AGG_FR24_SYSTEM_UNIT_PATHS="$WORK/fr24/fr24feed.service"
}

teardown() {
    [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

@test "status on a fresh device reports fr24 not_installed and available" {
    export AGG_DECODER_STATE=up
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.protocol_version == 1'
    # fr24 + piaware ship as the two adapters; fr24 sorts first (descriptor glob).
    echo "$output" | jq -e '.aggregators | length == 2'
    echo "$output" | jq -e '.aggregators[0].id == "fr24"'
    echo "$output" | jq -e '.aggregators[0].state == "not_installed"'
    echo "$output" | jq -e '.aggregators[0].available == true'
    echo "$output" | jq -e '.aggregators[0].enabled == false'
    echo "$output" | jq -e '.aggregators[0].configured == false'
    echo "$output" | jq -e '.aggregators[0].mlat_capable == false'
    echo "$output" | jq -e '.aggregators[0].decoder_reachable == true'
    echo "$output" | jq -e '.aggregators[0].secret_fields_present == []'
    echo "$output" | jq -e '.aggregators[0].fields | length == 2'
}

@test "status on a fresh device reports fr24 neither external nor managed" {
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="fr24")
        | .external_install==false and .managed_install==false'
}

@test "status flags a manually-installed fr24 binary as external_install" {
    : > "$AGG_FR24_SYSTEM_BIN"; chmod +x "$AGG_FR24_SYSTEM_BIN"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="fr24")
        | .external_install==true and .managed_install==false'
}

@test "status flags a vendor fr24feed.service unit as external_install" {
    : > "$AGG_FR24_SYSTEM_UNIT_PATHS"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="fr24") | .external_install==true'
}

@test "status flags an apt-installed fr24feed package as external_install" {
    : > "$AGG_TEST_DPKG_DIR/fr24feed"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="fr24") | .external_install==true'
}

@test "status reports our own fr24 install as managed_install, not external" {
    install -D -m 0755 /dev/null "$AGG_INSTALL_ROOT/fr24/fr24feed"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[] | select(.id=="fr24")
        | .managed_install==true and .external_install==false'
}

@test "list is an alias for status" {
    export AGG_DECODER_STATE=down
    run "$APLAGG" list --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators | length == 2'
    echo "$output" | jq -e '.aggregators[0].decoder_reachable == false'
}

@test "status reflects an enabled, configured adapter without leaking the secret" {
    export AGG_DECODER_STATE=up SVC_STATE=active
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":true,"mlat_enabled":false,"fields":{"email":"a@b.c","sharing_key":"TOPSECRET"}}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].state == "running"'
    echo "$output" | jq -e '.aggregators[0].enabled == true'
    echo "$output" | jq -e '.aggregators[0].configured == true'
    echo "$output" | jq -e '.aggregators[0].secret_fields_present == ["sharing_key"]'
    # The secret VALUE must never appear anywhere in the output.
    if echo "$output" | grep -q TOPSECRET; then
        echo "secret value leaked into status output" >&2
        return 1
    fi
}

@test "an installed-but-stopped adapter reports stopped" {
    export AGG_DECODER_STATE=up SVC_STATE=inactive
    # A non-empty install dir = installed; a bare dir reads as not_installed.
    mkdir -p "$AGG_INSTALL_ROOT/fr24"
    : > "$AGG_INSTALL_ROOT/fr24/fr24feed"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].state == "stopped"'
}

@test "missing --json is a usage error" {
    run "$APLAGG" status
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.result == "error"'
    echo "$output" | jq -e '.error_code == "usage_error"'
    echo "$output" | jq -e '.protocol_version == 1'
}

@test "unknown verb is a usage error" {
    run "$APLAGG" frobnicate --json
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "usage_error"'
}

@test "a string 'true' in state is not treated as a boolean (strict typing)" {
    export AGG_DECODER_STATE=up SVC_STATE=inactive
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":"true","mlat_enabled":"true"}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].enabled == false'
    echo "$output" | jq -e '.aggregators[0].configured_mlat_enabled == false'
    echo "$output" | jq -e '.aggregators[0].state == "not_installed"'
}

@test "malformed state json degrades to not-configured rather than failing" {
    export AGG_DECODER_STATE=up SVC_STATE=inactive
    printf '%s' 'this is not json' > "$AGG_STATE_DIR/fr24.json"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].enabled == false'
    echo "$output" | jq -e '.aggregators[0].state == "not_installed"'
}

@test "a descriptor whose id mismatches its filename fails loud" {
    BADDIR="$WORK/baddesc"
    mkdir -p "$BADDIR"
    cat > "$BADDIR/fr24.desc" <<'EOF'
AGG_ID="not-fr24"
AGG_DISPLAY_NAME="X"
AGG_ACQUIRE_METHOD="vendor-installer"
AGG_SERVICE_UNIT="x.service"
AGG_FIELDS_JSON='[]'
EOF
    export AGG_DESC_DIR="$BADDIR" AGG_DECODER_STATE=up
    # The descriptor-validation diagnostic goes to stderr; the JSON contract is
    # stdout-only (the webconfig reads them separately). Discard stderr so bats
    # does not merge it into $output.
    run bash -c '"$1" status --json 2>/dev/null' _ "$APLAGG"
    [ "$status" -eq 3 ]
    echo "$output" | jq -e '.result == "error"'
    echo "$output" | jq -e '.error_code == "state_error"'
}

@test "a well-formed reconcile_error surfaces in status" {
    export AGG_DECODER_STATE=up SVC_STATE=active
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":true,"mlat_enabled":false,"fields":{"email":"a@b.c","sharing_key":"K"},"reconcile_error":{"error_code":"acquire_failed","message":"could not download or verify fr24feed"}}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].reconcile_error.error_code == "acquire_failed"'
    echo "$output" | jq -e '.aggregators[0].reconcile_error.message == "could not download or verify fr24feed"'
}

@test "no reconcile_error key when state has none (e.g. after an ok reconcile)" {
    export AGG_DECODER_STATE=up SVC_STATE=active
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":true,"fields":{"email":"a@b.c","sharing_key":"K"}}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0] | has("reconcile_error") | not'
}

@test "a non-object reconcile_error is dropped from status" {
    export AGG_DECODER_STATE=up SVC_STATE=active
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":true,"fields":{"email":"a@b.c"},"reconcile_error":"boom"}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0] | has("reconcile_error") | not'
}

@test "a reconcile_error without a string error_code is dropped from status" {
    export AGG_DECODER_STATE=up SVC_STATE=active
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":true,"fields":{"email":"a@b.c"},"reconcile_error":{"error_code":123,"message":"x"}}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0] | has("reconcile_error") | not'
}

@test "reconcile_error message is length-capped" {
    export AGG_DECODER_STATE=up SVC_STATE=active
    long="$(printf 'x%.0s' {1..600})"
    cat > "$AGG_STATE_DIR/fr24.json" <<EOF
{"schema_version":1,"enabled":true,"fields":{"email":"a@b.c"},"reconcile_error":{"error_code":"state_error","message":"$long"}}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].reconcile_error.message | length == 300'
}

@test "a reconcile_error with a non-code-shaped error_code is dropped" {
    export AGG_DECODER_STATE=up SVC_STATE=active
    # Uppercase / spaces / punctuation are not valid error-code shape → drop.
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":true,"fields":{"email":"a@b.c"},"reconcile_error":{"error_code":"Big Scary <b>HTML</b>","message":"x"}}
EOF
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0] | has("reconcile_error") | not'
}

# --- real decoder probe (AGG_DECODER_STATE unset) ---------------------------
# The cases above bypass the probe via AGG_DECODER_STATE. These exercise the
# actual probe path through an AGG_NC stub, so we cover the graceful `nc -N`
# invocation without needing a live readsb on the build host. The stub
# advertises -N on `-h` (so _nc_supports_shutdown selects the graceful path)
# and records its argv; NC_RESULT picks connect success/failure.
_install_fake_nc() {
    export NC_ARGV_LOG="$WORK/nc.argv"; : >"$NC_ARGV_LOG"
    local nc="$WORK/fake-nc"
    cat > "$nc" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "-h" ]]; then
    # Mimic OpenBSD netcat: usage line plus a command-summary line for -N.
    printf 'usage: nc [-46CDdFhklNnrStUuvZz] ...\n\t-N\tshutdown(2) after EOF on stdin\n' >&2
    exit 1
fi
printf '%s\n' "$*" >>"$NC_ARGV_LOG"
[[ "${NC_RESULT:-0}" == "0" ]] && exit 0 || exit 1
EOF
    chmod +x "$nc"
    export AGG_NC="$nc"
}

@test "decoder probe: graceful nc -N reports reachable when connect succeeds" {
    _install_fake_nc
    export NC_RESULT=0          # connect ok
    unset AGG_DECODER_STATE     # exercise the real probe
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].decoder_reachable == true'
    # Probe used graceful shutdown against the configured endpoint.
    grep -q -- '-N 127.0.0.1 30005' "$NC_ARGV_LOG"
}

@test "decoder probe: reports not reachable when connect fails" {
    _install_fake_nc
    export NC_RESULT=1          # connect refused / timed out
    unset AGG_DECODER_STATE
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].decoder_reachable == false'
}
