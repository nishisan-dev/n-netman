# Plano: canal de injeção multicast + `nnet-agent`

## Contexto

Hoje o n-netman resolve o overlay **entre baremetais**: o `nnetd` cria bridge + VXLAN, troca rotas com os peers via gRPC/mTLS e instala tudo no kernel do host. Do ponto de vista da VM, porém, a bridge é só um fio: a VM não sabe nada sobre a topologia do overlay, e toda rota que ela precisa para alcançar as redes remotas tem de ser configurada à mão dentro dela.

Isso não escala. Cada rede nova anunciada no overlay exige tocar em N VMs, e o estado dentro da VM diverge silenciosamente do que o controller sabe.

A evolução: o controller passa a **publicar** o que sabe num grupo multicast na própria bridge do segmento, e um agente leve dentro da VM **consome** esse anúncio e programa as rotas. A VM continua com IP estático declarado localmente (fase 1), mas o roteamento vira dinâmico e converge sozinho. O acoplamento é por **tags de texto livre** na bridge — o controller decide o que publicar por segmento sem saber nada sobre quais VMs existem.

Resultado esperado: adicionar uma rede ao overlay passa a ser uma mudança em um arquivo no controller, e todas as VMs do segmento convergem em um ciclo de anúncio.

---

## Escopo

### Incluído

| Item | Descrição |
|---|---|
| Tags de segmento | `overlays[].bridge.tags` — lista de texto livre |
| Bloco `routing.inject` | Canal + regras com `match_tags`, na raiz do config do controller |
| Publisher multicast | Novo subsistema no `nnetd`, um socket por bridge com inject ativo |
| Wire format assinado | Protobuf sobre UDP multicast, HMAC-SHA256 com PSK, anti-replay |
| Binário `nnet-agent` | Novo daemon para dentro da VM: IP estático + rotas dinâmicas |
| Empacotamento separado | `.deb` `n-netman-agent`, unit systemd própria, config própria |
| Observabilidade | Métricas Prometheus e `/healthz` nos dois lados |
| Documentação | `docs/inject.md`, `docs/agent.md`, dois diagramas PlantUML |

### Fora do escopo (explícito)

- **IP dinâmico no agente** — fase 1 é só estático, conforme decidido.
- **DNS / search domain / MTU no anúncio** — o payload carrega rotas e default gateway; o resto fica para uma fase futura.
- **Escrita em netplan** — o agente programa netlink direto e não toca em arquivo de sistema.
- **Fragmentação de anúncio** — um anúncio cabe em um datagrama. O campo `generation` já entra no wire format para permitir fragmentação depois sem quebrar o protocolo.
- **Corrigir `scripts/lab-test.sh`** (defasado, usa nomes v1) e o drop-in `libvirt.service` × `libvirtd.service` — bugs pré-existentes, anotados abaixo mas não corrigidos aqui.

---

## Decisões tomadas

| Decisão | Escolha |
|---|---|
| Empacotamento do agente | Binário separado `nnet-agent`, `.deb` próprio |
| Semântica de `inject:` | Regras com `match_tags`, declaradas na **raiz** de `routing:` |
| Match de tags | **AND** — a bridge precisa ter todas as tags da regra; anuncia-se a **união** das regras que casaram |
| Autenticação | HMAC-SHA256 com PSK, reusando o padrão `psk_ref: file:...` já existente |
| Aplicação na VM | netlink direto, rotas com lease, **tabela `main`** |
| Gatilho do publisher | Declarativo por config (`routing.inject.enabled` + bridge com tags) |
| Match no agente | Por interface declarada + `expect_tags` |
| Payload | Rotas (prefixo, next-hop, métrica) + default gateway opcional |
| Default gateway | **Opt-in** por interface no agente |
| Grupo multicast | **Derivado do VNI**: `239.8.<(vni>>8)&0xFF>.<vni&0xFF>`, porta 4790 |

---

## Arquitetura

### Fluxo

```
  ┌─ HOST (baremetal) ────────────────────────────────────────┐
  │                                                            │
  │  nnetd                                                     │
  │   ├── controlplane.RouteTable   (rotas aprendidas dos peers)
  │   ├── inject.Publisher ──┐                                 │
  │   │     lê RouteTable + regras que casam com bridge.tags   │
  │   │     assina (HMAC) e envia a cada interval_seconds      │
  │   ▼                      │                                 │
  │  br-prod  tags:[it,external]  ip 10.100.0.1/24             │
  │   ├── vnet0 (VM local) ◄─┘                                 │
  │   └── vxlan-prod ────────────────► outros hosts do segmento│
  └────────────────────────────────────────────────────────────┘
                │ UDP 239.8.0.100:4790, TTL=1
                ▼
  ┌─ VM ───────────────────────────────────────────────────────┐
  │  nnet-agent                                                │
  │   1. garante 10.100.0.50/24 em ens3        (estático)      │
  │   2. JoinGroup no grupo, só em ens3                        │
  │   3. verifica HMAC → janela de tempo → sequência           │
  │   4. confere expect_tags ⊆ tags do anúncio                 │
  │   5. aplica import policy (allow/deny)                     │
  │   6. RouteManager.Sync(main, desejadas) proto=98           │
  │   7. lease vence sem anúncio → FlushByProtocol             │
  └────────────────────────────────────────────────────────────┘
```

### Consequência importante: múltiplos controllers por segmento

O `vxlan` é porta da bridge, então o anúncio multicast **atravessa o overlay** e chega às VMs de todos os hosts do mesmo segmento. Isso é correto — é um segmento L2 único — mas significa que **cada agente recebe anúncios de vários controllers**.

O agente trata isso explicitamente:

- Estado por `controller_id`: `map[string]*controllerState{lastSeq, lastTs, routes, expiresAt}`.
- **Lease independente** por controller — um controller que cai expira só as rotas dele.
- Conjunto desejado = **união** de todos os controllers vivos, deduplicado por `(prefix, next_hop)`; empate de prefixo com next-hops diferentes resolve por menor métrica, e empate de métrica instala ambos como rotas distintas (next-hops diferentes ⇒ entradas de kernel distintas).

### Gotcha operacional: IGMP snooping na bridge

Com `multicast_snooping=1` (default no Linux) e **sem querier** no segmento, o kernel pode parar de floodar o grupo depois que os grupos aprendidos expiram — o anúncio simplesmente some. Duas saídas, e o plano adota a primeira:

1. Quando inject está ativo para a bridge, o reconciler garante `multicast_querier=1` via sysfs (`/sys/class/net/<br>/bridge/multicast_querier`), tornando o host o querier do segmento.
2. Alternativa documentada: `multicast_snooping=0`.

Isso vira uma etapa do `reconcileBridgeForOverlay`, sob a flag de inject, com log explícito.

---

## Wire format

### `[NEW] api/v1/inject.proto`

Arquivo novo, `package nnetman.v1`, para não misturar com o contrato gRPC entre peers. `make proto` passa a gerar os dois.

```protobuf
// Envelope autenticado. É o que trafega no datagrama UDP.
message InjectEnvelope {
  uint32 version    = 1;  // 1
  string key_id     = 2;  // identifica o PSK (rotação)
  bytes  payload    = 3;  // Advertisement serializado
  bytes  mac        = 4;  // HMAC-SHA256
}

message Advertisement {
  string   controller_id   = 1;  // node.id de quem publicou
  uint32   vni             = 2;
  string   segment         = 3;  // nome do overlay
  repeated string tags     = 4;  // tags da bridge (para expect_tags)
  int64    timestamp_ms    = 5;  // anti-replay
  uint64   sequence        = 6;  // anti-replay
  uint64   generation      = 7;  // reservado p/ fragmentação futura
  uint32   lease_seconds   = 8;
  repeated InjectedRoute routes = 9;
  string   default_gateway = 10; // vazio = não anuncia default
}

message InjectedRoute {
  string prefix   = 1;
  string next_hop = 2;
  uint32 metric   = 3;
}
```

**Cálculo do MAC** (domain-separated, evita reuso de chave entre contextos):

```
mac = HMAC-SHA256(psk, "nnet-inject-v1" || BigEndian(version) || key_id || payload)
```

Verificação com `hmac.Equal` (tempo constante). Envelope com `version` desconhecida é descartado sem processar o payload.

**Anti-replay.** Aceita se `|now - timestamp_ms| <= 30s` **e** (`sequence > lastSeq` **ou** `timestamp_ms > lastTs + 30s`). A segunda condição cobre restart do controller, que zera a sequência. Exige relógios razoavelmente sincronizados — documentar dependência de NTP.

**Limite de tamanho.** Payload cifrado + envelope capados em 1400 bytes (abaixo do MTU 1450 do overlay, sem fragmentação IP). Se o conjunto de rotas não couber, o publisher **falha alto**: não publica nada para aquele segmento, loga `error` e incrementa `inject_publish_errors_total{reason="payload_too_large"}`. Nunca trunca em silêncio.

---

## Configuração — controller

### Schema novo em `internal/config/config.go`

```go
// BridgeConfig ganha Tags.
type BridgeConfig struct {
	Name string   `yaml:"name" validate:"required"`
	IPv4 string   `yaml:"ipv4,omitempty"`
	IPv6 string   `yaml:"ipv6,omitempty"`
	Tags []string `yaml:"tags,omitempty"`   // [NEW]
}

// RoutingConfig ganha Inject.
type RoutingConfig struct {
	Enabled bool         `yaml:"enabled"`
	Export  ExportConfig `yaml:"export"`
	Import  ImportConfig `yaml:"import"`
	Inject  InjectConfig `yaml:"inject"`    // [NEW]
}

// [NEW]
type InjectConfig struct {
	Enabled         bool         `yaml:"enabled"`
	GroupBase       string       `yaml:"group_base" validate:"omitempty,ip"`  // default 239.8.0.0
	Port            int          `yaml:"port" validate:"omitempty,min=1,max=65535"` // default 4790
	IntervalSeconds int          `yaml:"interval_seconds" validate:"omitempty,min=1,max=300"`
	LeaseSeconds    int          `yaml:"lease_seconds" validate:"omitempty,min=5,max=3600"`
	PSKRef          string       `yaml:"psk_ref"`
	KeyID           string       `yaml:"key_id"`
	Rules           []InjectRule `yaml:"rules" validate:"dive"`
}

// [NEW]
type InjectRule struct {
	MatchTags      []string `yaml:"match_tags" validate:"required,min=1"`
	Networks       []string `yaml:"networks" validate:"dive,cidr"`
	FromRIB        bool     `yaml:"from_rib"`
	RouteTags      []string `yaml:"route_tags"`       // filtra Route.Tags quando from_rib
	NextHop        string   `yaml:"next_hop" validate:"omitempty,ip"`
	Metric         int      `yaml:"metric" validate:"omitempty,min=1,max=4294967295"`
	DefaultGateway string   `yaml:"default_gateway" validate:"omitempty,ip"`
}
```

`BridgeConfig.UnmarshalYAML` (config.go:127) já aceita string ou struct — o campo novo entra na forma struct sem quebrar `bridge: br-prod`.

### YAML resultante

```yaml
overlays:
  - vni: 100
    name: "vxlan-prod"
    bridge:
      name: "br-prod"
      ipv4: "10.100.0.1/24"
      tags: ["it", "external", "public"]     # NOVO
    routing:
      export: { networks: ["172.16.10.0/24"], metric: 100 }
      import: { allow: ["172.16.0.0/16"], install: { table: 100 } }

routing:
  inject:                                     # NOVO — na raiz
    enabled: true
    port: 4790                                # default
    interval_seconds: 10
    lease_seconds: 30
    psk_ref: "file:/etc/n-netman/psk/inject.key"
    key_id: "k1"
    rules:
      - match_tags: ["it"]                    # AND: bridge precisa ter 'it'
        from_rib: true
        route_tags: ["it"]                    # só rotas do RIB com essa community
        networks: ["172.16.10.0/24"]
        metric: 100
      - match_tags: ["it", "external"]        # AND: precisa das duas
        default_gateway: "10.100.0.1"
```

### Resolução por segmento

Para cada overlay com `bridge.tags` não vazio, o publisher:

1. Seleciona as regras cujo `match_tags` seja **subconjunto** das tags da bridge.
2. Une `networks` + (se `from_rib`) rotas do `RouteTable` daquele VNI cujo `Route.Tags` intersecte `route_tags` (lista vazia = todas).
3. Next-hop default = **`overlays[].bridge.ipv4`** (o endereço do host no segmento) quando `next_hop` não é dado.
4. Grupo = `group_base + (vni & 0xFFFF)` → com default `239.8.0.0` e VNI 100, dá `239.8.0.100`.

### Validação semântica nova (`internal/config/loader.go`)

- `routing.inject.enabled` exige `psk_ref` não vazio.
- Todo overlay com `bridge.tags` e sem `bridge.ipv4` é **erro** quando inject está ativo e alguma regra casa — sem endereço na bridge não há next-hop válido. Espelha o `slog.Warn` que já existe em `cmd/nnetd/main.go:97-102`, mas aqui como erro de config.
- `group_base` precisa ser multicast e, somado a `vni & 0xFFFF`, permanecer multicast.
- Regra sem nenhum conteúdo (`networks` vazio, `from_rib` false, `default_gateway` vazio) é erro — regra inerte é quase sempre engano.
- Tags duplicadas na bridge → erro (evita `match_tags` com semântica ambígua).

---

## Configuração — agente

### `[NEW] internal/agentconfig/` (config próprio, schema próprio)

Pacote separado de propósito: o agente não deve carregar o schema do controller (overlays, peers, libvirt, PKI). Reusa o **padrão** do loader existente — `Defaults()` → `yaml.Unmarshal` → `validator.Struct` → `validateSemantics()` — não o schema.

```yaml
# /etc/n-netman/agent.yaml
version: 1

agent:
  id: "vm-app-01"

interfaces:
  - name: "ens3"
    address: "10.100.0.50/24"            # fase 1: estático obrigatório
    expect_tags: ["it"]                   # AND, igual ao match do controller
    psk_ref: "file:/etc/n-netman/psk/inject.key"
    key_ids: ["k1"]                       # opcional: restringe key_id aceito
    port: 4790
    group: ""                             # opcional; vazio = aceita o grupo do VNI anunciado
    install:
      table: 0                            # 0 = main
      metric: 100
      accept_default_gateway: false       # opt-in
    import:
      accept_all: false
      allow: ["172.16.0.0/16"]
      deny: []

observability:
  logging: { level: "info", format: "json" }
  metrics:     { enabled: true, listen: { address: "127.0.0.1", port: 9111 } }
  healthcheck: { enabled: true, listen: { address: "127.0.0.1", port: 9112 } }
```

Sobre `group`: como o agente não conhece o VNI de antemão, ele faz `JoinGroup` no grupo derivado se `group` for explícito; caso contrário assina **todos os grupos do `group_base`**? Não — isso é frágil. Decisão: o agente **exige** `vni` ou `group` na interface. Adiciono `vni: 100` como campo, e o grupo é derivado com a mesma fórmula do controller. `group` explícito continua como escape hatch.

```yaml
  - name: "ens3"
    vni: 100                              # deriva 239.8.0.100
    # group: "239.8.0.100"                # ou explícito
```

Validação: `vni` **ou** `group` obrigatório; `address` obrigatório e CIDR válido; `expect_tags` pode ser vazio (aceita qualquer segmento) mas emite `slog.Warn` no boot.

---

## Estrutura de código

```
api/v1/
  [NEW] inject.proto, inject.pb.go

cmd/
  [NEW] nnet-agent/
        main.go          — cobra: run | status | doctor | version
        run.go, status.go, doctor.go
        main_test.go

internal/
  [NEW] inject/
        codec.go       — encode/decode + HMAC + anti-replay
        codec_test.go
        group.go       — GroupForVNI(base net.IP, vni uint32) net.IP
        publisher.go   — Publisher (lado controller)
        publisher_test.go
        listener.go    — Listener (lado agente)
        listener_test.go
        psk.go         — resolução de "file:..." + checagem de permissão
  [NEW] agentconfig/
        config.go, loader.go, loader_test.go
  [NEW] agent/
        agent.go       — orquestra: endereço, listener, reconciliação de rota
        state.go       — estado por controller_id, lease, união de rotas
        state_test.go
  [NEW] netlink/addr.go — EnsureAddress/RemoveAddress genéricos por link
  [MOD] netlink/route.go       — RouteProtocolNNetAgent = 98
  [MOD] netlink/bridge.go      — AddAddress passa a delegar para addr.go
  [MOD] config/config.go       — BridgeConfig.Tags, RoutingConfig.Inject
  [MOD] config/loader.go       — validação semântica de inject
  [MOD] routing/routing.go     — expor EvalImportPolicy (hoje é privada)
  [MOD] reconciler/reconciler.go — multicast_querier quando inject ativo
  [MOD] observability/observability.go — métricas de inject e de agente

packaging/
  [NEW] n-netman-agent.service
  [MOD] postinstall.sh

examples/
  [NEW] agent.yaml
  [MOD] multi-overlay.yaml   — ganha tags + routing.inject

docs/
  [NEW] inject.md, agent.md
  [NEW] diagrams/inject_flow.puml, diagrams/agent_deployment.puml
  [MOD] README.md, configuration.md, routing.md, observability.md, cli.md
```

### Assinaturas principais

```go
// internal/inject/codec.go
func Seal(adv *pb.Advertisement, psk []byte, keyID string) ([]byte, error)
func Open(datagram []byte, keys KeyRing, now time.Time, rp *ReplayGuard) (*pb.Advertisement, error)

// internal/inject/group.go
func GroupForVNI(base net.IP, vni uint32) (net.IP, error)

// internal/inject/publisher.go
type Publisher struct{ /* não é thread-safe; um por bridge */ }
func NewPublisher(cfg PublisherConfig, opts ...Option) (*Publisher, error)
func (p *Publisher) Run(ctx context.Context) error
func (p *Publisher) Advertise() error   // um ciclo, para teste

// internal/inject/listener.go
func NewListener(ifname string, group net.IP, port int, opts ...Option) (*Listener, error)
func (l *Listener) Receive(ctx context.Context) (<-chan *pb.Advertisement, error)

// internal/agent/state.go
type SegmentState struct{ /* thread-safe: protegido por RWMutex */ }
func (s *SegmentState) Apply(adv *pb.Advertisement, now time.Time)
func (s *SegmentState) Desired(now time.Time) []nlink.RouteConfig  // união + expiração
```

O construtor segue o padrão de **functional options** já estabelecido no `reconciler` (`WithLogger`, `WithMetrics`) com acesso a métrica sempre nil-guarded, e injeção explícita de dependência — nada de singleton.

### Reuso confirmado

| Peça existente | Uso |
|---|---|
| `netlink.RouteManager.Sync(table, desired)` (`internal/netlink/route.go:210`) | Reconciliação de rota no agente. **Hoje é código morto** — este plano lhe dá o primeiro chamador. |
| `netlink.RouteManager.FlushByProtocol` (`:186`) | Expiração de lease e shutdown do agente |
| `controlplane.Route.Tags` (`controlplane.go:38`) | Filtro `route_tags` das regras. Campo já existe no wire e é propagado, mas **nunca foi avaliado** — o gancho já está pronto. |
| `controlplane.RouteTable.All()` (`:134`) | Fonte do `from_rib` |
| `routing.evalImportPolicy` (`routing.go:124`) | Import policy do agente, após exportar |
| `observability.NewMetrics` / `registerOrExisting` (`:78`, `:178`) | Métricas dos dois lados |
| `observability` HTTP servers (`:253`) | `/metrics` e `/healthz` do agente |
| `config` loader pattern (`loader.go`) | Estrutura do `agentconfig` |
| `AuthConfig.PSKRef` (`config.go:190`) | Convenção `file:...` já usada em `peers[].auth` |

---

## Segurança

- **PSK em disco**: o agente recusa carregar arquivo com permissão mais frouxa que `0600` (mesmo rigor que `internal/pki` já aplica às chaves privadas). Nunca loga o conteúdo.
- **HMAC**: `hmac.Equal`, tempo constante. Envelope com MAC inválido é descartado antes de qualquer parse do payload.
- **Anti-replay**: janela de 30s + sequência monotônica por `controller_id`.
- **`expect_tags`**: defesa contra VM cabeada no segmento errado — o agente descarta anúncio cujo segmento não tenha todas as tags esperadas.
- **Import policy no agente**: `allow`/`deny` com a mesma semântica já implementada e testada (deny por sobreposição, allow por contenção, default negar). Um controller comprometido ainda não consegue empurrar prefixo fora da allow-list da VM.
- **TTL=1** no socket multicast: o datagrama não é roteado para fora do segmento L2.
- **Cap de datagrama**: 1400 bytes, protege contra amplificação/fragmentação.

**Risco residual documentado**: o PSK é compartilhado por segmento. Uma VM comprometida com o PSK pode forjar anúncios para as outras VMs daquele segmento. A mitigação real é a assinatura assimétrica com a PKI existente — fica registrada como evolução natural da fase 2, e o campo `key_id` já está no wire format para viabilizar rotação e transição.

---

## Empacotamento

`.goreleaser.yaml`:

- Terceiro build `nnet-agent` (linux/amd64, CGO off, mesmos ldflags).
- O nfpm atual **não declara `ids:`**, então hoje pega todos os builds. Passa a declarar `ids: [nnetd, nnet]` — sem isso, o `.deb` do controller passaria a levar o agente junto.
- Segundo nfpm `n-netman-agent`: `ids: [nnet-agent]`, `examples/agent.yaml` → `/etc/n-netman/agent.yaml.example`, unit em `/lib/systemd/system/n-netman-agent.service`, diretório `/etc/n-netman/psk` com modo `0700`.

`[NEW] packaging/n-netman-agent.service` — roda **depois do netplan**:

```ini
[Unit]
Description=n-netman VM Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nnet-agent run -c /etc/n-netman/agent.yaml
Restart=always
RestartSec=5
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Diferente do `n-netman.service`, o agente **não** precisa escrever em `/etc/systemd` nem chamar `systemctl` — então dá para endurecer de verdade: sem root, só `CAP_NET_ADMIN`/`CAP_NET_RAW`, `ProtectSystem=strict`.

---

## Observabilidade

Campos novos em `observability.Metrics`, registrados via `registerOrExisting`, namespace `nnetman`:

**Controller:** `inject_advertisements_sent_total{vni}`, `inject_routes_advertised{vni}`, `inject_publish_errors_total{vni,reason}`, `inject_last_advertisement_timestamp_seconds{vni}`.

**Agente:** `agent_advertisements_received_total{interface}`, `agent_advertisements_rejected_total{interface,reason}` (`bad_mac`, `replay`, `tag_mismatch`, `policy`, `bad_version`), `agent_routes_installed{interface}`, `agent_controllers_active{interface}`, `agent_last_advertisement_timestamp_seconds{interface}`.

`/healthz` do agente = existe ao menos um controller vivo (lease não expirado) em **todas** as interfaces configuradas. `/status` expõe, por interface, os controllers ativos, as tags do segmento e as rotas instaladas.

`nnet inject status` (novo subcomando de leitura no CLI do controller) mostra, por bridge: tags, grupo, regras que casaram e quantas rotas estão sendo anunciadas.

---

## Sequência de commits (atômicos)

Branch: `feature/inject-agent`.

1. `feat(api): add inject advertisement wire format` — `inject.proto`, gerados, `make proto`
2. `feat(config): add bridge tags and routing.inject schema` — structs, validação, exemplos atualizados
3. `feat(inject): add HMAC-signed advertisement codec` — `codec.go`, `group.go`, `psk.go` + testes
4. `feat(netlink): add generic link address helper` — `addr.go`, `BridgeManager.AddAddress` delega
5. `feat(routing): export import policy evaluation` — torna `evalImportPolicy` reusável
6. `feat(inject): publish advertisements on tagged bridges` — `publisher.go` + wiring no `nnetd` + `multicast_querier`
7. `feat(agentconfig): add agent configuration schema and loader`
8. `feat(agent): add multicast listener and route reconciliation` — `listener.go`, `agent.go`, `state.go`
9. `feat(agent): add nnet-agent binary` — cobra `run`/`status`/`doctor`/`version`
10. `feat(cli): add nnet inject status`
11. `feat(packaging): ship n-netman-agent package and unit`
12. `test(inject): add integration tests behind build tag`
13. `docs: document injection channel and agent`

Cada commit deixa a suíte verde. Nenhum introduz fallback para comportamento legado — o schema novo é aditivo e o caminho é sempre o definitivo.

---

## Verificação

### Testes unitários (`go test -race ./...`)

Estilo do projeto: **stdlib `testing`, table-driven, sem testify** (o repo não tem testify; o `CLAUDE.md` §5.4 sugere, mas a base inteira diverge — mantenho a consistência local).

| Área | Casos |
|---|---|
| `inject/codec` | roundtrip Seal/Open; MAC adulterado rejeita; `version` desconhecida rejeita; timestamp fora da janela rejeita; sequência repetida rejeita; sequência zerada com timestamp novo aceita; payload acima do cap falha |
| `inject/group` | `GroupForVNI(239.8.0.0, 100)` = `239.8.0.100`; VNI 65535; base não-multicast é erro |
| `inject/publisher` | match AND das tags; união de regras; `route_tags` filtrando o RIB; next-hop default vindo de `bridge.ipv4`; regra que não casa não publica |
| `agent/state` | união de dois controllers; expiração independente por lease; dedup por `(prefix,next_hop)`; desempate por métrica |
| `agentconfig` | `vni` ou `group` obrigatório; `address` CIDR; `expect_tags` vazio gera warn; permissão de PSK frouxa é erro |
| `config` | `bridge.tags` na forma struct **e** compatibilidade de `bridge: br-prod` string; inject sem `psk_ref` é erro; tag duplicada é erro |

> [!IMPORTANT]
> `TestLoader_ShippedExamplesAreValid` (`internal/config/loader_test.go:640`) carrega `n-netman.yml` e `examples/multi-overlay.yaml`. Qualquer mudança de schema **quebra esse teste** se os exemplos não forem atualizados no mesmo commit. `TestLoader_Load_VagrantStyleV2` (`:570`) trava o formato gerado pelo Vagrantfile — se o lab mudar, esse teste muda junto.

### Testes de integração (`make test-integration`)

O alvo já existe no Makefile com guard de root, mas **nenhum arquivo carrega `//go:build integration`** hoje. Estes são os primeiros:

- Cria bridge dummy + par veth, roda `Publisher` de um lado e `Listener` do outro, valida que o anúncio chega íntegro e que as rotas entram na tabela `main` com proto 98.
- Valida que `FlushByProtocol` remove exatamente as rotas do agente e nada mais.
- Valida expiração de lease sem anúncio.

### Verificação manual no lab

O Vagrantfile hoje sobe 3 baremetais. Rodar uma VM aninhada seria caro, então o agente é validado num **network namespace ligado por veth à `br-prod`** — mesmo caminho de dados de uma VM real, sem virtualização aninhada.

`[NEW] scripts/lab-agent-test.sh`, executado no `host-a`:

```bash
# 1. namespace simulando a VM
ip netns add vm1
ip link add veth-vm1 type veth peer name vm1-eth0
ip link set veth-vm1 master br-prod up
ip link set vm1-eth0 netns vm1

# 2. agente dentro do namespace
ip netns exec vm1 nnet-agent run -c /etc/n-netman/agent.yaml &

# 3. verificações
ip netns exec vm1 ip route show          # rotas com proto 98
ip netns exec vm1 nnet-agent status
curl -s 127.0.0.1:9110/status | jq .inject   # lado controller
```

Roteiro completo:

1. `vagrant up` (3 nós, mTLS, VNI 100/200)
2. Em cada host: `nnet apply` + `nnetd`
3. `nnet inject status` no host-a → confirma tags, grupo `239.8.0.100`, regras casadas
4. Namespace `vm1` no host-a e `vm2` no host-b, ambos com agente
5. `ip netns exec vm1 ping <ip de vm2>` — só passa se as rotas injetadas estiverem corretas nos dois lados
6. Parar o `nnetd` do host-a → confirmar que as rotas anunciadas por ele **expiram** no `vm1` após o lease, e que as rotas do host-b **permanecem** (lease independente por controller)
7. Adulterar o PSK no `vm1` → confirmar `agent_advertisements_rejected_total{reason="bad_mac"}` subindo e nenhuma rota instalada

> [!CAUTION]
> Passo 6 é o que valida a decisão de estado por `controller_id`. Se as rotas do host-b sumirem junto, o lease está global e não por controller.

### Documentação (DoD §7.7)

- [ ] `docs/inject.md` — canal, schema, segurança, troubleshooting (PT-BR, com blocos `ip`/`bridge` equivalentes, no estilo de `docs/routing.md`)
- [ ] `docs/agent.md` — instalação, config, operação
- [ ] `docs/diagrams/inject_flow.puml` — sequência nnetd → mcast → agente → netlink
- [ ] `docs/diagrams/agent_deployment.puml` — deployment host + VMs
- [ ] Embed via `https://uml.nishisan.dev/proxy?src=<URL_RAW>`
- [ ] `docs/README.md`, `configuration.md`, `routing.md`, `observability.md`, `cli.md` atualizados
- [ ] Seções "O que já funciona / Em progresso" do `README.md` atualizadas
- [ ] Plano copiado para `planning/inject_agent_plan.md`

---

## Dívida pré-existente encontrada (não corrigida aqui)

Registro para decisão à parte — nenhum destes bloqueia o plano:

1. **`nnet libvirt enable` escreve em `libvirt.service.d`**, mas `nnet libvirt status` consulta `libvirtd` e a doc manda reiniciar `libvirtd`. Em Debian/Ubuntu o unit é `libvirtd.service` — o drop-in provavelmente não tem efeito no boot.
2. **`scripts/lab-test.sh` está defasado** — procura `vxlan100`/`br-nnet-100` (nomes v1), enquanto o Vagrantfile cria `vxlan-prod`/`br-prod`.
3. **`ip rule` nunca é removida** — `netlink.DeleteRulesByInterface` não tem chamador; regras vazam a cada reconfiguração.
4. **RPC `Keepalive` é código morto** — implementado no servidor, nenhum cliente chama; o health real usa `ExchangeState` como probe.
5. **`topology.*` e `routing.enabled` são config inerte** — já assumido conscientemente no plano de code review anterior.
