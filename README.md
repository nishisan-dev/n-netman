# n-netman — Nishi Network Manager

[![Go Version](https://img.shields.io/badge/go-1.23+-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-Nishi--NC-orange.svg)](LICENSE)

**n-netman** é um agente leve para criação e gerenciamento de **overlays VXLAN L3/L2** entre hosts Linux rodando KVM/libvirt.

## 🎯 Objetivo

Permitir que redes virtuais distribuídas sejam criadas de forma **declarativa e simples**, eliminando a necessidade de soluções complexas como OVS.

### O que já funciona

- ✅ Criação/atualização de interfaces VXLAN e bridges Linux
- ✅ Sincronização de FDB para peers configurados (flooding BUM)
- ✅ BUM em head-end-replication (FDB) e multicast (grupo IP)
- ✅ **Troca de rotas via gRPC** (ExchangeState, AnnounceRoutes, WithdrawRoutes)
- ✅ Instalação automática de rotas recebidas no kernel
- ✅ CLI `nnet` com `apply`, `status`, `routes`, `doctor`, `version`
- ✅ Carregamento/validação de config YAML com defaults
- ✅ Healthchecks HTTP e endpoint de métricas
- ✅ **Status real dos peers** via endpoint `/status` (healthy/unhealthy/disconnected)
- ✅ **Estatísticas de rotas** (exported, installed, per-peer)
- ✅ **Cleanup automático** de rotas no shutdown e quando peers caem (`flush_on_peer_down`)
- ✅ **Multi-Overlay (v2)** — Múltiplos VXLANs com routing independente por overlay
- ✅ Bridge com IPv4/IPv6 por overlay (para nexthop e anúncios)
- ✅ **TLS/mTLS** para comunicação gRPC entre peers
- ✅ **Policy-Based Routing** — Rotas instaladas em tabelas específicas por VNI
- ✅ **Integração libvirt** — Attach/detach de VMs via `nnet libvirt`

### Em progresso

- ⚠️ Netplan parsing e rotas conectadas/estáticas
- ⚠️ Políticas de import/export (`allow/deny`, `include_connected`, `include_netplan_static`)

### Ainda não funciona (resumo rápido)

- ❌ Políticas avançadas de import/export (`allow/deny/accept_all`, `export_all`)
- ❌ Validação de PSK entre peers

---

## 📋 Pré-requisitos

### Sistema Operacional
- Linux com kernel 3.7+ (suporte a VXLAN)
- Testado em Ubuntu 22.04+, Debian 12+

### Dependências
```bash
# Verificar suporte a VXLAN e bridges
lsmod | grep vxlan
lsmod | grep bridge

# Se não estiverem carregados:
sudo modprobe vxlan
sudo modprobe bridge
```

### Build
- Go 1.23 ou superior

```bash
# Verificar versão do Go
go version
```

---

## 🚀 Instalação

### Opção 1: Build do Fonte

```bash
# Clonar repositório
git clone https://github.com/nishisan-dev/n-netman.git
cd n-netman

# Build
make build

# Ou manualmente:
go build -o bin/nnetd ./cmd/nnetd
go build -o bin/nnet ./cmd/nnet
```

### Opção 2: Instalação no Sistema

```bash
# Build e instalar em $GOPATH/bin
make install

# Ou copiar manualmente
sudo cp bin/nnetd /usr/local/bin/
sudo cp bin/nnet /usr/local/bin/
```

---

## ⚙️ Configuração

### Criar Diretório de Configuração

```bash
sudo mkdir -p /etc/n-netman
```

### Arquivo de Configuração

Crie o arquivo `/etc/n-netman/n-netman.yaml`:

```yaml
version: 1

node:
  id: "host-a-01"          # Identificador único deste nó
  hostname: "host-a"
  tags:
    - "datacenter-1"
    - "kvm"

# Integração com netplan (somente leitura)
netplan:
  enabled: true
  underlay:
    prefer_interfaces:
      - "eth0"
      - "ens3"
    prefer_address_families:
      - "ipv4"

# Integração com KVM/libvirt (opcional)
kvm:
  enabled: false           # Defina como true se usar libvirt
  bridges:
    - name: "br-nnet-100"
      stp: false
      mtu: 1450
      manage: true

# Configuração do overlay VXLAN
overlay:
  vxlan:
    vni: 100               # VXLAN Network Identifier
    name: "vxlan100"
    dstport: 4789
    mtu: 1450
    learning: true
    bridge: "br-nnet-100"

  # Peers (outros hosts no overlay)
  peers:
    - id: "host-b-01"
      endpoint:
        address: "10.10.0.12"    # IP underlay do peer
      auth:
        mode: "psk"
        psk_ref: "file:/etc/n-netman/psk/host-b-01.key"
      health:
        keepalive_interval_ms: 1500
        dead_after_ms: 6000

    - id: "host-c-01"
      endpoint:
        address: "10.10.0.13"

# Roteamento entre peers
routing:
  enabled: true
  export:
    export_all: false
    networks:
      - "172.16.10.0/24"   # Redes que este nó anuncia
      - "2001:db8:10::/64" # Suporte IPv6
    include_connected: true
    include_netplan_static: true
    metric: 100
  import:
    accept_all: false
    allow:
      - "172.16.0.0/16"
      - "2001:db8::/32"
    deny:
      - "0.0.0.0/0"        # Bloquear default route
    install:
      table: 100           # Tabela de roteamento customizada
      replace_existing: true
      flush_on_peer_down: true
      route_lease_seconds: 30

# Topologia
topology:
  mode: "direct-preferred"
  transit: "deny"          # Não permitir trânsito por padrão

# Segurança do control-plane
security:
  control_plane:
    transport: "grpc"
    listen:
      address: "0.0.0.0"
      port: 9898
    tls:
      enabled: false
      cert_file: "/etc/n-netman/tls/server.crt"
      key_file: "/etc/n-netman/tls/server.key"
      ca_file: "/etc/n-netman/tls/ca.crt"

# Observabilidade
observability:
  logging:
    level: "info"
    format: "json"
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

### Chaves PSK (Opcional)

Se usar autenticação PSK entre peers:

```bash
sudo mkdir -p /etc/n-netman/psk

# Gerar chave para cada peer
openssl rand -hex 32 | sudo tee /etc/n-netman/psk/host-b-01.key
sudo chmod 600 /etc/n-netman/psk/*.key
```

### Certificados mTLS (Recomendado)

O `nnet` possui utilitários embutidos para gerenciar PKI:

```bash
# 1. Gerar CA Raiz
nnet cert init-ca --output-dir /etc/n-netman/tls

# 2. Gerar certificado para este nó
nnet cert gen-host \
  --host $(hostname) \
  --ip 192.168.56.11 \
  --ca-cert /etc/n-netman/tls/ca.crt \
  --ca-key /etc/n-netman/tls/ca.key \
  --output-dir /etc/n-netman/tls
```

### Multi-Overlay (Config v2) 🆕

A partir da versão 2 do config, você pode definir múltiplos overlays VXLAN, cada um com seu próprio routing:

```yaml
version: 2

node:
  id: "host-a"
  hostname: "host-a"

overlays:
  # Production Overlay (VNI 100)
  - vni: 100
    name: "vxlan-prod"
    dstport: 4789
    mtu: 1450
    learning: true
    bridge:
      name: "br-prod"
      ipv4: "10.100.0.1/24"
    underlay_interface: "ens3"    # Interface física para este overlay
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
            enabled: true          # ← Cria rules de PBR automaticamente

  # Management Overlay (VNI 200)
  - vni: 200
    name: "vxlan-mgmt"
    dstport: 4789
    mtu: 1450
    learning: true
    bridge:
      name: "br-mgmt"
      ipv4: "10.200.1.1/24"
    underlay_interface: "ens4"
    bum:
      mode: "multicast"
      group: "239.1.1.200"
    routing:
      export:
        networks:
          - "10.200.0.0/24"
        metric: 200
      import:
        accept_all: true
        install:
          table: 200
          lookup_rules:
            enabled: true          # ← Cria rules de PBR automaticamente

# Peers (shared across overlays)
overlay:
  peers:
    - id: "host-b"
      endpoint:
        address: "192.168.56.12"
```

### Policy-Based Routing (`lookup_rules`) 🆕

Quando `lookup_rules.enabled: true`, o n-netman cria automaticamente regras `ip rule` para direcionar tráfego da bridge para a tabela de roteamento correta:

```bash
# Regras criadas automaticamente para br-prod (table 100):
ip rule add iif br-prod lookup 100 priority 100
ip rule add oif br-prod lookup 100 priority 101

# Regras criadas automaticamente para br-mgmt (table 200):
ip rule add iif br-mgmt lookup 200 priority 100
ip rule add oif br-mgmt lookup 200 priority 101
```

**Por que isso é importante?**

Sem essas regras, o kernel Linux consulta apenas a `table main` para decisões de roteamento. Com `lookup_rules` habilitado:

1. Tráfego **entrando** por `br-prod` (iif) → consulta `table 100`
2. Tráfego **saindo** por `br-prod` (oif) → consulta `table 100`
3. Isolamento completo entre overlays — cada um usa sua própria tabela

Veja o exemplo completo em [`examples/multi-overlay.yaml`](examples/multi-overlay.yaml).

---

## 🎮 Uso

### CLI - Comandos Disponíveis

```bash
# Ver ajuda
nnet --help

# Ver versão
nnet version

# Verificar configuração e mostrar status
nnet -c /etc/n-netman/n-netman.yaml status

# Visualizar rotas configuradas
nnet -c /etc/n-netman/n-netman.yaml routes

# Dry-run (mostra o que seria feito sem executar)
nnet -c /etc/n-netman/n-netman.yaml apply --dry-run

# Aplicar configuração (requer root)
sudo nnet -c /etc/n-netman/n-netman.yaml apply

# Diagnóstico do sistema
nnet -c /etc/n-netman/n-netman.yaml doctor

# Integração libvirt - listar VMs
nnet libvirt list-vms --all

# Integração libvirt - attach VM a bridge
sudo nnet libvirt attach web-01 --bridge br-prod
```

### Integração libvirt

O n-netman oferece comandos para integrar VMs libvirt/KVM às bridges de overlay:

![Fluxo de integração](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/libvirt_integration.puml)

```bash
# Configurar dependência systemd (bridges existem antes das VMs)
sudo nnet libvirt enable

# Listar VMs e interfaces
nnet libvirt list-vms --all

# Attach VM a uma bridge
sudo nnet libvirt attach web-01 --bridge br-prod

# Detach por MAC
sudo nnet libvirt detach web-01 --mac 52:54:00:12:34:56

# Ver status da integração
nnet libvirt status
```

Veja a documentação completa em [docs/libvirt.md](docs/libvirt.md).

### Exemplo de Saída: `nnet status`

```
🖥️  Node: host-a (host-a)

📡 VXLAN Interfaces:
─────────────────────────────────────────
  🟢 UP vxlan100 (VNI 100, MTU 1450)

🌉 Bridges:
─────────────────────────────────────────
  🟢 UP br-nnet-100 (MTU 1450)
      Attached: [vxlan100]

👥 Configured Peers:
─────────────────────────────────────────
  ID      ENDPOINT       STATUS               ROUTES
  ──      ────────       ──────               ──────
  host-b  192.168.56.12  🟢 healthy (5s ago)   1
  host-c  192.168.56.13  🟢 healthy (3s ago)   1

📊 Route Statistics:
─────────────────────────────────────────
  📤 Exported:   1 route(s) (172.16.10.0/24)
  📥 Installed:  2 route(s) in table 100
      • 172.16.20.0/24 via 192.168.56.12 (host-b)
      • 172.16.30.0/24 via 192.168.56.13 (host-c)
```

### Daemon

```bash
# Iniciar daemon em foreground (requer root)
sudo nnetd -config /etc/n-netman/n-netman.yaml

# Ver versão
nnetd -version
```

### Systemd Service (Opcional)

Crie `/etc/systemd/system/n-netman.service`:

```ini
[Unit]
Description=n-netman VXLAN Overlay Manager
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nnetd -config /etc/n-netman/n-netman.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable n-netman
sudo systemctl start n-netman
sudo systemctl status n-netman
```

---

## 🧪 Lab Testing (Vagrant)

O projeto inclui um `Vagrantfile` para testar multi-overlay em um ambiente com 3 VMs.

### Topologia do Lab (Multi-Overlay)

```
┌───────────────────────────────────────────────────────────────────────────────┐
│                           Underlay Networks                                   │
├───────────────────────────────────────────────────────────────────────────────┤
│    Production: 192.168.56.0/24    │    Management: 192.168.57.0/24           │
├───────────────────┬───────────────┴─────┬─────────────────────────────────────┤
│     host-a        │       host-b        │        host-c                       │
│  Prod: .56.11     │    Prod: .56.12     │     Prod: .56.13                    │
│  Mgmt: .57.11     │    Mgmt: .57.12     │     Mgmt: .57.13                    │
│                   │                     │                                     │
│ VNI 100 (Prod):   │ VNI 100 (Prod):     │ VNI 100 (Prod):                     │
│ 172.16.10.0/24    │ 172.16.20.0/24      │ 172.16.30.0/24                      │
│                   │                     │                                     │
│ VNI 200 (Mgmt):   │ VNI 200 (Mgmt):     │ VNI 200 (Mgmt):                     │
│ 10.200.10.0/24    │ 10.200.20.0/24      │ 10.200.30.0/24                      │
└───────────────────┴─────────────────────┴─────────────────────────────────────┘
```

### Requisitos

- [Vagrant](https://www.vagrantup.com/) instalado
- [VirtualBox](https://www.virtualbox.org/) instalado
- ~2GB de RAM livre

### Subir o Lab

```bash
# Subir as 3 VMs (primeira vez demora ~5min)
vagrant up

# Ver status
vagrant status
```

### Testar a Troca de Rotas

```bash
# Terminal 1: host-a
vagrant ssh host-a
sudo nnetd -config /etc/n-netman/n-netman.yaml

# Terminal 2: host-b
vagrant ssh host-b
sudo nnetd -config /etc/n-netman/n-netman.yaml

# Terminal 3: host-c
vagrant ssh host-c
sudo nnetd -config /etc/n-netman/n-netman.yaml
```

Aguarde ~30 segundos e verifique as rotas aprendidas:

```bash
# Em qualquer VM - verificar ambas as tabelas
ip route show table 100   # Production overlay routes
ip route show table 200   # Management overlay routes

# Saída esperada (ex: em host-a):
# Table 100 (Production - VNI 100):
# 172.16.20.0/24 via 10.100.0.2 dev br-prod proto openr metric 100
# 172.16.30.0/24 via 10.100.0.3 dev br-prod proto openr metric 100

# Table 200 (Management - VNI 200):
# 10.200.20.0/24 via 10.200.1.2 dev br-mgmt proto openr metric 200
# 10.200.30.0/24 via 10.200.1.3 dev br-mgmt proto openr metric 200
```

### Script de Validação

```bash
# Em cada VM, rodar o script de teste
./n-netman/scripts/lab-test.sh
```

### Comandos Úteis

```bash
# Destruir VMs
vagrant destroy -f

# Recriar uma VM específica
vagrant destroy host-a -f && vagrant up host-a

# SSH em uma VM
vagrant ssh host-b
```

---

## 📊 Observabilidade

### Métricas Prometheus

Disponíveis em `http://127.0.0.1:9109/metrics`. As seguintes métricas são coletadas e exportadas:

| Métrica | Descrição |
|---------|-----------|
| `nnetman_reconciliations_total` | Total de ciclos de reconciliação |
| `nnetman_reconciliation_errors_total` | Erros de reconciliação |
| `nnetman_reconciliation_duration_seconds` | Duração dos ciclos de reconciliação |
| `nnetman_last_reconcile_timestamp_seconds` | Timestamp do último reconcile |
| `nnetman_vxlans_active` | Interfaces VXLAN ativas |
| `nnetman_bridges_active` | Bridges ativas |
| `nnetman_fdb_entries_total` | Total de entradas FDB |
| `nnetman_peers_configured` | Peers configurados |
| `nnetman_peers_connected` | Peers conectados |
| `nnetman_peers_healthy` | Peers saudáveis |
| `nnetman_routes_exported` | Rotas exportadas |
| `nnetman_routes_imported` | Rotas importadas |
| `nnetman_grpc_requests_total` | Total de requisições gRPC |
| `nnetman_grpc_request_duration_seconds` | Duração das requisições gRPC |

### Health Checks

```bash
# Liveness
curl http://127.0.0.1:9110/livez

# Readiness
curl http://127.0.0.1:9110/readyz

# Health geral
curl http://127.0.0.1:9110/healthz

# Status detalhado (peers + rotas)
curl http://127.0.0.1:9110/status
```

---

## 🧩 Componentes Internos (Go)

- `cmd/nnetd`: daemon (carrega config, inicia observabilidade e reconciler)
- `cmd/nnet`: CLI para aplicar config e inspecionar estado
- `internal/config`: structs, defaults e validação do YAML
- `internal/reconciler`: loop que garante bridge/VXLAN/FDB conforme config
- `internal/netlink`: wrappers de bridge/VXLAN/FDB/rotas via netlink
- `internal/controlplane`: servidor/cliente gRPC com ExchangeState/Announce/Withdraw
- `internal/routing`: políticas de export/import (helpers, ainda não aplicados no daemon)
- `internal/observability`: métricas Prometheus e healthchecks HTTP

---

## 🔧 Troubleshooting

### Verificar Interfaces Criadas

```bash
# VXLAN
ip -d link show vxlan100

# Bridge
ip -d link show br-nnet-100
bridge link show

# FDB entries
bridge fdb show dev vxlan100
```

### Verificar Rotas

```bash
# Rotas na tabela 100
ip route show table 100

# Todas as rotas
ip route show
```

### Logs

```bash
# Com systemd
journalctl -u n-netman -f

# Em foreground
nnetd -config /etc/n-netman/n-netman.yaml 2>&1 | jq .
```

### Diagnóstico Completo

```bash
nnet doctor
```

---

## 🏗️ Arquitetura

### Visão Geral dos Componentes

O diagrama abaixo mostra a arquitetura atual. O control-plane agora implementa troca real de rotas via gRPC.

Fonte: `docs/diagrams/architecture.puml`

![Arquitetura geral](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/architecture.puml)

### Fluxo de Reconciliação (Multi-Overlay)

Fonte: `docs/diagrams/reconciler-loop.puml`

![Fluxo de reconciliacao](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/reconciler-loop.puml)

### Troca de Rotas entre Peers

Fonte: `docs/diagrams/route-exchange.puml`

![Troca de rotas entre peers](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/route-exchange.puml)

### Topologia de Rede

Fonte: `docs/diagrams/topology.puml`

![Topologia de rede](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/topology.puml)

---

## ⚠️ Limitações Atuais (MVP)

Esta é uma versão MVP. As seguintes funcionalidades **ainda não estão implementadas**:

### Não Funcional
| Item | Status | Descrição |
|------|--------|-----------|
| **Validação de PSK** | ❌ | Chaves PSK são lidas mas não validadas |
| **Netplan parsing** | ❌ | Rotas do netplan não são lidas automaticamente |
| **Políticas de import/export** | ❌ | `allow/deny`, `include_connected`, `include_netplan_static` ainda não são aplicados |

### Funcional
| Item | Status | Descrição |
|------|--------|-----------|
| **VXLAN/Bridge** | ✅ | Criação funciona (requer root) |
| **FDB entries** | ✅ | Sincronização de peers funciona |
| **Troca de rotas gRPC** | ✅ | Handlers implementados, rotas instaladas |
| **TLS/mTLS** | ✅ | Comunicação gRPC criptografada entre peers |
| **Multi-Overlay** | ✅ | VNI-aware routing com tabelas independentes por overlay |
| **Reconciler** | ✅ | Loop funciona |
| **Métricas** | ✅ | Servidor Prometheus ativo e métricas coletadas |
| **Healthcheck** | ✅ | Endpoints funcionam |
| **Status de peers** | ✅ | Health check implementado com keepalive |
| **Integração libvirt** | ✅ | CLI `nnet libvirt` para attach/detach de VMs |

### Próximas Prioridades
1. Testes de integração com VMs reais em lab
2. Validação de PSK entre peers
3. Policies avançadas de import/export

---

## 🐛 Known Issues

### FDB Entries via Netlink Library

A biblioteca Go `vishvananda/netlink` possui um bug conhecido onde `NeighAppend()` retorna "operation not supported" ao adicionar entradas FDB em interfaces VXLAN que estão attached a uma bridge.

**Workaround implementado:** Utilizamos `exec.Command("bridge", "fdb", "append", ...)` diretamente ao invés da API da biblioteca.

**Referência:** [vishvananda/netlink#714](https://github.com/vishvananda/netlink/issues/714)

---

## 📜 Licença

Nishi Network Manager License (Non-Commercial Evaluation) - veja [LICENSE](LICENSE) para detalhes.

> **Nota:** Uso comercial requer licença separada. Contate o Licensor para mais informações.

---

## 🤝 Contribuindo

1. Fork o repositório
2. Crie uma branch (`git checkout -b feature/minha-feature`)
3. Commit suas mudanças (`git commit -am 'feat: adiciona minha feature'`)
4. Push para a branch (`git push origin feature/minha-feature`)
5. Abra um Pull Request
