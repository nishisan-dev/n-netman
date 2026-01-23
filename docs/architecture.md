# Arquitetura do n-netman

Este documento descreve a arquitetura interna do agente, suas camadas e o fluxo de operação.

## Camadas do Sistema

O n-netman opera em quatro camadas distintas:

```
┌─────────────────────────────────────────────────────────┐
│                    Control Plane                         │
│          gRPC Server/Client · Route Exchange             │
├─────────────────────────────────────────────────────────┤
│                      Overlay                             │
│      VXLAN Tunnels · Linux Bridges · FDB Entries         │
├─────────────────────────────────────────────────────────┤
│                  Virtualization (Opcional)               │
│             libvirt Networks · VM Attachment             │
├─────────────────────────────────────────────────────────┤
│                      Underlay                            │
│         Interfaces Físicas · Rotas do Host               │
└─────────────────────────────────────────────────────────┘
```

### Underlay

A camada underlay representa a conectividade física/lógica do host:
- Interfaces de rede (eth0, ens3, bond0)
- Endereços IP e rotas do sistema
- Gerenciada externamente (netplan, NetworkManager, manual)

O n-netman **lê** a configuração underlay para inferir endpoints VXLAN, mas **nunca modifica** interfaces underlay. Se `netplan.enabled: true`, o agente usa a lista `prefer_interfaces` para selecionar qual interface será a source do túnel VXLAN.

### Virtualization (Opcional)

Quando `kvm.enabled: true`, o agente pode:
- Criar bridges Linux para VMs
- Integrar com libvirt networks (modo `libvirt-network`)
- Anexar VMs automaticamente às bridges (futuro)

**Importante:** Esta camada é completamente opcional. O n-netman funciona sem KVM/libvirt, atuando como puro agente de overlay para hosts Linux.

### Overlay

A camada overlay é o core do n-netman:

| Componente | Responsabilidade |
|------------|------------------|
| **VXLAN Interface** | Túnel L2-over-L3 identificado pelo VNI |
| **Linux Bridge** | Agregação de interfaces (VXLAN + VMs/containers) |
| **FDB Entries** | Forwarding Database para replicação de tráfego BUM |
| **Bridge IP** | Endereço na bridge para atuar como gateway L3 |

### Control Plane

O control-plane é responsável pela troca de informações entre peers:

- **Servidor gRPC** escuta na porta configurada (default: 9898)
- **Cliente gRPC** conecta a cada peer para trocar rotas
- **Protocolo proprietário** (Protocol 99) com RPCs:
  - `ExchangeState` — Sincronização inicial de rotas
  - `AnnounceRoutes` — Anúncio de novas rotas
  - `WithdrawRoutes` — Retirada de rotas
  - `Keepalive` — Streaming bidirecional para health check

## Componentes Internos

```
nnetd (daemon)
├── config/           # Parsing e validação do YAML
├── reconciler/       # Loop de reconciliação
├── netlink/          # Wrappers para VXLAN, Bridge, FDB, Route
├── controlplane/     # Servidor e cliente gRPC
├── routing/          # Políticas de export/import
└── observability/    # Métricas, healthchecks, logging
```

### Config Loader

Responsável por:
1. Carregar o YAML (`config/loader.go`)
2. Aplicar defaults (MTU 1450, porta 4789, etc.)
3. Validar campos obrigatórios e semântica
4. Detectar versão (v1 = single overlay, v2 = multi-overlay)

### Reconciler

O coração do agente. Executa em loop contínuo:

```
                    ┌──────────────────┐
                    │    Início Loop   │
                    └────────┬─────────┘
                             │
              ┌──────────────▼──────────────┐
              │   Para cada Overlay:        │
              │   ├─ Reconcile Bridge       │
              │   ├─ Reconcile VXLAN        │
              │   ├─ Reconcile FDB          │
              │   └─ Reconcile Policy Rules │
              └──────────────┬──────────────┘
                             │
              ┌──────────────▼──────────────┐
              │   Expira rotas stale        │
              │   (route_lease_seconds)     │
              └──────────────┬──────────────┘
                             │
              ┌──────────────▼──────────────┐
              │   Sleep (interval)          │
              └──────────────┬──────────────┘
                             │
                    ┌────────▼────────┐
                    │   Próximo ciclo │
                    └─────────────────┘
```

O intervalo padrão é 5 segundos. Cada ciclo:
1. Garante que bridges existem com configuração correta
2. Garante que interfaces VXLAN existem e estão attached às bridges
3. Sincroniza FDB entries para peers (modo head-end-replication)
4. Cria `ip rule` para policy-based routing (se `lookup_rules.enabled`)
5. Remove rotas expiradas do kernel

### Netlink Wrappers

Camada de abstração sobre a biblioteca `vishvananda/netlink`:

| Wrapper | Operações |
|---------|-----------|
| `BridgeManager` | Ensure, SetIP, LinkUp |
| `VXLANManager` | Ensure, AttachToBridge |
| `FDBManager` | Sync, AppendBUM |
| `RouteManager` | Install, Remove, ListTable |
| `RuleManager` | Ensure iif/oif rules |

### Control Plane Server

Implementa o serviço gRPC `NNetMan`:

```go
type Server struct {
    cfg        *config.Config
    routeTable *RouteTable
    peerStatus map[string]*PeerStatus
    // ...
}
```

Principais operações:
- **ExchangeState:** Peer conecta, envia suas rotas, recebe rotas locais
- **AnnounceRoutes:** Recebe anúncios, adiciona à RouteTable, instala no kernel
- **WithdrawRoutes:** Remove rotas da RouteTable e do kernel
- **Keepalive:** Mantém conexão viva, atualiza `lastSeen` do peer

### Control Plane Client

Gerencia conexões ativas para cada peer:

```go
type Client struct {
    cfg          *config.Config
    server       *Server
    connections  map[string]*peerConn
    // ...
}
```

Responsabilidades:
- Conecta a cada peer configurado (retry com backoff)
- Envia `ExchangeState` na conexão inicial
- Mantém goroutine de keepalive por peer
- Detecta peer down e notifica RouteTable

## Fluxo de Inicialização

```
1. nnetd -config /etc/n-netman/n-netman.yaml

2. Config Loader
   ├─ Parse YAML
   ├─ Apply defaults
   └─ Validate

3. Start Observability
   ├─ Prometheus @ :9109
   └─ Healthcheck @ :9110

4. Start Control Plane
   ├─ gRPC Server @ :9898
   ├─ Load local routes
   └─ Start Client (connect to peers)

5. Start Reconciler Loop
   ├─ RunOnce() (immediate reconcile)
   └─ Run() (continuous loop)

6. Wait for shutdown signal (SIGINT, SIGTERM)

7. Cleanup
   ├─ Stop reconciler
   ├─ Stop control plane
   └─ Remove installed routes (optional)
```

## Fluxo de Reconciliação de Estado

O reconciler garante que o estado real (kernel) converge para o estado desejado (config):

**Estado Desejado (Config YAML):**
```yaml
overlays:
  - vni: 100
    bridge:
      name: br-prod
      ipv4: 10.100.0.1/24
```

**Reconciliação:**

1. Bridge `br-prod` existe?
   - Não → `ip link add br-prod type bridge`
2. Bridge tem IP correto?
   - Não → `ip addr add 10.100.0.1/24 dev br-prod`
3. Interface VXLAN `vxlan100` existe?
   - Não → `ip link add vxlan100 type vxlan id 100 ...`
4. VXLAN está attached à bridge?
   - Não → `ip link set vxlan100 master br-prod`
5. FDB entries para peers existem?
   - Não → `bridge fdb append 00:00:00:00:00:00 dev vxlan100 dst <peer-ip>`
6. Policy rules existem? (se `lookup_rules.enabled`)
   - Não → `ip rule add iif br-prod lookup 100`

Se qualquer operação falha, o erro é logado e o ciclo continua. O próximo ciclo tentará novamente (idempotência).

## O Papel de VXLAN, Bridges e Roteamento

### VXLAN

RFC 7348 — Virtual Extensible LAN. Encapsula frames Ethernet em pacotes UDP:

```
[Outer IP Header] [Outer UDP Header] [VXLAN Header] [Original Ethernet Frame]
                                         │
                                         └─ VNI (24 bits) identifica o overlay
```

O n-netman cria interfaces VXLAN no kernel Linux:
```bash
ip link add vxlan100 type vxlan id 100 dstport 4789 local <underlay-ip> learning
```

### Linux Bridges

Bridges Linux funcionam como switches L2 em software:
```bash
ip link add br-prod type bridge
ip link set vxlan100 master br-prod
ip link set vnet0 master br-prod  # VM interface
```

Qualquer tráfego que entra em uma porta da bridge é comutado para outras portas com base no MAC learning.

### Roteamento

Rotas aprendidas de peers são instaladas em tabelas customizadas:
```bash
# Rotas do overlay VNI 100 vão para table 100
ip route add 172.16.20.0/24 via 10.100.0.2 dev br-prod table 100 proto openr metric 100
```

Para garantir que o tráfego consulte a tabela correta, são criadas policy rules:
```bash
ip rule add iif br-prod lookup 100
ip rule add oif br-prod lookup 100
```

Isso isola completamente o roteamento de cada overlay.
