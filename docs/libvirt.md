# Integração libvirt

O n-netman oferece integração com **libvirt/KVM** para gerenciar interfaces de VMs conectadas às bridges de overlay.

## Diagrama

![Fluxo de integração](https://uml.nishisan.dev/proxy?src=https://raw.githubusercontent.com/nishisan-dev/n-netman/main/docs/diagrams/libvirt_integration.puml)

---

## Pré-requisitos

- libvirt instalado e funcionando (`libvirtd.service`)
- n-netman configurado e com bridges criadas (`nnet apply`)
- Acesso root para modificar VMs

---

## Comandos Disponíveis

| Comando | Descrição |
|---------|-----------|
| `nnet libvirt enable` | Configura dependência systemd |
| `nnet libvirt disable` | Remove dependência systemd |
| `nnet libvirt status` | Mostra estado da integração |
| `nnet libvirt list-vms` | Lista VMs e suas interfaces |
| `nnet libvirt attach <vm>` | Adiciona interface a uma VM |
| `nnet libvirt detach <vm>` | Remove interface de uma VM |

---

## Configurar Dependência Systemd

Para garantir que as bridges existam antes das VMs iniciarem no boot:

```bash
sudo nnet libvirt enable
```

Isso cria um drop-in em `/etc/systemd/system/libvirt.service.d/n-netman.conf` que faz o `libvirt.service` depender do `n-netman.service`.

Para reverter:

```bash
sudo nnet libvirt disable
```

---

## Listar VMs

```bash
# Apenas VMs rodando
nnet libvirt list-vms

# Incluir VMs desligadas
nnet libvirt list-vms --all
```

Exemplo de saída:

```
VM NAME   STATE    MAC                BRIDGE
───────   ─────    ───                ──────
web-01    running  52:54:00:11:22:33  br-prod ✓
          running  52:54:00:44:55:66  virbr0
db-01     shut off 52:54:00:77:88:99  br-prod ✓
```

O `✓` indica bridges gerenciadas pelo n-netman.

---

## Attach de Interface

Adiciona uma **nova interface** à VM, conectada a uma bridge:

```bash
sudo nnet libvirt attach web-01 --bridge br-prod
```

Com MAC específico:

```bash
sudo nnet libvirt attach web-01 --bridge br-prod --mac 52:54:00:12:34:56
```

A interface é:
- **Persistida** no domain XML (sobrevive reboot)
- **Aplicada live** se a VM estiver rodando (hot-plug)

---

## Detach de Interface

Remove uma interface da VM pelo **MAC address**:

```bash
sudo nnet libvirt detach web-01 --mac 52:54:00:12:34:56
```

---

## Status da Integração

Ver estado completo:

```bash
nnet libvirt status
```

Exemplo:

```
🔗 Libvirt Integration Status
─────────────────────────────────────────
  ✓ Systemd dependency configured (libvirt → n-netman)
  • n-netman.service: active
  • libvirtd.service: active

🌉 Managed Bridges:
─────────────────────────────────────────
  • br-prod (VNI 100) - UP
  • br-mgmt (VNI 200) - UP

🖥️  VMs using n-netman bridges:
─────────────────────────────────────────
  • web-01 → br-prod (MAC: 52:54:00:11:22:33)
  • db-01 → br-prod (MAC: 52:54:00:77:88:99)
```

---

## Troubleshooting

### Bridge não existe

```
✗ Error: bridge 'br-prod' does not exist.
  Did you run 'nnet apply' first?
```

**Solução:** Execute `sudo nnet apply` para criar as bridges.

### VM não inicia com interface

Se a VM falha ao iniciar com erro de bridge não encontrada:

**Solução:** Configure a dependência systemd:

```bash
sudo nnet libvirt enable
sudo systemctl restart libvirtd
```

### Interface não aparece na VM

Verifique se a VM estava rodando durante o attach. Se a VM estava desligada, a interface será ativada no próximo boot.
