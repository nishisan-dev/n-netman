# n-netman — Documentação

Documentação completa do n-netman, um agente leve para overlays VXLAN L3/L2 em Linux.

## Índice

### Fundamentos

| Documento | Descrição |
|-----------|-----------|
| [overview.md](overview.md) | O que é o n-netman, problema que resolve, princípios de design |
| [architecture.md](architecture.md) | Camadas, componentes internos, fluxos de inicialização e reconciliação |

### Modelo de Rede

| Documento | Descrição |
|-----------|-----------|
| [network-model.md](network-model.md) | VXLAN como L2 carrier, bridges Linux, onde ocorre L3 |
| [routing.md](routing.md) | Export/import de rotas, métricas, leases, conceito de transit |
| [topology.md](topology.md) | Modos direct-preferred, full-mesh, hub-spoke |

### Referência

| Documento | Descrição |
|-----------|-----------|
| [configuration.md](configuration.md) | Estrutura completa do arquivo YAML |
| [cli.md](cli.md) | Comandos disponíveis e exemplos de uso |
| [observability.md](observability.md) | Logs, métricas, healthchecks e troubleshooting |

## Quick Start

```bash
# 1. Build
make build

# 2. Configurar
sudo mkdir -p /etc/n-netman
sudo cp examples/multi-overlay.yaml /etc/n-netman/n-netman.yaml
# Editar o arquivo conforme seu ambiente

# 3. Validar
nnet -c /etc/n-netman/n-netman.yaml doctor

# 4. Aplicar
sudo nnet -c /etc/n-netman/n-netman.yaml apply

# 5. Iniciar daemon
sudo nnetd -config /etc/n-netman/n-netman.yaml
```

## Diagramas

Os diagramas de arquitetura estão em [diagrams/](diagrams/):

- `architecture.puml` — Visão geral dos componentes
- `reconciler-loop.puml` — Fluxo do loop de reconciliação
- `route-exchange.puml` — Troca de rotas entre peers
- `topology.puml` — Topologia de rede

## Convenções

- **YAML:** Exemplos usam schema v2 (multi-overlay)
- **Tabelas:** Rotas instaladas em tabelas 100, 200, etc.
- **Interfaces:** Nomenclatura `vxlan{vni}` e `br-{nome}`
- **Portas:** gRPC 9898, métricas 9109, health 9110
