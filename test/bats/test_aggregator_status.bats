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
}

teardown() {
    [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

@test "status on a fresh device reports fr24 not_installed and available" {
    export AGG_DECODER_STATE=up
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.protocol_version == 1'
    echo "$output" | jq -e '.aggregators | length == 1'
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

@test "list is an alias for status" {
    export AGG_DECODER_STATE=down
    run "$APLAGG" list --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators | length == 1'
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
