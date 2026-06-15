#!/usr/bin/env bats
#
# Unit tests for `apl-wifi list` — specifically that wifi connections
# NetworkManager knows but that have no /etc keyfile (e.g. netplan-generated
# /run profiles from flash-time setup) are surfaced read-only (managed:false),
# while the managed-keyfile path is unchanged. nmcli is stubbed via
# APL_WIFI_NMCLI; the stub's per-connection answers are driven by NM_* env vars.

setup() {
    command -v jq >/dev/null || skip "jq not installed"

    APLWIFI="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-wifi"
    [ -x "$APLWIFI" ] || skip "apl-wifi missing"

    WORK="$(mktemp -d)"

    # Managed keyfile fixture (uuid UUID-FRIENDS, active per the stub below).
    export APL_WIFI_KEYFILE_DIR="$WORK/keyfiles"
    mkdir -p "$APL_WIFI_KEYFILE_DIR"
    cat > "$APL_WIFI_KEYFILE_DIR/airplanes-wifi-friends.nmconnection" <<'EOF'
[connection]
id=airplanes-wifi-friends
uuid=UUID-FRIENDS
type=wifi
autoconnect-priority=2

[wifi]
ssid=GreenKingdom-Friends
hidden=false

[wifi-security]
psk=supersecret
EOF

    export APL_WIFI_LIB_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes"
    export APL_WIFI_ROLLBACK_DIR="$WORK/rollback"
    export APL_WIFI_LOCK="$WORK/wifi.lock"

    # Stub nmcli. Dispatches on argv: `--active` → active-connection list;
    # `uuid <X>` → single-field read (driven by NM_HOME_* with defaults);
    # otherwise → the full connection enumeration (NM_FAIL_ENUM=1 makes it fail).
    NMCLI="$WORK/fake-nmcli"
    cat > "$NMCLI" <<'EOF'
#!/usr/bin/env bash
field=""; prev=""; has_active=0; has_uuid=0
for a in "$@"; do
    [[ "$a" == "--active" ]] && has_active=1
    [[ "$prev" == "-g" ]] && field="$a"
    [[ "$prev" == "uuid" ]] && has_uuid=1
    prev="$a"
done
if [[ "$has_active" == 1 ]]; then
    printf '%s\n' "${NM_ACTIVE-UUID-FRIENDS:802-11-wireless:wlan0}"
    exit 0
fi
if [[ "$has_uuid" == 1 ]]; then
    case "$field" in
        802-11-wireless.ssid)              printf '%s\n' "${NM_HOME_SSID-GreenKingdom-Homelab}" ;;
        802-11-wireless.hidden)            printf '%s\n' "${NM_HOME_HIDDEN-no}" ;;
        802-11-wireless-security.key-mgmt) printf '%s\n' "${NM_HOME_KEYMGMT-wpa-psk}" ;;
        connection.autoconnect-priority)   printf '%s\n' "${NM_HOME_PRIO-0}" ;;
        connection.autoconnect)            printf '%s\n' "${NM_HOME_AUTOCONNECT-yes}" ;;
        *)                                 printf '\n' ;;
    esac
    exit 0
fi
# Full enumeration (UUID:TYPE).
[[ -n "$NM_FAIL_ENUM" ]] && exit 1
printf '%s\n' "${NM_CONNS-UUID-FRIENDS:802-11-wireless
UUID-HOME:802-11-wireless
UUID-ETH:802-3-ethernet}"
exit 0
EOF
    chmod +x "$NMCLI"
    export APL_WIFI_NMCLI="$NMCLI"
}

teardown() {
    [ -n "$WORK" ] && rm -rf "$WORK"
}

@test "list surfaces a /run foreign wifi connection read-only alongside the managed one" {
    run "$APLWIFI" list --json
    [ "$status" -eq 0 ]

    # Exactly two networks: the managed keyfile + the foreign /run profile.
    # The ethernet connection and the already-managed UUID-FRIENDS are not
    # double-listed.
    [ "$(jq '.networks | length' <<< "$output")" = "2" ]
    [ "$(jq '[.networks[] | select(.uuid == "UUID-FRIENDS")] | length' <<< "$output")" = "1" ]

    # Managed network: unchanged, active.
    [ "$(jq -r '.networks[] | select(.id == "airplanes-wifi-friends") | .managed' <<< "$output")" = "true" ]
    [ "$(jq -r '.networks[] | select(.id == "airplanes-wifi-friends") | .active' <<< "$output")" = "true" ]

    # Foreign /run network: synthetic foreign-<uuid> id, read-only, fields right.
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .id' <<< "$output")" = "foreign-UUID-HOME" ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .ssid' <<< "$output")" = "GreenKingdom-Homelab" ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .managed' <<< "$output")" = "false" ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .has_psk' <<< "$output")" = "true" ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .priority' <<< "$output")" = "0" ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .active' <<< "$output")" = "false" ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .first_run_profile' <<< "$output")" = "false" ]
}

@test "list preserves a colon/backslash SSID on the foreign entry (escaping disabled)" {
    export NM_HOME_SSID='Cafe:West\x'
    run "$APLWIFI" list --json
    [ "$status" -eq 0 ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .ssid' <<< "$output")" = 'Cafe:West\x' ]
}

@test "an active foreign /run network is tagged active and resolves the status SSID" {
    export NM_ACTIVE='UUID-HOME:802-11-wireless:wlan0'
    run "$APLWIFI" list --json
    [ "$status" -eq 0 ]
    [ "$(jq -r '.networks[] | select(.uuid == "UUID-HOME") | .active' <<< "$output")" = "true" ]
    # active_connection should pick up the foreign entry's ssid + id.
    [ "$(jq -r '.active_connection.ssid' <<< "$output")" = "GreenKingdom-Homelab" ]
    [ "$(jq -r '.active_connection.id' <<< "$output")" = "foreign-UUID-HOME" ]
}

@test "nmcli enumeration failure leaves the managed list intact (best-effort)" {
    export NM_FAIL_ENUM=1
    run "$APLWIFI" list --json
    [ "$status" -eq 0 ]
    [ "$(jq '.networks | length' <<< "$output")" = "1" ]
    [ "$(jq -r '.networks[0].id' <<< "$output")" = "airplanes-wifi-friends" ]
}

@test "NetworkManager unavailable: no foreign merge, just the keyfile list" {
    export APL_WIFI_NMCLI="$WORK/does-not-exist-nmcli"
    run "$APLWIFI" list --json
    [ "$status" -eq 0 ]
    [ "$(jq -r '.networkmanager_available' <<< "$output")" = "false" ]
    [ "$(jq '.networks | length' <<< "$output")" = "1" ]
}
