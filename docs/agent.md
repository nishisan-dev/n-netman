# nnet-agent — Agente nas VMs

O `nnet-agent` roda **dentro da VM**. Ele aplica o endereço estático declarado para cada interface e instala as rotas que o controller anuncia no segmento, para que a tabela de roteamento da VM não precise ser mantida à mão conforme o overlay muda.

![Deployment do agente](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/agent_deployment.puml)

## Por que um binário separado

O `nnet-agent` é um binário próprio, não um modo do `nnetd`. Uma VM não tem por que carregar libvirt, VXLAN ou um servidor gRPC: o trabalho do agente é escutar um socket e programar rotas. O binário não contém o runtime gRPC.

Isso também vale para o empacotamento: o `.deb` `n-netman` (controller) e o `n-netman-agent` são independentes. Instalar o controller não coloca um daemon de VM em cada hypervisor.

## Instalação

```bash
sudo dpkg -i n-netman-agent_<versão>_linux_amd64.deb
sudo cp /etc/n-netman/agent.yaml.example /etc/n-netman/agent.yaml
sudo nano /etc/n-netman/agent.yaml
```

Copie a chave compartilhada do segmento (a mesma do controller, ver [inject.md](inject.md#gerando-a-chave)):

```bash
sudo install -m 700 -d /etc/n-netman/psk
sudo install -m 600 /caminho/para/inject.key /etc/n-netman/psk/inject.key
```

Valide antes de iniciar:

```bash
sudo nnet-agent doctor
sudo systemctl enable --now n-netman-agent
```

## Configuração

O agente tem esquema próprio, separado do controller: ele não tem overlays, peers, libvirt nem PKI, e apontá-lo por engano para um arquivo do controller falha em vez de meio funcionar (`version` é fixado em 1).

```yaml
version: 1

agent:
  id: "vm-app-01"

# Precisa bater com o routing.inject do controller.
inject:
  group_base: "239.8.0.0"   # default
  port: 4790                # default

interfaces:
  - name: "ens3"
    address: "10.100.0.50/24"   # fase 1: estático, aplicado pelo agente
    vni: 100                    # deriva o grupo: 239.8.0.100
    # group: "239.8.0.100"      # alternativa a vni, para fixar o grupo
    expect_tags: ["it"]
    psk_ref: "file:/etc/n-netman/psk/inject.key"
    # key_ids: ["k1"]           # opcional: restringe as chaves aceitas

    install:
      table: 0                        # 0 = tabela main
      metric: 100
      accept_default_gateway: false   # opt-in

    import:
      accept_all: false
      allow: ["172.16.0.0/16"]
      deny: []

observability:
  logging: { level: "info", format: "json" }
  metrics: { enabled: true, listen: { address: "127.0.0.1", port: 9111 } }
  healthcheck: { enabled: true, listen: { address: "127.0.0.1", port: 9112 } }
```

### `expect_tags`

Protege contra uma NIC cabeada no segmento errado: o anúncio é aceito somente quando o segmento carrega **todas** as tags listadas. Um anúncio legítimo de outro segmento é descartado como `tag_mismatch`.

Deixar vazio aceita qualquer segmento alcançável naquela NIC, e o agente emite um `warn` no boot avisando disso.

### Tabela de roteamento

`table: 0` (ou omitido) significa a tabela **main**, que é o que uma VM quer: o tráfego normal usa as rotas sem precisar de nenhuma `ip rule`. Uma tabela dedicada é aceita, mas exigiria regras de política que o agente não gerencia.

As rotas são instaladas com o protocolo **98**, então parar o agente retira exatamente o que ele instalou e nada mais:

```bash
$ ip route show
default via 10.0.2.2 dev enp0s3 proto dhcp
10.100.0.0/24 dev ens3 proto kernel scope link src 10.100.0.50
172.16.10.0/24 via 10.100.0.1 dev ens3 proto 98 metric 100
172.16.20.0/24 via 10.100.0.1 dev ens3 proto 98 metric 100
```

### Default gateway

`accept_default_gateway` é **opt-in**. Desligado, um default anunciado é ignorado, para que um anúncio não assuma silenciosamente a rota default que o netplan configurou.

Esse flag governa a rota default sozinho: ele **não** passa pela `allow`. Uma política que lista só os prefixos específicos que quer descartaria o gateway em silêncio e faria o flag parecer quebrado. A `allow`/`deny` continua governando todos os prefixos específicos.

### Política de import

Mesma semântica do controller ([routing.md](routing.md)), avaliada pelo mesmo código:

- `deny` primeiro, casando por **sobreposição** em qualquer direção
- `accept_all` admite o restante
- `allow` casa por **contenção** — a rota precisa ser subconjunto do prefixo permitido
- sem `accept_all` e sem `allow` correspondente: **nega** (default seguro)

Isso limita o estrago de um controller comprometido: ele não consegue empurrar prefixo fora da allow-list da VM.

## Ordem no boot

A unit roda depois de `network-online.target`, porque o agente **acrescenta** ao que o netplan configurou em vez de substituí-lo. Se a NIC ainda não estiver pronta, o agente espera e tenta de novo em vez de morrer — perder essa corrida no boot não pode ser fatal.

A unit é mais confinada que a do controller: o agente nunca escreve em `/etc/systemd` nem chama `systemctl`. Roda como root para ler a PSK protegida, com capabilities limitadas a `CAP_NET_ADMIN` e `CAP_NET_RAW` sob `ProtectSystem=strict`. O diagnóstico `doctor` verifica se `CAP_NET_ADMIN` está efetiva; o UID root, sozinho, não garante essa permissão.

## Múltiplos controllers

O segmento L2 atravessa todos os hosts do overlay via VXLAN, então o anúncio multicast de um host **também chega às VMs dos outros hosts**. Isso é correto — é um segmento único — e significa que cada agente ouve vários controllers.

O agente trata isso explicitamente:

- Estado **por `controller_id`**, cada um com seu próprio lease.
- Quando um controller para de anunciar, apenas as rotas dele expiram; os demais seguem carregando as suas.
- O kernel mantém uma rota por destino por tabela, então prefixos concorrentes são resolvidos: **menor métrica vence**, e empate exato é desempatado pelo next-hop, de forma estável — sem ficar alternando entre controllers a cada ciclo.

Balanceamento de carga entre controllers exigiria rotas multipath e não é feito: um controller vivo já basta para alcançar o prefixo, e o outro assume quando o lease do primeiro expira.

## Comandos

```bash
nnet-agent run      # executa em foreground (é o que a unit chama)
nnet-agent status   # o que o agente rodando aprendeu
nnet-agent doctor   # verifica chave, interfaces e privilégios
nnet-agent version
```

### `nnet-agent status`

```
Agent:  vm-app-01 (v0.2.0)
Health: healthy

Interface ens3
  address: 10.100.0.50/24   group: 239.8.0.100:4790   table: 254
  expects tags: [it]
  controllers:
    ID      SEGMENT     VNI  ROUTES  EXPIRES IN
    host-a  vxlan-prod  100  2       27s
    host-b  vxlan-prod  100  2       25s
  routes:
    PREFIX          VIA          METRIC
    172.16.10.0/24  10.100.0.1   100
    172.16.20.0/24  10.100.0.2   100
```

### `nnet-agent doctor`

Verifica o que faz um agente ficar silenciosamente inútil: chave ilegível ou com permissão frouxa, interface ausente, privilégios insuficientes. Cada um desses produz um agente que inicia limpo e depois não faz nada.

## Endpoints

| Endpoint | Porta default | Descrição |
|---|---|---|
| `/metrics` | 9111 | Métricas Prometheus |
| `/healthz` | 9112 | Saudável = **toda** interface ainda ouve algum controller |
| `/readyz` | 9112 | Mesmo critério de `/healthz` |
| `/livez` | 9112 | Processo vivo |
| `/status` | 9112 | JSON consumido por `nnet-agent status` |

`/healthz` reflete o estado real: o processo estar de pé não diz nada sobre as rotas estarem frescas.

## Métricas

| Métrica | Tipo | Labels | Descrição |
|---|---|---|---|
| `nnetman_agent_advertisements_received_total` | counter | `interface` | Anúncios aceitos |
| `nnetman_agent_advertisements_rejected_total` | counter | `interface`, `reason` | Anúncios descartados |
| `nnetman_agent_routes_installed` | gauge | `interface` | Rotas programadas |
| `nnetman_agent_controllers_active` | gauge | `interface` | Controllers com lease vivo |
| `nnetman_agent_last_advertisement_timestamp_seconds` | gauge | `interface` | Último anúncio aceito |
| `nnetman_agent_route_sync_errors_total` | counter | `interface` | Falhas ao programar o kernel |

Valores de `reason`: `bad_mac`, `unknown_key`, `bad_version`, `replay`, `stale_timestamp`, `malformed`, `oversized`, `tag_mismatch`, `policy`.

O label `reason` é o que distingue um canal em silêncio de um canal sendo ativamente rejeitado — sem ele, uma chave errada e um relógio dessincronizado parecem a mesma coisa.

## Troubleshooting

| Sintoma | Causa provável | Verificação |
|---|---|---|
| Nenhuma rota, nenhum anúncio recebido | interface errada, ou o controller não anuncia | `nnet inject status` no host |
| `rejected{reason="bad_mac"}` | PSK divergente | comparar a chave nos dois lados |
| `rejected{reason="stale_timestamp"}` | relógios fora de sincronia | `timedatectl status` |
| `rejected{reason="tag_mismatch"}` | NIC no segmento errado, ou `expect_tags` incorreto | `nnet-agent status` |
| `rejected{reason="policy"}` | prefixo fora da `allow` | revisar `import.allow` |
| Rotas somem periodicamente | controller parou de anunciar, lease expirou | `nnet-agent status`, coluna `EXPIRES IN` |
| Anúncios param após alguns minutos | snooping sem querier na bridge do host | ver [inject.md](inject.md#igmp-snooping) |
| Agente inicia e não faz nada | interface ausente ou sem privilégio | `nnet-agent doctor` |

## Teste no lab

O `scripts/lab-agent-test.sh` roda o agente num network namespace conectado à `br-prod`, tomando o mesmo caminho de dados de uma VM sem virtualização aninhada:

```bash
sudo ./scripts/lab-agent-test.sh vm1 10.100.0.50/24
# ...
sudo ./scripts/lab-agent-test.sh --cleanup vm1
```

## Ver também

- [inject.md](inject.md) — o lado do controller
- [configuration.md](configuration.md) — esquema do YAML do controller
