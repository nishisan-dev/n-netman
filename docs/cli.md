# CLI

Este documento descreve a interface de linha de comando do n-netman.

## Filosofia do CLI

O CLI do n-netman segue princípios de simplicidade operacional:

1. **Stateless:** Cada invocação é independente, sem estado persistente
2. **Declarativo:** `apply` converge para o estado desejado, não executa ações incrementais
3. **Observável:** Comandos como `status` e `routes` mostram o estado real
4. **Não destrutivo por padrão:** `--dry-run` permite visualizar sem aplicar

## Binários

| Binário | Propósito |
|---------|-----------|
| `nnet` | CLI para operações do usuário |
| `nnetd` | Daemon que executa em background |

## Comandos Disponíveis

### nnet apply

Aplica a configuração e reconcilia o estado.

```bash
# Dry-run (mostra o que seria feito)
nnet -c /etc/n-netman/n-netman.yaml apply --dry-run

# Aplicar de verdade (requer root)
sudo nnet -c /etc/n-netman/n-netman.yaml apply
```

**O que faz:**
1. Carrega e valida o arquivo YAML
2. Cria bridges e interfaces VXLAN
3. Sincroniza FDB entries
4. Configura IPs nas bridges
5. Cria `ip rule` para PBR (se configurado)

**Quando usar:**
- Aplicar uma nova configuração
- Forçar reconciliação após mudança manual
- Validar config antes de iniciar o daemon

**Flags:**
| Flag | Descrição |
|------|-----------|
| `--dry-run` | Mostra o que seria feito sem executar |

**Requer root:** Sim (manipula interfaces de rede)

---

### nnet status

Mostra o status atual do sistema e peers.

```bash
nnet -c /etc/n-netman/n-netman.yaml status
```

**Saída típica:**

```
🖥️  Node: host-a (host-a-01)

📡 VXLAN Interfaces:
─────────────────────────────────────────
  🟢 UP vxlan100 (VNI 100, MTU 1450)
  🟢 UP vxlan200 (VNI 200, MTU 1450)

🌉 Bridges:
─────────────────────────────────────────
  🟢 UP br-prod (IP: 10.100.0.1/24, MTU 1450)
      Attached: [vxlan100]
  🟢 UP br-mgmt (IP: 10.200.1.1/24, MTU 1450)
      Attached: [vxlan200]

👥 Configured Peers:
─────────────────────────────────────────
  ID       ENDPOINT        STATUS                ROUTES
  ──       ────────        ──────                ──────
  host-b   192.168.56.12   🟢 healthy (5s ago)   2
  host-c   192.168.56.13   🟢 healthy (3s ago)   2

📊 Route Statistics:
─────────────────────────────────────────
  📤 Exported:   2 route(s)
  📥 Installed:  4 route(s) across 2 tables
```

**O que mostra:**
- Identidade do nó
- Estado das interfaces VXLAN (UP/DOWN)
- Estado das bridges e IPs configurados
- Status de conectividade com cada peer
- Estatísticas de rotas exportadas e instaladas

**Quando usar:**
- Verificar se o overlay está operacional
- Diagnosticar problemas de conectividade
- Confirmar status de peers após mudanças

**Requer root:** Não (apenas leitura)

---

### nnet routes

Lista as rotas configuradas e instaladas.

```bash
nnet -c /etc/n-netman/n-netman.yaml routes
```

**Saída típica:**

```
📤 Exported Routes (from config):
─────────────────────────────────────────
  VNI   PREFIX            NEXT-HOP      METRIC
  ───   ──────            ────────      ──────
  100   172.16.10.0/24    10.100.0.1    100
  200   10.200.10.0/24    10.200.1.1    200

📥 Installed Routes (learned from peers):
─────────────────────────────────────────
  TABLE  PREFIX            VIA           SOURCE    AGE
  ─────  ──────            ───           ──────    ───
  100    172.16.20.0/24    10.100.0.2    host-b    15s
  100    172.16.30.0/24    10.100.0.3    host-c    12s
  200    10.200.20.0/24    10.200.1.2    host-b    15s
  200    10.200.30.0/24    10.200.1.3    host-c    12s
```

**O que mostra:**
- Rotas que este nó exporta (anuncia para peers)
- Rotas instaladas no kernel (aprendidas de peers)
- Tabela de roteamento de cada rota
- Peer de origem e tempo desde instalação

**Quando usar:**
- Verificar quais rotas estão sendo anunciadas
- Confirmar que rotas de peers estão sendo instaladas
- Debugar problemas de alcançabilidade

**Requer root:** Não (apenas leitura)

---

### nnet doctor

Executa diagnóstico do sistema e ambiente.

```bash
nnet -c /etc/n-netman/n-netman.yaml doctor
```

**Saída típica:**

```
🔍 n-netman System Diagnostic
═══════════════════════════════════════════

📋 Configuration
  ✅ Config file loaded successfully
  ✅ Schema version: 2
  ✅ Overlays defined: 2 (VNI 100, 200)
  ✅ Peers configured: 2

🐧 Kernel Modules
  ✅ vxlan module loaded
  ✅ bridge module loaded

🌐 Network Interfaces
  ✅ vxlan100 exists (UP)
  ✅ vxlan200 exists (UP)
  ✅ br-prod exists (UP, IP: 10.100.0.1/24)
  ✅ br-mgmt exists (UP, IP: 10.200.1.1/24)

📡 FDB Entries
  ✅ vxlan100: 2 peer entries
  ✅ vxlan200: 2 peer entries

🔒 Security
  ⚠️  TLS is disabled (consider enabling for production)

📊 Control Plane
  ✅ gRPC listener ready on :9898
  ✅ 2/2 peers connected

❤️  Health Endpoints
  ✅ Metrics: http://127.0.0.1:9109/metrics
  ✅ Health: http://127.0.0.1:9110/healthz

═══════════════════════════════════════════
Overall Status: ✅ HEALTHY (1 warning)
```

**O que verifica:**
- Configuração válida e completa
- Módulos do kernel carregados (vxlan, bridge)
- Interfaces de rede criadas e UP
- Entradas FDB para peers
- TLS habilitado (warning se não)
- Conectividade com peers
- Endpoints de saúde acessíveis

**Quando usar:**
- Após instalação inicial para validar setup
- Quando há problemas de conectividade
- Antes de reportar bugs

---

### nnet cert

Gerencia PKI interna e gera certificados mTLS.

**Subcomandos:**

#### `nnet cert init-ca`
Inicializa uma nova CA raiz.

```bash
nnet cert init-ca --output-dir /etc/n-netman/certs --days 3650
```

#### `nnet cert gen-host`
Gera certificado de host assinado pela CA.

```bash
nnet cert gen-host \
  --host node-1 \
  --ip 192.168.1.10,10.0.0.1 \
  --ca-cert /etc/n-netman/certs/ca.crt \
  --ca-key /etc/n-netman/certs/ca.key \
  --output-dir /etc/n-netman/certs
```

**Requer root:** Não (mas requer permissão de escrita no diretório de saída)

---

### nnet version

Mostra versão do CLI.

```bash
nnet version
```

**Saída:**
```
nnet dev (commit: unknown, built: unknown)
```

O formato é `nnet <version> (commit: <commit>, built: <date>)`. Em builds de release esses valores são injetados via ldflags; em builds locais aparecem como `dev` / `unknown`.

---

## Daemon: nnetd

O daemon executa em foreground ou como serviço.

```bash
# Foreground (para desenvolvimento/debug)
sudo nnetd -config /etc/n-netman/n-netman.yaml

# Ver versão
nnetd -version
```

**O que faz:**
1. Carrega configuração
2. Inicia servidor de métricas Prometheus
3. Inicia endpoints de healthcheck
4. Inicia servidor gRPC (control-plane)
5. Conecta a peers configurados
6. Executa loop de reconciliação

**Sinais:**
| Sinal | Ação |
|-------|------|
| `SIGINT` | Shutdown graceful |
| `SIGTERM` | Shutdown graceful |
| `SIGHUP` | Reload config (não implementado) |

---

## Opções Globais

| Flag | Descrição |
|------|-----------|
| `-c, --config` | Caminho para arquivo de configuração |
| `-h, --help` | Mostra ajuda |

---

## Exemplos de Uso

### Workflow Típico

```bash
# 1. Validar configuração
nnet -c /etc/n-netman/n-netman.yaml doctor

# 2. Dry-run para ver o que será feito
nnet -c /etc/n-netman/n-netman.yaml apply --dry-run

# 3. Aplicar configuração
sudo nnet -c /etc/n-netman/n-netman.yaml apply

# 4. Verificar status
nnet -c /etc/n-netman/n-netman.yaml status

# 5. Iniciar daemon
sudo nnetd -config /etc/n-netman/n-netman.yaml
```

### Debug de Conectividade

```bash
# Ver rotas instaladas
nnet -c /etc/n-netman/n-netman.yaml routes

# Verificar tabela de roteamento no kernel
ip route show table 100

# Verificar FDB entries
bridge fdb show dev vxlan100

# Verificar interfaces
ip -d link show vxlan100
ip -d link show br-prod
```
