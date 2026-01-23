# n-netman — Visão Geral

## O que é o n-netman

O **n-netman** (Nishi Network Manager) é um agente leve para criação e gerenciamento de **overlays VXLAN L3/L2** entre hosts Linux. Ele elimina a necessidade de soluções complexas como Open vSwitch (OVS), oferecendo um control-plane minimalista baseado em gRPC.

O agente permite que redes virtuais distribuídas sejam criadas de forma **declarativa**, a partir de um único arquivo YAML que define:

- Túneis VXLAN e bridges Linux
- Peers (outros hosts no overlay)
- Políticas de exportação/importação de rotas
- Regras de topologia e trânsito

## Problema que Resolve

Em ambientes com múltiplos hosts Linux — seja para virtualização com KVM/libvirt, containers, ou mesmo conectividade pura entre bare-metal — surge a necessidade de criar redes L2/L3 que atravessem o underlay físico.

As alternativas tradicionais apresentam desafios:

| Solução | Problema |
|---------|----------|
| **OVS + OpenFlow** | Complexidade operacional alta, debugging difícil |
| **EVPN/VXLAN com FRR** | Curva de aprendizado íngreme, configuração extensa |
| **Kubernetes CNI (Calico, Cilium)** | Dependência de orquestrador, overhead desnecessário fora de K8s |
| **VPNs tradicionais (WireGuard, IPsec)** | Foco em L3 ponto-a-ponto, não em L2 fabric |

O n-netman preenche esse gap: oferece **overlays VXLAN com troca de rotas distribuída**, usando apenas ferramentas Linux nativas (bridges, netlink, routing tables) e um control-plane proprietário simples.

## O que ele NÃO se propõe a ser

Para evitar falsas expectativas, é importante entender os limites do projeto:

- **Não é um substituto para SDN completo** — Não há API REST para orquestração dinâmica de topologias
- **Não é um BGP speaker** — A troca de rotas usa protocolo gRPC próprio (Protocol 99), não BGP/EVPN
- **Não faz NAT ou firewall** — Utilize iptables/nftables separadamente
- **Não gerencia containers Docker/Podman** — O foco é em bridges Linux e opcionalmente VMs libvirt
- **Não é multi-tenant** — Cada instância do daemon gerencia um conjunto de overlays para um único tenant

## Princípios de Design

### 1. Simplicidade

- Arquivo de configuração YAML único e legível
- Sem banco de dados, sem cluster etcd, sem dependências externas
- CLI direta: `nnet apply`, `nnet status`, `nnet routes`

### 2. Idempotência

- O reconciler executa em loop contínuo, garantindo que o estado real converge para o estado desejado
- Aplicar a mesma configuração múltiplas vezes produz o mesmo resultado
- Reiniciar o daemon não quebra conectividade — ele reconstrói o estado

### 3. Linux-Native

- Usa netlink para criar/gerenciar VXLAN, bridges, FDB e rotas
- Rotas são instaladas em tabelas customizadas (`ip route show table 100`)
- Policy-Based Routing via `ip rule` para isolamento de overlays
- Logs estruturados com `slog`, métricas Prometheus, healthchecks HTTP

### 4. Modularidade

- **KVM/libvirt é opcional** — O agente funciona como puro networking agent em hosts bare-metal ou containers
- **Netplan é somente-leitura** — Infere configuração underlay, nunca modifica
- **Multi-overlay** — Suporte a múltiplos VNIs com routing independente (config v2)

### 5. Comportamento Explícito

- **Transit: deny por padrão** — Um nó não roteia tráfego de terceiros a menos que explicitamente permitido
- **Rotas expiram** — Lease seconds configurable, com flush automático quando peer cai
- **Sem mágica** — Se algo deve acontecer, está declarado no YAML

## Quando Usar o n-netman

✅ **Bom fit:**
- Labs de virtualização com 3-20 hosts KVM
- Ambientes bare-metal que precisam de conectividade L2 extendida
- POCs de redes overlay sem overhead de SDN
- Integração com redes existentes via tabelas de roteamento separadas

❌ **Não recomendado:**
- Ambientes com milhares de hosts (escala enterprise SDN)
- Redes que exigem BGP/EVPN compliance
- Clusters Kubernetes (use um CNI adequado)
- Cenários que exigem multi-tenancy forte
