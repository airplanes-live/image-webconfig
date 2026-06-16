# shellcheck shell=bash
# Shared SSH input validators. Sourced by apl-ssh (the webconfig SSH helper).
# Kept in its own library — mirroring wifi-validators.sh — so the same
# predicates can be reused by a boot-config flow (airplanes-first-run) without
# pulling in the whole helper.
#
# Predicates only: return 0 on accept, non-zero on reject, write nothing to
# stdout. Length is measured in characters (not bytes) for the password
# minimum, matching the webconfig password-min check in handlers.go.
#
# Two validators:
#  - apl_ssh_valid_password: at least APL_SSH_MIN_PASSWORD_LEN (12) characters
#    and free of C0/DEL control bytes. No upper bound and no other character-class
#    rules — a per-device SSH password is operator-chosen — but a newline must be
#    rejected: chpasswd reads user:password line-by-line, so an embedded newline
#    would inject a second record (e.g. setting root's password).
#  - apl_ssh_valid_pubkey: a single OpenSSH public key line: a known type token,
#    a base64 blob, and an optional comment. Rejects multi-line input, control
#    bytes, and unknown key types so a crafted value can't inject a second
#    authorized_keys entry or smuggle options/commands in front of the key.

APL_SSH_MIN_PASSWORD_LEN="${APL_SSH_MIN_PASSWORD_LEN:-12}"

# apl_ssh_valid_password VALUE
#   At least APL_SSH_MIN_PASSWORD_LEN characters. Counts characters, not bytes,
#   via ${#v} so a multibyte passphrase isn't under-counted.
apl_ssh_valid_password() {
	local v="$1"
	# Reject any C0 control byte (0x00-0x1F) or DEL (0x7F). A newline would let
	# the value inject a second `user:password` record into chpasswd's line-based
	# stdin (e.g. setting root's password); chpasswd otherwise accepts any byte.
	# Done with tr so an embedded LF can't slip past a regex that treats LF as a
	# line separator.
	local stripped total
	total="$(LC_ALL=C printf '%s' "$v" | wc -c)"
	stripped="$(LC_ALL=C printf '%s' "$v" | LC_ALL=C tr -d '\000-\037\177' | wc -c)"
	[[ "$stripped" == "$total" ]] || return 1
	(( ${#v} >= APL_SSH_MIN_PASSWORD_LEN ))
}

# apl_ssh_valid_pubkey VALUE
#   A single OpenSSH public key. Accepts exactly: <type> <base64-blob>[ <comment>]
#   where <type> is one of the supported algorithms and <base64-blob> is a
#   non-empty standard-base64 token. The optional comment is free text but must
#   not contain control characters. Rejects:
#     - anything with a newline, carriage return, or other C0/DEL control byte
#       (would let a value inject a second authorized_keys line)
#     - an authorized_keys options prefix (e.g. command="...",no-pty ssh-rsa …):
#       the type token must be the FIRST field, so an options field shifts the
#       type out of position and is rejected.
#     - unknown / unsupported key types.
apl_ssh_valid_pubkey() {
	local v="$1"
	# Reject any C0 control byte (0x00-0x1F) or DEL (0x7F). Done with tr so an
	# embedded LF can't slip past a regex that treats LF as a line separator.
	local stripped total
	total="$(LC_ALL=C printf '%s' "$v" | wc -c)"
	stripped="$(LC_ALL=C printf '%s' "$v" | LC_ALL=C tr -d '\000-\037\177' | wc -c)"
	[[ "$stripped" == "$total" ]] || return 1

	# Split into three whitespace-separated fields: type, blob, comment. read's
	# default IFS collapses runs of spaces/tabs; the trailing comment field
	# absorbs any further spaces, so a key with a multi-word comment is fine.
	local ktype kblob kcomment
	read -r ktype kblob kcomment <<< "$v"
	: "${kcomment}" # comment is optional and unused beyond presence; keep for clarity
	[[ -n "$ktype" && -n "$kblob" ]] || return 1

	case "$ktype" in
		ssh-ed25519|ssh-rsa|ssh-dss| \
		ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521| \
		sk-ssh-ed25519@openssh.com|sk-ecdsa-sha2-nistp256@openssh.com) ;;
		*) return 1 ;;
	esac

	# The blob must be non-empty standard base64 (A-Za-z0-9+/ with optional =
	# padding). This is a syntactic gate, not a cryptographic one — sshd is the
	# final arbiter of a structurally-valid but unusable key.
	[[ "$kblob" =~ ^[A-Za-z0-9+/]+=*$ ]] || return 1

	return 0
}
