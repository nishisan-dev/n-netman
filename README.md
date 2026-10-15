# n-netman — Linux VXLAN Overlay Networking & Routing for KVM/libvirt

**A lightweight, self-hosted network control plane for Linux.** Build multi-host **VXLAN L2/L3 overlays**, manage Linux bridges and forwarding databases, exchange routes over **gRPC**, and integrate with **KVM/libvirt**—without requiring Open vSwitch, FRRouting, or Kubernetes.

[![CI](https://github.com/nishisan-dev/n-netman/actions/workflows/ci.yaml/badge.svg)](https://github.com/nishisan-dev/n-netman/actions/workflows/ci.yaml)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Linux](https://img.shields.io/badge/platform-Linux-333333?logo=linux&logoColor=white)](docs/overview.md)
[![License: Non-commercial evaluation](https://img.shields.io/badge/license-non--commercial%20evaluation-orange)](LICENSE)

> **Em português:** o n-netman gerencia redes overlay VXLAN, bridges Linux, roteamento entre hosts e integração KVM/libvirt. O foco é automação de redes Linux com configuração declarativa e um control plane enxuto. A [documentação detalhada](docs/README.md) está em português.

## Why n-netman?

Managing distributed Linux virtualization hosts often means stitching together `ip link`, `bridge fdb`, static routes, policy rules, and VM networking. **n-netman turns those moving pieces into a declarative configuration and a continuously reconciled desired state.**

It is intended for homelabs, network labs, virtualization environments, and multi-host overlay experiments that need native Linux networking rather than a full software-defined networking stack.

- **Native Linux networking:** VXLAN, Linux bridge, FDB, routes, and policy routing through Netlink.
- **Multi-overlay support:** separate VNIs, bridges, forwarding modes, and routing tables.
- **Distributed route exchange:** gRPC state synchronization, route announcements/withdrawals, import filters, and route leases.
- **Virtualization integration:** libvirt attachment commands and an optional `nnet-agent` inside VMs.
- **Operational visibility:** CLI diagnostics, peer status, JSON logs, HTTP health endpoints, and Prometheus metrics.
- **Secure peering option:** TLS/mTLS with certificate-based peer identity.

### Capabilities at a glance

| Capability | Current status |
| --- | --- |
| Linux VXLAN interface and bridge reconciliation | Available |
| Head-end replication (FDB) and multicast BUM | Available |
| Multiple overlays / VNIs and per-overlay routing tables | Available |
| gRPC route exchange (announce, withdraw, state sync) | Available |
| Route import policies, leases, and peer-down cleanup | Available |
| Linux policy-based routing (interface-based rules) | Available |
| TLS/mTLS and certificate tooling | Available |
| KVM/libvirt integration and optional VM route agent | Available |
| Prometheus metrics, health checks, diagnostics | Available |
| Automatic export of all connected / Netplan static routes | Not implemented |
| `lookup_rules.mode: prefix` | Not implemented |
| Transport-aware PW/VXLAN route failover | **Planned; not yet implemented** |

For implementation details and current limitations, see [Routing](docs/routing.md), [Configuration](docs/configuration.md), and [Architecture](docs/architecture.md).

## Architecture

```text
                  +-------------------------------+
                  |           n-netman            |
                  |        nnetd / nnet CLI       |
                  +---------------+---------------+
                                  |
                 +----------------+-----------------+
                 |                                  |
        +--------v---------+              +---------v--------+
        |  Control plane   |              |    Reconciler    |
        | gRPC + route     |              | VXLAN / bridge   |
        | import / export  |              | FDB / ip rules   |
        +--------+---------+              +---------+--------+
                 |                                  |
          Peer exchange                      Linux Netlink
                 |                                  |
          Other Linux hosts              Kernel routing + VXLAN
                                                    |
                                          KVM/libvirt bridges + VMs
```

The **underlay** is provided by Linux and your existing network configuration. The overlay reconciler manages the VXLAN/bridge resources explicitly configured for n-netman; it is not a replacement for a physical switch, LACP configuration, or a datacenter pseudowire.

## Quick start

### Requirements

- Linux with kernel support for `vxlan` and `bridge` (VXLAN support starts in Linux 3.7; distribution features vary).
- **Go 1.26+** to build from source.
- Root or appropriate Linux capabilities for creating interfaces and updating routes.
- IP connectivity between overlay endpoints.

The project has been tested on Ubuntu 22.04+ and Debian 12+ according to its current project documentation.

### Build and inspect

```bash
git clone https://github.com/nishisan-dev/n-netman.git
cd n-netman

make build
make test

./bin/nnet --help
./bin/nnet version
```

The build produces three binaries:

| Binary | Purpose |
| --- | --- |
| `nnetd` | Network controller / control-plane daemon |
| `nnet` | CLI for apply, status, routes, doctor, certificates, and libvirt |
| `nnet-agent` | Optional route agent for virtual machines |

### Configure an overlay

Start from the included [multi-overlay YAML example](examples/multi-overlay.yaml). It demonstrates two VNIs, independent routing policies, configured peers, and optional route injection.

```bash
sudo mkdir -p /etc/n-netman
sudo cp examples/multi-overlay.yaml /etc/n-netman/n-netman.yaml

# IMPORTANT: edit the example IDs, peer addresses, underlay interfaces,
# bridges, and subnets for your own host before applying anything.
sudoedit /etc/n-netman/n-netman.yaml

./bin/nnet -c /etc/n-netman/n-netman.yaml doctor
./bin/nnet -c /etc/n-netman/n-netman.yaml apply --dry-run

# Only after reviewing the target interfaces and routes:
sudo ./bin/nnet -c /etc/n-netman/n-netman.yaml apply
sudo ./bin/nnetd -config /etc/n-netman/n-netman.yaml
```

**Safety:** `apply` and `nnetd` can change host network state. Test in a VM or lab namespace before using a remotely administered KVM host. The sample config is illustrative and is **not** a ready-to-run production configuration.

### Verify

```bash
./bin/nnet -c /etc/n-netman/n-netman.yaml status
./bin/nnet -c /etc/n-netman/n-netman.yaml routes
ip -d link show type vxlan
bridge fdb show
ip route show table 100
```

If observability is enabled with the documented defaults:

```bash
curl http://127.0.0.1:9110/healthz
curl http://127.0.0.1:9110/status
curl http://127.0.0.1:9109/metrics
```

## Routing and peer connectivity

n-netman already implements a small, purpose-built **gRPC routing control plane**. Peers can exchange routes using `ExchangeState`, `AnnounceRoutes`, and `WithdrawRoutes`; learned routes can be filtered and programmed in Linux via Netlink.

- Advertised prefixes are currently **explicitly listed** in the configuration (`export.networks`).
- Import filters support `allow`, `deny`, and `accept_all`; the default policy is conservative.
- Routes can use per-overlay routing tables and be withdrawn when a peer fails or a lease expires.
- Peer health is currently checked by periodic RPCs; the existing `Keepalive` stream is **not yet** the active fast-failure detector.

**Do not assume sub-second convergence or production-grade BGP compatibility:** n-netman is not BGP, and it does not implement AS paths, full routing policy semantics, or dynamic transport failover today.

Read the [Routing guide](docs/routing.md) for route semantics, protocol 99, import policies, and current restrictions.

## Linux virtualization and VM networking

The project supports KVM/libvirt workflows using native Linux bridges. The optional in-guest `nnet-agent` receives route advertisements from a controller and applies routes inside a VM.

```bash
./bin/nnet libvirt list-vms --all
./bin/nnet libvirt status
```

See [KVM/libvirt integration](docs/libvirt.md), [Route injection](docs/inject.md), and [VM agent](docs/agent.md).

## Security and observability

**Use mTLS for communication between remote peers.** The project includes certificate-management commands; plaintext control-plane examples are intended for isolated labs only. Route injection to VMs uses its own PSK mechanism—this is distinct from peer-to-peer gRPC authentication.

```bash
./bin/nnet cert init-ca --output-dir /etc/n-netman/tls
./bin/nnet cert gen-host --help
```

Prometheus metrics include reconciliation activity, peer connectivity, active VXLAN/bridge resources, and imported/exported route counts. See [Observability](docs/observability.md) and [Configuration](docs/configuration.md).

## Roadmap

The following items are **proposals**, not currently released functionality:

- **Transport-aware peering:** bind a TCP/gRPC session to a specific local address and interface.
- **Path health and hysteresis:** detect failure and stable recovery on each independent transport.
- **Automatic route preference/failover:** prefer an external MPLS pseudowire/VLAN path, then move to a separately managed VXLAN backup when the primary fails.
- **Routing-only mode:** exchange and manage routes without requiring n-netman to recreate existing KVM bridges or VLANs.

The goal is to reuse the current route manager, control plane, and VXLAN reconciler rather than introducing an additional routing daemon.

## Documentation

| Guide | What you'll find |
| --- | --- |
| [Overview](docs/overview.md) | Scope and concepts |
| [Architecture](docs/architecture.md) | Components and data flow |
| [Configuration](docs/configuration.md) | YAML options |
| [Network model](docs/network-model.md) | VXLAN, L2/L3 overlays, and bridges |
| [Routing](docs/routing.md) | gRPC route exchange, imports, and leases |
| [CLI](docs/cli.md) | `nnet` and `nnetd` commands |
| [KVM/libvirt](docs/libvirt.md) | VM and bridge integration |
| [Route injection](docs/inject.md) | Route advertisements to VMs |
| [VM agent](docs/agent.md) | In-guest `nnet-agent` |
| [Observability](docs/observability.md) | Metrics and health checks |
| [Topology](docs/topology.md) | Peer and transit model |

Also see the [example configurations](examples/) and [lab test scripts](scripts/).

## Development and testing

```bash
make build
make test
make test-coverage
```

Integration tests are available through `make test-integration` (privileged network operations) and `make test-integration-netns` (user namespace support required).

## License

**Source-available, not OSI open source.** n-netman uses the [Nishi Non-Commercial Evaluation License](LICENSE). Personal, educational, and other eligible non-commercial evaluation use is permitted under its terms; production and commercial use require a separate commercial license. Review the LICENSE before deploying.

---

**Keywords:** Linux VXLAN, overlay networking, Linux bridge, KVM, libvirt, virtual networking, gRPC routing, Netlink, multi-overlay, VNI, FDB, policy-based routing, network automation, self-hosted network management.
