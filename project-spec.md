# n-netman — Nishi Network Manager

## 1. Visão Geral

O **n-netman** é um agente leve para criação e gerenciamento de **overlays VXLAN L3/L2** entre hosts Linux rodando **KVM/libvirt (virsh)**.

O objetivo é permitir que redes virtuais distribuídas sejam criadas de forma **declarativa e simples**, integrando-se ao **netplan** para a camada underlay e ao **libvirt** para a camada de virtualização.

Ele elimina a necessidade de soluções complexas como **OVS**, oferecendo um **control-plane minimalista**.

### Estado atual (implementado)

* Criação/atualização de VXLAN e bridges Linux
* Sincronização de FDB para peers configurados
* CLI para `apply`, `status`, `routes` (exportadas do config) e `doctor`
* Healthchecks HTTP e endpoint de métricas disponíveis

### Em progresso

* Troca real de rotas via gRPC
* Status de peers e health do control-plane
* Leitura de netplan e integração libvirt

---

## 2. Arquitetura

### 2.1. Camadas

| Camada            | Responsabilidade                                   |
| ----------------- | -------------------------------------------------- |
| **Underlay**      | Interfaces físicas e rotas do host (via netplan)   |
| **Virtualização** | Integração com bridges e networks do libvirt       |
| **Overlay**       | Criação de VXLAN e bridges para interligação L2/L3 |
| **Control Plane** | Troca de rotas e estado entre agentes              |

---

## 3. Componentes do Sistema

### 3.1. Agente (Daemon)

O daemon:

* Lê o YAML do n-netman e valida a configuração
* Cria bridges Linux e interfaces VXLAN
* Sincroniza entradas FDB para peers configurados
* Inicia healthchecks e endpoint de métricas
* Sobe o control-plane gRPC e tenta conectar aos peers
* Reconcilia o estado continuamente

### 3.2. CLI

O CLI interage com o daemon e executa comandos de suporte.

| Comando       | Função                                                        |
| ------------- | ------------------------------------------------------------- |
| `nnet apply`  | Aplica a configuração YAML e reconcilia o estado              |
| `nnet status` | Mostra VXLAN, bridges e peers configurados (status `unknown`) |
| `nnet routes` | Lista rotas exportadas a partir do config                      |
| `nnet doctor` | Executa diagnóstico da rede e do ambiente                     |

---

## 4. Modelo de Configuração

### 4.1. Estrutura Principal

```yaml
node:
netplan:
kvm:
overlay:
routing:
topology:
security:
observability:
```

---

### 4.2. Node

Define a identidade do host.

| Campo      | Descrição                 |
| ---------- | ------------------------- |
| `id`       | Identificador único do nó |
| `hostname` | Nome do host              |
| `tags`     | Metadados opcionais       |

---

### 4.3. Netplan

Usado para inferir interfaces e rotas underlay.

| Campo                              | Descrição                         |
| ---------------------------------- | --------------------------------- |
| `enabled`                          | Ativa leitura do netplan          |
| `config_paths`                     | Diretórios YAML do netplan        |
| `underlay.prefer_interfaces`       | Lista de interfaces preferenciais |
| `underlay.prefer_address_families` | IPv4/IPv6                         |

---

### 4.4. KVM / Libvirt

Gerencia bridges e integração com VMs.

| Campo              | Descrição                           |
| ------------------ | ----------------------------------- |
| `mode`             | `linux-bridge` ou `libvirt-network` |
| `bridges[].name`   | Nome da bridge Linux                |
| `bridges[].manage` | Se o agente cria e controla         |
| `attach.targets[]` | Mapeamento de VM → bridge           |

---

### 4.5. Overlay VXLAN

Define os túneis e a malha de conexão.

| Campo                            | Descrição                   |
| -------------------------------- | --------------------------- |
| `vxlan.vni`                      | VXLAN Network Identifier    |
| `vxlan.bridge`                   | Bridge Linux associada      |
| `vxlan.dstport`                  | Porta UDP do VXLAN          |
| `peers[].endpoint.address`       | IP do peer                  |
| `peers[].endpoint.via_interface` | Interface underlay opcional |

---

## 5. Roteamento

Define como rotas são anunciadas e aprendidas.

### 5.1. Exportação

| Campo                    | Descrição                    |
| ------------------------ | ---------------------------- |
| `export_all`             | Anunciar todas as rotas      |
| `networks[]`             | Lista de redes específicas   |
| `include_connected`      | Rotas diretamente conectadas |
| `include_netplan_static` | Rotas estáticas do netplan   |

### 5.2. Importação

| Campo                 | Descrição                        |
| --------------------- | -------------------------------- |
| `accept_all`          | Aceita todas as rotas aprendidas |
| `allow[]`             | Lista de rotas permitidas        |
| `deny[]`              | Rotas rejeitadas                 |
| `install.table`       | Tabela de roteamento alvo        |
| `route_lease_seconds` | Tempo de validade das rotas      |

---

## 6. Topologia

Controla a forma como os nós se conectam.

| Campo                                  | Descrição                                    |
| -------------------------------------- | -------------------------------------------- |
| `mode`                                 | `direct-preferred`, `full-mesh`, `hub-spoke` |
| `relay_fallback`                       | Permite fallback via outros nós              |
| `transit`                              | `deny` (default) ou `allow`                  |
| `transit_policy.allowed_transit_peers` | Quem pode rotear tráfego de terceiros        |

---

## 7. Segurança

Define o transporte e autenticação do control-plane.

| Campo         | Descrição               |
| ------------- | ----------------------- |
| `transport`   | gRPC                    |
| `tls.enabled` | Usa TLS                 |
| `psk_ref`     | Chave pré-compartilhada |

---

## 8. Observabilidade

Permite acompanhamento e depuração.

| Campo                 | Descrição         |
| --------------------- | ----------------- |
| `logging.level`       | Nível de log      |
| `metrics.enabled`     | Exporta métricas  |
| `healthcheck.enabled` | Endpoint de saúde |

---

## 9. Roadmap de Evolução

| Fase    | Funcionalidade                   |
| ------- | -------------------------------- |
| **MVP** | VXLAN + bridge + export-all      |
| **v1**  | Export seletivo + leases         |
| **v2**  | Topologia mesh + transit         |
| **v3**  | Integração opcional com FRR/BIRD |

---

## 10. Objetivos do Projeto

* Simplicidade operacional
* Portabilidade
* Compatibilidade com netplan e libvirt
* Controle explícito de topologia e trânsito
* Clareza de arquitetura e debugging
