#!/usr/bin/env bash
# Create a local kind cluster for the Android Device Farm with KVM passthrough.
#
# Emulator pods require hardware acceleration via /dev/kvm (a hard requirement —
# there is no no-KVM fallback). kind nodes are privileged containers, so we bind
# /dev/kvm from the host into each node and label the nodes so emulator pods can
# target them via nodeSelector/affinity.
#
# USB passthrough (for physical devices, Phase 6) is left as a documented hook
# below; it is not wired up yet.
#
# Usage: hack/kind-up.sh [cluster-name]   (default: devicefarm)
set -euo pipefail

CLUSTER="${1:-devicefarm}"
KVM_NODE_LABEL="farm.example.com/kvm=true"
# Pinned to kind v0.32.0's primary node image (use the @sha256 digest, as kind
# requires, to guarantee an image built for this kind release). Override with
# KIND_NODE_IMAGE if needed.
K8S_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5}"

if [[ ! -e /dev/kvm ]]; then
  echo "ERROR: /dev/kvm not found on the host. KVM is a hard requirement." >&2
  echo "Enable nested virtualization / KVM and ensure your user is in the 'kvm' group." >&2
  exit 1
fi

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  echo "kind cluster '${CLUSTER}' already exists; nothing to do."
  exit 0
fi

echo ">> Creating kind cluster '${CLUSTER}' (node image: ${K8S_IMAGE}) with /dev/kvm passthrough"

# Single-node cluster: kind does not taint the control-plane node, so emulator
# pods schedule on it too. This matches the known-working single-node topology on
# cgroup v2 hosts (multi-node worker joins are flaky here). On real clusters,
# dedicated KVM node pools are selected via the same label below.
# extraMounts binds the host /dev/kvm device node into the node container.
cat <<EOF | kind create cluster --name "${CLUSTER}" --image "${K8S_IMAGE}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    labels:
      farm.example.com/kvm: "true"
    extraMounts:
      - hostPath: /dev/kvm
        containerPath: /dev/kvm
      # USB passthrough hook (Phase 6 — physical devices). Uncomment and adjust
      # once a USB-host node is needed:
      # - hostPath: /dev/bus/usb
      #   containerPath: /dev/bus/usb
EOF

echo ">> Cluster '${CLUSTER}' ready. KVM-capable nodes carry label: ${KVM_NODE_LABEL}"
kubectl --context "kind-${CLUSTER}" get nodes -o wide
