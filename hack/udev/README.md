# Auto re-pin wireless adb when the phone re-enumerates (optional)

`adb tcpip 5555` is dropped whenever the **phone** reboots — it falls back to
USB-only and the farm's `<ip>:5555` endpoint goes dark until you re-run
`hack/phone-prep.sh`. If the farm phone stays cabled to this machine for power, you
can make that re-pin automatic: a udev rule fires a systemd one-shot that runs
`phone-prep.sh` every time the device appears on USB.

This is host-side glue, not part of the farm; install it only on the machine the
phone is physically plugged into.

## Install

1. Find your phone's USB vendor id (with it plugged in):

   ```bash
   lsusb              # e.g. "ID 18d1:4ee7 Google Inc. Nexus/Pixel"
   ```

   The vendor id is the 4 hex digits before the colon (`18d1` for Google/Pixel,
   `04e8` Samsung, `2717` Xiaomi, `22b8` Motorola, …).

2. Edit `99-farm-phone.rules` below and set `ATTR{idVendor}` to your vendor id (and,
   if you want to scope it to one handset, add `ATTR{serial}=="<usb-serial>"`).

3. Install the unit + rule (paths assume this repo at `~/projects/android-device-farm`;
   adjust `REPO=` in the service file):

   ```bash
   sudo cp hack/udev/farm-phone-prep@.service /etc/systemd/system/
   sudo cp hack/udev/99-farm-phone.rules     /etc/udev/rules.d/
   sudo systemctl daemon-reload
   sudo udevadm control --reload-rules
   ```

4. Re-plug the phone (or `sudo udevadm trigger`). Check it ran:

   ```bash
   systemctl status 'farm-phone-prep@*'
   journalctl -u 'farm-phone-prep@*' -n 50
   ```

Notes
- The service runs as your user (`User=` in the unit) so it uses your `~/.android/adbkey`
  — keep that the same key the in-cluster adb server presents (see
  `docs/runbook.md` "Physical devices") so no RSA prompt appears.
- The rule debounces on the `add` action for the top-level USB device; adb needs a
  second to come up, which `phone-prep.sh` already sleeps for.
