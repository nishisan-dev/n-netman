# Configuração

Este documento descreve a estrutura completa do arquivo de configuração `n-netman.yaml`.

## Estrutura Geral

O arquivo de configuração é organizado em seções:

```yaml
version: 2                    # Versão do schema (1 ou 2)

node:                         # Identidade do nó
netplan:                      # Integração com netplan (somente leitura)
kvm:                          # Integração com KVM/libvirt (opcional)
overlays:                     # Definição de overlays (v2)
overlay:                      # Configuração legado de overlay (v1)
routing:                      # Políticas de roteamento (v1)
topology:                     # Modo de topologia
security:                     # Segurança do control-plane
observability:                # Logs, métricas, healthchecks
```

## Versões de Schema

| Versão | Descrição |
|--------|-----------|
| `1` | Single overlay, routing global |
| `2` | Multi-overlay, routing por overlay |

A versão 2 é recomendada para novos deployments.

---

## Seção: node

Define a identidade do host.

```yaml
node:
  id: "host-a-01"              # Identificador único (obrigatório)
  hostname: "host-a"           # Nome amigável
  tags:                        # Metadados opcionais
    - "datacenter-1"
    - "production"
```

| Campo | Tipo | Obrigatório | Descrição |
|-------|------|-------------|-----------|
| `id` | string | Sim | Identificador único do nó no overlay |
| `hostname` | string | Não | Nome amigável para logs e status |
| `tags` | []string | Não | Metadados para filtragem/políticas futuras |

---

## Seção: netplan

Integração com netplan para inferir configuração underlay.

```yaml
netplan:
  enabled: true
  config_paths:
    - "/etc/netplan"
  underlay:
    prefer_interfaces:
      - "ens3"
      - "eth0"
    prefer_address_families:
      - "ipv4"
      - "ipv6"
```

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `enabled` | bool | false | Habilita leitura do netplan |
| `config_paths` | []string | ["/etc/netplan"] | Diretórios a escanear |
| `underlay.prefer_interfaces` | []string | [] | Ordem de preferência de interfaces |
| `underlay.prefer_address_families` | []string | ["ipv4"] | Preferência de família de endereços |

**Nota:** A leitura é somente-leitura. O n-netman nunca modifica arquivos netplan.

---

## Seção: kvm

Integração opcional com KVM/libvirt.

```yaml
kvm:
  enabled: false              # Toggle principal
  provider: "libvirt"
  libvirt:
    uri: "qemu:///system"
    mode: "linux-bridge"      # linux-bridge | libvirt-network
    network:
      name: "nnet-overlay"
      autostart: true
      forward_mode: "bridge"
  bridges:
    - name: "br-nnet-100"
      stp: false
      mtu: 1450
      manage: true
  attach:
    enabled: false
    strategy: "by-name"       # by-name | by-tag | regex
    targets:
      - vm: "vm-web-01"
        bridge: "br-nnet-100"
        model: "virtio"
```

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `enabled` | bool | false | Habilita integração KVM |
| `libvirt.mode` | string | "linux-bridge" | Modo de operação |
| `bridges[].manage` | bool | false | Se o agente cria/gerencia a bridge |

**Importante:** Se `kvm.enabled: false`, o n-netman funciona como puro agente de overlay. Ideal para hosts que não rodam VMs.

---

## Seção: overlays (v2)

Define múltiplos overlays com configuração independente.

```yaml
overlays:
  - vni: 100
    name: "vxlan-prod"
    dstport: 4789
    mtu: 1450
    learning: true
    underlay_interface: "ens3"
    bridge:
      name: "br-prod"
      ipv4: "10.100.0.1/24"
      ipv6: "2001:db8:100::1/64"
    bum:
      mode: "head-end-replication"
    routing:
      export:
        networks:
          - "172.16.10.0/24"
        metric: 100
      import:
        accept_all: true
        install:
          table: 100
          lookup_rules:
            enabled: true
            priority: 100
```

### Campos do Overlay

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `vni` | int | (obrigatório) | VXLAN Network Identifier (1-16777215) |
| `name` | string | "vxlan{vni}" | Nome da interface VXLAN |
| `dstport` | int | 4789 | Porta UDP do VXLAN |
| `mtu` | int | 1450 | MTU da interface |
| `learning` | bool | true | MAC learning automático |
| `underlay_interface` | string | "" | Interface underlay específica |
| `bridge.name` | string | (obrigatório) | Nome da bridge Linux |
| `bridge.ipv4` | string | "" | Endereço IPv4 CIDR da bridge |
| `bridge.ipv6` | string | "" | Endereço IPv6 CIDR da bridge |

### Modos BUM

| Modo | Descrição |
|------|-----------|
| `head-end-replication` | Replica BUM via unicast para cada peer (padrão) |
| `multicast` | Usa grupo multicast (requer `bum.group`) |

```yaml
bum:
  mode: "multicast"
  group: "239.1.1.100"        # Convenção: 239.1.1.{vni % 256}
```

### Routing por Overlay

Cada overlay pode ter suas próprias políticas:

```yaml
routing:
  export:
    networks:
      - "172.16.10.0/24"
    include_connected: true
    metric: 100
  import:
    accept_all: false
    allow:
      - "172.16.0.0/16"
    deny:
      - "0.0.0.0/0"
    install:
      table: 100              # Tabela específica para este overlay
      flush_on_peer_down: true
      route_lease_seconds: 30
      lookup_rules:
        enabled: true         # Cria ip rule iif/oif
        priority: 100         # Prioridade das rules
```

---

## Seção: overlay (v1 legado)

Configuração de overlay para schema v1 (single overlay).

```yaml
overlay:
  vxlan:
    vni: 100
    name: "vxlan100"
    dstport: 4789
    mtu: 1450
    learning: true
    bridge: "br-nnet-100"
  peers:
    - id: "host-b-01"
      endpoint:
        address: "192.168.56.12"
        via_interface: "ens3"
      auth:
        mode: "psk"
        psk_ref: "file:/etc/n-netman/psk/host-b.key"
      health:
        keepalive_interval_ms: 1500
        dead_after_ms: 6000
```

### Configuração de Peers

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `id` | string | (obrigatório) | ID do peer |
| `endpoint.address` | string | (obrigatório) | IP underlay do peer |
| `endpoint.via_interface` | string | "" | Interface de saída forçada |
| `auth.mode` | string | "" | Modo de autenticação (psk) |
| `health.keepalive_interval_ms` | int | 1500 | Intervalo de keepalive |
| `health.dead_after_ms` | int | 6000 | Timeout para marcar peer como dead |

---

## Seção: routing (v1)

Políticas globais de roteamento (schema v1).

```yaml
routing:
  enabled: true
  export:
    export_all: false
    networks:
      - "172.16.10.0/24"
    include_connected: true
    include_netplan_static: true
    metric: 100
  import:
    accept_all: false
    allow:
      - "172.16.0.0/16"
    deny:
      - "0.0.0.0/0"
    install:
      table: 100
      replace_existing: true
      flush_on_peer_down: true
      route_lease_seconds: 30
```

---

## Seção: topology

Define o modo de topologia e regras de trânsito.

```yaml
topology:
  mode: "direct-preferred"    # direct-preferred | full-mesh | hub-spoke
  relay_fallback: true
  transit: "deny"             # deny | allow
  transit_policy:
    allowed_transit_peers: []
    denied_transit_peers:
      - "spoke-untrusted"
```

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `mode` | string | "direct-preferred" | Modo de topologia |
| `relay_fallback` | bool | true | Permite relay em caso de falha direta |
| `transit` | string | "deny" | Política de trânsito |

---

## Seção: security

Configura segurança do control-plane gRPC.

```yaml
security:
  control_plane:
    transport: "grpc"
    listen:
      address: "0.0.0.0"
      port: 9898
    tls:
      enabled: true
      cert_file: "/etc/n-netman/tls/server.crt"
      key_file: "/etc/n-netman/tls/server.key"
      ca_file: "/etc/n-netman/tls/ca.crt"
      skip_verify: false      # Apenas para labs
```

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `listen.address` | string | "0.0.0.0" | Endereço de bind |
| `listen.port` | int | 9898 | Porta gRPC |
| `tls.enabled` | bool | false | Habilita TLS |
| `tls.skip_verify` | bool | false | Ignora verificação de certificado |

**Geração de certificados:**
```bash
./scripts/gen-certs.sh /etc/n-netman/tls
```

---

## Seção: observability

Configura logging, métricas e healthchecks.

```yaml
observability:
  logging:
    level: "info"             # debug | info | warn | error
    format: "json"            # json | text
  metrics:
    enabled: true
    listen:
      address: "127.0.0.1"
      port: 9109
  healthcheck:
    enabled: true
    listen:
      address: "127.0.0.1"
      port: 9110
```

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `logging.level` | string | "info" | Nível de log |
| `logging.format` | string | "json" | Formato de output |
| `metrics.enabled` | bool | true | Exponha métricas Prometheus |
| `healthcheck.enabled` | bool | true | Habilita endpoints de saúde |

---

## Valores Padrão

Valores aplicados automaticamente quando não especificados:

| Campo | Valor Padrão |
|-------|--------------|
| `version` | 1 |
| `overlay.vxlan.dstport` | 4789 |
| `overlay.vxlan.mtu` | 1450 |
| `overlay.vxlan.learning` | true |
| `routing.import.install.table` | 100 |
| `routing.import.install.route_lease_seconds` | 30 |
| `topology.mode` | "direct-preferred" |
| `topology.transit` | "deny" |
| `security.control_plane.listen.port` | 9898 |
| `observability.logging.level` | "info" |
| `observability.metrics.listen.port` | 9109 |
| `observability.healthcheck.listen.port` | 9110 |
