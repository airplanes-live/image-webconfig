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
    export AGG_SELF="$APLAGG"
    mkdir -p "$AGG_INSTALL_ROOT"

    # Record systemctl invocations so disable/reset wiring can be asserted.
    # is-active reports inactive so the shared worker unit never reads as a
    # mutation-in-progress and self-blocks the next verb.
    SYSTEMCTL="$WORK/fake-systemctl"
    cat > "$SYSTEMCTL" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$WORK/systemctl.log"
[[ "\$1" == is-active ]] && echo inactive
exit 0
EOF
    chmod +x "$SYSTEMCTL"
    export AGG_SYSTEMCTL="$SYSTEMCTL"

    # set/reset are fire-and-forget: the verb validates then launches a detached
    # worker via systemd-run. Stub it to run the worker INLINE so the whole
    # transaction (state merge / teardown + overlay) is exercised deterministically.
    SR="$WORK/fake-systemd-run"
    cat > "$SR" <<'EOF'
#!/usr/bin/env bash
while [[ "$1" == --* ]]; do shift; done
"$@"
exit 0
EOF
    chmod +x "$SR"
    export AGG_SYSTEMD_RUN="$SR"
}

teardown() {
    [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

# run the helper with a JSON request piped on stdin; capture status+output.
agg() {
    # $1 = verb, $2 = JSON request piped on stdin.
    run bash -c 'printf "%s" "$3" | "$1" "$2" --json' _ "$APLAGG" "$1" "$2"
}

overlay_status() { jq -r '.status // ""' "$AGG_ENABLE_STATE" 2>/dev/null; }
overlay_code() { jq -r '.error_code // ""' "$AGG_ENABLE_STATE" 2>/dev/null; }

@test "set merges mlat_enabled and fields into 0600 state" {
    agg set '{"id":"fr24","mlat_enabled":true,"fields":{"email":"a@b.c"}}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result == "accepted" and .id == "fr24"'
    [ "$(overlay_status)" = "done" ]
    [ "$(stat -c '%a' "$AGG_STATE_DIR/fr24.json")" = "600" ]
    jq -e '.mlat_enabled == true and .fields.email == "a@b.c" and .schema_version == 1' "$AGG_STATE_DIR/fr24.json"
}

@test "set merges without dropping previously stored fields" {
    agg set '{"id":"fr24","fields":{"email":"a@b.c"}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    agg set '{"id":"fr24","fields":{"sharing_key":"K"}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    jq -e '.fields.email == "a@b.c" and .fields.sharing_key == "K"' "$AGG_STATE_DIR/fr24.json"
}

# A key change via set must reach the vendor daemon: fr24feed only reads
# fr24key from its ini at startup, so set re-renders the ini and try-restarts
# the instance. Without this, set on a running fr24 was a silent no-op (state
# updated, daemon kept feeding with the old key).
@test "set with a changed fr24 sharing key re-renders the ini and restarts" {
    export AGG_FR24_INI="$WORK/fr24feed.ini"
    printf 'receiver="beast-tcp"\nhost="127.0.0.1:30005"\nfr24key="OLDKEY"\n' > "$AGG_FR24_INI"
    agg set '{"id":"fr24","fields":{"sharing_key":"NEWKEY"}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    grep -q 'fr24key="NEWKEY"' "$AGG_FR24_INI"
    grep -q 'try-restart airplanes-aggregator@fr24.service' "$WORK/systemctl.log"
}

@test "set with an unchanged fr24 sharing key leaves the ini and service alone" {
    export AGG_FR24_INI="$WORK/fr24feed.ini"
    printf 'receiver="beast-tcp"\nhost="127.0.0.1:30005"\nfr24key="SAMEKEY"\n' > "$AGG_FR24_INI"
    agg set '{"id":"fr24","fields":{"sharing_key":"SAMEKEY"}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    ! grep -q 'try-restart' "$WORK/systemctl.log"
}

@test "set on a never-rendered fr24 (no ini) stores fields without touching services" {
    export AGG_FR24_INI="$WORK/fr24feed.ini"
    agg set '{"id":"fr24","fields":{"sharing_key":"NEWKEY"}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "done" ]
    [ ! -e "$AGG_FR24_INI" ]
    ! grep -q 'try-restart' "$WORK/systemctl.log"
    jq -e '.fields.sharing_key == "NEWKEY"' "$AGG_STATE_DIR/fr24.json"
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
    [ "$status" -eq 0 ]
    mkdir -p "$AGG_INSTALL_ROOT/fr24"
    agg reset '{"id":"fr24"}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result == "accepted"'
    [ "$(overlay_status)" = "done" ]
    [ ! -e "$AGG_STATE_DIR/fr24.json" ]
    [ ! -e "$AGG_INSTALL_ROOT/fr24" ]
    grep -q 'disable --now airplanes-aggregator@fr24.service' "$WORK/systemctl.log"
}

@test "a worker launch failure rolls back the overlay + spool and reports busy" {
    agg set '{"id":"fr24","mlat_enabled":true}'
    [ "$status" -eq 0 ]
    # systemd-run that fails to launch the worker (e.g. the unit name is taken).
    printf '#!/usr/bin/env bash\nexit 1\n' > "$WORK/fail-run"
    chmod +x "$WORK/fail-run"
    export AGG_SYSTEMD_RUN="$WORK/fail-run"
    agg reset '{"id":"fr24"}'
    [ "$status" -eq 4 ]
    echo "$output" | jq -e '.error_code == "lock_timeout"'
    # The foreground rolled back ITS overlay + secret spool — nothing left stuck.
    [ -z "$(ls -A "$AGG_REQ_DIR" 2>/dev/null)" ]
    [ "$(overlay_status)" != "running" ]
    # The worker never ran, so the adapter state is untouched.
    [ -f "$AGG_STATE_DIR/fr24.json" ]
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

@test "set surfaces corrupt existing state as a failed overlay" {
    mkdir -p "$AGG_STATE_DIR"
    printf '%s' 'this is not json' > "$AGG_STATE_DIR/fr24.json"
    agg set '{"id":"fr24","mlat_enabled":true}'
    # The foreground accepts; the detached worker reports the failure in the overlay.
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result == "accepted"'
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "state_error" ]
    # the corrupt file is left untouched for the operator to reset
    [ -f "$AGG_STATE_DIR/fr24.json" ]
}

@test "a multi-document request body is a parse_error" {
    agg set '{"id":"fr24"}{"id":"evil"}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "parse_error"'
}
