#!/usr/bin/env bats
#
# Unit tests for `apl-aggregator enable fr24`. The two feeder-only operations —
# the vendor .deb fetch and the signup wizard — are stubbed via
# AGG_FR24_ACQUIRE_OVERRIDE / AGG_FR24_SIGNUP_OVERRIDE so the enable
# orchestration (validation, ini, unit enable, state, secret handling) is fully
# exercised in CI. The real fetch+signup mechanism is validated on a feeder.

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
    export AGG_FR24_BIN="$WORK/install/fr24/fr24feed"
    export AGG_FR24_INI="$WORK/var/fr24/fr24feed.ini"
    export AGG_DECODER_STATE=up

    SYSTEMCTL="$WORK/fake-systemctl"
    cat > "$SYSTEMCTL" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$WORK/systemctl.log"
exit 0
EOF
    chmod +x "$SYSTEMCTL"
    export AGG_SYSTEMCTL="$SYSTEMCTL"

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

@test "enable with a provided key writes the ini, enables the unit, persists state" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"PROVIDEDKEY1"}}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result == "ok" and .id == "fr24" and .sharing_key_present == true'
    grep -q 'fr24key="PROVIDEDKEY1"' "$AGG_FR24_INI"
    grep -q 'host="127.0.0.1:30005"' "$AGG_FR24_INI"
    grep -q 'enable --now airplanes-aggregator@fr24.service' "$WORK/systemctl.log"
    jq -e '.enabled == true and .mlat_enabled == false and .fields.sharing_key == "PROVIDEDKEY1"' "$AGG_STATE_DIR/fr24.json"
    [ "$(stat -c '%a' "$AGG_STATE_DIR/fr24.json")" = "600" ]
    # The key must never appear in the helper's stdout envelope.
    ! echo "$output" | grep -q PROVIDEDKEY1
}

@test "enable without a key runs signup and stores the minted key" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c"}}'
    [ "$status" -eq 0 ]
    grep -q 'fr24key="SIGNEDUPKEY999"' "$AGG_FR24_INI"
    jq -e '.fields.sharing_key == "SIGNEDUPKEY999"' "$AGG_STATE_DIR/fr24.json"
    ! echo "$output" | grep -q SIGNEDUPKEY999
}

@test "enable rejects a missing or invalid email" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
}

@test "enable rejects a malformed sharing key" {
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"bad key!"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
}

@test "enable rejects out-of-range geo" {
    agg enable '{"id":"fr24","lat":999,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"VALIDKEY12"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
}

@test "enable fails cleanly when the local decoder is unreachable" {
    export AGG_DECODER_STATE=down
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"VALIDKEY12"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "decoder_unavailable"'
}

@test "enable surfaces a vendor acquire failure" {
    printf '#!/usr/bin/env bash\nexit 1\n' > "$WORK/fail-acquire"
    chmod +x "$WORK/fail-acquire"
    export AGG_FR24_ACQUIRE_OVERRIDE="$WORK/fail-acquire"
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c","sharing_key":"VALIDKEY12"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "acquire_failed"'
}

@test "enable surfaces a signup that yields no key" {
    printf '#!/usr/bin/env bash\n: # writes no ini\n' > "$WORK/empty-signup"
    chmod +x "$WORK/empty-signup"
    export AGG_FR24_SIGNUP_OVERRIDE="$WORK/empty-signup"
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{"email":"a@b.c"}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "signup_failed"'
}
