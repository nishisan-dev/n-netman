# Roteamento

Este documento descreve o modelo de troca de rotas do n-netman.

## Modelo de Roteamento

O n-netman implementa um protocolo proprietário de troca de rotas via gRPC (Protocol 99). Cada nó:

1. **Exporta** redes que conhece localmente (configuradas ou descobertas)
2. **Importa** redes anunciadas por peers (filtrando conforme políticas)
3. **Instala** rotas aprendidas em tabelas de roteamento Linux

Diferente de BGP, não há conceito de AS, path attributes ou confederações. O modelo é simples e direto.

## Exportação de Rotas

O nó anuncia rotas para seus peers conforme configuração:

```yaml
routing:
  export:
    export_all: false           # Se true, exporta todas as rotas do sistema
    networks:                   # Redes explícitas a exportar
      - "172.16.10.0/24"
      - "2001:db8:10::/64"
    include_connected: true     # Rotas diretamente conectadas
    include_netplan_static: true  # Rotas estáticas do netplan (futuro)
    metric: 100                 # Métrica aplicada às rotas anunciadas
```

### export_all vs Redes Explícitas

| Configuração | Comportamento |
|--------------|---------------|
| `export_all: true` | Exporta todas as rotas da tabela main do kernel |
| `export_all: false` + `networks: [...]` | Exporta apenas os prefixos listados |
| `include_connected: true` | Adiciona rotas conectadas (interfaces com IP) |
| `include_netplan_static: true` | Adiciona rotas estáticas lidas do netplan |

**Recomendação:** Use `export_all: false` com lista explícita de `networks`. Isso evita vazamento acidental de rotas internas.

### Métricas

A métrica (`metric: 100`) é enviada junto com cada rota:

```
Rota anunciada: 172.16.10.0/24 via 10.100.0.1 metric=100
```

Quando um peer recebe rotas de múltiplas fontes para o mesmo prefixo, a métrica pode ser usada para preferência (menor = melhor).

## Importação de Rotas

O nó filtra e instala rotas recebidas de peers:

```yaml
routing:
  import:
    accept_all: false          # Se true, aceita qualquer rota
    allow:                     # Prefixos permitidos (whitelist)
      - "172.16.0.0/16"
      - "2001:db8::/32"
    deny:                      # Prefixos bloqueados (blacklist)
      - "0.0.0.0/0"            # Bloqueia default route
      - "::/0"
    install:
      table: 100               # Tabela de roteamento alvo
      replace_existing: true   # Sobrescreve rotas existentes
      flush_on_peer_down: true # Remove rotas quando peer cai
      route_lease_seconds: 30  # TTL das rotas
      lookup_rules:
        enabled: true          # Cria ip rules para PBR
```

### Ordem de Avaliação

1. Rota chega de um peer
2. Se `accept_all: true`, aceita imediatamente
3. Senão, verifica se prefixo está em `deny` → rejeita
4. Verifica se prefixo está em `allow` → aceita
5. Se não está em nenhum → rejeita (default deny)

### Leases e Remoção de Rotas

Cada rota tem um tempo de vida (`route_lease_seconds`):

```
Rota recebida em T=0 com lease=30s
├── T=0: Rota instalada no kernel
├── T=15: Peer reenvia (refresh) → lease renovado
├── T=30: Se não renovada → rota removida
└── T=30: Se flush_on_peer_down=true e peer cai → rota removida imediatamente
```

O reconciler periodicamente executa `ExpireStale()` para remover rotas expiradas:
- Da RouteTable interna
- Do kernel (`ip route del ... table X`)

### Tabelas de Roteamento

Rotas são instaladas em tabelas customizadas para isolamento:

```bash
# Rotas do overlay VNI 100
ip route show table 100
172.16.20.0/24 via 10.100.0.2 dev br-prod proto openr metric 100
172.16.30.0/24 via 10.100.0.3 dev br-prod proto openr metric 100
```

O `proto openr` identifica rotas instaladas pelo n-netman (facilita cleanup e debugging).

### Policy-Based Routing (lookup_rules)

Quando `lookup_rules.enabled: true`, o n-netman cria regras `ip rule` para garantir que tráfego da bridge consulte a tabela correta:

```bash
# Regras criadas automaticamente
ip rule show
100:    from all iif br-prod lookup 100
101:    from all oif br-prod lookup 100
```

Isso é essencial para multi-overlay: cada overlay tem sua própria tabela e suas próprias regras.

**Sem lookup_rules:** O kernel consulta apenas a `table main`, ignorando rotas em tabelas customizadas.

**Com lookup_rules:** Tráfego entrando/saindo pela bridge do overlay consulta a tabela específica.

## Conceito de Transit

**Transit** define se um nó pode encaminhar tráfego que não é destinado a ele mesmo.

```yaml
topology:
  transit: "deny"              # Padrão: não roteia tráfego de terceiros
  transit_policy:
    allowed_transit_peers: []
    denied_transit_peers:
      - "host-untrusted"
```

### Por que deny é o Padrão?

1. **Segurança:** Um nó comprometido não pode ser usado para acessar outros segmentos
2. **Previsibilidade:** Tráfego segue caminhos explícitos e auditáveis
3. **Performance:** Evita tráfego desnecessário passando por nós intermediários

### Cenário: Transit Allow

```
Host-A (172.16.10.0/24)
    │
    │ transit: allow
    │
Host-B (172.16.20.0/24)  ◄── Hub com transit habilitado
    │
    │ transit: allow
    │
Host-C (172.16.30.0/24)
```

Se Host-B tem `transit: allow`:
- Host-A pode alcançar Host-C via Host-B
- Host-B precisa ter rotas para ambos os destinos
- Tráfego A→C passa por B (overlay hop)

### Cenário: Transit Deny (padrão)

```
Host-A ──────────────── Host-B ──────────────── Host-C
   │                                               │
   │           transit: deny (em B)                │
   │                                               │
   └───────────────── Sem caminho ────────────────┘
```

Se Host-B tem `transit: deny`:
- Host-A **não** consegue alcançar Host-C via Host-B
- A→B funciona, B→C funciona, mas A→C falha
- Para A→C, é necessário peering direto entre A e C

## Fluxo de Troca de Rotas

```
1. Conexão Inicial (ExchangeState)
   ├── Peer-A conecta a Peer-B
   ├── Peer-A envia suas rotas exportáveis
   ├── Peer-B responde com suas rotas exportáveis
   └── Ambos instalam rotas recebidas

2. Durante Operação (AnnounceRoutes)
   ├── Nova rede configurada em Peer-A
   ├── Peer-A envia AnnounceRoutes para todos peers
   └── Peers instalam a nova rota

3. Retirada (WithdrawRoutes)
   ├── Rede removida do config em Peer-A
   ├── Peer-A envia WithdrawRoutes para todos peers
   └── Peers removem a rota

4. Expiração
   ├── Peer-A fica indisponível
   ├── Keepalive timeout em Peer-B
   ├── Se flush_on_peer_down: true
   │   └── Rotas de Peer-A removidas imediatamente
   └── Senão: rotas expiram após route_lease_seconds
```

## Exemplo Completo

Dados três hosts:

| Host | Overlay IP | Redes Locais |
|------|------------|--------------|
| host-a | 10.100.0.1 | 172.16.10.0/24 |
| host-b | 10.100.0.2 | 172.16.20.0/24 |
| host-c | 10.100.0.3 | 172.16.30.0/24 |

Após convergência, a tabela de rotas em **host-a** será:

```bash
$ ip route show table 100
# Rota local (não instalada via control-plane)
172.16.10.0/24 dev br-prod proto kernel scope link src 10.100.0.1

# Rotas aprendidas de peers
172.16.20.0/24 via 10.100.0.2 dev br-prod proto openr metric 100
172.16.30.0/24 via 10.100.0.3 dev br-prod proto openr metric 100
```

O next-hop (`via 10.100.0.2`) é o IP da bridge do peer — garantindo que o tráfego entre no overlay correto.
