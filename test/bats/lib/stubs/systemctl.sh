#!/bin/bash
# systemctl stub for webconfig self-update bats. Installed at /usr/bin/systemctl.
# The container is systemd-less; the stub records every invocation to
# /tmp/airplanes-test/systemctl-calls.log and returns 0 (or non-zero, per
# /tmp/airplanes-test/systemctl-mode).
#
# Modes:
#   ok                     — every call returns 0 (default)
#   restart-fails-first    — the FIRST `systemctl restart <unit>` returns 1;
#                            subsequent calls (including the rollback restart)
#                            return 0. Exercises the helper's restart-fail
#                            branch.
#   daemon-reload-fails    — every `systemctl daemon-reload` returns 1.
#                            Exercises the helper's daemon-reload-fail branch.

LOG=/tmp/airplanes-test/systemctl-calls.log
CONTROL=/tmp/airplanes-test/systemctl-mode
RESTART_COUNTER=/tmp/airplanes-test/systemctl-restart-count

printf 'systemctl %s\n' "$*" >> "$LOG"

mode="$(cat "$CONTROL" 2>/dev/null || echo ok)"

case "$mode" in
    ok)
        exit 0
        ;;
    restart-fails-first)
        if [ "${1:-}" = "restart" ]; then
            count="$(cat "$RESTART_COUNTER" 2>/dev/null || echo 0)"
            count=$((count + 1))
            echo "$count" > "$RESTART_COUNTER"
            if [ "$count" -eq 1 ]; then
                echo "systemctl-stub: simulating restart failure" >&2
                exit 1
            fi
        fi
        exit 0
        ;;
    daemon-reload-fails)
        if [ "${1:-}" = "daemon-reload" ]; then
            echo "systemctl-stub: simulating daemon-reload failure" >&2
            exit 1
        fi
        exit 0
        ;;
    *)
        echo "systemctl-stub: unknown systemctl-mode '$mode'" >&2
        exit 1
        ;;
esac
