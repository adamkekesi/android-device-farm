#!/usr/bin/env bash
#
# phone-prep — one-time (and after every PHONE reboot) prep of a physical Android
# device so the farm can use it over wireless adb.
#
# Why this exists:
#   STF's adb server runs INSIDE the cluster and can only reach a device over TCP
#   (`adb connect host:port`) — it has no USB. Android's "Wireless debugging" toggle
#   hands out a RANDOM mDNS port that changes constantly, which is useless as a
#   stable farm endpoint. So we use classic `adb tcpip 5555` over USB to pin a fixed
#   port, then advertise <wlan-ip>:5555 to the farm.
#
#   `adb tcpip` is dropped when the PHONE reboots (it falls back to USB-only), so
#   re-run this script then — or wire up the udev helper in hack/udev/ to do it
#   automatically when the phone re-enumerates on USB.
#
# What it does (all over USB):
#   * adb tcpip 5555                     — pin a stable wireless adb port
#   * print <wlan-ip>:5555               — the adbEndpoint to put in the farm values
#   * sleep-when-idle baseline           — DON'T force the screen always-on. Allow
#     (stay_on 0 + finite timeout)         idle sleep; the in-cluster physical-net
#                                          agent raises "stay awake" only while the
#                                          device is leased/viewed (screenPower).
#   * wifi_sleep_policy never             — keep Wi-Fi up with the screen off so
#                                          wireless adb / STF presence survives sleep
#   * locksettings set-disabled true     — best-effort remove a non-secure keyguard
#                                          (see note: a secure PIN must be cleared in
#                                          Settings first)
#   * wake + dismiss-keyguard            — leave it awake and unlocked right now
#
# Usage:
#   hack/phone-prep.sh            # auto-selects the single USB device
#   hack/phone-prep.sh <serial>   # when multiple devices are attached
#   PORT=5557 hack/phone-prep.sh  # override the pinned tcpip port (default 5555)
#   SCREEN_OFF_TIMEOUT_MS=120000 hack/phone-prep.sh   # idle timeout (default 60000)
#
# Prereqs: the phone in Developer Options with "USB debugging" on; tap "Always
# allow from this computer" on the RSA prompt the first time (use the same key the
# in-cluster adb server presents — see docs/runbook.md "Physical devices").
set -euo pipefail

PORT="${PORT:-5555}"
IFACE="${IFACE:-wlan0}"

command -v adb >/dev/null 2>&1 || { echo "error: adb not found on PATH" >&2; exit 1; }

# Pick the target serial: explicit arg, else the only USB device, else complain.
serial="${1:-}"
if [ -z "$serial" ]; then
  mapfile -t devs < <(adb devices | awk 'NR>1 && $2=="device"{print $1}')
  if [ "${#devs[@]}" -eq 0 ]; then
    echo "error: no authorized USB device. Plug the phone in, enable USB debugging," >&2
    echo "       and accept the 'Allow USB debugging?' prompt, then re-run." >&2
    exit 1
  elif [ "${#devs[@]}" -gt 1 ]; then
    echo "error: multiple devices attached; pass one explicitly:" >&2
    printf '  hack/phone-prep.sh %s\n' "${devs[@]}" >&2
    exit 1
  fi
  serial="${devs[0]}"
fi

a() { adb -s "$serial" "$@"; }

model="$(a shell getprop ro.product.model 2>/dev/null | tr -d '\r')"
echo ">> Preparing ${model:-device} ($serial)"

# Power baseline: let the device SLEEP when idle. We deliberately do NOT force it
# always-on — the in-cluster physical-net agent raises "stay awake" only while the
# device is leased / actively viewed in STF (physicalProvider.screenPower), and lets
# it sleep otherwise. Keep Wi-Fi alive with the screen off so wireless adb (and thus
# STF presence) survives idle sleep.
echo ">> Power baseline: sleep when idle (agent keeps it awake while leased/viewed)"
a shell settings put global stay_on_while_plugged_in 0 >/dev/null || true
a shell settings put system screen_off_timeout "${SCREEN_OFF_TIMEOUT_MS:-60000}" >/dev/null || true
a shell settings put global wifi_sleep_policy 2 >/dev/null 2>&1 || true   # keep Wi-Fi on with screen off (best-effort; deprecated on newer OS)

# Best-effort disable the keyguard. This only works when there is NO secure
# credential set; if a PIN/pattern/password is configured, Android refuses and you
# must clear it in Settings > Security > Screen lock > None first (the farm cannot
# enter your credentials for you).
echo ">> Disabling the (non-secure) keyguard"
if ! a shell locksettings set-disabled true >/dev/null 2>&1; then
  echo "   note: could not disable the lock over adb — a secure lock is set."
  echo "   Clear it manually: Settings > Security > Screen lock > None, then re-run."
fi

# Wake it and dismiss the keyguard right now so it's usable immediately.
a shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1 || true
a shell wm dismiss-keyguard >/dev/null 2>&1 || true

# Pin a stable wireless adb port. (Android's "Wireless debugging" toggle would use a
# random, ever-changing port instead — this gives the farm a fixed endpoint.)
echo ">> Enabling wireless adb on port $PORT (adb tcpip)"
a tcpip "$PORT" >/dev/null
sleep 1

# Discover the device's LAN IP so we can print the adbEndpoint.
ip="$(a shell ip -f inet addr show "$IFACE" 2>/dev/null \
        | awk '/inet /{print $2}' | cut -d/ -f1 | head -n1 | tr -d '\r')"
if [ -z "$ip" ]; then
  ip="$(a shell ip route 2>/dev/null | awk '/wlan0/ && /src/{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -n1 | tr -d '\r')"
fi

echo
if [ -n "$ip" ]; then
  echo "Device ready. adbEndpoint:"
  echo
  echo "    ${ip}:${PORT}"
  echo
  echo "Next: put it in the farm and install (see docs/runbook.md):"
  echo "    helm upgrade devicefarmer charts/devicefarmer -n devicefarmer --reuse-values \\"
  echo "      --set physicalProvider.networkDevices[0].name=${model:-phone} \\"
  echo "      --set physicalProvider.networkDevices[0].adbEndpoint=${ip}:${PORT} \\"
  echo "      --set physicalProvider.networkDevices[0].class=physical-pixel \\"
  echo "      --set physicalProvider.networkDevices[0].poolRef=physical-pool"
  echo
  echo "Tip: reserve a fixed DHCP lease for ${ip} so the endpoint stays stable."
else
  echo "Device is in tcpip mode on port ${PORT}, but its $IFACE IP could not be read."
  echo "Find it under Settings > About phone > IP address and use <ip>:${PORT}."
fi
