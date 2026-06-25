# client (farmctl)

A small Go client + CLI for leasing devices from the farm. It operates on
`DeviceLease` custom resources (`farm.example.com/v1alpha1`) via the cluster API.

Build:

```bash
mise run client-build   # -> client/bin/farmctl
```

Usage:

```bash
# Acquire a device of a class and wait until it's bound:
farmctl acquire --class pixel-api34 --namespace devicefarmer --ttl 600 --wait
# -> created lease devicefarmer/lease-abcde
# -> bound: device=... adb=...-adb.devicefarmer.svc.cluster.local:5555

# Keep it alive (call faster than the TTL):
farmctl heartbeat --name lease-abcde --namespace devicefarmer

# Release when done (frees + recycles the device):
farmctl release --name lease-abcde --namespace devicefarmer
```

**CI pattern (pinger sidecar):** `acquire --wait` at job start, run a loop that
calls `heartbeat` on an interval shorter than the TTL, and `release` on exit. If
the job dies, the missed heartbeats let the lease reaper reclaim the device after
the TTL.

It uses the ambient kubeconfig (`KUBECONFIG` / `~/.kube/config` / in-cluster).
