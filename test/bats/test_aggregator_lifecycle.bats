#!/usr/bin/env bats
#
# Unit tests for the mutating `apl-aggregator` verbs (set / disable / reset):
# strict request parsing, registry-checked ids, atomic 0600 state writes, the
# flock guard, and teardown. systemctl is stubbed via AGG_SYSTEMCTL; the real
# shipped fr24.desc is used so a valid id resolves.

setup() {
    command -v jq >/dev/null || skip "jq not installed"
    command -v flock >/dev/null || skip "flock not installed"

    APLAGG="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-aggregator"
    [ -x "$APLAGG" ] || skip "apl-aggregator missing"

    WORK="$(mktemp -d)"
    export AGG_STATE_DIR="$WORK/state"
    export AGG_INSTALL_ROOT="$WORK/install"
    export AGG_ENABLE_STATE="$WORK/enable.state"
    export AGG_REQ_DIR="$WORK/req"
    export AGG_DESC_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes-webconfig/aggregators"
    export AGG_LOCK="$WORK/aggregator.lock"
    mkdir -p "$AGG_INSTALL_ROOT"

    # Record systemctl invocations so disable/reset wiring can be asserted.
    SYSTEMCTL="$WORK/fake-systemctl"
    cat > "$SYSTEMCTL" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$WORK/systemctl.log"
exit 0
EOF
    chmod +x "$SYSTEMCTL"
    export AGG_SYSTEMCTL="$SYSTEMCTL"
}

teardown() {
    [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

# run the helper with a JSON request piped on stdin; capture status+output.
agg() {
    # $1 = verb, $2 = JSON request piped on stdin.
    run bash -c 'printf "%s" "$3" | "$1" "$2" --json' _ "$APLAGG" "$1" "$2"
}

@test "set merges mlat_enabled and fields into 0600 state" {
    agg set '{"id":"fr24","mlat_enabled":true,"fields":{"email":"a@b.c"}}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result == "ok" and .id == "fr24"'
    [ "$(stat -c '%a' "$AGG_STATE_DIR/fr24.json")" = "600" ]
    jq -e '.mlat_enabled == true and .fields.email == "a@b.c" and .schema_version == 1' "$AGG_STATE_DIR/fr24.json"
}

@test "set merges without dropping previously stored fields" {
    agg set '{"id":"fr24","fields":{"email":"a@b.c"}}'
    [ "$status" -eq 0 ]
    agg set '{"id":"fr24","fields":{"sharing_key":"K"}}'
    [ "$status" -eq 0 ]
    jq -e '.fields.email == "a@b.c" and .fields.sharing_key == "K"' "$AGG_STATE_DIR/fr24.json"
}

@test "set rejects a non-boolean mlat_enabled" {
    agg set '{"id":"fr24","mlat_enabled":"yes"}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
}

@test "an unknown id is not_found" {
    agg set '{"id":"nope","mlat_enabled":true}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "not_found"'
}

@test "a non-object request body is a parse_error" {
    agg set '["not","an","object"]'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "parse_error"'
}

@test "an empty request body is a parse_error" {
    agg set ''
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "parse_error"'
}

@test "disable flips enabled=false and disables the unit" {
    agg set '{"id":"fr24","mlat_enabled":true}'
    agg disable '{"id":"fr24"}'
    [ "$status" -eq 0 ]
    jq -e '.enabled == false' "$AGG_STATE_DIR/fr24.json"
    grep -q 'disable --now airplanes-aggregator@fr24.service' "$WORK/systemctl.log"
}

@test "reset removes state and the install dir" {
    agg set '{"id":"fr24","mlat_enabled":true}'
    mkdir -p "$AGG_INSTALL_ROOT/fr24"
    agg reset '{"id":"fr24"}'
    [ "$status" -eq 0 ]
    [ ! -e "$AGG_STATE_DIR/fr24.json" ]
    [ ! -e "$AGG_INSTALL_ROOT/fr24" ]
    grep -q 'disable --now airplanes-aggregator@fr24.service' "$WORK/systemctl.log"
}

@test "set fails with lock_timeout when the lock is already held" {
    export AGG_LOCK_TIMEOUT=1
    : >"$AGG_LOCK"
    exec 8>"$AGG_LOCK"
    flock -x 8
    agg set '{"id":"fr24","mlat_enabled":true}'
    flock -u 8
    exec 8>&-
    [ "$status" -eq 4 ]
    echo "$output" | jq -e '.error_code == "lock_timeout"'
}

@test "set refuses to overwrite corrupt existing state" {
    mkdir -p "$AGG_STATE_DIR"
    printf '%s' 'this is not json' > "$AGG_STATE_DIR/fr24.json"
    agg set '{"id":"fr24","mlat_enabled":true}'
    [ "$status" -eq 3 ]
    echo "$output" | jq -e '.error_code == "state_error"'
    # the corrupt file is left untouched for the operator to reset
    [ -f "$AGG_STATE_DIR/fr24.json" ]
}

@test "a multi-document request body is a parse_error" {
    agg set '{"id":"fr24"}{"id":"evil"}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "parse_error"'
}
