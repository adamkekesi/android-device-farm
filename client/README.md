# client

A small Go client library + CLI for leasing devices from the farm:
`acquire`, `heartbeat`, `release`, plus an optional pinger sidecar pattern for CI
jobs.

This is the **Phase 4** deliverable (see `docs/IMPLEMENTATION_PLAN.md` §6). It is
intentionally empty until the `DeviceLease` reconciler exists.

The client talks to the cluster's Kubernetes API and operates on `DeviceLease`
custom resources in the `farm.example.com/v1alpha1` group:

- `acquire`   — create a `DeviceLease` for a `DeviceClass` and wait for it to bind.
- `heartbeat` — patch `lastHeartbeat` to extend the lease TTL.
- `release`   — release the lease so its device is recycled back into the pool.
