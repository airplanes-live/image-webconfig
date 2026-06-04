#!/usr/bin/env bats
#
# Unit tests for `apl-aggregator enable fr24`, which is fire-and-forget: the
# verb validates synchronously then launches a detached worker (systemd-run)
# that does the slow vendor acquire/signup and reports through the progress
# overlay. Here AGG_SYSTEMD_RUN is stubbed to run the worker INLINE so the whole
# orchestration (validation, ini, unit enable, state, secret handling, progress
# overlay, reconciliation) is exercised in CI. The real fetch+signup+systemd
# mechanism is validated on a feeder.

setup() {
    command -v jq >/dev/null || skip "jq not installed"
    APLAGG="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-aggregator"
    [ -x "$APLAGG" ] || skip "apl-aggregator missing"

    WORK="$(mktemp -d)"
    export AGG_STATE_DIR="$WORK/state"
    export AGG_INSTALL_ROOT="$WORK/install"
    export AGG_STATE_HOME="$WORK/var"
    export AGG_DESC_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes-webconfig/aggregators"
    export AGG_LOCK="$WORK/aggregator.lock"
    export AGG_REQ_DIR="$WORK/req"
    export AGG_ENABLE_STATE="$WORK/enable.state"
    export AGG_FR24_BIN="$WORK/install/fr24/fr24feed"
    export AGG_FR24_INI="$WORK/var/fr24/fr24feed.ini"
    export AGG_DECODER_STATE=up
    export AGG_LIVENESS_WAIT=0
    export AGG_SELF="$APLAGG"
    export AGG_ENABLE_GRACE=10
    mkdir -p "$AGG_STATE_DIR"

    # systemctl stub: logs every call; is-active reports the enable unit per
    # ENABLE_UNIT_STATE (default inactive so enable isn't self-blocked) and any
    # other unit as active.
    export SYSTEMCTL_LOG="$WORK/systemctl.log"
    export ENABLE_UNIT_STATE=inactive
    SYSTEMCTL="$WORK/fake-systemctl"
    cat > "$SYSTEMCTL" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
if [[ "$1" == is-active ]]; then
    case "$2" in
        *aggregator-enable*) echo "${ENABLE_UNIT_STATE:-inactive}" ;;
        *) echo active ;;
    esac
    exit 0
fi
exit 0
EOF
    chmod +x "$SYSTEMCTL"
    export AGG_SYSTEMCTL="$SYSTEMCTL"

    # systemd-run stub: strip the leading --flags and run the worker INLINE,
    # then exit 0 — mimicking systemd-run's "launched, returns immediately"
    # semantics regardless of the worker's eventual outcome.
    SR="$WORK/fake-systemd-run"
    cat > "$SR" <<'EOF'
#!/usr/bin/env bash
while [[ "$1" == --* ]]; do shift; done
"$@"
exit 0
EOF
    chmod +x "$SR"
    export AGG_SYSTEMD_RUN="$SR"

    # Default acquire stub: drop a fake fr24feed binary at the requested dest.
    ACQ="$WORK/fake-acquire"
    cat > "$ACQ" <<'EOF'
#!/usr/bin/env bash
install -D -m 0755 /dev/null "$1"
EOF
    chmod +x "$ACQ"
    export AGG_FR24_ACQUIRE_OVERRIDE="$ACQ"

    # Default signup stub: write an ini carrying a minted key at the requested path.
    SIGN="$WORK/fake-signup"
    cat > "$SIGN" <<'EOF'
#!/usr/bin/env bash
mkdir -p "$(dirname "$1")"
cat > "$1" <<INI
receiver="beast-tcp"
host="127.0.0.1:30005"
fr24key="SIGNEDUPKEY999"
mlat="no"
INI
EOF
    chmod +x "$SIGN"
    export AGG_FR24_SIGNUP_OVERRIDE="$SIGN"
}

teardown() {
    [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

agg() {
    # $1 = verb, $2 = JSON request piped on stdin.
    run bash -c 'printf "%s" "$3" | "$1" "$2" --json' _ "$APLAGG" "$1" "$2"
}

overlay_status() { jq -r '.status // ""' "$AGG_ENABLE_STATE" 2>/dev/null; }
overlay_code() { jq -r '.error_code // ""' "$AGG_ENABLE_STATE" 2>/dev/null; }

# --- the accepted contract -------------------------------------------------

# The accepted contract. (True non-blocking / launch-to-worker-window behaviour
# rides on real systemd-run and is validated on a feeder, not here — the bats
# systemd-run stub runs the worker inline for deterministic outcome assertions.)
@test "enable returns accepted with a request id" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"PROVIDEDKEY1"}}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result == "accepted" and .id == "fr24" and (.request_id | length > 0)'
    # The sharing key must never appear in the accepted envelope.
    ! echo "$output" | grep -q PROVIDEDKEY1
}

# --- worker (run inline by the stub) outcomes ------------------------------

@test "enable with a provided key writes the ini, enables the unit, persists state" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"PROVIDEDKEY1"}}'
    [ "$status" -eq 0 ]
    grep -q 'fr24key="PROVIDEDKEY1"' "$AGG_FR24_INI"
    grep -q 'host="127.0.0.1:30005"' "$AGG_FR24_INI"
    grep -q 'enable --now airplanes-aggregator@fr24.service' "$SYSTEMCTL_LOG"
    jq -e '.enabled == true and .mlat_enabled == false and .fields.sharing_key == "PROVIDEDKEY1"' "$AGG_STATE_DIR/fr24.json"
    [ "$(stat -c '%a' "$AGG_STATE_DIR/fr24.json")" = "600" ]
    # Terminal overlay is "done"; the secret spool is gone.
    [ "$(overlay_status)" = "done" ]
    [ -z "$(ls -A "$AGG_REQ_DIR" 2>/dev/null)" ]
}

@test "enable without a key runs signup and stores the minted key" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c"}}'
    [ "$status" -eq 0 ]
    grep -q 'fr24key="SIGNEDUPKEY999"' "$AGG_FR24_INI"
    jq -e '.fields.sharing_key == "SIGNEDUPKEY999"' "$AGG_STATE_DIR/fr24.json"
    [ "$(overlay_status)" = "done" ]
}

@test "a worker vendor-acquire failure lands in the progress overlay" {
    printf '#!/usr/bin/env bash\nexit 1\n' > "$WORK/fail-acquire"
    chmod +x "$WORK/fail-acquire"
    export AGG_FR24_ACQUIRE_OVERRIDE="$WORK/fail-acquire"
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"VALIDKEY12"}}'
    [ "$status" -eq 0 ]   # launch accepted
    echo "$output" | jq -e '.result == "accepted"'
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "acquire_failed" ]
}

@test "a worker signup that yields no key lands failed in the overlay" {
    printf '#!/usr/bin/env bash\n: # writes no ini\n' > "$WORK/empty-signup"
    chmod +x "$WORK/empty-signup"
    export AGG_FR24_SIGNUP_OVERRIDE="$WORK/empty-signup"
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c"}}'
    [ "$status" -eq 0 ]
    [ "$(overlay_status)" = "failed" ]
    [ "$(overlay_code)" = "signup_failed" ]
}

# --- synchronous validation (no worker launched) ---------------------------

@test "enable rejects a missing or invalid email synchronously" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
    [ ! -f "$AGG_ENABLE_STATE" ]   # no overlay, no worker
}

@test "enable rejects a malformed sharing key synchronously" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"bad key!"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
}

@test "enable rejects out-of-range geo synchronously" {
    agg enable '{"id":"fr24","lat":999,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"VALIDKEY12"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
}

@test "enable fails cleanly when the local decoder is unreachable" {
    export AGG_DECODER_STATE=down
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"VALIDKEY12"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "decoder_unavailable"'
    [ ! -f "$AGG_ENABLE_STATE" ]
}

# --- single-in-flight guard ------------------------------------------------

@test "a second enable while the worker unit is active is rejected" {
    export ENABLE_UNIT_STATE=active
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"VALIDKEY12"}}'
    [ "$status" -eq 4 ]
    echo "$output" | jq -e '.error_code == "lock_timeout"'
}

@test "disable is rejected while an enable worker is active" {
    export ENABLE_UNIT_STATE=active
    seed_state() { printf '%s' '{"schema_version":1,"enabled":false,"fields":{}}' > "$AGG_STATE_DIR/fr24.json"; }
    seed_state
    agg disable '{"id":"fr24"}'
    [ "$status" -eq 4 ]
    echo "$output" | jq -e '.error_code == "lock_timeout"'
}

# --- status overlay + reconciliation ---------------------------------------

@test "status reports installing while a recent worker is active" {
    export ENABLE_UNIT_STATE=active
    printf '%s' "{\"id\":\"fr24\",\"request_id\":\"r1\",\"started_at\":$(date +%s),\"status\":\"running\",\"step\":\"acquiring\"}" > "$AGG_ENABLE_STATE"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].state == "installing" and .aggregators[0].enable.step == "acquiring"'
}

@test "status reconciles a dead worker to failed past the grace window" {
    export ENABLE_UNIT_STATE=inactive
    # running overlay, but started well beyond the grace window and the unit is
    # not active → the worker died without writing a terminal state.
    printf '%s' '{"id":"fr24","request_id":"r1","started_at":1,"status":"running","step":"acquiring"}' > "$AGG_ENABLE_STATE"
    run "$APLAGG" status --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].state == "failed" and .aggregators[0].enable.status == "failed"'
}

# --- the worker entrypoint directly ----------------------------------------

@test "__enable-worker consumes the spool, enables, and deletes the secret file" {
    mkdir -p "$AGG_REQ_DIR"
    printf '%s' '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"WORKERKEY01"}}' \
        > "$AGG_REQ_DIR/req1.json"
    run "$APLAGG" __enable-worker req1 fr24
    [ "$status" -eq 0 ]
    jq -e '.enabled == true and .fields.sharing_key == "WORKERKEY01"' "$AGG_STATE_DIR/fr24.json"
    [ "$(overlay_status)" = "done" ]
    [ ! -f "$AGG_REQ_DIR/req1.json" ]   # secret spool unlinked
}

@test "__enable-worker with a missing spool records a failed overlay attributed to the id" {
    run "$APLAGG" __enable-worker nosuchreq fr24
    [ "$status" -eq 3 ]
    [ "$(overlay_status)" = "failed" ]
    # The overlay must carry the adapter id so status surfaces it (not id:"").
    [ "$(jq -r '.id' "$AGG_ENABLE_STATE")" = "fr24" ]
}
