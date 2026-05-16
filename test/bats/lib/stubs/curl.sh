#!/bin/bash
# curl stub for webconfig self-update bats. Installed at /usr/bin/curl,
# shadowing the real curl which sits at /usr/local/bin/curl.real. The helper
# under test re-execs with env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin so it
# picks up this stub; install.sh inherits the same PATH and uses this stub
# for its file:// downloads, which delegate to real curl.
#
# Behavior is controlled by /tmp/airplanes-test/health-mode, written by each
# bats setup():
#   ok    — /health returns 0 (success — used for happy-path tests)
#   fail  — /health returns 7 (connection-refused-equivalent; helper retries)
#   slow  — /health sleeps the helper's full 2s curl timeout then returns 28
#           (timeout; same observable outcome as fail but exercises the
#           per-attempt timeout path)
#
# Any URL that does NOT look like the helper's health probe is passed through
# to /usr/local/bin/curl.real verbatim, so install.sh's file:// downloads
# work normally.

REAL_CURL=/usr/local/bin/curl.real
CONTROL=/tmp/airplanes-test/health-mode

is_health_url=0
for arg in "$@"; do
    case "$arg" in
        */health*) is_health_url=1; break ;;
    esac
done

if [ "$is_health_url" -eq 1 ]; then
    mode="$(cat "$CONTROL" 2>/dev/null || echo ok)"
    case "$mode" in
        ok)
            exit 0
            ;;
        fail)
            # Match curl's exit-code 7 (Couldn't connect to host) so anyone
            # debugging sees a realistic shape in the journal.
            exit 7
            ;;
        slow)
            sleep 3
            exit 28
            ;;
        *)
            echo "curl-stub: unknown health-mode '$mode' (expected ok|fail|slow)" >&2
            exit 1
            ;;
    esac
fi

exec "$REAL_CURL" "$@"
