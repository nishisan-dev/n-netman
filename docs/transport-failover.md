# Peering por transporte e failover (configuração v3)

`version: 3` habilita sessões gRPC separadas para cada caminho até um peer. O
PW existente é observado, e a VXLAN é criada somente com `managed: true` e
`bridge.managed: true`. Os fluxos v1/v2 continuam usando a implementação
anterior. Não há BGP, FRR, BIRD, processo BFD ou dependência de comandos do host
para o novo controle de transporte, FDB e rotas.

## Configuração SP/RJ

Use [SP](../configs/v3-t1-sp.yaml) e [RJ](../configs/v3-t1-rj.yaml). Substitua
`br185`, `underlay0`, IPs de documentação dos VTEPs e caminhos dos certificados.
O IP de trânsito PW deve estar na interface **L3**, normalmente a bridge KVM;
portanto `bind_device` também aponta para essa bridge. Nenhum master, slave,
endereço, MTU, VLAN, bond ou TAP desse transporte externo é alterado.

A bridge VXLAN tem o IP de trânsito 192.168.200.x. Os VTEPs têm IPs separados
no underlay, alcançados por outro dispositivo. A resolução de rota do VTEP é
verificada novamente em cada heartbeat, assim como a resolução direta do peer
no dispositivo do transporte. Não há fallback de bind ou dial por outra interface.

É possível operar só peering/roteamento: use transportes `external` ou VXLAN
`managed: false`, observando interfaces já existentes. Para VXLAN observada,
informe `bridge.name`, `bridge.ipv4` e `peer_ip`; nenhuma VXLAN/FDB é criada.
`overlays:` pode ser incluído explicitamente para continuar reconciliando
recursos v2 e usar injeção estática de rotas nas VMs. Peers v3 usam `sessions:`,
não o campo legado `endpoint:`. A nova RIB por sessão não é redistribuída
implicitamente para overlays ou VMs; configure `routing.inject.rules.networks`
para os anúncios explícitos às VMs. v1/v2 preservam a injeção a partir da RIB.

IDs são únicos; uma sessão referencia um transporte existente. Origem, destino,
listen e device devem corresponder aos endpoints L3 desse transporte. O listener
usa IP específico e `SO_BINDTODEVICE`; a mesma porta funciona em IPs distintos.
Sessões com mesmo par de endpoints/porta são recusadas como ambíguas.
`remote_session_id` e `remote_transport_id` permitem nomes diferentes do outro
lado. O primeiro assume o ID local da sessão; o segundo, se omitido, é aprendido
no snapshot autenticado, dentro do endpoint configurado. Os exemplos fixam ambos.

Tabelas aceitam `main` (254) e números positivos. Candidatos do mesmo
prefixo/tabela têm métricas diferentes, inclusive entre peers: não há ECMP.
Gateway, dispositivo, métrica e tabela vêm exclusivamente da política **local**.

Import/export v3 aceita IPv4 canônico. Allow/deny compara prefixos **exatos**;
`deny` prevalece, e `deny: [0.0.0.0/0]` recusa o anúncio da default sem recusar
os prefixos mais específicos listados em `allow`. Default não é instalada nem
exportada neste modo, mesmo com `accept_all: true`. A exportação contém somente
`export.networks`, sem conectadas, trânsito ou exportação automática.

## Segurança e saúde

mTLS é obrigatório por padrão. O certificado de cada nó precisa conter seu
`node.id` como SAN DNS; a conexão cliente verifica o certificado pelo ID do peer,
e o servidor confere o `node_id` recebido com o certificado autenticado e o
endereço TCP de origem. Use o `nnet cert` existente para gerar CA e certificados
com os IDs dos nós. Arquivos de chave e journal devem ter acesso restrito.

Somente laboratório pode usar explicitamente:

```yaml
security:
  control_plane:
    insecure_lab: true
    tls: {enabled: false}
```

Sem TLS a associação ao peer depende do endpoint configurado e não fornece
identidade criptográfica. Não use essa opção para produção.

Cada HELLO usa o RPC `Keepalive`, com stream limitado ao prazo de um ACK. A
conexão TCP/gRPC é reutilizada entre probes; um socket aberto ou um ACK TCP não
conta como sucesso. O ACK ecoa sessão, transporte, epoch, geração e sequence.
Sequences antigas/duplicadas são rejeitadas. Cada reconexão troca o epoch e
exige snapshot; snapshots antigos não revertem deltas mais recentes.

Os defaults são intervalo 1000 ms, ACK 700 ms, três falhas para DOWN e dez ACKs
para UP. Limites: intervalo 100–60000 ms, ACK de no mínimo 50 ms e menor que o
intervalo, falhas 1–100, sucessos 1–1000. Cada falha conta uma vez por ciclo;
a reconexão ocorre nessa cadência limitada, com jitter de até 10%. Não há retry
exponencial prolongado mascarando o prazo configurado.

`INIT/PROBING` não instala rotas. `RECOVERING` exige os sucessos consecutivos
configurados. `UP` habilita candidatos com anúncios válidos. `SUSPECT` conserva
as rotas durante as falhas toleradas; `DOWN` invalida somente a sessão afetada.
Uma falha durante RECOVERING zera a contagem de estabilidade.

Snapshots completos são atualizados a cada dez segundos, com leases de trinta
segundos, independentemente da renovação de saúde por ACK. Announce/withdraw
são suportados com identidade e geração; `SessionManager.SetExports` altera o
subconjunto vivo da allowlist configurada e transmite os deltas em cada sessão.
Não há comando CLI ou reload de YAML para alterar esse subconjunto nesta entrega;
para mudar a lista configurada, edite o YAML e reinicie o serviço.
Anúncios têm limite de 4096 rotas, leases 1–3600 s e mensagens até 1 MiB.
O detector de replays retém epochs aposentados por uma hora, limitado a 4096
por sessão; excesso de churn nessa janela falha de forma fechada.

## Rotas, ownership e restart

Com ambos UP há duas rotas para o prefixo, métricas 100/200, na mesma tabela.
O kernel prefere 100. DOWN ou withdraw do PW retira apenas 100. Dez sucessos
restauram a primária no exemplo. Se ambos falham não fica rota gerenciada.
Uma rota de terceiros de maior prioridade pode alterar a seleção real do kernel.

`proto 99` identifica o protocolo, mas não autoriza remoção sozinho. O journal
`state_file` (default `/var/lib/n-netman/path-routes.json`) registra peer, sessão,
transporte, destino, tabela, gateway, dispositivo e métrica. Adições usam
`RouteAdd`, sem substituir rotas manuais; colisões de prefixo/tabela/métrica são
erros operacionais. Remoções exigem correspondência exata com o journal e FIB.
O journal é escrito atomicamente, com fsync, antes da adição no kernel. Um lock
impede dois daemons de usarem o mesmo journal. Preserve esse arquivo entre runs;
se ele for perdido, rotas existentes não serão adotadas automaticamente.
Não compartilhe o mesmo conjunto de rotas entre daemons com journals diferentes.

No restart, as entradas antigas do journal são removidas conservadoramente antes
dos testes de saúde. Não se restaura UP de estado persistido. A parada normal
retira os candidatos; um crash pode deixá-los no kernel **até o restart**.
Não existe garantia de failover enquanto o daemon está indisponível. O serviço
systemd incluído usa `Restart=always`. Nunca é feito `flush cache` ou flush geral
por protocolo no fluxo v3.

Bridges gerenciadas existentes são verificadas antes da alteração: tipo e MTU
precisam ser coerentes; não são removidas portas/endereço existentes. Endereço
configurado ausente pode ser adicionado com autorização `bridge.managed: true`;
mesmo IP com máscara diferente causa erro. VXLAN com VNI, VTEP, porta, underlay
ou master incompatível é recusada, sem recriação destrutiva. FDB unicast aprendida
é preservada; somente entradas BUM da VXLAN gerenciada são reconciliadas.

## Operação

```bash
make build
sudo install -m 0755 bin/nnetd bin/nnet /usr/local/bin/
sudo install -d -m 0700 /var/lib/n-netman
sudo nnet -c /etc/n-netman/n-netman.yaml apply --dry-run
sudo nnet -c /etc/n-netman/n-netman.yaml apply
sudo nnet -c /etc/n-netman/n-netman.yaml doctor
sudo nnetd -config /etc/n-netman/n-netman.yaml
nnet -c /etc/n-netman/n-netman.yaml status
nnet -c /etc/n-netman/n-netman.yaml peers
nnet -c /etc/n-netman/n-netman.yaml routes
```

Go é necessário somente no build. Os binários usam gRPC e Netlink no Linux.
São necessárias `CAP_NET_ADMIN` para os objetos/rotas e `CAP_NET_RAW` para
`SO_BINDTODEVICE` no kernel alvo, além de acesso ao journal e certificados.
O serviço `packaging/n-netman.service` executa como root. Não é necessário Go,
`ip`, `bridge`, `ping` ou `nc` no host para o novo fluxo do daemon.

`apply` cria/reconcilia recursos autorizados; **o daemon** instala rotas somente
após saúde/snapshot. O dry-run usa o mesmo planner dos transportes para mostrar
criação, preservação, attach, endereço, MTU, UP e alterações exatas de FDB; mostra
também os candidatos condicionais e a limpeza prevista no próximo restart. Não
simula saúde futura nem instala rotas antes de testar o peer. Para overlays
legados opt-in, mantém a apresentação resumida do reconciler preexistente.

`doctor` testa IP local/device, FIB direta, underlay, privilégio real de bind e
conflitos de candidatos. Antes de criar uma VXLAN gerenciada, a ausência de suas
interfaces é esperada; execute `apply`, depois `doctor`. Um PW indisponível
não impede o daemon de testar o caminho VXLAN em paralelo.

`/status` expõe uma entrada por `peer/session`, fonte, device, RTT do último ACK
válido, erro, estado e prontidão de interface/underlay. O contador installed
consulta journal + FIB. Os logs de transição/candidatos incluem peer/session/
transport, estados anterior/novo, motivo, prefixo, tabela, via e métrica.

Prometheus acrescenta `nnetman_transport_health`, `nnetman_peer_session_state`
(one-hot), `nnetman_peer_heartbeat_rtt_seconds`,
`nnetman_peer_heartbeat_timeouts_total`, `nnetman_failovers_total`,
`nnetman_route_candidates` e `nnetman_route_reconcile_errors_total`.
Candidatos são agregados por estado para evitar labels de prefixo de alta
cardinalidade; failovers têm razões de cardinalidade limitada.

## Validação reproduzível

```bash
go test -race ./...
go vet ./...
make build
make test-transport-netns
```

O último comando compila o teste fora do namespace e usa user/network namespaces
isolados. Requer Linux com user namespaces habilitados; caso indisponíveis,
execute o binário de testes com permissões apropriadas em um laboratório isolado.
A suíte não altera as interfaces do host e não exige ferramentas de rede externas.

O lab inicia dois processos com certificados mTLS, bridges PW, veth de underlay,
VXLAN e IPs de produção. Confere FIB **e tráfego TCP de ida e volta**, ambos UP,
blackhole PW com carrier UP, histerese/retorno, SIGKILL/restart com PW indisponível,
withdraw/announce com sessão UP, queda só VXLAN, ambos DOWN e preservação da
bridge/porta KVM, do índice VXLAN e de uma rota estrangeira `proto 99`.
Testes unitários adicionais exercitam ACK de aplicação parado com conexão gRPC
aberta, identidades/epochs/sequences incorretas, leases, múltiplos peers,
colisões e journal, além da suíte v1/v2 existente.

Execução de referência validada em kernel 6.8. O desenho usa APIs presentes no
Linux 5.4, mas a execução no Ubuntu `5.4.0-88-generic` ainda precisa ser repetida
no laboratório alvo. A configuração dos T1 reais (interfaces, VTEPs, certificados,
firewall e sub-redes) também deve ser validada antes da implantação.
