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

## Physical devices (future-ready)

Enable the USB-host DaemonSet (`physicalProvider.enabled=true`) on nodes labelled
`farm.example.com/usb-host=true`. It registers attached devices as
`providerType: physical` Devices that flow through the same lease/health contract.
Remote-adb reachability for physical devices is environment-specific.
