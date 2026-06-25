# Android Device Farm: Implementation Plan

A plan for building a self-hosted Android device farm on Kubernetes, split into two
deliverables: a Helm chart that deploys the DeviceFarmer (STF) control plane, and a
Kubebuilder operator that manages a dynamic pool of emulators (and, later, physical
devices) with per-workflow leasing.

This document is written to be executed incrementally by Claude Code. Work phase by
phase. Do not start a later phase until the previous phase's acceptance criteria pass.

---

## 1. Goal and requirements

Build a system that satisfies these requirements (the acceptance backbone for the whole
project):

1. Start Android emulators on demand, supporting multiple device types, a global cap on
   running emulators, and discarding idle emulators of one type to make room when a new
   type is requested and there is no spare capacity.
2. Track which emulator is in use by which workflow, and guarantee an emulator is bound
   to at most one workflow at a time.
3. Detect emulator failures and crashes, and recover by recreating devices in a clean
   state.
4. (Future) Register physical devices into the same pool behind the same interface.

A secondary goal is remote UI access to devices through a browser, provided by the STF
control plane and optionally by a per-device viewer.

---

## 2. Assumptions and fixed decisions

State any deviation from these in the PR description if you change them.

- Language: Go for the operator. Target the current Go release at implementation time.
- Operator framework: Kubebuilder with the `go/v4` layout. Use the current
  controller-runtime. Do not use the legacy operator-sdk 0.x layout.
- Target Kubernetes: 1.30+. All CRDs are `apiextensions.k8s.io/v1` with full structural
  schemas. Do not emit any `v1beta1` CRD, RBAC, or Ingress resources.
- Device control plane: DeviceFarmer, using `devicefarmer/stf` images and the maintained
  `minicap`, `minitouch`, and `STFService.apk` components. Do not target the abandoned
  `openstf/*` images.
- Datastore: RethinkDB, because DeviceFarmer requires it. It is a managed dependency, not
  something to replace in this project.
- Emulator runtime: Google's Android emulator container images, running on KVM-capable
  nodes. Hardware acceleration via `/dev/kvm` is a hard requirement; there is no software or
  no-KVM fallback. Size the node pools accordingly (bare-metal or `.metal` instances), and
  mount `/dev/kvm` into every emulator pod.
- Ingress: Gateway API if available on the target cluster, otherwise ingress-nginx. Do not
  introduce Traefik v1 CRDs.
- Packaging: Helm for the control-plane chart.
- Task runner and toolchain: mise. Every tool the project needs (Go, kubebuilder,
  controller-gen, kustomize, helm, kubectl, kind, golangci-lint, setup-envtest) is pinned
  in `.mise.toml`, and every repeatable command is a mise task. Do not use Make. Kubebuilder
  scaffolds a Makefile by default; replace its targets with mise tasks. You may keep the
  scaffolded Makefile temporarily and have mise tasks shell into it, then retire it once the
  tasks are ported. A starter `.mise.toml` is in the appendix.
- Pin every tool version in `.mise.toml`. The versions in this document and in the starter
  config are starting points; verify the current stable release of each before committing.

Open assumptions made to keep scope bounded (flag if the user wants otherwise):
- Single-tenant to start. Multi-tenant quotas are a later enhancement.
- Physical device support is scaffolded in the API from the start but only implemented in
  the final phase.
- Cloud target is AWS, but nothing in the operator should be AWS-specific. KVM comes from
  bare-metal or `.metal` instances; keep node requirements abstract via labels and taints.

---

## 3. Architecture

Two independently deployable components in one monorepo.

**A. Control plane (Helm chart).** Deploys DeviceFarmer: RethinkDB, the database migrate
job, the app-side and dev-side triproxy, processor, app, auth, api, websocket, the storage
plugins (apk, image, temp), the reaper, and a reverse proxy or ingress in front. This is
relatively static infrastructure, so Helm is the right tool, not an operator.

**B. Device pool (Kubebuilder operator).** The dynamic part that needs continuous
reconciliation: provisioning emulators per type, enforcing the global cap, type-aware
eviction, leasing with heartbeats, health and recovery, and registering ready devices into
the STF control plane. Later it also manages physical-device providers.

The operator registers each ready device into STF over adb so devices are visible and
controllable in the STF browser UI. Leasing and pool policy live in the operator; STF
provides the human-facing UI and remote control.

---

## 4. Repository layout

```
/                         repo root
  /charts/devicefarmer     Helm chart for the STF control plane
  /operator                Kubebuilder project
    /api/v1alpha1          CRD Go types
    /internal/controller   reconcilers
    /internal/provider     emulator and physical providers (device lifecycle)
    /internal/stf          STF registration client
    /cmd                   manager entrypoint
    /config                kustomize manifests (CRDs, RBAC, manager)
  /client                  small Go client + CLI for leasing (acquire/heartbeat/release)
  /hack                    kind setup, KVM/USB passthrough, dev scripts
  /docs                    architecture notes, runbook
  .mise.toml               pinned tool versions, env, and tasks
  .mise/tasks/             file-based tasks (bash) for anything too complex for inline TOML
```

---

## 5. API design (CRDs)

Group: `farm.example.com`, version `v1alpha1`. Four kinds. Users author `DeviceClass`,
`DevicePool`, and `DeviceLease`. The operator owns `Device`.

### DeviceClass (cluster-scoped)
The catalog of device types.

- `spec.providerType`: `emulator` or `physical`.
- `spec.image`: emulator container image (emulator providers only).
- `spec.apiLevel`, `spec.arch`: for selection and labeling.
- `spec.avd`: device profile and emulator parameters (skin, density, extra args).
- `spec.resources`: pod requests and limits.
- `spec.readinessProbe`: override for boot-completed detection.
- `spec.nodeSelector`, `spec.tolerations`: target KVM or USB-host nodes.

### DevicePool (namespaced)
Capacity and policy for a set of classes.

- `spec.classes[]`: references to DeviceClass, each with `minWarm` and `maxWarm`.
- `spec.maxConcurrent`: global cap on running devices in this pool.
- `spec.evictionPolicy`: e.g. `LRUIdle` (evict the least-recently-used idle device of
  another class to make room).
- `spec.stfRef`: how to reach the STF control plane for registration.
- `status`: counts of total, ready, leased, and provisioning devices per class.

### Device (namespaced, operator-owned)
One running device instance. Created and managed by the operator, not by users.

- `status.classRef`, `status.providerType`.
- `status.phase`: `Provisioning`, `Ready`, `Leased`, `Draining`, `Failed`.
- `status.adbEndpoint`: host:port for adb.
- `status.uiEndpoint`: optional per-device viewer URL.
- `status.leaseRef`: the bound DeviceLease, if any.
- `status.lastUsedTime`: drives LRU eviction.
- Owns its backing pod (or Deployment) and Service via owner references.

### DeviceLease (namespaced)
A claim by a workflow.

- `spec.classRef`: requested device class.
- `spec.requester`: opaque identifier (CI job id, user).
- `spec.ttlSeconds`: lease lifetime; refreshed by heartbeats.
- `status.phase`: `Pending`, `Bound`, `Expired`, `Released`, `Failed`.
- `status.deviceRef`, `status.adbEndpoint`, `status.uiEndpoint`.
- `status.expiresAt`, `status.lastHeartbeat`.

Binding uses optimistic concurrency on the `Device` object (resourceVersion conflicts
retry), which gives atomic single-owner binding without an external database. A reaper
releases leases whose `expiresAt` has passed.

This model maps directly to the four requirements: DeviceClass plus DevicePool cover
on-demand-by-type, the cap, and eviction; DeviceLease covers exclusive ownership; the
health loop covers recovery; and `providerType: physical` plus a provider DaemonSet cover
physical devices behind the same lease and health contract.

---

## 6. Phased implementation

Each phase ends with acceptance criteria. Write tests for each phase before moving on.
All commands below run through mise (`mise run <task>`).

### Phase 0: Scaffolding and dev loop
- Initialize the monorepo and the Kubebuilder `go/v4` project.
- Author `.mise.toml` (see appendix) with the pinned toolchain and these tasks at minimum:
  `build`, `test`, `lint`, `generate`, `manifests`, `docker-build`, `deploy`, `install`,
  `kind-up`, `kind-down`. Use mise `sources`/`outputs` so codegen and build tasks skip when
  inputs are unchanged.
- `hack/kind-up.sh` creates a kind cluster with KVM passthrough (and a path for USB
  passthrough to be used later).
- GitHub Actions: install the pinned toolchain with `jdx/mise-action`, then run
  `mise run lint`, `mise run test`, and the image build. Use a distroless base for the
  operator image and build multi-arch.

Acceptance: `mise install` provisions the full toolchain; `mise run kind-up` produces a
working cluster; the empty operator image builds and the manager pod starts and stays
healthy.

### Phase 1: DeviceFarmer control-plane Helm chart
- Build the chart from DeviceFarmer's docker-compose topology as the source of truth, since
  there is no maintained official chart. Each STF process becomes a Deployment, RethinkDB a
  StatefulSet with a PVC, and the schema migration a Helm pre-install/upgrade hook Job.
- `values.yaml` exposes image tags, replica counts, RethinkDB persistence, the auth
  provider (start with the built-in mock auth), the ingress host, and TLS.
- Front the app and websocket with the chosen ingress. Create the default admin and root
  group on first migrate.
- Add `chart-lint`, `chart-template`, and `chart-install` mise tasks.

Acceptance: `mise run chart-install` brings up the STF UI on the kind cluster; an emulator
that you manually `adb connect` to a provider appears in the STF device list and is
controllable in the browser.

### Phase 2: Operator API types
- Define the four CRDs as Go types with kubebuilder validation markers. Generate the v1
  CRDs and deepcopy code via `mise run generate`. Add sample CRs under `config/samples`.

Acceptance: CRDs install cleanly on a 1.30+ cluster; sample CRs apply and round-trip
through the API server with schema validation.

### Phase 3: Emulator provisioning and warm pool
- Reconcile DevicePool: for each class, maintain `minWarm` ready devices by creating Device
  objects, each backing an emulator pod from the class image plus an adb Service. Apply the
  class `nodeSelector` and `tolerations` so emulator pods land on KVM-capable nodes, and
  mount `/dev/kvm` into each emulator pod.
- Detect readiness via a boot-completed probe (`getprop sys.boot_completed`), then set the
  Device phase to Ready.
- Register Ready devices into STF (via the STF provider or adb connect through `stfRef`).

Acceptance: a DevicePool with `minWarm: 2` keeps two ready emulators alive; they register
and appear in STF; deleting an emulator pod causes the operator to recreate it and return
to the desired warm count.

### Phase 4: Leasing
- Reconcile DeviceLease: bind a Ready, unleased Device of the requested class using
  optimistic concurrency. If none is free and the pool is below `maxConcurrent`, provision
  one. If the pool is at `maxConcurrent` and an idle device of another class exists, evict
  it per `evictionPolicy`, then provision the requested type. Publish adb and UI endpoints
  in the lease status.
- Heartbeat: a lease is refreshed by patching `lastHeartbeat`, which extends `expiresAt`.
  A reaper releases expired or released leases and recreates the device in a clean state
  before returning it to the pool.
- Provide the `client/` CLI and a small library: `acquire`, `heartbeat`, `release`, plus an
  optional pinger sidecar pattern for CI jobs.

Acceptance: many concurrent lease requests never double-bind the same device; a lease whose
heartbeats stop is auto-released after its TTL and its device is recycled; requesting a new
type when at capacity evicts an idle device of another type and serves the request.

### Phase 5: Failure handling and recovery
- Add a health monitor: periodic adb liveness checks plus the lease heartbeat. On failure,
  mark the Device `Failed`, drain any lease with a clear status reason, and recreate. Apply
  exponential backoff to crash-looping devices so a bad node or image does not thrash.
- Surface device and lease health in status conditions and events.

Acceptance: a deliberately wedged emulator is detected and replaced within a bounded time;
a lease holder whose device failed gets an actionable status; crash loops back off instead
of spinning.

### Phase 6: Physical device provider (future-ready)
- Implement a provider DaemonSet that runs on nodes labeled as USB hosts, runs adb, applies
  udev rules for stable serial naming, and registers attached physical devices as Device
  objects with `providerType: physical`. These flow through the same DeviceLease and health
  contract as emulators.

Acceptance: a physical device attached to a labeled host node becomes leasable through the
same DeviceLease flow and is controllable in STF.

---

## 7. Cross-cutting concerns

Implement these alongside the phases, not as an afterthought.

- Observability: expose Prometheus metrics for pool size, ready/leased/free counts per
  class, lease wait time, provision latency, eviction count, and device failure rate. Add
  OpenTelemetry traces around the provision and lease paths. These metrics are the basis
  for any later autoscaling.
- Security: least-privilege RBAC for the manager; NetworkPolicies isolating emulator pods;
  STF behind the ingress auth and TLS; lease ownership checks so a requester can only
  refresh or release its own leases.
- Testing: envtest for controller logic; table-driven unit tests for the eviction and lease
  state machines (these are the correctness-critical pieces); an e2e suite on kind that
  exercises acquire, heartbeat expiry, eviction, and recovery. Wire setup-envtest through a
  mise task so `KUBEBUILDER_ASSETS` is resolved consistently.
- Documentation: a README per component, an architecture diagram, and a short operator
  runbook covering KVM node setup, scaling the warm pool, and recovering a stuck device.

---

## 8. Out of scope for v1

- Multi-tenant quotas and per-client fairness.
- iOS devices.
- Replacing STF's internals (RethinkDB, ZeroMQ, minicap). This plan modernizes the
  orchestration around STF, not STF itself.
- A bespoke per-device WebRTC viewer. STF provides browser control for v1. A per-device
  viewer (ws-scrcpy-web or the Google emulator WebRTC stack, exposed via `uiEndpoint`) can
  be added later if STF's viewer is insufficient.

---

## 9. How to work this plan

- Proceed strictly phase by phase. Each phase must compile, pass its tests, and meet its
  acceptance criteria before the next begins.
- Run everything through mise. New contributors clone the repo, run `mise install`, and have
  the exact pinned toolchain plus all tasks. There is no separate Makefile, `.nvmrc`, or
  tool-version file to keep in sync.
- Keep the eviction and lease logic behind clean interfaces with heavy unit tests. They are
  where one-workflow-per-device correctness lives.
- Verify and pin the current stable versions of every tool in `.mise.toml` before
  committing. Treat the versions in this document and the starter config as illustrative.
- Prefer small, reviewable PRs scoped to a phase or a single CRD or reconciler.
- When a design choice is ambiguous, choose the simpler option, implement it behind an
  interface, and note the tradeoff in the PR description.

---

## Appendix A: Starter `.mise.toml`

> The live, version-verified `.mise.toml` lives at the repo root. This appendix is the
> original starter from the plan, kept for reference. The root config adjusts the operator
> tasks to run inside `operator/` (per the §4 layout) and pins versions confirmed on
> 2026-06-25.

```toml
# Pin every tool the project needs. Versions are starting points: verify current
# stable releases before committing. Run `mise install`, then `mise tasks` to list tasks.

[tools]
go = "1.24"
helm = "3"                 # move to 4 once your cluster tooling supports it
kubectl = "1.31"
kind = "latest"
kustomize = "latest"
golangci-lint = "latest"
"ubi:kubernetes-sigs/kubebuilder" = "latest"
"go:sigs.k8s.io/controller-tools/cmd/controller-gen" = "latest"
"go:sigs.k8s.io/controller-runtime/tools/setup-envtest" = "latest"

[env]
IMG = "ghcr.io/yourorg/devicefarm-operator:dev"
KIND_CLUSTER = "devicefarm"
ENVTEST_K8S_VERSION = "1.31"

[tasks.generate]
description = "controller-gen: deepcopy, RBAC, and CRD manifests"
run = [
  "controller-gen object:headerFile=hack/boilerplate.go.txt paths=./...",
  "controller-gen rbac:roleName=manager-role crd webhook paths=./... output:crd:artifacts:config=config/crd/bases",
]
sources = ["api/**/*.go", "internal/controller/**/*.go"]
outputs = ["config/crd/bases/**/*.yaml"]

[tasks.manifests]
description = "Alias kept for familiarity"
depends = ["generate"]

[tasks.fmt]
run = "go fmt ./..."

[tasks.vet]
run = "go vet ./..."

[tasks.lint]
run = "golangci-lint run"

[tasks.test]
description = "Unit and envtest controller tests"
depends = ["generate"]
run = [
  "setup-envtest use {{env.ENVTEST_K8S_VERSION}} --bin-dir bin",
  "KUBEBUILDER_ASSETS=$(setup-envtest use -i -p path {{env.ENVTEST_K8S_VERSION}} --bin-dir bin) go test ./... -coverprofile cover.out",
]
sources = ["**/*.go"]

[tasks.build]
depends = ["generate"]
run = "go build -o bin/manager ./cmd"

[tasks.docker-build]
run = "docker build -t {{env.IMG}} ."

[tasks.docker-push]
run = "docker push {{env.IMG}}"

[tasks.install]
description = "Install CRDs into the current cluster"
depends = ["generate"]
run = "kubectl apply -k config/crd"

[tasks.deploy]
description = "Deploy the operator into the current cluster"
depends = ["generate"]
run = [
  "cd config/manager && kustomize edit set image controller={{env.IMG}}",
  "kubectl apply -k config/default",
]

[tasks.kind-up]
description = "Create a kind cluster with KVM passthrough"
run = "./hack/kind-up.sh {{env.KIND_CLUSTER}}"

[tasks.kind-down]
run = "kind delete cluster --name {{env.KIND_CLUSTER}}"

[tasks.kind-load]
description = "Load the operator image into kind"
depends = ["docker-build"]
run = "kind load docker-image {{env.IMG}} --name {{env.KIND_CLUSTER}}"

[tasks.chart-lint]
dir = "charts/devicefarmer"
run = "helm lint ."

[tasks.chart-template]
dir = "charts/devicefarmer"
run = "helm template devicefarmer ."

[tasks.chart-install]
description = "Install the DeviceFarmer control-plane chart"
dir = "charts/devicefarmer"
run = "helm upgrade --install devicefarmer . -n devicefarmer --create-namespace"

[tasks.dev]
description = "Full local loop: cluster, chart, build, load, deploy"
depends = ["kind-up", "chart-install", "kind-load", "deploy"]
```

Notes:
- mise runs independent `depends` in parallel and skips tasks whose `sources` are unchanged,
  so `generate` and `build` no-op when nothing relevant changed.
- For anything too involved for inline TOML (the kind/KVM setup, multi-step e2e), put a bash
  script in `.mise/tasks/` and reference it; those run with or without mise installed.
- Confirm the exact flags for `setup-envtest` and `controller-gen` against the versions you
  pin; their CLI surface shifts between releases.
