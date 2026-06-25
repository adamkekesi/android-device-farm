# Android Device Farm

A self-hosted Android device farm on Kubernetes, split into two independently
deployable components in one monorepo:

- **Control plane** (`charts/devicefarmer`) — a Helm chart that deploys
  [DeviceFarmer](https://github.com/DeviceFarmer) (STF): RethinkDB, the triproxies,
  processor, app, auth, api, websocket, storage plugins, reaper, and ingress. This
  is relatively static infrastructure, so Helm is the right tool.
- **Device pool** (`operator/`) — a [Kubebuilder](https://kubebuilder.io) operator
  that manages a dynamic pool of emulators (and, later, physical devices):
  on-demand provisioning per device type, a global capacity cap, type-aware
  eviction, per-workflow leasing with heartbeats, health/recovery, and registration
  of ready devices into STF.

The full design and the phased delivery plan live in
[`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md). Work it **phase by
phase** — do not start a later phase until the previous phase's acceptance criteria
pass.

## Repository layout

```
charts/devicefarmer   Helm chart for the STF control plane
operator/             Kubebuilder project (go/v4 layout)
  api/v1alpha1        CRD Go types (DeviceClass, DevicePool, Device, DeviceLease)
  internal/controller reconcilers
  internal/provider   emulator & physical providers (device lifecycle)
  internal/stf        STF registration client
  cmd/                manager entrypoint
  config/             kustomize manifests (CRDs, RBAC, manager)
client/               Go client + CLI for leasing (acquire/heartbeat/release)
hack/                 kind setup, KVM/USB passthrough, dev scripts
docs/                 architecture notes, runbook, implementation plan
.mise.toml            pinned tool versions, env, and tasks
```

## Getting started

The toolchain (Go, kubebuilder, controller-gen, kustomize, helm, kubectl, kind,
golangci-lint, setup-envtest) is pinned in `.mise.toml`. Everything runs through
[mise](https://mise.jdx.dev).

```bash
mise install          # provision the exact pinned toolchain
mise tasks            # list every available task
mise run kind-up      # create a local kind cluster with KVM passthrough
mise run build        # build the operator
mise run test         # unit + envtest controller tests
mise run chart-install # install the DeviceFarmer control plane
mise run dev          # full local loop: cluster, chart, build, load, deploy
```

> **KVM is a hard requirement.** Emulator pods run Google's Android emulator images
> on KVM-capable nodes with `/dev/kvm` mounted in. There is no no-KVM fallback.

### Local environment caveat (this machine)

This dev box has older `kind`/`kubectl`/`helm` in `~/.local/bin` (installed by the
sibling `gha-runner-k8s` project), and `~/.profile` re-prepends `~/.local/bin` to
PATH *after* `mise activate` runs in `.bashrc`. As a result, `mise run …` tasks can
pick up those stale tools instead of the versions pinned in `.mise.toml`.

The most visible symptom is `mise run kind-load` failing with
`ERROR: failed to detect containerd snapshotter` — that's the old `kind` 0.22 being
unable to introspect the kind node's containerd 2.x.

Until the environment is reconciled, force the pinned tools to the front of PATH for
the current shell:

```bash
export PATH="$(dirname "$(mise which kind)"):$(dirname "$(mise which kubectl)"):$(dirname "$(mise which helm)"):$PATH"
```

(or invoke a single tool directly, e.g. `"$(mise which kind)" get clusters`).
Tools not present in `~/.local/bin` — go, kubebuilder, kustomize, golangci-lint,
controller-gen — already resolve to the pinned versions with no workaround.

## CRDs (group `farm.example.com`, version `v1alpha1`)

| Kind          | Scope      | Owner    | Purpose                                            |
| ------------- | ---------- | -------- | -------------------------------------------------- |
| `DeviceClass` | cluster    | user     | Catalog of device types (emulator / physical)      |
| `DevicePool`  | namespaced | user     | Capacity & policy (warm counts, cap, eviction)     |
| `DeviceLease` | namespaced | user     | A workflow's exclusive claim on a device           |
| `Device`      | namespaced | operator | One running device instance (operator-managed)     |

Short names: `dfc` (DeviceClass), `dfp` (DevicePool), `dfd` (Device), `dfl`
(DeviceLease). Use the short name or the fully-qualified `deviceclasses.farm.example.com`
for DeviceClass — the bare `deviceclass` resolves to the built-in
`resource.k8s.io/DeviceClass` (Dynamic Resource Allocation) on k8s 1.31+.

See `docs/IMPLEMENTATION_PLAN.md` §5 for the full API design.

## Status

- **Phase 0 — scaffolding & dev loop:** done. Toolchain provisions, `kind-up`
  yields a working cluster, the operator image builds, and the manager pod runs.
- **Phase 1 — DeviceFarmer control-plane chart:** done. `charts/devicefarmer`
  deploys the full STF topology (RethinkDB, triproxies, app, auth, api, websocket,
  processor, reaper, groups-engine, storage temp/apk/image) with a schema-migrate
  hook Job and an Ingress. Verified on kind: the STF UI serves at the ingress host
  (`/` → `/auth/mock/`, mock login returns 200). Device registration is Phase 3.
- **Phase 2 — operator API types:** done. The four CRDs (`DeviceClass`, `DevicePool`,
  `Device`, `DeviceLease`) are defined in `operator/api/v1alpha1` with validation
  markers, defaults, and printer columns. Verified on kind: CRDs install, samples
  round-trip, defaulting applies, and invalid input is rejected. Reconcilers come
  in Phases 3–5.
- **Phase 3 — emulator provisioning & warm pool:** in progress. Increment 1 (the
  `DevicePool` warm-pool reconciler in `operator/internal/controller`) is done and
  envtest-verified (84.8% pkg coverage): it provisions `minWarm` `Device`s per
  class, recreates them on deletion, reports per-class status + a `Ready` condition,
  and flags missing `DeviceClass`es. Increment 2 (the `Device` reconciler) is also
  done and envtest-verified (84.1% pkg coverage): it backs each `Device` with an
  emulator Pod (`/dev/kvm` passthrough, privileged, class image/nodeSelector/
  tolerations, boot-completed readiness probe) and an adb `Service`, publishes the
  adb endpoint, transitions to `Ready` on pod readiness, recreates the pod on
  deletion, and fails on a missing class. **Remaining:** live-emulator boot on a
  KVM cluster and STF registration (needs STF's `provider`/`adb` components) — the
  heavier, real-cluster verification.

See the implementation plan for the phase breakdown and acceptance criteria.
