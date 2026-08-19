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

**Importante:** O campo `version:` é **obrigatório** — não há mais default `1`. Um arquivo sem a chave `version` é rejeitado na validação.

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
```

**Nota:** A prioridade das `ip rule` criadas é fixa no código (não configurável): a regra `iif` usa prioridade `100` e a regra `oif` usa `101`.

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
    include_connected: true   # Reservado — NÃO implementado (ignorado)
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
```

**Nota:** A prioridade das `ip rule` é fixa no código (não configurável): `iif` usa prioridade `100` e `oif` usa `101`.

### Validação de Overlays (v2)

Cada overlay deve ser único. São rejeitados na validação overlays com `vni`, `name`, `bridge.name` ou `import.install.table` duplicados entre si — cada overlay precisa da sua própria tabela de roteamento.

### Peers (v2 — nível raiz)

No schema v2, os peers são declarados no **nível raiz** da configuração através da chave `peers:` (não dentro de `overlay.peers`). Cada peer pode opcionalmente declarar `vnis` para limitar a quais overlays ele pertence; se `vnis` for omitido, o peer participa de **todos** os overlays.

```yaml
peers:
  - id: "peer-01"
    endpoint:
      address: "192.168.1.10"
    vnis: [100, 200]          # Restringe o peer aos overlays VNI 100 e 200
    health:
      keepalive_interval_ms: 1500
      dead_after_ms: 6000

  - id: "peer-02"
    endpoint:
      address: "192.168.1.11"
    # vnis omitido -> participa de todos os overlays
```

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `id` | string | (obrigatório) | ID do peer (deve casar com o CN do certificado quando TLS habilitado) |
| `endpoint.address` | string | (obrigatório) | IP underlay do peer |
| `vnis` | []int | (todos) | Lista de VNIs aos quais o peer pertence; omitido = todos |
| `health.keepalive_interval_ms` | int | 1500 | Intervalo de keepalive |
| `health.dead_after_ms` | int | 6000 | Timeout para marcar peer como dead |

Consulte `examples/multi-overlay.yaml` como referência canônica de configuração v2.

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
    export_all: false             # Reservado — NÃO implementado (ignorado)
    networks:                      # Única fonte de export hoje
      - "172.16.10.0/24"
    include_connected: true        # Reservado — NÃO implementado (ignorado)
    include_netplan_static: true   # Reservado — NÃO implementado (ignorado)
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
```

| Campo | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `listen.address` | string | "0.0.0.0" | Endereço de bind |
| `listen.port` | int | 9898 | Porta gRPC |
| `tls.enabled` | bool | false | Habilita TLS (mTLS) |
| `tls.cert_file` | string | "" | Certificado do nó (obrigatório se `enabled`) |
| `tls.key_file` | string | "" | Chave privada do nó (obrigatório se `enabled`) |
| `tls.ca_file` | string | "" | CA que assina os certificados (obrigatório se `enabled`) |

**Importante:** Quando `tls.enabled: true`, os três arquivos `cert_file`, `key_file` **e** `ca_file` são **obrigatórios**. O daemon recusa subir sem CA — a verificação do certificado do servidor nunca é ignorada.

**Identidade via mTLS:**

- O certificado de cada peer deve conter o **endereço de endpoint** do peer nos SANs (Subject Alternative Names); caso contrário a verificação TLS falha.
- O **CommonName (CN)** do certificado deve ser igual ao `node.id` do peer. A identidade é autenticada via mTLS: um `node_id` enviado que não bate com o CN do certificado apresentado é **rejeitado** (impede que um peer falsifique a identidade de outro).

**Geração de certificados:**

```bash
# 1. Gerar CA
nnet cert init-ca --output-dir /etc/n-netman/tls

# 2. Gerar certificado do host
nnet cert gen-host \
  --host $HOSTNAME \
  --ip <IP-UNDERLAY> \
  --ca-cert /etc/n-netman/tls/ca.crt \
  --ca-key /etc/n-netman/tls/ca.key \
  --output-dir /etc/n-netman/tls
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


## Seção: routing.inject

Canal de injeção de rotas para as VMs. Declarado na **raiz** de `routing:`, para que uma política valha para toda bridge que carregue a tag. Ver [inject.md](inject.md).

```yaml
routing:
  inject:
    enabled: true
    group_base: "239.8.0.0"     # default; precisa terminar em 0.0
    port: 4790                  # default
    interval_seconds: 10        # intervalo entre anúncios
    lease_seconds: 30           # validade das rotas no agente
    psk_ref: "file:/etc/n-netman/psk/inject.key"
    key_id: "k1"                # identifica a chave, para rotação
    rules:
      - match_tags: ["it"]      # AND: a bridge precisa ter todas
        from_rib: true          # anuncia também rotas aprendidas de peers
        route_tags: ["it"]      # filtra as do RIB por community
        networks: ["172.16.10.0/24"]
        metric: 100
        next_hop: "10.100.0.254"     # opcional; default é bridge.ipv4
        default_gateway: "10.100.0.1" # opcional
```

| Campo | Tipo | Default | Descrição |
|---|---|---|---|
| `enabled` | bool | `false` | Liga o canal |
| `group_base` | IPv4 multicast | `239.8.0.0` | Base do grupo; o VNI entra nos dois últimos octetos |
| `port` | int | `4790` | Porta UDP |
| `interval_seconds` | int | `10` | Intervalo de republicação |
| `lease_seconds` | int | `30` | Por quanto tempo o agente mantém as rotas |
| `psk_ref` | string | — | Obrigatório quando `enabled`. Formato `file:/caminho` |
| `key_id` | string | `""` | Identificador da chave nos anúncios |
| `rules[].match_tags` | []string | — | Obrigatório. Casa quando **todas** estão na bridge |
| `rules[].networks` | []CIDR | `[]` | Prefixos anunciados literalmente |
| `rules[].from_rib` | bool | `false` | Anuncia também as rotas aprendidas do overlay |
| `rules[].route_tags` | []string | `[]` | Filtra as rotas do RIB por community; vazio = todas |
| `rules[].next_hop` | IP | `bridge.ipv4` | Sobrescreve o next-hop anunciado |
| `rules[].metric` | int | `100` | Métrica anunciada |
| `rules[].default_gateway` | IP | `""` | Anuncia um default para o segmento |

Uma regra que não anuncia nada (`networks` vazio, `from_rib` falso e sem `default_gateway`) é rejeitada na validação.

## Seção: overlays[].bridge.tags

Rótulos de texto livre que descrevem o segmento que a bridge carrega. As regras de `routing.inject` selecionam bridges por essas tags.

```yaml
overlays:
  - vni: 100
    bridge:
      name: "br-prod"
      ipv4: "10.100.0.1/24"
      tags: ["it", "external", "public"]
```

Tags duplicadas ou vazias são rejeitadas. Uma bridge sem tags não participa do canal de injeção.

Quando uma regra seleciona a bridge e nem `bridge.ipv4` nem `rules[].next_hop` estão definidos, a configuração é rejeitada: as rotas anunciadas não teriam next-hop válido.

## Seção: overlays[].routing.export.tags

Communities anexadas a cada rota exportada por aquele overlay. Viajam com a rota até os peers e são o que as regras de inject filtram via `route_tags`.

```yaml
    routing:
      export:
        networks: ["172.16.10.0/24"]
        tags: ["it"]
```


## Valores Padrão

Valores aplicados automaticamente quando não especificados:

| Campo | Valor Padrão |
|-------|--------------|
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
