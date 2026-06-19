#!/usr/bin/env bats
#
# Unit tests for `apl-wifi export | import` — the Wi-Fi section of the combined
# device backup. export emits managed networks INCLUDING the PSK (the only
# PSK-bearing wifi surface); import restores them non-disruptively: keyfiles are
# written and loaded but nothing is brought up/down and the active connection is
# never rewritten. nmcli is stubbed via APL_WIFI_NMCLI.

setup() {
    command -v jq >/dev/null || skip "jq not installed"

    APLWIFI="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-wifi"
    [ -x "$APLWIFI" ] || skip "apl-wifi missing"

    WORK="$(mktemp -d)"
    export APL_WIFI_KEYFILE_DIR="$WORK/keyfiles"
    mkdir -p "$APL_WIFI_KEYFILE_DIR"
    export APL_WIFI_LIB_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes"
    export APL_WIFI_ROLLBACK_DIR="$WORK/rollback"
    export APL_WIFI_LOCK="$WORK/wifi.lock"

    echo "11111111-2222-3333-4444-555555555555" > "$WORK/uuid"
    export APL_WIFI_UUID_SOURCE="$WORK/uuid"

    # Minimal nmcli stub: `--active` prints NM_ACTIVE (UUID:TYPE:DEVICE) when
    # set, nothing otherwise; every other call (connection load, …) succeeds.
    NMCLI="$WORK/fake-nmcli"
    cat > "$NMCLI" <<'EOF'
#!/usr/bin/env bash
for a in "$@"; do
    if [[ "$a" == "--active" ]]; then
        if [[ -n "${NM_ACTIVE-}" ]]; then printf '%s\n' "$NM_ACTIVE"; fi
        exit 0
    fi
done
exit 0
EOF
    chmod +x "$NMCLI"
    export APL_WIFI_NMCLI="$NMCLI"
}

teardown() {
    [ -n "$WORK" ] && rm -rf "$WORK"
}

# managed_keyfile ID SSID [PSK] [PRIORITY] — write a keyfile with uuid=uuid-ID.
managed_keyfile() {
    local id="$1" ssid="$2" psk="${3-}" prio="${4-0}"
    {
        printf '[connection]\nid=%s\nuuid=uuid-%s\ntype=wifi\nautoconnect=true\n' "$id" "$id"
        [ "$prio" -gt 0 ] && printf 'autoconnect-priority=%s\n' "$prio"
        printf '\n[wifi]\nssid=%s\nmode=infrastructure\n\n' "$ssid"
        [ -n "$psk" ] && printf '[wifi-security]\nkey-mgmt=wpa-psk\npsk=%s\n\n' "$psk"
        printf '[ipv4]\nmethod=auto\n'
    } > "$APL_WIFI_KEYFILE_DIR/$id.nmconnection"
}

@test "export emits managed networks with PSK and skips non-managed keyfiles" {
    managed_keyfile airplanes-wifi-home GreenKingdom supersecretpsk 5
    managed_keyfile airplanes-config-wifi FirstRunNet firstrunpass 0
    # A non-managed keyfile (id doesn't match the managed regex) must be excluded.
    managed_keyfile myhotspot SomeoneElse hunter2hunter 0

    run "$APLWIFI" export --json
    [ "$status" -eq 0 ]
    [ "$(jq -r '.status' <<< "$output")" = "ok" ]
    [ "$(jq '.schema_version' <<< "$output")" = "1" ]
    [ "$(jq '.networks | length' <<< "$output")" = "2" ]

    # The home network carries its PSK verbatim plus shape fields.
    [ "$(jq -r '.networks[] | select(.id=="airplanes-wifi-home") | .psk' <<< "$output")" = "supersecretpsk" ]
    [ "$(jq -r '.networks[] | select(.id=="airplanes-wifi-home") | .ssid' <<< "$output")" = "GreenKingdom" ]
    [ "$(jq '.networks[] | select(.id=="airplanes-wifi-home") | .priority' <<< "$output")" = "5" ]
    [ "$(jq '.networks[] | select(.id=="airplanes-wifi-home") | .hidden' <<< "$output")" = "false" ]

    # The non-managed keyfile is absent.
    [ "$(jq '[.networks[] | select(.id=="myhotspot")] | length' <<< "$output")" = "0" ]
}

@test "export returns an empty network list when nothing is managed" {
    run "$APLWIFI" export --json
    [ "$status" -eq 0 ]
    [ "$(jq '.networks | length' <<< "$output")" = "0" ]
}

@test "import writes keyfiles additively and reports per-network results" {
    cat > "$WORK/req.json" <<'JSON'
{"schema_version":1,"networks":[
  {"ssid":"Cafe","psk":"hunter2hunter","hidden":false,"priority":3},
  {"id":"airplanes-wifi-office","ssid":"Office","psk":"office123456","hidden":true,"priority":7}
]}
JSON
    run "$APLWIFI" import --json < "$WORK/req.json"
    [ "$status" -eq 0 ]
    [ "$(jq -r '.status' <<< "$output")" = "applied" ]
    [ "$(jq '.imported' <<< "$output")" = "2" ]
    [ "$(jq '[.results[] | select(.status=="applied")] | length' <<< "$output")" = "2" ]

    # The explicit managed id is honoured verbatim; PSK + hidden persisted.
    local office="$APL_WIFI_KEYFILE_DIR/airplanes-wifi-office.nmconnection"
    [ -f "$office" ]
    grep -q '^psk=office123456$' "$office"
    grep -q '^hidden=true$' "$office"

    # The id-less network gets a derived managed id from its SSID.
    [ -f "$APL_WIFI_KEYFILE_DIR/airplanes-wifi-cafe.nmconnection" ]
}

@test "import rejects the whole payload if any network is invalid, writing nothing" {
    cat > "$WORK/req.json" <<'JSON'
{"schema_version":1,"networks":[
  {"ssid":"GoodNet","psk":"goodpassword1"},
  {"ssid":""}
]}
JSON
    run "$APLWIFI" import --json < "$WORK/req.json"
    [ "$status" -eq 2 ]
    [ "$(jq -r '.status' <<< "$output")" = "rejected" ]
    [ "$(jq -r '.reason' <<< "$output")" = "invalid_networks" ]
    # All-or-nothing: the valid network must NOT have been written.
    [ -z "$(ls -A "$APL_WIFI_KEYFILE_DIR")" ]
}

@test "import rejects a null PSK rather than silently restoring an open network" {
    cat > "$WORK/req.json" <<'JSON'
{"schema_version":1,"networks":[
  {"id":"airplanes-wifi-secured","ssid":"Secured","psk":null}
]}
JSON
    run "$APLWIFI" import --json < "$WORK/req.json"
    [ "$status" -eq 2 ]
    [ "$(jq -r '.status' <<< "$output")" = "rejected" ]
    [ "$(jq -r '.reason' <<< "$output")" = "invalid_networks" ]
    [ -z "$(ls -A "$APL_WIFI_KEYFILE_DIR")" ]
}

@test "import rejects a non-string SSID instead of coercing it" {
    echo '{"schema_version":1,"networks":[{"ssid":12345}]}' > "$WORK/req.json"
    run "$APLWIFI" import --json < "$WORK/req.json"
    [ "$status" -eq 2 ]
    [ "$(jq -r '.status' <<< "$output")" = "rejected" ]
    [ -z "$(ls -A "$APL_WIFI_KEYFILE_DIR")" ]
}

@test "import refuses an unsupported schema_version" {
    echo '{"schema_version":99,"networks":[]}' > "$WORK/req.json"
    run "$APLWIFI" import --json < "$WORK/req.json"
    [ "$status" -eq 5 ]
    [ "$(jq -r '.status' <<< "$output")" = "parse_error" ]
}

@test "import never rewrites the active connection" {
    managed_keyfile airplanes-wifi-live OriginalName livepass0000 0
    export NM_ACTIVE="uuid-airplanes-wifi-live:802-11-wireless:wlan0"

    cat > "$WORK/req.json" <<'JSON'
{"schema_version":1,"networks":[
  {"id":"airplanes-wifi-live","ssid":"HijackName","psk":"newpass00000","hidden":false,"priority":0}
]}
JSON
    run "$APLWIFI" import --json < "$WORK/req.json"
    [ "$status" -eq 0 ]
    [ "$(jq -r '.results[0].status' <<< "$output")" = "skipped" ]
    [ "$(jq -r '.results[0].reason' <<< "$output")" = "active" ]
    # The live keyfile is untouched — still the original SSID.
    grep -q '^ssid=OriginalName$' "$APL_WIFI_KEYFILE_DIR/airplanes-wifi-live.nmconnection"
}
