#!/usr/bin/env bats
#
# Unit tests for `apl-aggregator detail` — the on-demand per-adapter status that
# carries status_detail parsed from the vendor status tool. systemctl is stubbed
# via AGG_SYSTEMCTL (SVC_STATE); the vendor tools via the AGG_PIAWARE_STATUS /
# AGG_FR24_STATUS seams; the local decoder probe via AGG_DECODER_STATE. The real
# shipped descriptors are exercised so descriptor + helper stay in sync.

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

    # A marker the vendor stubs touch, so a test can assert the vendor tool was
    # NOT invoked (inactive service / overlay in flight / not configured).
    export VENDOR_MARKER="$WORK/vendor-called"
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

# A stub piaware-status echoing real-feeder output — note the benign
# "Local ADS-B receiver (dump1090) is not running" line (expected: the decoder
# is readsb) and the feeder-id line (identity-bearing, must not leak).
stub_piaware_status() {
    local f="$WORK/piaware-status"
    cat > "$f" <<EOF
#!/usr/bin/env bash
: > "$VENDOR_MARKER"
cat <<'OUT'
PiAware master process (piaware) is running with pid 13859.
PiAware ADS-B client (faup1090) is running with pid 13875.
PiAware ADS-B UAT client (faup978) is not running (disabled by configuration settings)
PiAware mlat client (fa-mlat-client) is running with pid 13878.
Local ADS-B receiver (dump1090) is not running.
readsb (pid 29262) is listening for ES connections on port 30005.
faup1090 is connected to the ADS-B receiver.
piaware is connected to FlightAware.
dump1090 is producing data on localhost:30005.
Your feeder ID is 8bb46ddf-d507-4c18-9724-0b5f94cba900 (from /var/cache/piaware/feeder_id)
OUT
EOF
    chmod +x "$f"
    export AGG_PIAWARE_STATUS="$f"
}

stub_fr24_status() {
    local f="$WORK/fr24feed-status"
    cat > "$f" <<EOF
#!/usr/bin/env bash
: > "$VENDOR_MARKER"
cat <<'OUT'
[ ok ] FR24 Feeder/Decoder Process: running.
[ ok ] FR24 Stats Timestamp: 2026-06-06 00:06:24.
[ ok ] FR24 Link: connected [UDP].
[ ok ] FR24 Radar: T-EDKA146.
[ ok ] FR24 Tracked AC: 17.
[ ok ] Receiver: connected (174255916 MSGS/0 SYNC).
OUT
EOF
    chmod +x "$f"
    export AGG_FR24_STATUS="$f"
}

@test "detail wraps a single adapter in the status envelope" {
    seed_piaware_enabled; stub_piaware_status; export SVC_STATE=active
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

@test "piaware detail surfaces connection/ADS-B/MLAT, never the dump1090 false-alarm or the feeder id" {
    seed_piaware_enabled; stub_piaware_status; export SVC_STATE=active
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="FlightAware") | .value=="Connected" and .severity=="ok"'
    echo "$output" | jq -e '.aggregators[0].status_detail | map(.label) | index("ADS-B") != null'
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="MLAT") | .severity=="ok"'
    # The benign "dump1090 is not running" line must NOT yield an error row.
    echo "$output" | jq -e '.aggregators[0].status_detail | all(.severity != "err")'
    # Vendor noise (dump1090) and the identity-bearing feeder id must not leak.
    if echo "$output" | grep -qiE 'dump1090|8bb46ddf'; then echo "leaked vendor noise / feeder id" >&2; return 1; fi
}

@test "fr24 detail parses [ ok ] lines into whitelisted rows" {
    seed_fr24_enabled; stub_fr24_status; export SVC_STATE=active
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Connection") | .value=="connected [UDP]" and .severity=="ok"'
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Tracked aircraft") | .value=="17"'
    echo "$output" | jq -e '.aggregators[0].status_detail | map(.label) | index("Receiver") != null'
    # Process / timestamp lines are not surfaced (not on the whitelist).
    echo "$output" | jq -e '.aggregators[0].status_detail | map(.label) | index("FR24 Stats Timestamp") == null'
    echo "$output" | jq -e '.aggregators[0].status_detail | map(.label) | index("FR24 Feeder/Decoder Process") == null'
}

@test "fr24 [ fail ] prefix maps to err severity" {
    seed_fr24_enabled; export SVC_STATE=active
    local f="$WORK/fr24feed-status"
    cat > "$f" <<'EOF'
#!/usr/bin/env bash
echo "[ fail ] FR24 Link: disconnected."
EOF
    chmod +x "$f"; export AGG_FR24_STATUS="$f"
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Connection") | .severity=="err"'
}

@test "an inactive service yields a Service: Stopped row and does not run the vendor tool" {
    seed_piaware_enabled; stub_piaware_status; export SVC_STATE=inactive
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Service","value":"Stopped","severity":"na"}]'
    [ ! -e "$VENDOR_MARKER" ]
}

@test "a failed service yields a Service: Failed err row" {
    seed_piaware_enabled; export SVC_STATE=failed
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Service","value":"Failed","severity":"err"}]'
}

@test "an enable in flight yields an Updating row, not stale vendor status" {
    seed_piaware_enabled; stub_piaware_status; export SVC_STATE=active
    printf '%s' '{"id":"piaware","status":"running"}' > "$AGG_ENABLE_STATE"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[0].label == "Status"'
    [ ! -e "$VENDOR_MARKER" ]
}

@test "a missing vendor binary yields a Status unavailable row, still HTTP-ok envelope" {
    seed_piaware_enabled; export SVC_STATE=active
    export AGG_PIAWARE_STATUS="$WORK/does-not-exist"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.protocol_version == 1'
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Status","value":"Status unavailable","severity":"warn"}]'
}

@test "ANSI escapes and control chars are stripped from values" {
    seed_fr24_enabled; export SVC_STATE=active
    local f="$WORK/fr24feed-status"
    printf '#!/usr/bin/env bash\nprintf "[ ok ] FR24 Radar: \\033[31mT-EDKA146\\033[0m.\\n"\n' > "$f"
    chmod +x "$f"; export AGG_FR24_STATUS="$f"
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail[] | select(.label=="Radar") | .value=="T-EDKA146"'
    if printf '%s' "$output" | grep -q $'\033'; then echo "ESC leaked into output" >&2; return 1; fi
}

@test "a not-configured adapter has no status_detail and runs no vendor tool" {
    stub_piaware_status; export SVC_STATE=active
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].configured == false'
    echo "$output" | jq -e '.aggregators[0] | has("status_detail") | not'
    [ ! -e "$VENDOR_MARKER" ]
}

@test "a vendor tool that hangs after partial output yields Status unavailable, not stale healthy rows" {
    seed_piaware_enabled; export SVC_STATE=active AGG_STATUS_TIMEOUT=1
    local f="$WORK/piaware-status"
    cat > "$f" <<'EOF'
#!/usr/bin/env bash
echo "piaware is connected to FlightAware."
sleep 10
EOF
    chmod +x "$f"; export AGG_PIAWARE_STATUS="$f"
    run detail piaware
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Status","value":"Status unavailable","severity":"warn"}]'
}

@test "non-empty but unrecognized vendor output yields Status unavailable" {
    seed_fr24_enabled; export SVC_STATE=active
    local f="$WORK/fr24feed-status"
    cat > "$f" <<'EOF'
#!/usr/bin/env bash
echo "some unexpected text that matches no whitelist label"
echo "[ ok ] FR24 Stats Timestamp: 2026-01-01."
EOF
    chmod +x "$f"; export AGG_FR24_STATUS="$f"
    run detail fr24
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.aggregators[0].status_detail == [{"label":"Status","value":"Status unavailable","severity":"warn"}]'
}
