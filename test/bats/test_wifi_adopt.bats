#!/usr/bin/env bats
#
# Unit tests for `apl-wifi adopt` and `apl-wifi activate` on a foreign
# (netplan/flash-time) network, plus the foreign-aware last-network delete
# guard. nmcli is stubbed via APL_WIFI_NMCLI; the stub answers single-field
# reads, the UUID,TYPE enumeration, and the connection up/delete/reload verbs,
# all driven by NM_* env vars. The keyfile dir + UUID source are temp files so
# adopt can really write a keyfile and we can assert on it.

setup() {
    command -v jq >/dev/null || skip "jq not installed"

    APLWIFI="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-wifi"
    [ -x "$APLWIFI" ] || skip "apl-wifi missing"

    WORK="$(mktemp -d)"

    # One managed keyfile fixture so there's a keyfile uuid to exclude from the
    # foreign set (UUID-FRIENDS), and a fallback for the delete-guard tests.
    export APL_WIFI_KEYFILE_DIR="$WORK/keyfiles"
    mkdir -p "$APL_WIFI_KEYFILE_DIR"
    cat > "$APL_WIFI_KEYFILE_DIR/airplanes-wifi-friends.nmconnection" <<'EOF'
[connection]
id=airplanes-wifi-friends
uuid=UUID-FRIENDS
type=wifi

[wifi]
ssid=GreenKingdom-Friends
hidden=false

[wifi-security]
psk=supersecret
EOF

    export APL_WIFI_LIB_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes"
    export APL_WIFI_ROLLBACK_DIR="$WORK/rollback"
    export APL_WIFI_LOCK="$WORK/wifi.lock"

    # Fixed uuid for the keyfile adopt writes (so `connection up` is predictable).
    export APL_WIFI_UUID_SOURCE="$WORK/uuid-source"
    printf 'NEW-UUID-1\n' > "$APL_WIFI_UUID_SOURCE"

    export NM_DELETED_FILE="$WORK/deleted"
    export NM_UP_FILE="$WORK/up"

    NMCLI="$WORK/fake-nmcli"
    cat > "$NMCLI" <<'EOF'
#!/usr/bin/env bash
# Parse argv: detect the `connection <verb>`, -g <field>, uuid <id>, --active.
action=""; field=""; uuid=""; has_active=0
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
    a="${args[i]}"
    case "$a" in
        --active) has_active=1 ;;
        -g)         field="${args[i+1]}" ;;
        uuid)       uuid="${args[i+1]}" ;;
        connection) action="${args[i+1]}" ;;
    esac
done

is_deleted() { [[ -f "$NM_DELETED_FILE" ]] && grep -qxF "$1" "$NM_DELETED_FILE"; }

case "$action" in
    reload) exit 0 ;;
    load)   exit 0 ;;
    down)   exit 0 ;;
    up)
        [[ -n "$NM_UP_FAIL" ]] && { echo "Error: Connection activation failed: no carrier" >&2; exit 4; }
        printf '%s\n' "$uuid" >> "$NM_UP_FILE"
        exit 0 ;;
    delete)
        [[ -n "$NM_DELETE_FAIL" ]] && { echo "Error: cannot delete" >&2; exit 1; }
        printf '%s\n' "$uuid" >> "$NM_DELETED_FILE"
        echo "Connection deleted."
        exit 0 ;;
    show)
        if [[ "$has_active" == 1 ]]; then
            printf '%s\n' "${NM_ACTIVE-UUID-FRIENDS:802-11-wireless:wlan0}"
            exit 0
        fi
        if [[ -n "$uuid" ]]; then
            is_deleted "$uuid" && exit 1
            case "$field" in
                connection.uuid)                    [[ -n "$NM_HOME_DEAD" ]] && exit 1; printf '%s\n' "$uuid" ;;
                connection.type)                    printf '%s\n' "802-11-wireless" ;;
                802-11-wireless.ssid)               printf '%s\n' "${NM_HOME_SSID-GreenKingdom-Homelab}" ;;
                802-11-wireless.hidden)             printf '%s\n' "${NM_HOME_HIDDEN-no}" ;;
                802-11-wireless-security.key-mgmt)  printf '%s\n' "${NM_HOME_KEYMGMT-wpa-psk}" ;;
                802-11-wireless-security.psk-flags) printf '%s\n' "${NM_HOME_PSKFLAGS-0}" ;;
                802-11-wireless-security.psk)       printf '%s\n' "${NM_HOME_PSK-4c9b510e8cfb841600c67de0619f198e7efb43b77a09716d352b1afed258b9e0}" ;;
                connection.autoconnect-priority)    printf '%s\n' "${NM_HOME_PRIO-0}" ;;
                connection.autoconnect)             printf '%s\n' "${NM_HOME_AUTOCONNECT-yes}" ;;
                *)                                  printf '\n' ;;
            esac
            exit 0
        fi
        # Enumeration (UUID,TYPE). Drop already-deleted uuids.
        [[ -n "$NM_FAIL_ENUM" ]] && exit 1
        while IFS= read -r line; do
            [[ -n "$line" ]] || continue
            u="${line%%:*}"
            is_deleted "$u" && continue
            printf '%s\n' "$line"
        done <<< "${NM_CONNS-UUID-FRIENDS:802-11-wireless
UUID-HOME:802-11-wireless
UUID-ETH:802-3-ethernet}"
        exit 0 ;;
esac
exit 0
EOF
    chmod +x "$NMCLI"
    export APL_WIFI_NMCLI="$NMCLI"
}

teardown() {
    [ -n "$WORK" ] && rm -rf "$WORK"
}

adopt()   { printf '%s' "$1" | "$APLWIFI" adopt --json; }
activate(){ printf '%s' "$1" | "$APLWIFI" activate --json; }
wdelete() { printf '%s' "$1" | "$APLWIFI" delete --json; }

adopted_keyfile() { echo "$APL_WIFI_KEYFILE_DIR/airplanes-wifi-greenkingdom-homelab.nmconnection"; }

@test "adopt: a foreign network becomes a managed keyfile and the netplan profile is removed" {
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -eq 0 ]
    [ "$(jq -r .status <<< "$output")" = "applied" ]
    [ "$(jq -r .id <<< "$output")" = "airplanes-wifi-greenkingdom-homelab" ]
    # Managed keyfile written with the copied SSID + PSK.
    [ -f "$(adopted_keyfile)" ]
    grep -q '^ssid=GreenKingdom-Homelab$' "$(adopted_keyfile)"
    grep -q '^key-mgmt=wpa-psk$' "$(adopted_keyfile)"
    # The old foreign connection was deleted (removes its netplan source).
    grep -qxF "UUID-HOME" "$NM_DELETED_FILE"
    # Inactive target: no bring-up needed.
    [ ! -s "$NM_UP_FILE" ]
}

@test "adopt: an active foreign network is brought up before the old profile is deleted" {
    export NM_ACTIVE='UUID-HOME:802-11-wireless:wlan0'
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -eq 0 ]
    [ "$(jq -r .status <<< "$output")" = "applied" ]
    # The new uuid was activated, and the old one deleted.
    grep -qxF "NEW-UUID-1" "$NM_UP_FILE"
    grep -qxF "UUID-HOME" "$NM_DELETED_FILE"
}

@test "adopt: an open foreign network is adopted without a PSK" {
    export NM_HOME_KEYMGMT=""
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -eq 0 ]
    [ "$(jq -r .status <<< "$output")" = "applied" ]
    [ -f "$(adopted_keyfile)" ]
    ! grep -q '^\[wifi-security\]' "$(adopted_keyfile)"
}

@test "adopt: a wpa-psk network with no stored secret is rejected (never downgraded to open)" {
    export NM_HOME_PSK=""
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .status <<< "$output")" = "rejected" ]
    [ "$(jq -r .reason <<< "$output")" = "psk_not_available" ]
    [ ! -f "$(adopted_keyfile)" ]
    [ ! -s "$NM_DELETED_FILE" ]
}

@test "adopt: an agent-owned PSK (psk-flags != 0) is rejected" {
    export NM_HOME_PSKFLAGS="1"
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .reason <<< "$output")" = "psk_not_available" ]
    [ ! -f "$(adopted_keyfile)" ]
}

@test "adopt: unsupported security (WPA3 sae) is refused, not mangled" {
    export NM_HOME_KEYMGMT="sae"
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .reason <<< "$output")" = "unsupported_security" ]
    [ "$(jq -r .key_mgmt <<< "$output")" = "sae" ]
    [ ! -f "$(adopted_keyfile)" ]
}

@test "adopt: a UUID that is not a known foreign wifi connection is rejected" {
    run adopt '{"id":"foreign-UUID-NOPE"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .reason <<< "$output")" = "unknown_id" ]
    [ ! -s "$NM_DELETED_FILE" ]
}

@test "adopt: a managed (already-adopted) keyfile uuid is not adoptable" {
    # UUID-FRIENDS has a keyfile, so it's excluded from the foreign set.
    run adopt '{"id":"foreign-UUID-FRIENDS"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .reason <<< "$output")" = "unknown_id" ]
}

@test "adopt: a failed delete rolls back the freshly-written keyfile" {
    export NM_DELETE_FAIL=1
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .status <<< "$output")" = "filesystem_error" ]
    # Rolled back: no managed keyfile left behind.
    [ ! -f "$(adopted_keyfile)" ]
}

@test "adopt: a failed delete during active adoption restores the original link" {
    export NM_ACTIVE='UUID-HOME:802-11-wireless:wlan0'
    export NM_DELETE_FAIL=1
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .status <<< "$output")" = "filesystem_error" ]
    # New keyfile rolled back, and the original foreign link brought back up.
    [ ! -f "$(adopted_keyfile)" ]
    grep -qxF "UUID-HOME" "$NM_UP_FILE"
}

@test "adopt: an empty key-mgmt on an unreadable connection is rejected, not treated as open" {
    export NM_HOME_KEYMGMT=""
    export NM_HOME_DEAD=1
    run adopt '{"id":"foreign-UUID-HOME"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .reason <<< "$output")" = "read_failed" ]
    [ ! -f "$(adopted_keyfile)" ]
    [ ! -s "$NM_DELETED_FILE" ]
}

@test "activate: a foreign network can be activated" {
    run activate '{"id":"foreign-UUID-HOME"}'
    [ "$status" -eq 0 ]
    [ "$(jq -r .status <<< "$output")" = "applied" ]
    [ "$(jq -r .id <<< "$output")" = "foreign-UUID-HOME" ]
    grep -qxF "UUID-HOME" "$NM_UP_FILE"
}

@test "activate: an unknown foreign uuid is rejected" {
    run activate '{"id":"foreign-UUID-NOPE"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .reason <<< "$output")" = "unknown_id" ]
}

@test "delete: a foreign fallback releases the last-network lock" {
    # Only one managed keyfile (friends), but UUID-HOME is a foreign fallback,
    # so deleting the managed one does NOT require force_last. Make friends
    # inactive so the separate active-no-uplink guard doesn't pre-empt this.
    export NM_ACTIVE=''
    run wdelete '{"id":"airplanes-wifi-friends"}'
    [ "$status" -eq 0 ]
    [ "$(jq -r .status <<< "$output")" = "applied" ]
}

@test "delete: with no fallback at all the last-network lock still fires" {
    # No foreign profiles: enumeration returns only the managed keyfile.
    export NM_CONNS='UUID-FRIENDS:802-11-wireless'
    run wdelete '{"id":"airplanes-wifi-friends"}'
    [ "$status" -ne 0 ]
    [ "$(jq -r .reason <<< "$output")" = "requires_force_flag" ]
}
