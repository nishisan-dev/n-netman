# Injeção de Rotas nas VMs

Este documento descreve o canal de injeção: como o controller anuncia rotas para as VMs conectadas a uma bridge do overlay.

![Fluxo de injeção](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/inject_flow.puml)

## O Problema

O overlay resolve a conectividade **entre baremetais**. Do ponto de vista da VM, porém, a bridge é apenas um fio: a VM não sabe nada sobre a topologia e precisa das rotas configuradas à mão.

Isso não escala. Cada rede nova anunciada no overlay exige tocar em N VMs, e o estado dentro da VM diverge silenciosamente do que o controller sabe.

O canal de injeção inverte isso: o controller **publica** o que sabe num grupo multicast na própria bridge, e o [nnet-agent](agent.md) dentro da VM consome e programa as rotas.

## Modelo

O acoplamento é por **tags de texto livre** na bridge. O controller decide o que publicar por segmento sem saber quais VMs existem.

```
bridge br-prod  tags: [it, external, public]
                          │
                          │ regra casa quando TODAS as suas match_tags
                          │ estão presentes na bridge (AND)
                          ▼
routing.inject.rules[]  →  união das regras que casaram
                          →  anúncio assinado no grupo do segmento
```

Comparado à troca de rotas entre peers ([routing.md](routing.md)), o canal de injeção é:

| | Entre peers (gRPC) | Injeção (multicast) |
|---|---|---|
| Transporte | TCP unicast :9898 | UDP multicast, TTL 1 |
| Descoberta | peers estáticos no YAML | nenhuma — o controller não sabe quais VMs existem |
| Identidade | mTLS, CN do certificado | HMAC-SHA256 com PSK por segmento |
| Direção | bidirecional | unidirecional (controller → VM) |
| Protocolo no kernel | 99 | 98 |

## Configuração

O bloco vive na **raiz** de `routing:`, e não por overlay, para que uma política escrita uma vez valha para toda bridge que carregue a tag.

```yaml
overlays:
  - vni: 100
    name: "vxlan-prod"
    bridge:
      name: "br-prod"
      ipv4: "10.100.0.1/24"     # obrigatório: é o next-hop anunciado
      tags: ["it", "external"]  # rótulos livres do segmento
    routing:
      export:
        networks: ["172.16.10.0/24"]
        tags: ["it"]            # communities anexadas às rotas exportadas

routing:
  inject:
    enabled: true
    group_base: "239.8.0.0"     # default
    port: 4790                  # default
    interval_seconds: 10        # de quanto em quanto tempo republica
    lease_seconds: 30           # por quanto tempo o agente mantém as rotas
    psk_ref: "file:/etc/n-netman/psk/inject.key"
    key_id: "k1"                # identifica a chave, para rotação
    rules:
      - match_tags: ["it"]      # AND: a bridge precisa ter todas
        from_rib: true          # anuncia também as rotas aprendidas
        route_tags: ["it"]      # ...filtradas por community; omita para todas
        networks:               # prefixos anunciados literalmente
          - "172.16.10.0/24"
        metric: 100
        # next_hop: "10.100.0.254"   # opcional; default é o bridge.ipv4
      - match_tags: ["it", "external"]
        default_gateway: "10.100.0.1"
```

### Semântica do match

Uma regra casa quando **todas** as suas `match_tags` estão presentes na bridge. O que o segmento recebe é a **união** de todas as regras que casaram.

| Tags da bridge | `match_tags: [it]` | `match_tags: [it, external]` |
|---|---|---|
| `[it]` | casa | não casa |
| `[it, external]` | casa | casa |
| `[it, external, public]` | casa | casa |
| `[external]` | não casa | não casa |

Uma bridge sem tags, ou cujas tags não casam com nenhuma regra, simplesmente não participa do canal.

### Next-hop

O next-hop anunciado é o **IP da bridge** (`bridge.ipv4`), ou seja, o endereço deste host no segmento. Isso vale inclusive para as rotas vindas do RIB: a VM manda o pacote para o host, que é quem atravessa o overlay — não para o peer que originou o prefixo.

Uma bridge selecionada por uma regra sem `bridge.ipv4` e sem `next_hop` é **erro de configuração**: as rotas anunciadas não teriam para onde apontar.

### Grupo multicast

O grupo é derivado do VNI: os 16 bits baixos do VNI entram nos dois últimos octetos de `group_base`.

```
group_base 239.8.0.0 + VNI 100  →  239.8.0.100
group_base 239.8.0.0 + VNI 200  →  239.8.0.200
group_base 239.8.0.0 + VNI 300  →  239.8.1.44
```

Por isso `group_base` precisa terminar em `0.0` — assim a derivação nunca sai da faixa multicast. A faixa `239.8.x.x` é distinta da convenção `239.1.1.<vni>` usada para BUM do VXLAN, então as duas nunca colidem.

## Segurança

Multicast não comporta mTLS: não há sessão nem identidade de par. A autenticação é por **chave compartilhada por segmento**.

- **HMAC-SHA256** sobre um preimage com separação de domínio (`nnet-inject-v1`), com o `key_id` prefixado por comprimento — assim um `key_id` e um payload não podem ser recortados em outro ponto para forjar o mesmo preimage.
- O MAC é verificado **antes** de fazer parse do payload e **antes** de tocar o estado de anti-replay. Um emissor não autenticado não consegue nem exercitar o decoder nem envenenar o guard.
- **Anti-replay**: janela de 30s no timestamp mais sequência monotônica por controller. Um controller reiniciado zera a sequência, então o anúncio também é aceito quando o timestamp saltou uma janela inteira à frente. **Isso depende de relógios sincronizados — mantenha NTP ativo.**
- **TTL 1**: o datagrama não é roteado para fora do segmento L2.
- **Permissão do PSK**: um arquivo com permissão mais frouxa que `0600` é recusado, mesmo rigor que o `internal/pki` aplica às chaves privadas.

### Risco residual

O PSK é compartilhado pelo segmento. Uma VM comprometida com o PSK pode forjar anúncios para as outras VMs daquele segmento. Duas defesas atenuam o impacto:

1. O agente aplica sua própria política `allow`/`deny` sobre os prefixos recebidos. Um anúncio forjado não consegue empurrar prefixo fora da allow-list da VM.
2. `expect_tags` no agente descarta anúncios de segmentos que não sejam o esperado.

A mitigação definitiva é assinatura assimétrica com a PKI já existente no projeto. O campo `key_id` já está no formato de fio para viabilizar rotação e transição.

### Gerando a chave

```bash
sudo mkdir -p /etc/n-netman/psk
sudo sh -c 'head -c 32 /dev/urandom | base64 > /etc/n-netman/psk/inject.key'
sudo chmod 700 /etc/n-netman/psk
sudo chmod 600 /etc/n-netman/psk/inject.key
```

A mesma chave precisa estar no controller e em cada VM do segmento. Espaços em branco nas pontas são removidos dos dois lados, então a quebra de linha do `>` não causa divergência.

## IGMP snooping

Com `multicast_snooping=1` (default do Linux) e **sem querier** no segmento, o kernel para de encaminhar o grupo depois que as memberships aprendidas expiram. O canal fica em silêncio sem nada aparentar quebrado: a bridge está de pé e o publisher reporta sucesso.

O reconciler resolve isso automaticamente, ligando `multicast_querier=1` nas bridges que o inject seleciona. Se a escrita em sysfs falhar, o daemon loga um `warn` com o contorno manual:

```bash
echo 1 | sudo tee /sys/class/net/br-prod/bridge/multicast_querier
# ou, alternativamente
echo 0 | sudo tee /sys/class/net/br-prod/bridge/multicast_snooping
```

## Limite de tamanho

Um anúncio precisa caber em um único datagrama (1400 bytes, abaixo do MTU 1450 do overlay). Se o conjunto de rotas não couber, o publisher **falha alto**: não publica nada naquele ciclo, loga `error` e incrementa `inject_publish_errors_total{reason="seal"}`.

Isso é deliberado. Truncar em silêncio deixaria os agentes convergindo para um conjunto plausível porém incompleto — pior que não convergir. Na prática cabem algo em torno de 60 a 80 rotas; acima disso, restrinja com `route_tags` ou `networks`.

## Verificação

```bash
# O que cada bridge está anunciando (funciona com o daemon parado também)
nnet inject status
```

```
Inject channel: group base 239.8.0.0, port 4790, every 10s, lease 30s

BRIDGE   VNI  TAGS           GROUP             RULES MATCHED
br-prod  100  [it external]  239.8.0.100:4790  2
br-mgmt  200  [mgmt]         239.8.0.200:4790  0
```

`RULES MATCHED` igual a zero é o diagnóstico mais útil: a bridge está no canal, mas nenhuma regra a seleciona, então nada será anunciado.

```bash
# Ver os datagramas na bridge
sudo tcpdump -i br-prod -n udp port 4790

# Confirmar que o host é o querier do segmento
cat /sys/class/net/br-prod/bridge/multicast_querier
```

## Troubleshooting

| Sintoma | Causa provável | Verificação |
|---|---|---|
| `RULES MATCHED` = 0 | tags da bridge não casam com nenhuma `match_tags` | `nnet inject status` |
| Daemon não sobe, erro de psk | arquivo ausente, ou permissão mais frouxa que 0600 | `ls -l /etc/n-netman/psk/inject.key` |
| Erro de config sobre next-hop | bridge selecionada sem `bridge.ipv4` nem `next_hop` na regra | revisar o overlay |
| `inject_publish_errors_total{reason="seal"}` subindo | anúncio maior que 1400 bytes | reduzir via `route_tags`/`networks` |
| `inject_publish_errors_total{reason="send"}` subindo | bridge inexistente ou recém-recriada | `ip link show br-prod` |
| Anúncios param depois de alguns minutos | snooping sem querier no segmento | `cat /sys/class/net/br-prod/bridge/multicast_querier` |
| Agente rejeita tudo com `bad_mac` | PSK divergente entre controller e VM | comparar os arquivos nos dois lados |
| Agente rejeita tudo com `stale_timestamp` | relógios fora de sincronia | `timedatectl status` nos dois lados |
| `route_tags` não casa nada | as rotas exportadas não declaram `export.tags` | ver `routing.export.tags` |

## Métricas

| Métrica | Tipo | Labels | Descrição |
|---|---|---|---|
| `nnetman_inject_advertisements_sent_total` | counter | `vni` | Anúncios publicados |
| `nnetman_inject_publish_errors_total` | counter | `vni`, `reason` | Ciclos que não publicaram (`build`, `seal`, `send`) |
| `nnetman_inject_routes_advertised` | gauge | `vni` | Rotas no último anúncio |
| `nnetman_inject_last_advertisement_timestamp_seconds` | gauge | `vni` | Momento do último anúncio |

## Ver também

- [agent.md](agent.md) — o lado da VM
- [routing.md](routing.md) — troca de rotas entre peers
- [configuration.md](configuration.md) — esquema completo do YAML
