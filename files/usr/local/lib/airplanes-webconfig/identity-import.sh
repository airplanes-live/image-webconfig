#!/bin/bash
#
# Replace this feeder's identity (UUID + claim secret) from a backup
# JSON document supplied on stdin. The wire shape is what apl-feed
# backup emits:
#
#   {"schema_version":1,
#    "feeder_uuid":"...",
#    "claim":{"secret":"...","version":null|N}}
#
# --force is required because the whole point of import is to replace
# whatever's already on the device with the supplied values. apl-feed
# restore validates the payload, writes UUID first, then secret, and
# rolls back the UUID on a secret-write failure, then restarts feed
# and mlat when the UUID actually changes.
#
# Invoked by airplanes-webconfig via the fixed-argv sudoers entry that
# pins this script's path. The wrapper exists so a compromised webconfig
# has one inert verb to reach this surface and cannot drift across other
# apl-feed subcommands via the same grant.
set -euo pipefail
exec /usr/local/bin/apl-feed restore /dev/stdin --force
