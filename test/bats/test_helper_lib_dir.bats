#!/usr/bin/env bats
#
# On a feeder, the runtime overlay lays wifi-validators.sh / wifi-keyfile.sh /
# ssh-validators.sh at /opt/airplanes/current/lib/airplanes. apl-wifi and
# apl-ssh must default their lib dir to that path: a stale default makes every
# wifi/ssh call fail on a real device while this suite — which overrides the
# APL_*_LIB_DIR env seam to source from the repo checkout — stays green. Pin
# the shipped defaults so a relayout can't strand the helpers silently again.

@test "apl-wifi defaults APL_WIFI_LIB_DIR to the overlay lib path" {
    grep -qF 'APL_WIFI_LIB_DIR="${APL_WIFI_LIB_DIR:-/opt/airplanes/current/lib/airplanes}"' \
        "$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-wifi"
}

@test "apl-ssh defaults APL_SSH_LIB_DIR to the overlay lib path" {
    grep -qF 'APL_SSH_LIB_DIR="${APL_SSH_LIB_DIR:-/opt/airplanes/current/lib/airplanes}"' \
        "$BATS_TEST_DIRNAME/../../files/usr/local/bin/apl-ssh"
}
