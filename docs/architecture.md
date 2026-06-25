# Architecture

Two independently deployable components in one monorepo. See
[`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md) for the authoritative design;
this file is a short orientation.

```
                         ┌─────────────────────────────────────────┐
                         │            Kubernetes cluster            │
                         │                                          │
   user / CI ──lease──▶  │   ┌───────────────┐   reconcile          │
   (client CLI)          │   │   Operator    │──────────┐           │
                         │   │ (Kubebuilder) │          ▼           │
                         │   └───────────────┘   ┌──────────────┐   │
                         │     │  owns           │  Device pods  │   │
                         │     │  Device CRs     │  (emulators,  │   │
                         │     ▼                 │  /dev/kvm)    │   │
                         │   register over adb   └──────┬───────┘   │
                         │     │                        │ adb       │
                         │     ▼                        ▼           │
                         │   ┌──────────────────────────────────┐  │
                         │   │   DeviceFarmer / STF (Helm)       │  │
                         │   │   RethinkDB · triproxy · app ·    │  │
                         │   │   api · websocket · storage ·     │  │
                         │   │   reaper · ingress                │  │
                         │   └──────────────────────────────────┘  │
                         │              ▲ browser UI / remote ctrl  │
                         └──────────────┼───────────────────────────┘
                                        │
                                     operator
```

- **Leasing and pool policy** live in the operator. Binding uses optimistic
  concurrency on the `Device` object (resourceVersion conflicts retry), giving
  atomic single-owner binding without an external database.
- **STF** provides the human-facing browser UI and remote control. The operator
  registers each ready device into STF over adb.
- **RethinkDB** is a managed dependency of STF, not something this project
  replaces.
