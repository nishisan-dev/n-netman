# n-netman — Nishi Network Manager

[![Go Version](https://img.shields.io/badge/go-1.23+-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

**n-netman** é um agente leve para criação e gerenciamento de **overlays VXLAN L3/L2** entre hosts Linux rodando KVM/libvirt.

## 🎯 Objetivo

Permitir que redes virtuais distribuídas sejam criadas de forma **declarativa e simples**, eliminando a necessidade de soluções complexas como OVS.

### O que já funciona

- ✅ Criação/atualização de interfaces VXLAN e bridges Linux
- ✅ Sincronização de FDB para peers configurados (flooding BUM)
- ✅ **Troca de rotas via gRPC** (ExchangeState, AnnounceRoutes, WithdrawRoutes)
- ✅ Instalação automática de rotas recebidas no kernel
- ✅ CLI `nnet` com `apply`, `status`, `routes`, `doctor`
- ✅ Carregamento/validação de config YAML com defaults
- ✅ Healthchecks HTTP e endpoint de métricas

### Em progresso

- ⚠️ Status de peers (`nnet status` mostra `unknown` até health check)
- ⚠️ TLS para comunicação gRPC entre peers
- ⚠️ Integração libvirt/attach de VMs
- ⚠️ Netplan parsing e rotas conectadas/estáticas

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
git clone https://github.com/lucas/n-netman.git
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
    networks:
      - "172.16.10.0/24"   # Redes que este nó anuncia
      - "2001:db8:10::/64" # Suporte IPv6
    include_connected: true
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

---

## 🎮 Uso

### CLI - Comandos Disponíveis

```bash
# Ver ajuda
nnet --help

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
```

### Exemplo de Saída: `nnet status`

```
🖥️  Node: host-a-01 (host-a)

📡 VXLAN Interfaces:
─────────────────────────────────────────
  🟢 UP vxlan100 (VNI 100, MTU 1450)

🌉 Bridges:
─────────────────────────────────────────
  🟢 UP br-nnet-100 (MTU 1450)
      Attached: [vxlan100]

👥 Configured Peers:
─────────────────────────────────────────
  ID          ENDPOINT      STATUS
  ──          ────────      ──────
  host-b-01   10.10.0.12    ⏳ unknown
  host-c-01   10.10.0.13    ⏳ unknown
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

O projeto inclui um `Vagrantfile` para testar a troca de rotas em um ambiente com 3 VMs.

### Topologia do Lab

```
┌─────────────────────────────────────────────────────────────┐
│                  Underlay: 192.168.56.0/24                  │
├───────────────────┬──────────────────┬──────────────────────┤
│     host-a        │     host-b       │      host-c          │
│  192.168.56.11    │  192.168.56.12   │   192.168.56.13      │
│                   │                  │                      │
│ Overlay:          │ Overlay:         │ Overlay:             │
│ 172.16.10.0/24    │ 172.16.20.0/24   │ 172.16.30.0/24       │
└───────────────────┴──────────────────┴──────────────────────┘
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

Aguarde ~5 segundos e verifique as rotas aprendidas:

```bash
# Em qualquer VM
ip route show table 100

# Saída esperada (ex: em host-a):
# 172.16.20.0/24 via <next-hop> dev br-nnet-100 proto 99
# 172.16.30.0/24 via <next-hop> dev br-nnet-100 proto 99
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

Disponíveis em `http://127.0.0.1:9109/metrics`. Nota: os contadores ainda não são atualizados pelo reconciler/control-plane.

| Métrica | Descrição |
|---------|-----------|
| `nnetman_reconciliations_total` | Total de ciclos de reconciliação |
| `nnetman_reconciliation_errors_total` | Erros de reconciliação |
| `nnetman_vxlans_active` | Interfaces VXLAN ativas |
| `nnetman_bridges_active` | Bridges ativas |
| `nnetman_peers_configured` | Peers configurados |
| `nnetman_peers_healthy` | Peers saudáveis |
| `nnetman_routes_exported` | Rotas exportadas |
| `nnetman_routes_imported` | Rotas importadas |

### Health Checks

```bash
# Liveness
curl http://127.0.0.1:9110/livez

# Readiness
curl http://127.0.0.1:9110/readyz

# Health geral
curl http://127.0.0.1:9110/healthz
```

---

## 🧩 Componentes Internos (Go)

- `cmd/nnetd`: daemon (carrega config, inicia observabilidade e reconciler)
- `cmd/nnet`: CLI para aplicar config e inspecionar estado
- `internal/config`: structs, defaults e validação do YAML
- `internal/reconciler`: loop que garante bridge/VXLAN/FDB conforme config
- `internal/netlink`: wrappers de bridge/VXLAN/FDB/rotas via netlink
- `internal/controlplane`: servidor/cliente gRPC com ExchangeState/Announce/Withdraw
- `internal/routing`: políticas de export/import (somente redes do config)
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

```plantuml
@startuml
skinparam componentStyle rectangle

package "n-netman daemon" {
    [Config Loader] --> [Reconciler]
    [Reconciler] --> [VXLAN Manager]
    [Reconciler] --> [Bridge Manager]
    [Reconciler] --> [FDB Manager]
    
    [gRPC Server] --> [Route Table]
    [gRPC Client] --> [Route Table]
    [Route Table] --> [Route Manager]
    
    [Observability] --> [Prometheus Metrics]
    [Observability] --> [Health Endpoints]
}

package "Linux Kernel" {
    [netlink API]
    [VXLAN Module]
    [Bridge Module]
    [Routing Tables]
}

[VXLAN Manager] --> [netlink API]
[Bridge Manager] --> [netlink API]
[FDB Manager] --> [netlink API]
[Route Manager] --> [Routing Tables]

cloud "Peer Nodes" {
    [Peer A gRPC]
    [Peer B gRPC]
}

[gRPC Client] --> [Peer A gRPC]
[gRPC Client] --> [Peer B gRPC]

@enduml
```

### Fluxo de Reconciliação

```plantuml
@startuml
title Reconciler Loop

participant "Config" as C
participant "Reconciler" as R
participant "BridgeManager" as BM
participant "VXLANManager" as VM
participant "FDBManager" as FM
participant "Linux Kernel" as K

loop Every 10 seconds
    R -> C: Read desired state
    
    R -> BM: Ensure bridge exists
    BM -> K: netlink: create/update bridge
    K --> BM: OK
    
    R -> VM: Ensure VXLAN exists
    VM -> K: netlink: create/update vxlan
    K --> VM: OK
    
    VM -> BM: Attach VXLAN to bridge
    BM -> K: netlink: set master
    K --> BM: OK
    
    R -> FM: Sync FDB entries
    loop For each peer
        FM -> K: netlink: add FDB entry
        K --> FM: OK
    end
    
    R -> R: Sleep 10s
end

@enduml
```

### Troca de Rotas entre Peers

```plantuml
@startuml
title Route Exchange Protocol

participant "Host A\n(curitiba-a-01)" as A
participant "Host B\n(curitiba-b-01)" as B
participant "Host C\n(curitiba-c-01)" as C

== Initial State Exchange ==
A -> B: ExchangeState(my_routes)
B --> A: StateResponse(peer_routes)

A -> C: ExchangeState(my_routes)
C --> A: StateResponse(peer_routes)

== Route Announcement ==
note over A: New local route detected:\n172.16.30.0/24

A -> B: AnnounceRoutes([172.16.30.0/24])
B --> A: RouteAck(accepted=true)
note over B: Install route:\nip route add 172.16.30.0/24\n  via <overlay-ip> table 100

A -> C: AnnounceRoutes([172.16.30.0/24])
C --> A: RouteAck(accepted=true)

== Keepalive ==
loop Every 1.5s
    A -> B: Keepalive(seq=N)
    B --> A: KeepaliveAck(seq=N)
end

== Route Withdrawal ==
note over A: Route removed locally

A -> B: WithdrawRoutes([172.16.30.0/24])
B --> A: RouteAck(processed=1)
note over B: Remove route from table 100

@enduml
```

### Topologia de Rede

```plantuml
@startuml
title VXLAN Overlay Network

cloud "Underlay Network\n(10.10.0.0/24)" {
    node "Host A\n10.10.0.11" as HA {
        rectangle "br-nnet-100" as BA
        rectangle "vxlan100" as VA
        rectangle "VM-A1" as VMA1
        rectangle "VM-A2" as VMA2
        
        VMA1 --> BA
        VMA2 --> BA
        VA --> BA
    }
    
    node "Host B\n10.10.0.12" as HB {
        rectangle "br-nnet-100" as BB
        rectangle "vxlan100" as VB
        rectangle "VM-B1" as VMB1
        
        VMB1 --> BB
        VB --> BB
    }
    
    node "Host C\n10.10.0.13" as HC {
        rectangle "br-nnet-100" as BC
        rectangle "vxlan100" as VC
        rectangle "VM-C1" as VMC1
        
        VMC1 --> BC
        VC --> BC
    }
}

VA <-[#blue,dashed]-> VB : VXLAN VNI 100\nUDP 4789
VA <-[#blue,dashed]-> VC : VXLAN VNI 100\nUDP 4789
VB <-[#blue,dashed]-> VC : VXLAN VNI 100\nUDP 4789

note bottom of HA
  Overlay: 172.16.10.0/24
end note

note bottom of HB
  Overlay: 172.16.20.0/24
end note

note bottom of HC
  Overlay: 172.16.30.0/24
end note

@enduml
```

---

## ⚠️ Limitações Atuais (MVP)

Esta é uma versão MVP. As seguintes funcionalidades **ainda não estão implementadas**:

### Não Funcional
| Item | Status | Descrição |
|------|--------|-----------|
| **TLS no gRPC** | ❌ | Comunicação entre peers não é criptografada |
| **Validação de PSK** | ❌ | Chaves PSK são lidas mas não validadas |
| **Integração libvirt** | ❌ | Attach automático de VMs não implementado |
| **Netplan parsing** | ❌ | Rotas do netplan não são lidas automaticamente |

### Parcialmente Funcional
| Item | Status | Descrição |
|------|--------|-----------|
| **VXLAN/Bridge** | ✅ | Criação funciona (requer root) |
| **FDB entries** | ✅ | Sincronização de peers funciona |
| **Troca de rotas gRPC** | ✅ | Handlers implementados, rotas instaladas |
| **Reconciler** | ✅ | Loop funciona |
| **Métricas** | ⚠️ | Servidor inicia, mas métricas não são atualizadas |
| **Healthcheck** | ✅ | Endpoints funcionam |
| **Status de peers** | ⚠️ | Health check implementado, status pode demorar |

### Próximas Prioridades
1. Adicionar TLS ao control plane
2. Testes de integração com VMs reais em lab
3. Validação de PSK entre peers
4. Integração com libvirt para attach automático de VMs

---

## 📜 Licença

MIT License - veja [LICENSE](LICENSE) para detalhes.

---

## 🤝 Contribuindo

1. Fork o repositório
2. Crie uma branch (`git checkout -b feature/minha-feature`)
3. Commit suas mudanças (`git commit -am 'feat: adiciona minha feature'`)
4. Push para a branch (`git push origin feature/minha-feature`)
5. Abra um Pull Request
