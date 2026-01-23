# Modelo de Rede

Este documento descreve como o n-netman utiliza VXLAN, bridges Linux e roteamento para formar o overlay.

## VXLAN como L2 Carrier

O n-netman usa VXLAN (RFC 7348) como tecnologia de tunneling para transportar frames Ethernet sobre a rede IP underlay.

### Encapsulamento

```
Host A (10.100.0.1/24)                      Host B (10.100.0.2/24)
      │                                           │
      │ [Ethernet Frame: src=aa:bb, dst=cc:dd]    │
      │                                           │
      ▼                                           ▼
┌─────────────────┐                       ┌─────────────────┐
│  br-prod        │                       │  br-prod        │
│  (bridge)       │                       │  (bridge)       │
└────────┬────────┘                       └────────┬────────┘
         │                                         │
         ▼                                         ▼
┌─────────────────┐                       ┌─────────────────┐
│  vxlan100       │                       │  vxlan100       │
│  VNI=100        │                       │  VNI=100        │
└────────┬────────┘                       └────────┬────────┘
         │                                         │
         ▼ Encapsulamento VXLAN                    ▼
┌───────────────────────────────────────────────────────────┐
│  Outer IP: 192.168.56.11 → 192.168.56.12                  │
│  Outer UDP: sport=random, dport=4789                      │
│  VXLAN Header: VNI=100                                    │
│  Inner Ethernet: aa:bb → cc:dd                            │
│  Inner Payload: [dados originais]                         │
└───────────────────────────────────────────────────────────┘
```

O VXLAN permite que hosts em diferentes redes IP (underlay) participem do mesmo domínio L2 (overlay). O **VNI (VXLAN Network Identifier)** de 24 bits identifica cada overlay — suportando até ~16 milhões de redes isoladas.

### Propriedades do Túnel VXLAN

| Propriedade | Valor Padrão | Descrição |
|-------------|--------------|-----------|
| `vni` | (obrigatório) | Identificador do overlay (1-16777215) |
| `dstport` | 4789 | Porta UDP de destino |
| `mtu` | 1450 | MTU da interface VXLAN (50 bytes menor que underlay) |
| `learning` | true | MAC learning automático |

O MTU de 1450 assume um underlay com MTU 1500. O overhead do VXLAN (50 bytes) inclui:
- Outer Ethernet: 14 bytes
- Outer IP: 20 bytes
- Outer UDP: 8 bytes
- VXLAN Header: 8 bytes

## Uso de Bridges Linux

A bridge Linux atua como um switch L2 em software, conectando múltiplas interfaces no mesmo domínio de broadcast.

### Estrutura Típica

```
                    ┌───────────────────────┐
                    │      br-prod          │
                    │   (Linux Bridge)      │
                    │   IP: 10.100.0.1/24   │◄── Gateway L3 do overlay
                    └─────────┬─────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          │                   │                   │
    ┌─────▼─────┐       ┌─────▼─────┐       ┌─────▼─────┐
    │ vxlan100  │       │  vnet0    │       │  vnet1    │
    │  (VXLAN)  │       │   (VM1)   │       │   (VM2)   │
    └───────────┘       └───────────┘       └───────────┘
```

### Funções da Bridge

1. **Switching L2:** Frames entre VMs locais são comutados diretamente
2. **Gateway L3:** O IP da bridge (ex: `10.100.0.1/24`) serve como default gateway para VMs
3. **Ingresso VXLAN:** Frames do overlay chegam pela interface VXLAN e são entregues às VMs

### Configuração de Bridge

```yaml
bridge:
  name: "br-prod"
  ipv4: "10.100.0.1/24"   # Endereço do gateway para este overlay
  # ipv6: "2001:db8:100::1/64"  # Dual-stack opcional
```

Quando o IP da bridge é configurado, o n-netman:
1. Cria a bridge se não existir
2. Atribui o IP via `ip addr add`
3. Usa esse IP como next-hop quando anuncia rotas aos peers

## Onde Ocorre o L3 (Roteamento)

O VXLAN transporta L2, mas o n-netman também oferece funcionalidade L3 via troca de rotas:

### Domínios

| Domínio | Descrição |
|---------|-----------|
| **Intra-Bridge** | Comunicação L2 pura entre portas da mesma bridge |
| **Inter-Host (mesmo VNI)** | L2 via VXLAN tunnel (switching distribuído) |
| **Inter-Subnet** | L3 via gateway da bridge + rotas instaladas |

### Exemplo de Fluxo L3

```
VM1 (172.16.10.100) em Host-A quer alcançar VM3 (172.16.20.100) em Host-B

1. VM1 envia pacote para seu default gateway (10.100.0.1 = br-prod em Host-A)

2. Host-A consulta tabela de roteamento:
   ip route show table 100
   → 172.16.20.0/24 via 10.100.0.2 dev br-prod

3. Host-A encapsula o pacote em VXLAN e envia para Host-B

4. Host-B recebe, desencapsula, e entrega à br-prod local

5. br-prod em Host-B roteia para VM3 (172.16.20.100)
```

## Como Peers são Conectados

### Registro de Peers

Cada peer é declarado explicitamente no config:

```yaml
overlay:
  peers:
    - id: "host-b"
      endpoint:
        address: "192.168.56.12"  # IP underlay do peer
      health:
        keepalive_interval_ms: 1500
        dead_after_ms: 6000
```

### Estabelecimento de Conexão

```
Host-A                              Host-B
   │                                   │
   │──── gRPC Connect (TLS) ──────────►│
   │                                   │
   │◄─── ExchangeState Request ────────│
   │     (rotas de Host-A)             │
   │                                   │
   │──── ExchangeState Response ──────►│
   │     (rotas de Host-B)             │
   │                                   │
   │◄───── Keepalive Stream ──────────►│
   │        (bidirecional)             │
```

### FDB para BUM Traffic

Para tráfego BUM (Broadcast, Unknown Unicast, Multicast), o modo **head-end-replication** replica frames para todos os peers conhecidos:

```bash
# Entradas FDB criadas pelo reconciler
bridge fdb show dev vxlan100
00:00:00:00:00:00 dev vxlan100 dst 192.168.56.12 self permanent  # Host-B
00:00:00:00:00:00 dev vxlan100 dst 192.168.56.13 self permanent  # Host-C
```

O MAC `00:00:00:00:00:00` é especial: indica que tráfego de destino desconhecido deve ser replicado para esses endpoints.

### Modos de BUM

| Modo | Descrição | Uso |
|------|-----------|-----|
| `head-end-replication` | Cada host replica BUM para todos peers via unicast | Padrão, funciona em qualquer underlay |
| `multicast` | BUM é enviado para grupo multicast | Requer underlay com suporte IGMP/PIM |

## O que Acontece Quando um Peer Cai

O n-netman possui mecanismos para detectar e reagir a falhas de peers:

### Detecção

1. **Keepalive Timeout:** Se nenhum keepalive é recebido em `dead_after_ms` (default: 6000ms), o peer é marcado como `unhealthy`
2. **Conexão gRPC Perdida:** Se a conexão gRPC fecha, o peer é marcado como `disconnected`

### Reação

1. **Peer Status:** Atualizado para `unhealthy` ou `disconnected`
2. **Rotas Removidas:** Se `flush_on_peer_down: true`, todas as rotas daquele peer são removidas:
   - Da `RouteTable` interna
   - Do kernel (`ip route del ... table X`)
3. **FDB Mantido:** Entradas FDB não são removidas imediatamente (o peer pode voltar)

### Reconexão

Quando o peer volta:
1. Cliente tenta reconectar (backoff exponencial)
2. `ExchangeState` é trocado novamente
3. Rotas são reinstaladas
4. Status volta para `healthy`

### Comportamento com flush_on_peer_down

```yaml
import:
  install:
    flush_on_peer_down: true   # Remove rotas do peer quando ele cai
    route_lease_seconds: 30     # Rotas expiram após 30s sem refresh
```

Com `flush_on_peer_down: true`:
- Rotas são removidas **imediatamente** quando peer é detectado como down
- Convergência rápida, mas pode causar flapping se rede underlay for instável

Com `flush_on_peer_down: false`:
- Rotas expiram naturalmente pelo `route_lease_seconds`
- Mais resiliente a blips transitórios, mas convergência mais lenta
