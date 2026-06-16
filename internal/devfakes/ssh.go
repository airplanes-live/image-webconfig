package devfakes

import (
	"encoding/json"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// sshMinPasswordLen mirrors APL_SSH_MIN_PASSWORD_LEN in the production helper
// (and the webconfig password min in handlers.go). The dev fake re-checks it so
// the SPA's rejected-envelope branch is reachable without a Pi.
const sshMinPasswordLen = 12

// sshCmd fakes `apl-ssh <verb> --json` for cmd/devserver. It emits the same
// status-string envelopes the production bash helper does so the SPA renders
// identically in dev and on a Pi. State is held in-memory on *State; the Go
// handler has already verified + stripped current_password before the body
// reaches here, so the fake never sees the admin password.
func sshCmd(state *State, verb string, body []byte) (wexec.Result, error) {
	switch verb {
	case "status":
		return sshStatus(state), nil
	case "enable-password", "set-password":
		return sshSetPassword(state, body), nil
	case "disable-password":
		state.SSHDisablePassword()
		return sshEnvelope(map[string]any{"status": "applied", "password_disabled": true, "reloaded": true}), nil
	case "set-key":
		return sshSetKey(state, body), nil
	case "clear-key":
		state.SSHClearKey()
		return sshEnvelope(map[string]any{"status": "applied", "key_cleared": true}), nil
	}
	return sshEnvelope(map[string]any{"status": "usage_error", "message": "unknown verb " + verb}), nil
}

func sshEnvelope(v any) wexec.Result {
	b, _ := json.Marshal(v)
	return wexec.Result{Stdout: append(b, '\n')}
}

func sshStatus(state *State) wexec.Result {
	piPresent, passwordEnabled, keyPresent := state.SSHStatus()
	return sshEnvelope(map[string]any{
		"status":                 "ok",
		"pi_present":             piPresent,
		"password_auth_allowed":  passwordEnabled,
		"password_hash_unlocked": passwordEnabled,
		"managed_key_present":    keyPresent,
	})
}

func sshSetPassword(state *State, body []byte) wexec.Result {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return sshEnvelope(map[string]any{"status": "parse_error", "message": "request body must be a JSON object"})
	}
	if len([]rune(req.Password)) < sshMinPasswordLen {
		return sshEnvelope(map[string]any{
			"status":  "rejected",
			"reason":  "password_too_short",
			"message": "password must be at least 12 characters",
		})
	}
	state.SSHEnablePassword()
	return sshEnvelope(map[string]any{"status": "applied", "password_set": true, "reloaded": true})
}

func sshSetKey(state *State, body []byte) wexec.Result {
	var req struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return sshEnvelope(map[string]any{"status": "parse_error", "message": "request body must be a JSON object"})
	}
	if !sshValidPubkey(req.Key) {
		return sshEnvelope(map[string]any{
			"status":  "rejected",
			"reason":  "invalid_pubkey",
			"message": "not a valid OpenSSH public key (expected: <type> <base64-key> [comment])",
		})
	}
	state.SSHSetKey()
	return sshEnvelope(map[string]any{"status": "applied", "key_set": true})
}

// sshValidPubkey is a permissive mirror of apl_ssh_valid_pubkey: a known type
// token followed by a non-empty base64-ish blob. Only used to make the dev
// rejected-envelope branch reachable; the real validation lives in the helper.
func sshValidPubkey(v string) bool {
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	fields := splitFields(v)
	if len(fields) < 2 {
		return false
	}
	switch fields[0] {
	case "ssh-ed25519", "ssh-rsa", "ssh-dss",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
	default:
		return false
	}
	blob := fields[1]
	if blob == "" {
		return false
	}
	for _, c := range blob {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	return true
}

// splitFields splits on runs of spaces/tabs, returning at most the type, blob,
// and the remaining comment as a third field. Mirrors the helper's `read -r`.
func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ' ' || c == '\t' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			if len(out) == 2 {
				// Everything after the blob is the comment; stop splitting.
				break
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
