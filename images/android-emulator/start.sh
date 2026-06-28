#!/bin/bash
# Boot the Play Store AVD headless and block while it runs. adb is bound to
# 127.0.0.1:5555 (the operator's socat sidecar re-exposes it on the pod network),
# and we wait for sys.boot_completed so the pod's readiness probe goes green.
set -ex -o pipefail

export ANDROID_SDK_ROOT=/opt/sdk ANDROID_HOME=/opt/sdk
AVD_NAME="${AVD_NAME:-play}"
CONSOLE_PORT=5554
ADB_PORT=5555
CFG="${HOME}/.android/avd/${AVD_NAME}.avd/config.ini"

# Tunables (overridable via env on the DeviceClass if needed).
: "${EMULATOR_RAM_SIZE:=4096}"
: "${EMULATOR_NUM_CORE:=4}"
# -prop ro.adb.secure=0 disables adbd key authorization so ANY adb client (your
# host, STF) is accepted without the "allow USB debugging" handshake — needed
# because Play-certified (user) images enforce adb auth and there's no UI to tap.
: "${EMULATOR_OPTS:=-no-window -no-audio -no-boot-anim -no-snapshot -gpu swiftshader_indirect -skip-adb-auth -prop ro.adb.secure=0 -netfast -wipe-data}"

# A real phone geometry. Without this the AVD defaults to 320x640@160, which
# minicap mis-projects in STF (content in a top band, black below). 720x1280@320
# is a normal portrait phone, light enough for software GL streaming.
: "${LCD_WIDTH:=720}"
: "${LCD_HEIGHT:=1280}"
: "${LCD_DENSITY:=320}"

set_ini() { # key value file
  grep -q "^$1 *=" "$3" && sed -i -E "s|^$1 *=.*|$1=$2|" "$3" || echo "$1=$2" >> "$3"
}

if [ -f "$CFG" ]; then
  set_ini hw.lcd.width   "$LCD_WIDTH"   "$CFG"
  set_ini hw.lcd.height  "$LCD_HEIGHT"  "$CFG"
  set_ini hw.lcd.density "$LCD_DENSITY" "$CFG"
  sed -i -E "s/^hw\.ramSize=.*/hw.ramSize=${EMULATOR_RAM_SIZE}/" "$CFG" || true
  grep -q '^hw.ramSize' "$CFG" || echo "hw.ramSize=${EMULATOR_RAM_SIZE}" >> "$CFG"
  sed -i -E "s/^hw\.cpu\.ncore=.*/hw.cpu.ncore=${EMULATOR_NUM_CORE}/" "$CFG" || true
  grep -q '^hw.cpu.ncore' "$CFG" || echo "hw.cpu.ncore=${EMULATOR_NUM_CORE}" >> "$CFG"
  # Make sure the Play Store flag is on (it is for playstore images, but be safe).
  grep -q '^PlayStore.enabled' "$CFG" \
    && sed -i -E "s/^PlayStore\.enabled=.*/PlayStore.enabled=true/" "$CFG" \
    || echo 'PlayStore.enabled=true' >> "$CFG"
fi

# Clear any stale locks from an unclean shutdown.
find "${HOME}/.android/avd" -name '*.lock' -exec rm -f {} \; 2>/dev/null || true

# shellcheck disable=SC2086
emulator -avd "${AVD_NAME}" -ports ${CONSOLE_PORT},${ADB_PORT} ${EMULATOR_OPTS} &
EMULATOR_PID=$!

adb wait-for-device
until [ "$(adb shell getprop sys.boot_completed 2>&1 | tr -d '\r' | head -n1)" = "1" ]; do
  sleep 5
  echo "waiting for boot…"
done
echo "boot completed"

wait "${EMULATOR_PID}"
