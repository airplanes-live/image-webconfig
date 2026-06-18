#!/bin/bash
#
# Rotate this feeder's claim secret with airplanes.live, replacing the
# local secret on success. apl-feed claim rotate mints a new secret,
# posts it bearer-authed with the current one, and promotes the pending
# secret to active only after the server accepts it (resuming a pending
# rotation if one was interrupted). --json makes it emit a single
# schema-v1 result object on stdout for airplanes-webconfig to parse.
#
# --max-retry-time 20 caps the helper's server round-trip + backoff so a
# slow or rate-limited server surfaces as a structured deadline_exceeded
# result well inside webconfig's own request budget, rather than the
# wrapper being killed mid-rotation.
#
# Invoked by airplanes-webconfig via the fixed-argv sudoers entry that
# pins this script's path. The wrapper takes no arguments and never
# forwards "$@", so a compromised webconfig has one inert verb to reach
# this surface and cannot drift across other apl-feed subcommands (e.g.
# rotate --abort, or apl-feed apply) via the same grant.
set -euo pipefail
exec /usr/local/bin/apl-feed claim rotate --json --max-retry-time 20
