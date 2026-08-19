#!/bin/bash
# Exercise the route injection channel end to end inside the Vagrant lab.
#
# Runs nnet-agent in a network namespace attached to br-prod. The namespace
# takes the same data path a VM would, without nested virtualisation.
#
# Usage (inside a lab host, as root):
#   sudo ./scripts/lab-agent-test.sh [namespace] [address/prefix]
#
# Clean up with:
#   sudo ./scripts/lab-agent-test.sh --cleanup [namespace]

set -euo pipefail

NS="${2:-vm1}"
ADDR="${3:-10.100.0.50/24}"
BRIDGE="br-prod"
VETH_HOST="veth-${NS}"
VETH_NS="${NS}-eth0"
AGENT_CFG="/etc/n-netman/agent-${NS}.yaml"

cleanup() {
    echo "Cleaning up ${NS}..."
    ip netns pids "$NS" 2>/dev/null | xargs -r kill 2>/dev/null || true
    ip netns del "$NS" 2>/dev/null || true
    ip link del "$VETH_HOST" 2>/dev/null || true
    rm -f "$AGENT_CFG"
    echo "Done."
}

if [ "${1:-}" = "--cleanup" ]; then
    cleanup
    exit 0
fi

if [ "$(id -u)" != "0" ]; then
    echo "This script needs root (it creates namespaces and programs routes)." >&2
    exit 1
fi

if ! ip link show "$BRIDGE" >/dev/null 2>&1; then
    echo "Bridge $BRIDGE does not exist. Run 'nnet apply' and start nnetd first." >&2
    exit 1
fi

if [ ! -f /etc/n-netman/psk/inject.key ]; then
    echo "Missing /etc/n-netman/psk/inject.key. The lab provisioning creates it." >&2
    exit 1
fi

echo "==> 1. Creating namespace $NS attached to $BRIDGE"
cleanup >/dev/null 2>&1 || true
ip netns add "$NS"
ip link add "$VETH_HOST" type veth peer name "$VETH_NS"
ip link set "$VETH_HOST" master "$BRIDGE" up
ip link set "$VETH_NS" netns "$NS"
ip netns exec "$NS" ip link set lo up

echo "==> 2. Writing $AGENT_CFG"
cat > "$AGENT_CFG" <<EOF
version: 1
agent:
  id: "${NS}"
inject:
  port: 4790
interfaces:
  - name: "${VETH_NS}"
    address: "${ADDR}"
    vni: 100
    expect_tags: ["it"]
    psk_ref: "file:/etc/n-netman/psk/inject.key"
    install:
      table: 0
      metric: 100
      accept_default_gateway: false
    import:
      accept_all: true
observability:
  logging:
    level: "debug"
    format: "text"
  metrics:
    enabled: false
  healthcheck:
    enabled: true
    listen:
      address: "127.0.0.1"
      port: 9112
EOF

echo "==> 3. Checking the agent's view of this namespace"
ip netns exec "$NS" nnet-agent doctor -c "$AGENT_CFG" || true

echo "==> 4. Starting nnet-agent in $NS"
ip netns exec "$NS" nnet-agent run -c "$AGENT_CFG" &
AGENT_PID=$!
sleep 8

echo
echo "==> 5. Controller side: what is being advertised"
nnet inject status || true

echo
echo "==> 6. Agent side: what was learned"
ip netns exec "$NS" nnet-agent status -c "$AGENT_CFG" || true

echo
echo "==> 7. Routes actually programmed in $NS (proto 98 is the agent)"
ip netns exec "$NS" ip route show

echo
echo "==> 8. Agent health"
ip netns exec "$NS" curl -s http://127.0.0.1:9112/healthz || echo "(curl unavailable)"

echo
echo "Agent still running as PID $AGENT_PID."
echo "Try these next:"
echo "  # reachability to another host's namespace"
echo "  ip netns exec $NS ping -c3 <address of another lab namespace>"
echo "  # stop the local controller and watch its routes expire, while the"
echo "  # other hosts' routes stay"
echo "  systemctl stop n-netman && sleep 25 && ip netns exec $NS ip route show"
echo
echo "Clean up with: sudo $0 --cleanup $NS"
