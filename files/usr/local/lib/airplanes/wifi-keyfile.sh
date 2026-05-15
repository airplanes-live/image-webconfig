# shellcheck shell=bash
# Shared NetworkManager keyfile writer. Sourced by airplanes-first-run
# (boot-config bootstrap) and apl-wifi (webconfig runtime). Atomic via
# mktemp → fsync → chmod 0600 → mv -fT, same pattern as the original inline
# implementation in airplanes-first-run.
#
# The writer takes explicit positional arguments rather than reading globals
# so apl-wifi can produce many keyfiles for the same image (one per managed
# network). Callers validate inputs with wifi-validators.sh before invoking;
# the writer itself does no validation — it just lays bytes down.

# apl_wifi_write_keyfile DEST ID UUID SSID PSK HIDDEN PRIORITY AUTOCONNECT
#
#   DEST         Absolute path of the .nmconnection file to write.
#   ID           [connection] id= value (NM-displayed connection name).
#   UUID         [connection] uuid= value.
#   SSID         [wifi] ssid= value, written verbatim.
#   PSK          [wifi-security] psk= value. Empty string = open network
#                (the [wifi-security] section is omitted entirely).
#   HIDDEN       "true" or "false" — emits [wifi] hidden=true only when true.
#   PRIORITY     Integer 0–999. Emits [connection] autoconnect-priority=N
#                only when > 0 (NM defaults to 0 when the key is absent, so
#                this keeps first-run-produced keyfiles byte-compatible with
#                the pre-refactor format).
#   AUTOCONNECT  "true" or "false" — emits [connection] autoconnect=N.
#
# Returns 0 on success, non-zero on any I/O failure. Leaves the destination
# untouched on failure (atomic rename or nothing).
apl_wifi_write_keyfile() {
	local dest="$1" id="$2" uuid="$3" ssid="$4" psk="$5" hidden="$6" priority="$7" autoconnect="$8"
	local dir tmp
	dir="$(dirname -- "$dest")"

	if ! install -d -m 0700 "$dir" 2>/dev/null; then
		return 1
	fi
	# Best-effort root ownership of the directory; tests run unprivileged
	# and silently skip the chown.
	[[ "$(id -u)" -eq 0 ]] && chown root:root "$dir" 2>/dev/null

	tmp="$(mktemp "${dir}/.airplanes-wifi.tmp.XXXXXX")" || return 1

	if ! {
		printf '[connection]\n'
		printf 'id=%s\n' "$id"
		printf 'uuid=%s\n' "$uuid"
		printf 'type=wifi\n'
		printf 'autoconnect=%s\n' "$autoconnect"
		[[ "$priority" -gt 0 ]] 2>/dev/null && printf 'autoconnect-priority=%d\n' "$priority"
		printf '\n'
		printf '[wifi]\n'
		printf 'ssid=%s\n' "$ssid"
		printf 'mode=infrastructure\n'
		[[ "$hidden" == "true" ]] && printf 'hidden=true\n'
		printf '\n'
		if [[ -n "$psk" ]]; then
			printf '[wifi-security]\n'
			printf 'key-mgmt=wpa-psk\n'
			printf 'psk=%s\n' "$psk"
			printf '\n'
		fi
		printf '[ipv4]\n'
		printf 'method=auto\n'
		printf '\n'
		printf '[ipv6]\n'
		printf 'method=auto\n'
	} > "$tmp"; then
		rm -f -- "$tmp"
		return 1
	fi

	if ! chmod 0600 "$tmp"; then
		rm -f -- "$tmp"
		return 1
	fi
	[[ "$(id -u)" -eq 0 ]] && chown root:root "$tmp" 2>/dev/null

	sync -d "$tmp" 2>/dev/null || sync
	if ! mv -fT "$tmp" "$dest"; then
		rm -f -- "$tmp"
		return 1
	fi
	sync "$dir" 2>/dev/null || sync
	return 0
}

# apl_wifi_delete_keyfile DEST
#   Idempotent removal. Returns 0 if file was removed OR didn't exist.
#   Returns non-zero only on a real filesystem failure (e.g. directory not
#   writable). The directory sync after removal is a courtesy to ensure
#   NetworkManager's inotify watcher sees the unlink before the helper's
#   caller proceeds with a `nmcli connection reload`.
apl_wifi_delete_keyfile() {
	local dest="$1"
	if [[ -e "$dest" ]]; then
		rm -f -- "$dest" || return 1
		sync "$(dirname -- "$dest")" 2>/dev/null || sync
	fi
	return 0
}
