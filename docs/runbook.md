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
mise run chart-install          # DeviceFarmer (STF) control plane
mise run install                # install CRDs
mise run docker-build && mise run kind-load && mise run deploy   # operator
```

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
