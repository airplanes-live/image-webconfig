#!/usr/bin/env bats
#
# Unit tests for apl-ssh — the privileged SSH-access helper for the pi account.
# getent/chpasswd/passwd/sshd/systemctl are stubbed via the APL_SSH_* test
# seams; the snippet + managed-key files land in a temp tree so we can assert
# the contract artifacts byte-for-byte.

setup() {
    command -v jq >/dev/null || skip "jq not installed"

    APLSSH="$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-ssh"
    [ -x "$APLSSH" ] || skip "apl-ssh missing"

    WORK="$(mktemp -d)"
    export APL_SSH_LIB_DIR="$BATS_TEST_DIRNAME/../../files/usr/local/lib/airplanes"
    export APL_SSH_SNIPPET="$WORK/sshd_config.d/99-airplanes-ssh-pi.conf"
    export APL_SSH_KEY_DIR="$WORK/authkeys"
    export APL_SSH_KEY_FILE="$WORK/authkeys/pi"
    export APL_SSH_LOCK="$WORK/run/ssh.lock"
    mkdir -p "$WORK/run"

    # Action log so tests can assert which privileged commands ran.
    export ACTIONS="$WORK/actions.log"
    : > "$ACTIONS"

    mkdir -p "$WORK/bin"

    # getent: pi present unless PI_ABSENT=1.
    cat > "$WORK/bin/getent" <<EOF
#!/usr/bin/env bash
if [[ "\$2" == "pi" && "\${PI_ABSENT:-0}" != "1" ]]; then
    echo "pi:x:1000:1000:,,,:/home/pi:/bin/bash"; exit 0
fi
exit 2
EOF
    # chpasswd: read user:pass from stdin, log it (NOT the password), succeed.
    cat > "$WORK/bin/chpasswd" <<EOF
#!/usr/bin/env bash
read -r line
echo "chpasswd \${line%%:*}" >> "$ACTIONS"
exit 0
EOF
    # passwd: -S reports status (P/L), -l logs a lock.
    cat > "$WORK/bin/passwd" <<EOF
#!/usr/bin/env bash
if [[ "\$1" == "-S" ]]; then echo "pi \${PW_STATUS:-P} 01/01/2026 0 99999 7 -1"; exit 0; fi
if [[ "\$1" == "-l" ]]; then echo "passwd -l \$2" >> "$ACTIONS"; exit 0; fi
exit 0
EOF
    # sshd -T: report passwordauthentication per PW_ALLOWED (default yes).
    cat > "$WORK/bin/sshd" <<EOF
#!/usr/bin/env bash
echo "passwordauthentication \${PW_ALLOWED:-yes}"
exit 0
EOF
    # systemctl reload ssh: log it, succeed.
    cat > "$WORK/bin/systemctl" <<EOF
#!/usr/bin/env bash
echo "systemctl \$*" >> "$ACTIONS"
exit 0
EOF
    chmod +x "$WORK"/bin/*
    export APL_SSH_GETENT="$WORK/bin/getent"
    export APL_SSH_CHPASSWD="$WORK/bin/chpasswd"
    export APL_SSH_PASSWD="$WORK/bin/passwd"
    export APL_SSH_SSHD="$WORK/bin/sshd"
    export APL_SSH_SYSTEMCTL="$WORK/bin/systemctl"
}

teardown() {
    [ -n "${WORK:-}" ] && rm -rf "$WORK"
}

@test "status emits granular facts as a JSON envelope" {
    run "$APLSSH" status --json <<< '{}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.status == "ok"'
    echo "$output" | jq -e '.pi_present == true'
    echo "$output" | jq -e '.password_auth_allowed == true'
    echo "$output" | jq -e '.password_hash_unlocked == true'
    echo "$output" | jq -e '.managed_key_present == false'
}

@test "status reports password_auth_allowed=false when sshd says no" {
    PW_ALLOWED=no run "$APLSSH" status --json <<< '{}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.password_auth_allowed == false'
}

@test "status reports managed_key_present=true once a key file exists" {
    mkdir -p "$APL_SSH_KEY_DIR"
    echo "ssh-ed25519 AAAA user@host" > "$APL_SSH_KEY_FILE"
    run "$APLSSH" status --json <<< '{}'
    echo "$output" | jq -e '.managed_key_present == true'
}

@test "enable-password rejects a password shorter than 12 chars" {
    run "$APLSSH" enable-password --json <<< '{"password":"short"}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.status == "rejected"'
    echo "$output" | jq -e '.reason == "password_too_short"'
    # No chpasswd, no snippet written.
    ! grep -q chpasswd "$ACTIONS"
    [ ! -f "$APL_SSH_SNIPPET" ]
}

@test "enable-password rejects a password containing a newline (chpasswd injection guard)" {
    # The \n is a JSON escape, so the parsed password contains a real newline.
    # chpasswd reads user:password line-by-line, so a newline would inject a
    # second record (e.g. root:...). The validator must reject before chpasswd.
    run "$APLSSH" enable-password --json <<< '{"password":"abcdefghijkl\nroot:pwned1234567"}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.status == "rejected"'
    ! grep -q chpasswd "$ACTIONS"
    [ ! -f "$APL_SSH_SNIPPET" ]
}

@test "enable-password sets the password and writes the byte-exact snippet" {
    run "$APLSSH" enable-password --json <<< '{"password":"longenoughpassword"}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.status == "applied"'
    echo "$output" | jq -e '.password_set == true'
    grep -q "chpasswd pi" "$ACTIONS"
    grep -q "systemctl reload ssh" "$ACTIONS"
    # Snippet content must match the cross-repo contract byte-for-byte.
    [ -f "$APL_SSH_SNIPPET" ]
    expected="$(cat <<'SNIPPET'
# airplanes.live per-device opt-in: enables password SSH for the pi account
# only (Match-scoped, so other users keep the 90-airplanes.conf default of
# PasswordAuthentication no). Written by airplanes-first-run (boot config) and
# webconfig's apl-ssh helper.
Match User pi
    PasswordAuthentication yes
Match all
SNIPPET
)"
    [ "$(cat "$APL_SSH_SNIPPET")" == "$expected" ]
}

@test "snippet file is mode 0644" {
    "$APLSSH" enable-password --json <<< '{"password":"longenoughpassword"}' >/dev/null
    perm="$(stat -c '%a' "$APL_SSH_SNIPPET")"
    [ "$perm" == "644" ]
}

@test "set-password without a password field is a parse_error" {
    run "$APLSSH" set-password --json <<< '{}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.status == "parse_error"'
}

@test "disable-password ALWAYS locks the pi password and removes the snippet" {
    # Pre-seed an enabled snippet AND a managed key — disable must lock the
    # password regardless of key presence (the key is unaffected).
    mkdir -p "$(dirname "$APL_SSH_SNIPPET")" "$APL_SSH_KEY_DIR"
    echo "snippet" > "$APL_SSH_SNIPPET"
    echo "ssh-ed25519 AAAA user@host" > "$APL_SSH_KEY_FILE"

    run "$APLSSH" disable-password --json <<< '{}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.status == "applied"'
    echo "$output" | jq -e '.password_disabled == true'
    grep -q "passwd -l pi" "$ACTIONS"
    [ ! -f "$APL_SSH_SNIPPET" ]
    # The managed key is untouched by a password disable.
    [ -f "$APL_SSH_KEY_FILE" ]
}

@test "set-key rejects a value that is not an OpenSSH public key" {
    run "$APLSSH" set-key --json <<< '{"key":"not-a-key"}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.status == "rejected"'
    echo "$output" | jq -e '.reason == "invalid_pubkey"'
    [ ! -f "$APL_SSH_KEY_FILE" ]
}

@test "set-key rejects an unknown key type" {
    run "$APLSSH" set-key --json <<< '{"key":"ssh-vax AAAAB3 user@host"}'
    [ "$status" -eq 2 ]
    echo "$output" | jq -e '.reason == "invalid_pubkey"'
}

@test "set-key writes a single key file atomically, 0644" {
    run "$APLSSH" set-key --json <<< '{"key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTBLOB user@host"}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.status == "applied"'
    echo "$output" | jq -e '.key_set == true'
    [ -f "$APL_SSH_KEY_FILE" ]
    # Single line only.
    [ "$(wc -l < "$APL_SSH_KEY_FILE")" -eq 1 ]
    [ "$(cat "$APL_SSH_KEY_FILE")" == "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTBLOB user@host" ]
    [ "$(stat -c '%a' "$APL_SSH_KEY_FILE")" == "644" ]
}

@test "set-key OVERWRITES an existing managed key (single key, not appended)" {
    mkdir -p "$APL_SSH_KEY_DIR"
    echo "ssh-rsa OLDKEY old@host" > "$APL_SSH_KEY_FILE"
    "$APLSSH" set-key --json <<< '{"key":"ssh-ed25519 NEWBLOB new@host"}' >/dev/null
    [ "$(wc -l < "$APL_SSH_KEY_FILE")" -eq 1 ]
    grep -q "NEWBLOB" "$APL_SSH_KEY_FILE"
    ! grep -q "OLDKEY" "$APL_SSH_KEY_FILE"
}

@test "clear-key removes the managed key" {
    mkdir -p "$APL_SSH_KEY_DIR"
    echo "ssh-ed25519 AAAA user@host" > "$APL_SSH_KEY_FILE"
    run "$APLSSH" clear-key --json <<< '{}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.status == "applied"'
    echo "$output" | jq -e '.key_cleared == true'
    [ ! -f "$APL_SSH_KEY_FILE" ]
}

@test "clear-key on an already-absent key is idempotent (applied)" {
    run "$APLSSH" clear-key --json <<< '{}'
    [ "$status" -eq 0 ]
    echo "$output" | jq -e '.status == "applied"'
}

@test "an unknown subcommand emits usage_error" {
    run "$APLSSH" frobnicate --json <<< '{}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.status == "usage_error"'
}

@test "missing --json emits usage_error" {
    run "$APLSSH" status <<< '{}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.status == "usage_error"'
}

@test "an unknown request field is a parse_error" {
    run "$APLSSH" set-password --json <<< '{"password":"longenoughpassword","x":1}'
    [ "$status" -eq 5 ]
    echo "$output" | jq -e '.status == "parse_error"'
}

@test "a held lock yields lock_timeout (exit 4)" {
    export APL_SSH_LOCK_TIMEOUT=1
    # Hold the lock in a background subshell, then race a mutation against it.
    (
        exec 8> "$APL_SSH_LOCK"
        flock -x 8
        sleep 3
    ) &
    holder=$!
    # Give the holder a moment to acquire.
    sleep 0.3
    run "$APLSSH" set-key --json <<< '{"key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host"}'
    kill "$holder" 2>/dev/null || true
    wait "$holder" 2>/dev/null || true
    [ "$status" -eq 4 ]
    echo "$output" | jq -e '.status == "lock_timeout"'
}

@test "every path emits exactly one JSON line on stdout" {
    for verb_body in \
        'status:{}' \
        'enable-password:{"password":"longenoughpassword"}' \
        'disable-password:{}' \
        'clear-key:{}'; do
        verb="${verb_body%%:*}"; body="${verb_body#*:}"
        run "$APLSSH" "$verb" --json <<< "$body"
        # Exactly one line, and it parses as a JSON object with a status field.
        [ "$(printf '%s' "$output" | wc -l)" -le 1 ]
        echo "$output" | jq -e 'type == "object" and has("status")'
    done
}
