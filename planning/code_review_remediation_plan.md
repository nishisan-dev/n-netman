# Code Review — n-netman (bugs + melhorias)

## Contexto

`n-netman` é um agente Go (~6.000 linhas, 23 arquivos) que cria overlays VXLAN L3/L2 entre
hosts KVM/libvirt: um daemon (`nnetd`) reconcilia config YAML declarativa para o kernel via
netlink, troca rotas com peers por um control plane gRPC (TLS/mTLS opcional), expõe
health/métricas, e um CLI (`nnet`) faz apply/status/routes/doctor/cert/libvirt.

Este review foi solicitado para **encontrar bugs e propor melhorias (código e documentação)**.
A análise combinou leitura manual dos arquivos críticos com uma revisão multiagente
(10 dimensões × finder + verificação adversarial finding-a-finding + crítico de completude).
Resultado: **91 achados** sobrevivendo à verificação adversarial — **3 críticos, 14 altos,
31 médios, 43 baixos** (13 de segurança). `go build ./...` e `go vet ./...` passam (exit 0);
os problemas são lógicos/semânticos, não de compilação. Há **1 único arquivo de teste**
(`internal/config/loader_test.go`) cobrindo todo o daemon.

> Modo: este documento é o **entregável de consultoria** (review). Nenhum arquivo de produção
> foi alterado. A última seção propõe um roadmap de remediação para aprovação.

---

## Resumo executivo — os 8 temas que mais importam

1. **Política de import é ficção** — o daemon instala no kernel **toda** rota recebida sem
   aplicar `allow/deny/accept_all`; e mesmo a função de match (`matchesPrefix`) é logicamente
   incorreta. Um peer pode injetar `0.0.0.0/0`. *(Segurança/Correção)*
2. **Identidade de peer não é autenticada** — `NodeId` vem do corpo da requisição, não do
   certificado mTLS; não há validação de prefixo/next-hop. Permite spoofing e route poisoning.
3. **TLS "ligado" pode não autenticar nada** — sem `ca_file`, o client faz
   `InsecureSkipVerify=true` (silencioso) e o server não exige cert de cliente; nunca há
   verificação de `ServerName`. "TLS enabled" dá falsa sensação de segurança.
4. **Tabela de rotas indexada só por prefixo** — colapsa rotas de VNIs/peers diferentes
   (last-writer-wins); quebra multi-overlay, withdraw e cleanup por peer.
5. **Ciclo de vida de rota furado** — withdraw remove da tabela mas **não do kernel**; rotas
   recebidas pelo caminho *client* nunca são instaladas; flush de shutdown limpa só **uma**
   tabela (vaza rotas em multi-overlay); deletes não filtram pelo protocolo n-netman.
6. **Observabilidade enganosa** — `/healthz` é sempre "healthy", `/readyz` nunca volta a
   not-ready, e **todas** as métricas Prometheus (exceto `peers_configured`) são registradas
   mas **nunca atualizadas**. O README afirma "métricas coletadas".
7. **Modelo de peers do v2 inexistente** — em config v2 (`overlays:`), peers só existem no
   bloco legado `overlay.peers`. Um v2 "limpo" sobe com **zero peers** e fica inerte
   reportando ready=200.
8. **Robustez do daemon** — erro em um overlay aborta a reconciliação dos demais; peers que
   falham no startup nunca reconectam; RPCs do client sem deadline (um peer travado bloqueia
   propagação); ordering de shutdown causa "ressurreição" de rotas.

---

## P0 — Críticos / Altos (corrigir primeiro)

### Segurança do control plane

- **[CRIT] Import policy nunca aplicada** — `cmd/nnetd/main.go:installReceivedRoutes` instala
  todas as rotas recebidas com `routeMgr.Replace` sem chamar `ShouldImportForOverlay`. O
  pacote `internal/routing` **não é importado** pelo daemon (confirmado por grep). `allow/deny`
  dos exemplos não têm efeito → peer injeta rota default/arbitrária.
  **Fix:** aplicar `routing.Manager.ShouldImportForOverlay(route, overlay)` antes de instalar;
  derivar o overlay pela VNI da rota.
- **[ALTO-SEC] `matchesPrefix` permite bypass de deny** (`internal/routing/routing.go:170`) —
  só casa quando a policy é supernet da rota. `deny: [10.1.0.0/16]` + anúncio `10.0.0.0/8` →
  não casa → admitido. **Fix:** considerar match se *qualquer* dos dois contém o outro
  (overlap), não apenas policy⊇rota.
- **[ALTO-SEC] Identidade de peer não autenticada / sem validação de payload**
  (`controlplane.go:ExchangeState/AnnounceRoutes/WithdrawRoutes`) — `PeerID` vem de
  `req.NodeId`; um peer pode se passar por outro e retirar/substituir rotas alheias
  (`WithdrawRoutes` confia em `r.PeerID == req.NodeId`). Prefix/next-hop não são validados no
  handler. **Fix:** vincular a identidade ao CN/SAN do cert mTLS (via `peer.FromContext`);
  validar `ParseCIDR`/`ParseIP` e rejeitar rotas inválidas no servidor.
- **[CRIT/ALTO-SEC] TLS sem CA = sem autenticação** (`controlplane/tls.go:62-71`) — client cai
  em `InsecureSkipVerify=true` quando `ca_file` ausente; server não seta `ClientAuth` sem CA;
  `ServerName` nunca é definido (sem verificação de hostname/SAN). **Fix:** exigir `ca_file`
  quando `tls.enabled` (na `validateSemantics`), definir `ServerName` = ID/host do peer, e
  remover o fallback inseguro (ou exigir flag explícita `insecure: true` documentada).
- **[MÉD-SEC] gRPC server sem limites** (`controlplane.go:Start`) — sem `MaxRecvMsgSize`,
  `MaxConcurrentStreams`, `ConnectionTimeout`, `KeepaliveEnforcementPolicy` → DoS por exaustão.
  **Fix:** adicionar limites conservadores (ex.: 1MB recv) e enforcement de keepalive.

### Ciclo de vida de rotas

- **[ALTO] Tabela de rotas indexada só por prefixo** (`controlplane.go:RouteTable` chave =
  `prefix`) — rotas de VNIs/peers distintos com o mesmo prefixo se sobrescrevem. **Fix:** chave
  composta `(VNI, prefix, peerID)` (ou mapa aninhado por VNI), ajustando `Add/Remove/Get/
  GetByPeer/Withdraw`.
- **[CRIT] Withdraw não remove do kernel** (`controlplane.go:WithdrawRoutes`) — remove só da
  tabela; nenhuma callback de cleanup é chamada → rota fica instalada no kernel. **Fix:**
  acionar um callback `onRoutesWithdrawn` que faz `routeMgr.Delete` na tabela correta por VNI.
- **[CRIT/ALTO] Rotas recebidas no caminho *client* nunca são instaladas**
  (`controlplane.go:exchangeWithPeer` e resposta de `AnnounceRoutes`) — só o caminho *server*
  tem `onRoutesReceived`. Rotas aprendidas via ExchangeState/Announce de saída entram na tabela
  mas não no kernel. **Fix:** invocar o mesmo instalador no client após `routeTable.Add`.
- **[ALTO] Deletes não filtram pelo protocolo n-netman** (`netlink/route.go:Delete`,
  callers em `main.go:429,464`) — `RouteDel` por `Dst+Gw+Table` pode apagar rota que o daemon
  não instalou. **Fix:** setar `Protocol: RouteProtocolNNetMan` no `Delete` (e considerar
  Priority) para escopar a remoção.
- **[MÉD] Flush de shutdown limpa só a tabela default/legada** (`main.go:164-174`) — multi-
  overlay com tabelas próprias vaza rotas. **Fix:** iterar `vniToTable` (já construído no
  refresh loop) e dar flush em cada tabela distinta.
- **[MÉD] Erro de um overlay aborta os demais** (`reconciler.go:Reconcile`) — `return` no
  primeiro erro. **Fix:** acumular erros por overlay (`errors.Join`) e seguir reconciliando os
  outros.

### Robustez / disponibilidade

- **[ALTO] Peers que falham no startup nunca reconectam** (`controlplane.go:ConnectToPeers` é
  one-shot; `main.go` chama uma vez). **Fix:** loop de reconexão com backoff no refresh loop.
- **[ALTO] RPCs do client sem deadline** (`exchangeWithPeer`, `announceToSinglePeer`,
  `WithdrawRoutes`) — peer travado bloqueia propagação a todos. **Fix:** `context.WithTimeout`
  por RPC (o `CheckPeerHealth` já faz isso; replicar).
- **[ALTO] Modelo de peers do v2 inexistente** (`config.go:GetPeers` retorna só
  `Overlay.Peers`) — v2 sem bloco legado = zero peers, daemon inerte. **Fix:** adicionar
  `peers` ao schema v2 (global ou por-overlay), tornar `GetPeers()` version-aware, e validar na
  `validateSemantics` que v2 com routing/control-plane tem ≥1 peer.

### Observabilidade

- **[ALTO] `/healthz` sempre "healthy"** (`observability.go`) — `SetHealthy(false)` nunca é
  chamado. **Fix:** ligar health a sinais reais (reconciler.lastErr, peers saudáveis) ou
  documentar como liveness puro.
- **[MÉD] Métricas nunca atualizadas** — só `peers_configured` é setado; reconciler/control
  plane sequer recebem o `*Metrics`. README afirma "métricas coletadas". **Fix:** injetar
  `*Metrics` no reconciler e no control plane e atualizar contadores/gauges; **ou** ajustar a
  doc para refletir o estado real.
- **[MÉD] `/readyz` nunca volta a not-ready** (`main.go:151`) — fica ready mesmo com
  subsistema quebrado / durante shutdown. **Fix:** `SetReady(false)` no início do shutdown e
  quando init parcial falha.

### Qualidade / regressão

- **[ALTO] Cobertura de testes quase nula** — só `loader_test.go`. As unidades puras
  (`RouteTable`, `matchesPrefix`/`ShouldImport`, `vniToTable`/`getTableForRoute`, validação de
  versão/duplicatas) são 100% testáveis e não têm teste. **Fix:** suíte de unit tests para a
  lógica pura + testes de integração netlink/gRPC atrás de build tag, com `go test -race ./...`
  no CI.

---

## P1 — Médios (corrigir em seguida)

**Concorrência**
- Data race em `Server.startTime` (write sob lock em `Start`, read sem lock em `Keepalive`).
  Fix: snapshot sob `RLock` ou setar em `NewServer`. *(downgrade high→medium: write único no
  startup)*.
- Ordering de shutdown: `FlushByProtocol` roda **inline** antes do `defer cpServer.Stop()`
  (LIFO) → handler de install pode reinstalar rota recém-removida. Fix: parar o control plane
  **antes** do flush. *(Nota: não é data race de memória — `RouteManager` não tem estado
  compartilhado; é ordering lógico.)*
- `Server.Stop` segura `s.mu` durante `GracefulStop`, que espera handlers que pegam `s.mu` →
  risco de stall. Fix: snapshot do `grpcServer` e chamar `GracefulStop` fora do lock.

**Config / validação**
- Sem detecção de VNI/nome/tabela duplicados em v2 (`loader.go`). Fix: validar unicidade.
- `Defaults().Version=1` mascara `version` ausente → caminho de validação errado. Fix: exigir
  `version` explícito.
- BUM `multicast` aceita group inválido/não-multicast e degrada silenciosamente. Fix: validar
  que o group é multicast quando `mode=multicast`.
- `bridge.ipv4/ipv6` (CIDR) do v2 nunca validados. Fix: `validate:"omitempty,cidr"`.

**Netlink (idempotência/drift)**
- `VXLAN.Create` só compara VNI ao reusar interface — ignora DstPort/LocalIP/MTU/Learning/
  Group/VtepDev e **não reanexa à bridge**. Fix: reconciliar atributos e reatachar.
- `FDB.SyncPeers` lista entradas aprendidas (MAC real) junto com BUM (all-zeros); pode pular o
  add do BUM e tentar deletes errados. Fix: filtrar só entradas all-zeros na comparação.
- `Bridge/VXLAN.Create` deletam e recriam interface de mesmo nome porém tipo diferente —
  destrutivo para interfaces alheias. Fix: falhar com erro claro em vez de apagar.

**Libvirt**
- `AttachInterface`/`DetachInterface` passam `--live` incondicionalmente → falham em VM
  desligada (a doc/CLI dizem suportar VM parada). Fix: usar `--config` sozinho quando a VM não
  está running; adicionar `--live` só se running.
- Descoberta de MAC pós-attach pega "última na bridge" — ambígua com múltiplas interfaces na
  mesma bridge. Fix: snapshot de MACs antes/depois e diff.
- `model` de `AttachTarget` é ignorado (hardcoded `virtio`).

**Outros**
- Servers HTTP (metrics/health) sem timeouts (Slowloris). Fix: `ReadHeaderTimeout`/`Read`/
  `WriteTimeout`.
- FDB BUM global (todo peer floda em todo overlay) — sem mapeamento peer→VNI. Fix: escopar
  peers por overlay (depende do modelo de peers v2).
- `detectLocalIP` devolve IP do underlay e vira next-hop quando overlay não tem `bridge.ipv4`
  → anúncio com next-hop inalcançável (black-hole). Fix: exigir next-hop do overlay (bridge IP)
  quando há `export.networks`.
- Rotas instaladas antes do next-hop ser alcançável (ordering reconciler vs install).

---

## P2 — Documentação (mismatches concretos com o código)

- `routing.md`/README apresentam **import filtering (allow/deny/accept_all)** como funcional —
  o daemon ignora (ver P0). Corrigir para "não aplicado" e alertar do risco de segurança.
- `export_all`/`include_connected` documentados, mas export usa só a lista `networks`.
- `observability.logging.level`/`format` documentados como configuráveis, mas o daemon
  hardcoda `slog` JSON/Info (`main.go:42-45`). Implementar ou remover da doc.
- `configuration.md` cita `tls.skip_verify` — **não existe** no loader.
- `configuration.md` cita `lookup_rules.priority` — **não existe**; e `lookup_rules.mode:
  prefix` é parseado mas **não implementado** (só `interface`).
- `cli.md` documenta flags globais `-v/--verbose` que **não existem**.
- `observability.md` mostra corpos de resposta de `/livez,/readyz,/healthz` que **não batem**
  com o código; `cli.md` mostra saída de `nnet version` em formato diferente do real.
- README marca "Métricas ✅ coletadas", "Status de peers ✅ com keepalive" — métricas não são
  populadas e o RPC `Keepalive` é código morto (health usa `ExchangeState`).
- **DoD de docs (CLAUDE.md §7):** ao corrigir, atualizar `docs/` + diagramas PlantUML
  afetados (`architecture.puml`, `route-exchange.puml`) e revalidar render.

---

## P3 — Baixos / polish (amostra representativa)

`apply` imprime struct `Bridge` com `%s` (saída truncada); `--mac` sem validação permite
injeção de argumento ao `virsh`; `MustRegister` em `DefaultRegisterer` pode dar panic em
registro duplicado; `FDBManager.Delete`/`GetServiceStatus` engolem erros; serial fixo `1` na CA;
diretório de certs `0755` contendo chaves; `ListRulesByTable` só enumera IPv4; `Routing.Enabled`
e `topology.transit*` nunca consultados (config inerte); goroutines (Serve/refresh) vazadas no
shutdown sem `WaitGroup`; `Stop` usa `%v` em vez de `%w`. *(Lista completa dos 43 baixos
disponível no output do workflow.)*

## Achados refutados pela verificação adversarial (não são bugs)

Para honestidade do review, estes foram levantados e **descartados** após leitura do código:
colisão de prioridade de `ip rule` entre overlays (seletores `iif/oif` por bridge são
disjuntos); `GetOverlays` "perder" `Routing.Enabled` (campo é globalmente inerte); CA sem
`pathlen`/`SubjectKeyId` (stdlib Go já popula SKID/AKID; host certs não são CA); SANs
`localhost/127.0.0.1` no host cert (sem autorização por-identidade, não amplia poder);
`handleStatus` com interpolação não-escapada (branch praticamente inalcançável); shadowing de
`routeTable` no refresh loop (escopado ao bloco interno, sem efeito); guarda de mutex em
`observability.Server.Stop` (sem concorrência real).

---

## Roadmap de remediação proposto

Commits atômicos por tema (CLAUDE.md §2/§4), com testes acompanhando cada mudança de produção
(CLAUDE.md §3) e branch por escopo (`fix/…`, `docs/…`, `refactor/…`).

1. **`fix/route-import-policy`** — aplicar política de import + corrigir `matchesPrefix`
   (+ unit tests). *(P0 segurança)*
2. **`fix/tls-auth`** — exigir CA quando TLS on, `ServerName`, identidade via cert; remover
   `InsecureSkipVerify` silencioso. *(P0 segurança)*
3. **`fix/route-lifecycle`** — chave composta na `RouteTable`; withdraw→kernel; instalar rotas
   do caminho client; flush multi-tabela; `Delete` escopado por protocolo (+ unit tests).
4. **`fix/peers-v2`** — modelo de peers v2 + validação + escopo FDB por overlay.
5. **`fix/daemon-robustness`** — reconexão com backoff; deadlines por RPC; erro por-overlay
   não aborta os demais; ordering de shutdown + `SetReady(false)`.
6. **`fix/observability`** — popular métricas e ligar `/healthz` a estado real (ou alinhar doc).
7. **`fix/config-validation`** — duplicatas VNI/tabela; CIDR de bridge; BUM group; `version`
   obrigatória.
8. **`fix/netlink-idempotency`** + **`fix/libvirt-live`** — drift de VXLAN/bridge/FDB;
   `--live` condicional.
9. **`docs/sync-with-code`** — corrigir todos os mismatches P2 + diagramas.
10. **`chore/tests-and-ci`** — suíte de unit tests + `go test -race ./...` no CI.

## Verificação (como validar cada correção)

- **Build/estática:** `go build ./...`, `go vet ./...`, `go test -race ./...` (alvo: verde).
- **Política de import:** unit test de `ShouldImportForOverlay`/`matchesPrefix` com casos de
  deny supernet; teste de integração instalando rota negada e confirmando ausência no kernel.
- **TLS:** subir 2 daemons com mTLS e CA; confirmar handshake; confirmar que `enabled` sem CA
  **falha** em vez de cair em inseguro.
- **Lab end-to-end:** usar `Vagrantfile` + `scripts/lab-test.sh` (3 hosts a/b/c) para validar
  troca de rotas, withdraw removendo do kernel, flush multi-tabela no shutdown e reconexão.
- **Métricas/health:** `curl :9109/metrics` mostra contadores variando; derrubar peer e ver
  `/healthz`/`/status` refletirem.

---

## Status de Execução (concluído)

Roadmap P0→P3 executado em 10 commits atômicos na branch `fix/code-review-remediation`.
Verificação final: `go build ./...`, `go vet ./...` e `go test -race ./...` **verdes**;
`gofmt` limpo. Cobertura de testes saiu de 1 arquivo para 6 pacotes testados
(config, routing, controlplane, observability, netlink, cmd/nnetd).

| Commit | Stream | Itens |
|---|---|---|
| `fix(config)` | Config/peers v2 | peers raiz v2 + `vnis`, `version` obrigatória, duplicatas VNI/nome/bridge/tabela, CIDR de bridge, grupo multicast BUM |
| `fix(routing)` | Import policy | política aplicada no daemon; deny por sobreposição, allow por contenção, default nega |
| `fix(routes)` | Ciclo de vida | RouteTable chave composta; withdraw→kernel; install no caminho client; flush multi-tabela; Delete escopado por protocolo; fontes de peer version-aware |
| `fix(tls)` | TLS/mTLS | CA obrigatória, `ServerName`, identidade pelo CN; validação de payload |
| `fix(daemon)` | Robustez | deadlines por RPC; reconexão (`grpc.NewClient`); erro por-overlay não aborta; ordering de shutdown; limites gRPC; `Stop` sem deadlock; race de `startTime` |
| `fix(observability)` | Observabilidade | métricas populadas; `/healthz` real; timeouts HTTP; registro idempotente |
| `fix(netlink,libvirt)` | Idempotência | reconcile de drift VXLAN/bridge sem destruir; FDB só BUM; FDB por overlay; libvirt `--live` condicional/model/MAC |
| `fix(p3+docs)` | Polish + docs | logging aplicado; `%s` de struct; serial CA aleatório; perms 0700; sync de docs |
| `chore(ci)` | CI/testes | workflow CI (`go test -race`); testes de resolução de tabela |
| `fix(lab)` | Lab | Vagrant v2 peers no raiz + teste de regressão |

**Refutados** (não corrigidos, por design): colisão de prioridade de `ip rule` (seletores
por bridge são disjuntos); `routing.enabled`/`topology.transit` permanecem inertes (não
implementados — documentados como reservados).
