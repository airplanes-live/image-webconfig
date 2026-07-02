#!/usr/bin/env bats
#
# Unit tests for `apl-aggregator detail` — the on-demand per-adapter status that
# carries status_detail. The status SOURCES are JSON: piaware's
# /run/piaware/status.json (seamed via AGG_PIAWARE_STATUS_JSON) and fr24feed's
# monitor.json API (seamed via AGG_FR24_MONITOR_URL, pointed at a file:// fixture
# here). systemctl is stubbed via AGG_SYSTEMCTL (SVC_STATE). The real shipped
# descriptors are exercised so descriptor + helper stay in sync.

setup() {
    command -v jq >/dev/null || skip "jq not installed"
    command -v curl >/dev/null || skip "curl not installed"
    APLAGG="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-aggregator"
    [ -x "$APLAGG" ] || skip "apl-aggregator missing"

    WORK="$(mktemp -d)"
    export AGG_STATE_DIR="$WORK/state"
    export AGG_INSTALL_ROOT="$WORK/install"
    export AGG_ENABLE_STATE="$WORK/enable.state"
    export AGG_REQ_DIR="$WORK/req"
    mkdir -p "$AGG_STATE_DIR" "$AGG_INSTALL_ROOT"
    export AGG_DESC_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes-webconfig/aggregators"

    SYSTEMCTL="$WORK/fake-systemctl"
    cat > "$SYSTEMCTL" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${SVC_STATE-inactive}"
[[ "${SVC_STATE-inactive}" == "active" ]] && exit 0 || exit 3
EOF
    chmod +x "$SYSTEMCTL"
    export AGG_SYSTEMCTL="$SYSTEMCTL"
    export AGG_DECODER_STATE=up
}

teardown() { [ -n "${WORK:-}" ] && rm -rf "$WORK"; }

# detail <id>: run the detail verb with {"id":id} on stdin.
detail() { "$APLAGG" detail --json <<<"{\"id\":\"$1\"}"; }

seed_piaware_enabled() {
    cat > "$AGG_STATE_DIR/piaware.json" <<'EOF'
{"schema_version":1,"enabled":true,"mlat_enabled":true,"fields":{"feeder_id":"8bb46ddf-d507-4c18-9724-0b5f94cba900"}}
EOF
}
seed_fr24_enabled() {
    cat > "$AGG_STATE_DIR/fr24.json" <<'EOF'
{"schema_version":1,"enabled":true,"mlat_enabled":false,"fields":{"email":"a@b.c","sharing_key":"KEY123"}}
EOF
}

# piaware status.json fixture (the real shape: per-component {status,message}).
# $1 = mlat status (default red, the normal pre-claim state). Includes
# unclaimed_feeder_id to prove the producer does NOT surface it.
stub_piaware_json() {
    cat > "$WORK/piaware-status.json" <<EOF
{"expiry":99999999999999,
 "piaware":{"status":"green","message":"PiAware 11.0 is running"},
 "adept":{"status":"green","message":"Connected to FlightAware and logged in"},
 "radio":{"status":"green","message":"Received Mode S data recently"},
 "mlat":{"status":"${1:-red}","message":"Multilateration is not enabled"},
 "unclaimed_feeder_id":"8bb46ddf-d507-4c18-9724-0b5f94cba900"}
EOF
    export AGG_PIAWARE_STATUS_JSON="$WORK/piaware-status.json"
}

# fr24 monitor.json fixture, served to the producer via a file:// URL.
# $1 = rx_connected (default 1), $2 = feed_num_ac_tracked (default 7),
# $3 = feed_status (default connected). feed_last_config_result stays "error" on
# purpose: the verdict must key on feed_status, never on that stale config field.
stub_fr24_json() {
    cat > "$WORK/monitor.json" <<EOF
{"rx_connected":"${1:-1}","feed_num_ac_tracked":"${2:-7}","feed_status":"${3:-connected}","feed_last_config_result":"error","fr24key":"KEY123"}
EOF
    export AGG_FR24_MONITOR_URL="file://$WORK/monitor.json"
}

@test "detail wraps a single adapter in the status envelope" {
    seed_piaware_enabled; stub_piaware_json; export SVC_STATE=active
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.protocol_version == 1'
    echo "$output" | jq -e '.aggregators | length == 1'
    echo "$output" | jq -e '.aggregators[0].id == "piaware"'
}

@test "detail of an unknown id is not_found" {
    run detail nope
    echo "$output" | jq -e '.result == "error"'
    echo "$output" | jq -e '.error_code == "not_found"'
}

@test "piaware detail maps status.json to fixed-text rows; mlat-red is na; nothing vendor-derived leaks" {
    seed_piaware_enabled; stub_piaware_json; export SVC_STATE=active
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="FlightAware") | .value=="Connected" and .severity=="ok"'
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="ADS-B") | .value=="Receiving" and .severity=="ok"'
    # mlat "red" is the normal unclaimed state -> na, never err.
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="MLAT") | .value=="Off" and .severity=="na"'
    echo "$output" | jq -e '.aggregators[0].status_detail | all(.severity != "err")'
    # Fixed text only: the raw vendor message and the unclaimed_feeder_id in
    # status.json must never reach the response.
    if echo "$output" | grep -qE 'logged in|8bb46ddf'; then echo "vendor message / feeder id leaked" >&2; return 1; fi
}

@test "piaware mlat green maps to MLAT Synchronized/ok" {
    seed_piaware_enabled; stub_piaware_json green; export SVC_STATE=active
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="MLAT") | .value=="Synchronized" and .severity=="ok"'
}

@test "piaware adept red maps FlightAware to err" {
    seed_piaware_enabled; export SVC_STATE=active
    cat > "$WORK/piaware-status.json" <<'EOF'
{"adept":{"status":"red","message":"Not connected to FlightAware"},
 "radio":{"status":"green","message":"Received Mode S data recently"}}
EOF
    export AGG_PIAWARE_STATUS_JSON="$WORK/piaware-status.json"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="FlightAware") | .value=="Not connected" and .severity=="err"'
}

@test "fr24 detail surfaces receiver + aircraft from monitor.json" {
    seed_fr24_enabled; stub_fr24_json 1 7; export SVC_STATE=active
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Receiver") | .value=="Connected" and .severity=="ok"'
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Aircraft tracked") | .value=="7" and .severity=="ok"'
}

@test "fr24 rx_connected=0 maps receiver to err" {
    seed_fr24_enabled; stub_fr24_json 0 0; export SVC_STATE=active
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Receiver") | .value=="Not connected" and .severity=="err"'
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Aircraft tracked") | .value=="0" and .severity=="na"'
}

@test "fr24 feed_status=connected surfaces a Feed Connected row despite a stale config error" {
    seed_fr24_enabled; stub_fr24_json 1 7 connected; export SVC_STATE=active
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Feed") | .value=="Connected" and .severity=="ok"'
}

@test "fr24 feed_status=disconnected surfaces a Feed Not feeding err row (receiver stays ok)" {
    seed_fr24_enabled; stub_fr24_json 1 7 disconnected; export SVC_STATE=active
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Feed") | .value=="Not feeding" and .severity=="err"'
    # The LOCAL receiver row stays healthy — proving Feed reflects the upstream link.
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Receiver") | .value=="Connected" and .severity=="ok"'
}

# Rejected sharing key (validated against 1.0.53): absent feed_status + config
# error gets an actionable fixed-text Feed row; the vendor's own message stays
# in the logs, never in a row value.
@test "fr24 absent feed_status + config error surfaces a rejected Feed row" {
    seed_fr24_enabled; export SVC_STATE=active
    cat > "$WORK/monitor.json" <<'EOF'
{"rx_connected":"1","feed_num_ac_tracked":"0","feed_last_config_result":"error","feed_last_config_info":"Not found, check your key!"}
EOF
    export AGG_FR24_MONITOR_URL="file://$WORK/monitor.json"
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Feed") | .value=="Rejected \u2014 check your sharing key" and .severity=="err"'
    echo "$output" | jq -e '[.aggregators[0].status_detail[].value] | all(contains("Not found") | not)'
}

@test "fr24 monitor without feed_status omits the Feed row" {
    seed_fr24_enabled; export SVC_STATE=active
    cat > "$WORK/monitor.json" <<'EOF'
{"rx_connected":"1","feed_num_ac_tracked":"7"}
EOF
    export AGG_FR24_MONITOR_URL="file://$WORK/monitor.json"
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail | all(.label != "Feed")'
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Receiver") | .value=="Connected"'
}

@test "fr24 detail never surfaces the raw feed_status_message" {
    seed_fr24_enabled; export SVC_STATE=active
    cat > "$WORK/monitor.json" <<'EOF'
{"rx_connected":"1","feed_num_ac_tracked":"7","feed_status":"disconnected","feed_status_message":"acct-98765 near 51.5,-0.12"}
EOF
    export AGG_FR24_MONITOR_URL="file://$WORK/monitor.json"
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Feed") | .value=="Not feeding"'
    if echo "$output" | grep -qE 'acct-98765|51\.5'; then echo "raw feed_status_message leaked" >&2; return 1; fi
}

@test "an inactive service yields only a Service: Stopped row (no status source read)" {
    seed_piaware_enabled; stub_piaware_json; export SVC_STATE=inactive
    run detail piaware
    [ "$status" -eq 0 ]
    # If the producer had run, we'd see FlightAware/ADS-B rows; the gate means we don't.
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Service","value":"Stopped","severity":"na"}]'
}

@test "a failed service yields a Service: Failed err row" {
    seed_piaware_enabled; export SVC_STATE=failed
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Service","value":"Failed","severity":"err"}]'
}

@test "an enable in flight yields an Updating row, not stale status" {
    seed_piaware_enabled; stub_piaware_json; export SVC_STATE=active
    printf '%s' '{"id":"piaware","status":"running"}' > "$AGG_ENABLE_STATE"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[0].label == "Status"'
}

@test "a missing status source yields Status unavailable (still HTTP-ok)" {
    seed_piaware_enabled; export SVC_STATE=active
    export AGG_PIAWARE_STATUS_JSON="$WORK/does-not-exist.json"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.protocol_version == 1'
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Status","value":"Status unavailable","severity":"warn"}]'
}

@test "a missing fr24 monitor endpoint yields Status unavailable" {
    seed_fr24_enabled; export SVC_STATE=active
    export AGG_FR24_MONITOR_URL="file://$WORK/no-such-monitor.json"
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Status","value":"Status unavailable","severity":"warn"}]'
}

@test "malformed status JSON yields Status unavailable" {
    seed_piaware_enabled; export SVC_STATE=active
    printf '%s' 'this is not json' > "$WORK/piaware-status.json"
    export AGG_PIAWARE_STATUS_JSON="$WORK/piaware-status.json"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Status","value":"Status unavailable","severity":"warn"}]'
}

@test "a stale piaware status.json (expiry in the past) yields Status unavailable" {
    # A wedged-but-still-active piaware can leave a stale snapshot; the expiry
    # check must reject it rather than show stale green.
    seed_piaware_enabled; export SVC_STATE=active
    cat > "$WORK/piaware-status.json" <<'EOF'
{"expiry":1,"adept":{"status":"green","message":"Connected"},"radio":{"status":"green","message":"Receiving"}}
EOF
    export AGG_PIAWARE_STATUS_JSON="$WORK/piaware-status.json"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Status","value":"Status unavailable","severity":"warn"}]'
}

@test "a not-configured adapter has no status_detail" {
    stub_piaware_json; export SVC_STATE=active
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].configured == false'
    echo "$output" | jq -e '.aggregators[0] | has("status_detail") | not'
}
