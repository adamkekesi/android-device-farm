# Operator runbook

Quick operations for the Android Device Farm. See `IMPLEMENTATION_PLAN.md` for the
design and `../README.md` for the workflow.

## KVM node setup

Emulator pods need `/dev/kvm`. Nodes that can run emulators must:
- expose `/dev/kvm` (bare-metal or `.metal` instances; nested virt for kind/dev),
- carry the label your `DeviceClass.spec.nodeSelector` targets, e.g.
  `farm.example.com/kvm=true`,
- (optional) a taint like `dedicated=android-emulator:NoSchedule` matched by the
  class `tolerations`, to keep general workloads off.

Locally, `mise run kind-up` creates a single-node kind cluster with `/dev/kvm`
passed through and the KVM label set.

## Deploy

```bash
mise run install                # install CRDs
mise run docker-build && mise run kind-load && mise run deploy   # operator

# DeviceFarmer (STF). On local kind, kind-up publishes the ingress on host :8080,
# so expose STF at http://localhost:8080 (no port-forward, no /etc/hosts):
helm upgrade --install devicefarmer charts/devicefarmer -n devicefarmer --create-namespace \
  --set ingress.host=localhost --set ingress.externalURL=http://localhost:8080
```

The UI is then at **http://localhost:8080/** (mock login: the seeded admin). For a
real DNS host, set `ingress.host`/`ingress.externalURL` accordingly and point DNS
at the ingress. `mise run chart-install` uses the chart default host (`stf.local`).

**In-browser device screen/control** works because `revProxy.enabled` (default)
puts an nginx reverse proxy in front that routes STF's dynamic per-device screen
WebSocket (`/d/<provider>/<serial>/<port>/`) to the provider — something a plain
Ingress can't do. Open a Ready device in the STF UI to mirror and control it.

### Blank device screen / "Use" does nothing after the farm sits idle

STF's units talk over a ZeroMQ mesh through the `triproxy-app`/`triproxy-dev`
brokers (websocket → processor → triproxy-dev → provider). Those sockets have no
app-level heartbeat, so if a link goes stale while the farm is idle for a long
time, control messages are silently dropped: the UI loads, the REST device data
is correct, the screen WS even upgrades — but clicking **Use** never reaches the
provider, so no screen starts and the client errors on a null control channel.

Symptom check: `kubectl -n devicefarmer logs deploy/devicefarmer-provider` shows
no `owned by ...` / `minicap` activity when you click Use. Re-establish the mesh
by rolling the control plane (RethinkDB and the operator-managed emulator pods
are untouched; the provider re-attaches the device automatically):

```bash
kubectl -n devicefarmer rollout restart \
  deploy/devicefarmer-triproxy-app deploy/devicefarmer-triproxy-dev
kubectl -n devicefarmer rollout status deploy/devicefarmer-triproxy-dev
kubectl -n devicefarmer rollout restart \
  deploy/devicefarmer-processor deploy/devicefarmer-websocket \
  deploy/devicefarmer-groups-engine deploy/devicefarmer-reaper \
  deploy/devicefarmer-app deploy/devicefarmer-api deploy/devicefarmer-provider
```

Wait for the provider to log `Fully operational` / `Providing all N device(s)`,
then reload the STF tab and Use the device.

## Host adb access (farm emulators in your local `adb devices`)

The farm keeps adb inside the cluster, so emulators don't appear in your host
`adb` by default and per-device `kubectl port-forward` is tedious. The control
plane ships an **in-cluster `adb-registrar`** (chart default,
`adbRegistrar.enabled=true`) that does this automatically and is tied to the
cluster lifecycle — you deploy/scale the farm and it follows.

How it works: the operator exposes each device's adb as a **NodePort** Service, so
it's reachable from the host at `<nodeIP>:<nodePort>` (the kind node sits on the
Docker network — no route, no port-forward). The registrar's `watcher` container
lists Ready/Leased devices and their NodePorts; its `driver` container drives your
host adb server (`adb -H <gateway>`) to connect new devices and disconnect gone
ones.

The one host prerequisite: your adb server must listen on the kind network so a
pod can drive it. Run once per machine:

```bash
mise run host-adb      # = adb -a -P 5037 nodaemon server, backgrounded
```

After that, `adb devices` shows the farm emulators as `<nodeIP>:<nodePort>` and
stays correct as the pool churns. For persistence across reboots, run the same
`adb -a … nodaemon server` from a `systemd --user` service.

Security note: network-mode adb means any container on the kind network can drive
your adb server. That's fine for a local dev farm; don't do it on a shared host.
If your host isn't at the default kind gateway, set `adbRegistrar.hostADB.host`.

**Alternative (no NodePort / no network-mode adb):** `mise run adb-sync` runs a
host-side loop that adds one pod-network route (sudo, once) and `adb connect`s
each device by pod IP. Use this if you'd rather not expose your adb server; don't
run both.

## Google Play emulators (API 36)

The `emu29` class uses a slim AOSP-ish image whose Google Play Services is from
2020 — modern Google Sign-In fails with "version not supported". The `emu36-play`
class fixes that with a locally built API-36 `google_apis_playstore` image
(current GMS, Play-certified). Build/load and enable it:

```bash
mise run emulator-load            # build + stream the image into kind
kubectl apply -f images/android-emulator/deviceclass-emu36-play.yaml
# add it to a pool: spec.classes: [{name: emu36-play, minWarm: 1}]
```

Two non-obvious things this image handles:

- **Emulator pinned to 34.1.20.** The current emulator (36.x) ships a QEMU that
  segfaults under this host's KVM (vCPU threads hang). 34.1.20 (`emulator-legacy`)
  runs API 36 fine. See `images/android-emulator/Dockerfile` (`EMULATOR_BUILD`).
- **adb authorization.** Play-certified (`user`) images enforce adb auth and
  `ro.adb.secure=0` can't be overridden, so the farm uses a shared adb key:
  - the image authorizes a baked key (`images/android-emulator/adbkeys/`, gitignored),
  - STF's adb server presents it via `ADB_VENDOR_KEYS` — create the secret and
    enable it:
    ```bash
    kubectl -n devicefarmer create secret generic devicefarmer-adb-key \
      --from-file=adbkey=images/android-emulator/adbkeys/adbkey \
      --from-file=adbkey.pub=images/android-emulator/adbkeys/adbkey.pub
    helm upgrade devicefarmer charts/devicefarmer -n devicefarmer \
      --set ingress.host=localhost --set ingress.externalURL=http://localhost:8080 \
      --set adb.adbKeySecret=devicefarmer-adb-key
    ```
  - your host adb uses the same key (it's installed at `~/.android/adbkey`), so
    `adb connect` to these devices authorizes with no "allow USB debugging" tap.

Rebuilding the image regenerates the farm key; re-run the secret + `helm upgrade`
and copy the key to `~/.android/` again if so.

## Scaling the warm pool

Edit the `DevicePool`:
- `spec.classes[].minWarm` — idle devices kept hot per class.
- `spec.maxConcurrent` — hard cap on running devices in the pool.
- `spec.evictionPolicy: LRUIdle` — at capacity, a new device type evicts the
  least-recently-used idle device of another class.

The pool reserves capacity for pending leases, so warm devices yield to demand.

## Leasing

```bash
farmctl acquire --class pixel-api34 -n devicefarmer --ttl 600 --wait
farmctl heartbeat --name <lease> -n devicefarmer   # call faster than the TTL
farmctl release   --name <lease> -n devicefarmer
```

A lease whose heartbeats stop is auto-released after its TTL and its device is
recycled.

## Recovering a stuck device

The operator detects crash-loops, pod failures, and post-boot wedges, then
recreates the emulator pod with capped exponential backoff. To inspect/intervene:

```bash
kubectl -n devicefarmer get dfd                 # device phases
kubectl -n devicefarmer describe dfd <name>      # Ready condition reason
kubectl -n devicefarmer get pod <name>-emulator -o yaml   # pod/probe state
kubectl -n devicefarmer delete dfd <name>        # force a full rebuild (pool refills)
```

A bound lease shows a `DeviceHealthy` condition while its device self-heals.

## Physical devices

A physical handset joins the farm over **wireless adb**: STF's adb server runs
in-cluster and can only reach a device over TCP, so we pin a stable port with
`adb tcpip 5555` and register the phone as a `providerType: physical` Device whose
`adbEndpoint` is `<phone-ip>:5555`. It then flows through the same lease/health
contract as an emulator.

> Why not Android's "Wireless debugging" toggle? It hands out a random mDNS port that
> changes constantly — useless as a fixed farm endpoint. `adb tcpip 5555` gives a
> stable one.
>
> (For real cluster nodes with handsets cabled in, the alternative USB-host DaemonSet
> still exists — `physicalProvider.enabled=true` on nodes labelled
> `farm.example.com/usb-host=true`. The wireless path below is what fits kind/dev and
> any LAN-reachable device.)

### 1. Prep the device (USB, one-time)

Plug the phone in, enable Developer Options → USB debugging, then:

```bash
mise run phone-prep            # or: mise run phone-prep -- <serial> if several attached
```

This pins `tcpip 5555`, sets a **sleep-when-idle** power baseline, best-effort disables
the keyguard, and prints the `adbEndpoint` (`<ip>:5555`). Tap **Always allow from this
computer** on the RSA prompt. Reserve a fixed DHCP lease for that IP so the endpoint
stays stable.

> Disabling the lock needs no *secure* credential set — if a PIN/pattern is
> configured, clear it first under Settings → Security → Screen lock → None (the farm
> cannot enter your credentials for you). `adb tcpip` resets to USB-only on a **phone**
> reboot; re-run `phone-prep` then, or install the udev auto-re-pin helper
> (`hack/udev/`).

### 2. Register it into the farm

```bash
mise run physical-install      # FIRST: apply the physical DeviceClass + physical-pool

helm upgrade devicefarmer charts/devicefarmer -n devicefarmer --reuse-values \
  --set physicalProvider.networkDevices[0].name=pixel7 \
  --set physicalProvider.networkDevices[0].adbEndpoint=<ip>:5555 \
  --set physicalProvider.networkDevices[0].class=physical-pixel \
  --set physicalProvider.networkDevices[0].poolRef=physical-pool \
  --set adb.adbKeySecret=devicefarmer-adb-key
```

Apply the class/pool **before** the agent creates Devices. If you don't, a Device whose
`classRef` has no DeviceClass goes `Failed` with `DeviceClassNotFound` — but the operator
watches DeviceClasses and **auto-recovers** such Devices to `Ready` the moment the class
is applied (no restart/poke needed), so order is forgiving, not fatal.

The in-cluster `physical-net` agent upserts the `phys-<name>` Device CR and keeps
`adb connect <ip>:5555` alive against the cluster adb server; the provider picks it up
and it appears in STF. Verify:

```bash
kubectl -n devicefarmer get dfd            # phys-… reaches Ready
```

> **Multiple devices:** list them all in a values file rather than reusing
> `--set physicalProvider.networkDevices[0]...`, which *replaces* the existing entry and
> silently drops your other phones. See `images/physical/networkdevices.example.yaml`:
> ```bash
> helm upgrade devicefarmer charts/devicefarmer -n devicefarmer --reuse-values \
>   -f images/physical/networkdevices.example.yaml
> ```
> The agent reads its device list once at startup, so it's pinned to a `checksum/config`
> annotation — any change to `physicalProvider` rolls the agent pod automatically, so new
> devices are picked up without a manual restart.

Open it in the STF UI (`http://localhost:8080/`) → **Use** mirrors the screen and
input works.

### Staying connected across reboots

- **adb identity must be stable.** Set `adb.adbKeySecret` to a persistent Secret (the
  same `devicefarmer-adb-key` the Play-emulator section creates, with `~/.android/adbkey`
  matching). Otherwise the adb server regenerates a key on each restart and the phone
  re-prompts, breaking unattended reconnect. Authorize that key once on the phone.
- **The reconnect is continuous**, not one-shot: the `physical-net` agent re-issues
  `adb connect` every `physicalProvider.reconnectIntervalSeconds`, so the device
  re-attaches automatically after the adb-server pod or the whole cluster restarts.
  (The operator's own registration Job is one-shot and can't do this.)
- **Make the kind node come back on host reboot** (kind doesn't guarantee it):
  ```bash
  docker update --restart=unless-stopped ${KIND_CLUSTER:-devicefarm}-control-plane
  ```
  Cluster state (Device CR, class, pool) persists on the node volume; the operator,
  STF, and the agent resume on their own.
- **Phone reboot** drops `tcpip` — re-run `mise run phone-prep`, or install the udev
  helper (`hack/udev/`) to do it automatically when the phone re-enumerates on USB.

### Screen power (sleep when idle, awake while in use)

By default the device screen is **not** forced always-on. `phone-prep` sets a
sleep-when-idle baseline (`stay_on_while_plugged_in 0`, a finite `screen_off_timeout`,
and `wifi_sleep_policy=2` so Wi-Fi — hence wireless adb and STF presence — survives the
screen going off). The in-cluster `physical-net` agent then drives screen power per
`physicalProvider.screenPower`:

- `leased` (default): keep the screen awake **only** while the device is **leased**
  (`farmctl`, `Device.phase == Leased`) **or** actively viewed in STF (minicap running
  on the device); otherwise let it sleep. On going in-use the agent wakes it
  (`KEYCODE_WAKEUP` + dismiss-keyguard); on going idle it sleeps it (`KEYCODE_SLEEP`).
  Transitions are detected per loop, so there's up to `reconnectIntervalSeconds` of lag
  at the start of a session — lower the interval if you want it snappier.
- `always`: keep it awake whenever charging (the old behaviour).
- `none`: don't touch screen power.

> Note: "in use" via minicap covers people who just click **Use** in the STF UI without
> taking a `farmctl` lease — so this works even while a device's operator phase is
> `Failed`. A purely automated `farmctl` lease (no STF viewer) keeps it awake via the
> phase check. Compatible with the brightness-0 "dark screen" trick: while in use the
> screen is on (optionally dark, STF still captures full content); while idle it's off.

### Unlock & control while locked

- **Auto-unlock:** the farm cannot defeat a *secure* lock (it can't type your PIN).
  With the secure lock set to None (done by `phone-prep`), the device never reaches a
  credential-gated lock screen; while in use the agent wakes it and dismisses the
  keyguard, so it's effectively always unlocked during a session.
- **Control while locked:** STF's screen mirror and input work regardless of lock
  state — you can see and tap the lock screen. With a secure lock you'd type the PIN
  yourself through STF; with the lock disabled there's nothing to get past.
