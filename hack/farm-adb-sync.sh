#!/usr/bin/env bash
#
# farm-adb-sync — make the farm's emulators show up in your host `adb devices`
# automatically, with no port-forwarding.
#
# How it works:
#   * Each emulator pod exposes its adb on <podIP>:5556 (the adb-proxy/socat
#     sidecar). Those pod IPs live on the kind node's pod network, which the host
#     can't reach by default — so we add ONE host route to the pod CIDR via the
#     kind node's Docker IP (the only step that needs sudo, done once per cluster).
#   * Then this loop reconciles the host adb server against the Ready Devices:
#     `adb connect <podIP>:5556` for each, and `adb disconnect` for any that went
#     away (lease release, self-heal, scale-down). Pod IPs churn; the route covers
#     the whole CIDR, so only the connect set changes.
#
# Usage:  mise run adb-sync        (Ctrl-C to stop)
# Env:    FARM_CONTEXT (kube context, default kind-devicefarm)
#         FARM_NAMESPACE (default devicefarmer)
#         FARM_NODE (kind node container, default <cluster>-control-plane)
#         FARM_ADB_PORT (default 5556)  FARM_SYNC_INTERVAL (seconds, default 5)
set -euo pipefail

CTX="${FARM_CONTEXT:-kind-devicefarm}"
NS="${FARM_NAMESPACE:-devicefarmer}"
NODE="${FARM_NODE:-${KIND_CLUSTER:-devicefarm}-control-plane}"
PORT="${FARM_ADB_PORT:-5556}"
INTERVAL="${FARM_SYNC_INTERVAL:-5}"

k() { kubectl --context "$CTX" "$@"; }

# Add the host route to the pod network if it isn't there yet (idempotent).
ensure_route() {
  local node_ip cidr
  node_ip="$(docker inspect "$NODE" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')"
  if [ -z "$node_ip" ]; then
    echo "error: could not find Docker IP for node '$NODE' (is the cluster up?)" >&2
    exit 1
  fi
  # Single-node kind hands the whole pod range out of one CIDR; fall back to /16.
  cidr="$(k get node "$NODE" -o jsonpath='{.spec.podCIDR}' 2>/dev/null || true)"
  cidr="${cidr:-10.244.0.0/16}"
  if ip route show "$cidr" 2>/dev/null | grep -q "via $node_ip"; then
    return 0
  fi
  echo "Adding host route $cidr via $node_ip (one-time, needs sudo)…"
  sudo ip route replace "$cidr" via "$node_ip"
}

# adb endpoints we WANT connected: <podIP>:PORT for every booted Device.
# Ready (free) and Leased (in use) both have a running, adb-reachable emulator;
# Provisioning/Draining/Failed don't, so they're skipped.
desired() {
  local dev ip
  for dev in $(k -n "$NS" get dfd \
      -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.phase}{"\n"}{end}' 2>/dev/null \
      | awk '$2=="Ready" || $2=="Leased" {print $1}'); do
    ip="$(k -n "$NS" get pod "${dev}-emulator" -o jsonpath='{.status.podIP}' 2>/dev/null || true)"
    [ -n "$ip" ] && echo "${ip}:${PORT}"
  done
}

# adb endpoints we already have (pod-network targets only — don't touch others).
current() {
  adb devices 2>/dev/null | awk -v p=":$PORT" '$1 ~ /^10\.244\./ && index($1,p) {print $1}'
}

ensure_route
echo "Syncing farm devices into host adb (context=$CTX ns=$NS, every ${INTERVAL}s). Ctrl-C to stop."
trap 'echo; echo "stopped (leaving current adb connections in place)"; exit 0' INT TERM

while true; do
  mapfile -t want < <(desired)
  mapfile -t have < <(current)
  for t in "${want[@]:-}"; do
    [ -z "$t" ] && continue
    printf '%s\n' "${have[@]:-}" | grep -qxF "$t" || { echo "+ connect    $t"; adb connect "$t" >/dev/null 2>&1 || true; }
  done
  for h in "${have[@]:-}"; do
    [ -z "$h" ] && continue
    printf '%s\n' "${want[@]:-}" | grep -qxF "$h" || { echo "- disconnect $h"; adb disconnect "$h" >/dev/null 2>&1 || true; }
  done
  sleep "$INTERVAL"
done
