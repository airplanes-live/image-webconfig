#!/usr/bin/env bats
#
# Unit tests for `apl-aggregator export | import` (identity backup/restore) and
# the enable state-fallback that lets a restored identity be reused.

setup() {
    command -v jq >/dev/null || skip "jq not installed"
    APLAGG="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-aggregator"
    [ -x "$APLAGG" ] || skip "apl-aggregator missing"

    WORK="$(mktemp -d)"
    export AGG_STATE_DIR="$WORK/state"
    export AGG_INSTALL_ROOT="$WORK/install"
    export AGG_ENABLE_STATE="$WORK/enable.state"
    export AGG_REQ_DIR="$WORK/req"
    export AGG_STATE_HOME="$WORK/var"
    export AGG_DESC_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes-webconfig/aggregators"
    export AGG_LOCK="$WORK/aggregator.lock"
    export AGG_FR24_BIN="$WORK/install/fr24/fr24feed"
    export AGG_FR24_INI="$WORK/var/fr24/fr24feed.ini"
    export AGG_DECODER_STATE=up
    export AGG_LIVENESS_WAIT=0
    mkdir -p "$AGG_STATE_DIR"

    SYSTEMCTL="$WORK/fake-systemctl"
    printf '#!/usr/bin/env bash\nexit 0\n' > "$SYSTEMCTL"; chmod +x "$SYSTEMCTL"
    export AGG_SYSTEMCTL="$SYSTEMCTL"

    ACQ="$WORK/fake-acquire"
    printf '#!/usr/bin/env bash\ninstall -D -m 0755 /dev/null "$1"\n' > "$ACQ"; chmod +x "$ACQ"
    export AGG_FR24_ACQUIRE_OVERRIDE="$ACQ"

    # enable is fire-and-forget: run the worker inline so the restored-key
    # reuse path is fully exercised in one synchronous call.
    export AGG_SELF="$APLAGG"
    SR="$WORK/fake-systemd-run"
    printf '#!/usr/bin/env bash\nwhile [[ "$1" == --* ]]; do shift; done\n"$@"\nexit 0\n' > "$SR"; chmod +x "$SR"
    export AGG_SYSTEMD_RUN="$SR"
}

teardown() { [ -n "${WORK:-}" ] && rm -rf "$WORK"; }

agg() { run bash -c 'printf "%s" "$3" | "$1" "$2" --json' _ "$APLAGG" "$1" "$2"; }

seed_fr24() {
    cat > "$AGG_STATE_DIR/fr24.json" <<EOF
{"schema_version":1,"enabled":${1:-false},"mlat_enabled":false,"fields":{"email":"a@b.c","sharing_key":"BACKUPKEY42"}}
EOF
}

@test "export with no adapters yields an empty backup" {
    run "$APLAGG" export --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.kind == "aggregator-backup" and .schema_version == 1 and (.aggregators | length == 0)'
}

@test "export emits identities including the secret value" {
    seed_fr24 false
    run "$APLAGG" export --json
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators.fr24.fields.email == "a@b.c" and .aggregators.fr24.fields.sharing_key == "BACKUPKEY42"'
}

@test "export -> import round-trips onto a clean device" {
    seed_fr24 false
    run "$APLAGG" export --json
    blob="$output"
    rm -f "$AGG_STATE_DIR/fr24.json"
    agg import "$blob"
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.result == "ok" and .imported == 1'
    # restored, but NOT auto-enabled
    jq -e '.enabled == false and .fields.sharing_key == "BACKUPKEY42"' "$AGG_STATE_DIR/fr24.json"
}

@test "import refuses a non-backup body" {
    agg import '{"hello":"world"}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "parse_error"'
}

@test "import refuses an unknown adapter" {
    agg import '{"kind":"aggregator-backup","schema_version":1,"aggregators":{"nope":{"fields":{}}}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
}

@test "import refuses an invalid fr24 key in the blob" {
    agg import '{"kind":"aggregator-backup","schema_version":1,"aggregators":{"fr24":{"fields":{"sharing_key":"bad key"}}}}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "parse_error"'
}

@test "import refuses to overwrite an enabled adapter" {
    seed_fr24 true
    agg import '{"kind":"aggregator-backup","schema_version":1,"aggregators":{"fr24":{"fields":{"email":"x@y.z","sharing_key":"NEWKEY99"}}}}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.error_code == "rejected"'
    # the enabled adapter's state is untouched
    jq -e '.fields.sharing_key == "BACKUPKEY42"' "$AGG_STATE_DIR/fr24.json"
}

@test "import rejects a structurally invalid backup" {
    agg import '{"kind":"aggregator-backup","schema_version":1,"aggregators":{"fr24":"not-an-object"}}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.error_code == "parse_error"'
}

@test "import fails when an existing adapter's state is corrupt" {
    printf '%s' 'not json at all' > "$AGG_STATE_DIR/fr24.json"
    agg import '{"kind":"aggregator-backup","schema_version":1,"aggregators":{"fr24":{"fields":{"email":"x@y.z","sharing_key":"NEWKEY99"}}}}'
    [ "$status" -eq 3 ]
    echo "$output" | jq -e '.error_code == "state_error"'
}

@test "enable reuses a restored key without re-entry" {
    # Simulate a restore: state seeded with identities, enabled=false.
    seed_fr24 false
    # enable with NO key/email in the request -> should reuse the seeded ones.
    agg enable '{"id":"fr24","lat":47.0,"lon":8.0,"alt":400,"fields":{}}'
    [ "$status" -eq 0 ]
    grep -q 'fr24key="BACKUPKEY42"' "$AGG_FR24_INI"
    jq -e '.enabled == true and .fields.email == "a@b.c"' "$AGG_STATE_DIR/fr24.json"
}
