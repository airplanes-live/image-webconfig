#!/bin/bash
#
# Emit the feeder identity (UUID + claim secret + claim version) as JSON
# on stdout. Format mirrors apl-feed backup exactly so the artifact can
# be fed back into apl-feed restore on another feeder.
#
# Invoked by airplanes-webconfig via the fixed-argv sudoers entry that
# pins this script's path. The wrapper exists so a compromised webconfig
# has one inert verb to reach this surface and cannot drift across other
# apl-feed subcommands via the same grant.
set -euo pipefail
exec /usr/local/bin/apl-feed backup -
