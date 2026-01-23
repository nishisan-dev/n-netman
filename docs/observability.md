# Observabilidade

Este documento descreve os mecanismos de logging, métricas e healthchecks do n-netman.

## Logs

O n-netman usa `slog` (Go 1.21+) para logging estruturado.

### Configuração

```yaml
observability:
  logging:
    level: "info"       # debug | info | warn | error
    format: "json"      # json | text
```

### Níveis de Log

| Nível | Uso |
|-------|-----|
| `debug` | Detalhes internos, útil para desenvolvimento |
| `info` | Operações normais, eventos significativos |
| `warn` | Condições inesperadas que não impedem operação |
| `error` | Falhas que afetam funcionalidade |

### Formato JSON

```json
{
  "time": "2026-01-23T09:51:00Z",
  "level": "INFO",
  "msg": "route installed",
  "component": "controlplane",
  "prefix": "172.16.20.0/24",
  "peer": "host-b",
  "table": 100
}
```

### Formato Text

```
2026-01-23T09:51:00Z INFO route installed component=controlplane prefix=172.16.20.0/24 peer=host-b table=100
```

### Visualização

```bash
# Com systemd
journalctl -u n-netman -f

# Em foreground com jq
sudo nnetd -config /etc/n-netman/n-netman.yaml 2>&1 | jq .

# Filtrar por componente
journalctl -u n-netman -f | jq 'select(.component == "reconciler")'
```

### Componentes Logados

| Componente | Eventos |
|------------|---------|
| `config` | Carregamento, validação, defaults aplicados |
| `reconciler` | Ciclos de reconciliação, criação de interfaces |
| `controlplane` | Conexões gRPC, troca de rotas, keepalive |
| `netlink` | Operações de bridge, VXLAN, FDB, rotas |
| `observability` | Inicialização de métricas e healthchecks |

---

## Métricas Prometheus

Métricas disponíveis no endpoint `/metrics`.

### Configuração

```yaml
observability:
  metrics:
    enabled: true
    listen:
      address: "127.0.0.1"   # Vincular apenas localmente
      port: 9109
```

**Endpoint:** `http://<address>:<port>/metrics`

### Métricas Disponíveis

#### Reconciliação

| Métrica | Tipo | Descrição |
|---------|------|-----------|
| `nnetman_reconciliations_total` | Counter | Total de ciclos de reconciliação |
| `nnetman_reconciliation_errors_total` | Counter | Ciclos que falharam |
| `nnetman_reconciliation_duration_seconds` | Histogram | Duração dos ciclos |
| `nnetman_last_reconcile_timestamp_seconds` | Gauge | Timestamp do último ciclo |

#### Recursos de Rede

| Métrica | Tipo | Descrição |
|---------|------|-----------|
| `nnetman_vxlans_active` | Gauge | Interfaces VXLAN ativas |
| `nnetman_bridges_active` | Gauge | Bridges ativas |
| `nnetman_fdb_entries_total` | Gauge | Total de entradas FDB |

#### Peers

| Métrica | Tipo | Descrição |
|---------|------|-----------|
| `nnetman_peers_configured` | Gauge | Peers configurados |
| `nnetman_peers_connected` | Gauge | Peers com conexão gRPC ativa |
| `nnetman_peers_healthy` | Gauge | Peers recebendo keepalive |

#### Roteamento

| Métrica | Tipo | Descrição |
|---------|------|-----------|
| `nnetman_routes_exported` | Gauge | Rotas exportadas (config local) |
| `nnetman_routes_imported` | Gauge | Rotas instaladas (de peers) |

#### gRPC

| Métrica | Tipo | Descrição |
|---------|------|-----------|
| `nnetman_grpc_requests_total` | Counter | Total de requisições gRPC |
| `nnetman_grpc_request_duration_seconds` | Histogram | Latência das requisições |

### Exemplo de Scrape

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'nnetman'
    static_configs:
      - targets:
          - 'host-a:9109'
          - 'host-b:9109'
          - 'host-c:9109'
```

### Alertas Sugeridos

```yaml
# alertmanager rules
groups:
  - name: nnetman
    rules:
      - alert: NNetManPeerDown
        expr: nnetman_peers_healthy < nnetman_peers_configured
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "Peer unhealthy in n-netman"

      - alert: NNetManReconcileErrors
        expr: rate(nnetman_reconciliation_errors_total[5m]) > 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "n-netman reconciliation failing"
```

---

## Healthchecks

Endpoints HTTP para verificação de saúde.

### Configuração

```yaml
observability:
  healthcheck:
    enabled: true
    listen:
      address: "127.0.0.1"
      port: 9110
```

### Endpoints

#### /livez

Verifica se o processo está vivo.

```bash
curl http://127.0.0.1:9110/livez
```

**Resposta:**
```
OK
```

**Status codes:**
- `200`: Processo rodando
- `503`: Processo em shutdown

**Uso:** Liveness probe para Kubernetes ou systemd.

#### /readyz

Verifica se o agente está pronto para receber tráfego.

```bash
curl http://127.0.0.1:9110/readyz
```

**Resposta:**
```
OK
```

**Status codes:**
- `200`: Pronto (config carregada, gRPC listening)
- `503`: Não pronto (inicializando)

**Uso:** Readiness probe para Kubernetes.

#### /healthz

Verificação geral de saúde.

```bash
curl http://127.0.0.1:9110/healthz
```

**Resposta:**
```json
{
  "status": "healthy",
  "checks": {
    "config": "ok",
    "grpc": "ok",
    "reconciler": "ok"
  }
}
```

**Status codes:**
- `200`: Todos os checks passaram
- `503`: Algum check falhou

#### /status

Status detalhado do sistema (JSON).

```bash
curl http://127.0.0.1:9110/status
```

**Resposta:**
```json
{
  "node": {
    "id": "host-a-01",
    "hostname": "host-a"
  },
  "overlays": [
    {
      "vni": 100,
      "bridge": "br-prod",
      "bridge_ip": "10.100.0.1/24",
      "vxlan": "vxlan100",
      "status": "up"
    }
  ],
  "peers": [
    {
      "id": "host-b",
      "endpoint": "192.168.56.12:9898",
      "status": "healthy",
      "last_seen": "2026-01-23T09:51:00Z",
      "routes_received": 2
    }
  ],
  "routes": {
    "exported": 2,
    "installed": 4
  }
}
```

**Uso:** Dashboards, debugging, integração com monitoring.

---

## Como Depurar Problemas Comuns

### Peer não conecta

**Sintomas:**
- Status: `disconnected` ou `unhealthy`
- Rotas não instaladas

**Diagnóstico:**
```bash
# Verificar conectividade underlay
ping <peer-underlay-ip>

# Verificar porta gRPC
nc -zv <peer-ip> 9898

# Verificar firewall
sudo iptables -L -n | grep 9898

# Verificar logs
journalctl -u n-netman | grep "peer" | tail -20
```

**Possíveis causas:**
- Firewall bloqueando porta 9898/tcp
- Peer não está rodando
- Problema de DNS/resolução
- TLS mismatch (um lado com TLS, outro sem)

### Rotas não instaladas

**Sintomas:**
- `nnet routes` mostra 0 rotas instaladas
- Tráfego não alcança destino

**Diagnóstico:**
```bash
# Verificar rotas no kernel
ip route show table 100

# Verificar se ip rules existem
ip rule show | grep br-prod

# Verificar logs de route install
journalctl -u n-netman | grep "route installed"
```

**Possíveis causas:**
- `routing.import.accept_all: false` e prefixo não está em `allow`
- Prefixo está em `deny`
- Tabela de roteamento errada
- `lookup_rules.enabled: false` (tráfego não consulta tabela)

### VXLAN não funciona

**Sintomas:**
- Interface VXLAN existe mas tráfego não passa
- ARP não resolve

**Diagnóstico:**
```bash
# Verificar interface
ip -d link show vxlan100

# Verificar FDB
bridge fdb show dev vxlan100

# Verificar se VXLAN está attached à bridge
bridge link show | grep vxlan

# Tcpdump no underlay
tcpdump -i ens3 udp port 4789
```

**Possíveis causas:**
- MTU incorreto (fragmentação)
- FDB entries não criadas
- VXLAN não attached à bridge
- Firewall bloqueando UDP 4789

### Métricas não aparecem

**Sintomas:**
- `/metrics` retorna erro ou vazio

**Diagnóstico:**
```bash
# Verificar se endpoint responde
curl -v http://127.0.0.1:9109/metrics

# Verificar bind address
netstat -tlnp | grep 9109

# Verificar logs
journalctl -u n-netman | grep "metrics"
```

**Possíveis causas:**
- `metrics.enabled: false` no config
- Porta já em uso
- Bind em 127.0.0.1 e tentando acessar externamente

### Logs excessivos

**Solução:** Ajustar nível de log

```yaml
observability:
  logging:
    level: "warn"     # Reduz verbosidade
```

Para debug temporário sem editar config:
```bash
# Ainda não implementado - workaround:
# Edite o config temporariamente
```

---

## Integração com Grafana

Dashboard sugerido com painéis:

1. **Overview**
   - Número de peers healthy/unhealthy
   - Rotas exportadas vs instaladas
   - Taxa de reconciliação

2. **Peers**
   - Status por peer (singlestat)
   - Latência gRPC por peer
   - Rotas por peer

3. **Roteamento**
   - Rotas instaladas ao longo do tempo
   - Expiração de rotas
   - Withdraw events

4. **Performance**
   - Duração de reconciliação
   - Erros de reconciliação
   - Uso de CPU/memória (via node_exporter)
