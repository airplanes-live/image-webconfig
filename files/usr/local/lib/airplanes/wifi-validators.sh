# shellcheck shell=bash
# Shared Wi-Fi input validators. Sourced by airplanes-first-run (boot-config
# flow) and apl-wifi (webconfig flow). Both must agree on what counts as a
# valid SSID/PSK/country/priority/hidden so an input the operator tested via
# airplanes-config.txt isn't later rejected by the UI (or vice versa). The JS
# twins in webconfig/web/assets/app.js are kept in sync by
# test/test_validator_parity.sh.
#
# Predicates only: return 0 on accept, non-zero on reject, write nothing to
# stdout. Mirrors feed/scripts/lib/configure-validators.sh.
#
# Hardening over the previous inline implementation:
#  - SSID and PSK reject CR, LF, NUL and other C0 control bytes (0x00–0x1F,
#    0x7F). Without this gate, a crafted airplanes-config.txt could inject a
#    second section into the NM keyfile.
#  - Byte length, not character length. The wire constraint is bytes; the JS
#    twin uses TextEncoder().encode(s).length so the two agree on multi-byte
#    SSIDs.
#  - No trim. WPA-PSK passphrases legitimately contain leading or trailing
#    spaces. Callers preserve the value verbatim.

# apl_wifi_valid_ssid VALUE
#   1–32 bytes, no C0 control characters (0x00–0x1F or 0x7F).
apl_wifi_valid_ssid() {
	local v="$1" total stripped
	total="$(LC_ALL=C printf '%s' "$v" | wc -c)"
	(( total >= 1 && total <= 32 )) || return 1
	stripped="$(LC_ALL=C printf '%s' "$v" | LC_ALL=C tr -d '\000-\037\177' | wc -c)"
	[[ "$stripped" == "$total" ]] || return 1
	return 0
}

# apl_wifi_valid_psk VALUE
#   8–63 printable ASCII (0x20–0x7E) OR exactly 64 hex characters.
apl_wifi_valid_psk() {
	local v="$1"
	if [[ "$v" =~ ^[A-Fa-f0-9]{64}$ ]]; then
		return 0
	fi
	local total kept
	total="$(LC_ALL=C printf '%s' "$v" | wc -c)"
	(( total >= 8 && total <= 63 )) || return 1
	# Keep only printable ASCII bytes (0x20–0x7E), count what survived.
	# Mismatch with the original byte count means at least one bad byte
	# (control, high-bit, etc.) was present. Done via tr (not grep -q),
	# since grep treats LF as a line separator and would miss a stray
	# 0x0A inside the PSK.
	kept="$(LC_ALL=C printf '%s' "$v" | LC_ALL=C tr -cd ' -~' | wc -c)"
	[[ "$kept" == "$total" ]] || return 1
	return 0
}

# apl_wifi_valid_country VALUE
#   ISO 3166-1 alpha-2 (two uppercase ASCII letters).
apl_wifi_valid_country() {
	[[ "$1" =~ ^[A-Z]{2}$ ]]
}

# apl_wifi_valid_priority VALUE
#   Integer 0–999. No leading zeros (except the literal "0"); no sign; no
#   whitespace. Matches the NM [connection] autoconnect-priority range we
#   surface in the UI.
apl_wifi_valid_priority() {
	[[ "$1" =~ ^(0|[1-9][0-9]{0,2})$ ]]
}

# apl_wifi_valid_hidden VALUE
#   Literal "true" or "false". Case-sensitive — matches NM keyfile syntax.
apl_wifi_valid_hidden() {
	[[ "$1" == "true" || "$1" == "false" ]]
}
