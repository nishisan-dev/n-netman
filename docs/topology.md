# Topologia

Este documento descreve os modos de topologia suportados pelo n-netman.

## Modos de Topologia

O n-netman suporta três modos de topologia que definem como os nós se conectam:

```yaml
topology:
  mode: "direct-preferred"   # direct-preferred | full-mesh | hub-spoke
  relay_fallback: true
  transit: "deny"
```

### direct-preferred (Padrão)

Cada nó tenta conexão direta com cada peer configurado. Se a conexão direta falhar e `relay_fallback: true`, pode usar outro nó como relay.

```
      Host-A ◄──────────────────► Host-B
         │                           │
         │                           │
         └─────────► Host-C ◄────────┘
                  (conexões diretas)
```

**Características:**
- Peers são explicitamente configurados
- Conexão direta é sempre tentada primeiro
- Fallback via relay é opcional (`relay_fallback: true`)
- Menor latência quando conexão direta funciona

**Quando usar:**
- Topologias onde a maioria dos nós pode se comunicar diretamente
- Ambientes com conectividade underlay parcial
- POCs e labs pequenos

### full-mesh

Todos os nós se conectam a todos os outros nós.

```
      Host-A ◄────────────────────► Host-B
         ▲                             ▲
         │                             │
         │    ┌───────────────────┐    │
         │    │                   │    │
         └────┤     Host-C        ├────┘
              │                   │
              └───────────────────┘
            (todos conectam a todos)
```

**Características:**
- N×(N-1)/2 conexões para N nós
- Caminho direto para qualquer destino
- Maior consumo de recursos (conexões, FDB entries)
- Não escala bem além de ~20 nós

**Quando usar:**
- Clusters pequenos com alta necessidade de convergência
- Ambientes onde todos os nós devem conhecer todos os prefixos

### hub-spoke

Um ou mais nós centrais (hubs) conectam-se a nós periféricos (spokes). Spokes não se conectam entre si.

```
                    ┌─────────┐
                    │  Hub-A  │
                    └────┬────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
    ┌────▼────┐    ┌─────▼────┐    ┌─────▼────┐
    │ Spoke-1 │    │ Spoke-2  │    │ Spoke-3  │
    └─────────┘    └──────────┘    └──────────┘
```

**Características:**
- Spokes só conhecem rotas do hub
- Tráfego spoke-to-spoke passa pelo hub (requer `transit: allow` no hub)
- Simples de gerenciar
- Hub é ponto único de falha

**Quando usar:**
- Topologias centralizadas (datacenter central + filiais)
- Quando controle central de roteamento é desejado
- Ambientes com conectividade limitada entre spokes

## Relay Fallback

O conceito de relay permite que um nó intermediário encaminhe tráfego quando a conexão direta não está disponível.

```yaml
topology:
  mode: "direct-preferred"
  relay_fallback: true
```

### Cenário de Relay

```
Host-A ───X─── Host-C    (conexão direta falhou)
   │              ▲
   │              │
   └──► Host-B ───┘      (Host-B como relay)
       transit: allow
```

Para relay funcionar:
1. Host-B deve ter `transit: allow`
2. Host-A deve ter rota para Host-C via Host-B
3. Host-B deve ter rota para Host-C

**Importante:** Relay é um mecanismo de fallback, não o padrão. O n-netman sempre tenta conexão direta primeiro.

## Implicações de Permitir Trânsito

Permitir `transit: allow` tem consequências importantes:

### Vantagens

- **Resiliência:** Caminhos alternativos quando links diretos falham
- **Simplificação:** Menos conexões diretas necessárias
- **Alcançabilidade:** Spokes podem alcançar outros spokes via hub

### Desvantagens

- **Latência:** Tráfego passa por hop intermediário
- **Carga:** Nó de trânsito processa tráfego de terceiros
- **Segurança:** Nó de trânsito pode inspecionar/modificar tráfego
- **Debugging:** Caminhos menos previsíveis

### Controle Fino com transit_policy

```yaml
topology:
  transit: "allow"
  transit_policy:
    allowed_transit_peers:
      - "hub-trusted"       # Apenas este nó pode fazer transit
    denied_transit_peers:
      - "spoke-untrusted"   # Este nó nunca faz transit
```

## Exemplos de Fluxo

### Exemplo 1: Direct-Preferred com Sucesso

```
Host-A (172.16.10.0/24) → Host-B (172.16.20.0/24)

1. Host-A consulta tabela 100:
   172.16.20.0/24 via 10.100.0.2 dev br-prod

2. Pacote encapsulado em VXLAN
   Outer: Host-A underlay → Host-B underlay

3. Host-B desencapsula e roteia para destino
```

Caminho: A → B (1 hop overlay)

### Exemplo 2: Hub-Spoke com Transit

```
Spoke-1 (172.16.10.0/24) → Spoke-2 (172.16.20.0/24)
Hub tem transit: allow

1. Spoke-1 consulta tabela:
   172.16.20.0/24 via 10.200.0.1 dev br-overlay  (Hub é gateway)

2. Pacote vai para Hub via VXLAN

3. Hub recebe, consulta sua tabela:
   172.16.20.0/24 via 10.200.0.3 dev br-overlay  (Spoke-2)

4. Hub encapsula novamente e envia para Spoke-2

5. Spoke-2 desencapsula e entrega
```

Caminho: Spoke-1 → Hub → Spoke-2 (2 hops overlay)

### Exemplo 3: Transit Deny Bloqueia

```
Host-A → Host-C (via Host-B)
Host-B tem transit: deny

1. Host-A tenta enviar para Host-C
   Rota: 172.16.30.0/24 via 10.100.0.2 (Host-B)

2. Pacote chega em Host-B

3. Host-B verifica: destino não é local
   transit: deny → pacote descartado

4. Host-A não consegue alcançar Host-C via Host-B
```

O pacote é dropado silenciosamente. Não há ICMP unreachable (o VXLAN é transparente ao kernel quanto a policy de transit — isso é controlado pela não-existência de rotas).

## Considerações de Design

### Full-Mesh para Pequenos Clusters

Para clusters de até 10 nós, full-mesh é geralmente a melhor escolha:
- Convergência rápida
- Sem dependência de nós intermediários
- Overhead de conexões é aceitável (45 conexões para 10 nós)

### Hub-Spoke para Topologias Hierárquicas

Para ambientes com datacenter central e sites remotos:
- Hub no datacenter com `transit: allow`
- Spokes nos sites remotos
- Spokes não precisam conhecer uns aos outros diretamente

### Direct-Preferred para Flexibilidade

Quando você quer controle fino sobre quais nós se conectam:
- Configure peers explicitamente
- Use `relay_fallback: true` para resiliência
- Habilite `transit: allow` apenas em nós confiáveis
