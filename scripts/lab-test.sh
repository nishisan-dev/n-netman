#!/bin/bash
# n-netman Lab Test Script
# Executar em cada VM após o Vagrant up

set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${YELLOW}=== n-netman Lab Test ===${NC}"
echo ""

# 1. Verificar config
echo -e "${YELLOW}1. Verificando configuração...${NC}"
nnet -c /etc/n-netman/n-netman.yaml doctor
echo ""

# 2. Aplicar configuração (criar bridge/vxlan)
echo -e "${YELLOW}2. Aplicando configuração...${NC}"
sudo nnet -c /etc/n-netman/n-netman.yaml apply
echo ""

# 3. Verificar interfaces criadas
echo -e "${YELLOW}3. Verificando interfaces...${NC}"
ip -d link show vxlan100 2>/dev/null && echo -e "${GREEN}✓ vxlan100 existe${NC}" || echo -e "${RED}✗ vxlan100 não existe${NC}"
ip -d link show br-nnet-100 2>/dev/null && echo -e "${GREEN}✓ br-nnet-100 existe${NC}" || echo -e "${RED}✗ br-nnet-100 não existe${NC}"
echo ""

# 4. Iniciar daemon em background
echo -e "${YELLOW}4. Iniciando daemon...${NC}"
sudo pkill nnetd 2>/dev/null || true
sudo nnetd -config /etc/n-netman/n-netman.yaml &
DAEMON_PID=$!
echo "Daemon PID: $DAEMON_PID"
sleep 3
echo ""

# 5. Verificar status
echo -e "${YELLOW}5. Status:${NC}"
nnet -c /etc/n-netman/n-netman.yaml status
echo ""

# 6. Verificar rotas
echo -e "${YELLOW}6. Rotas na tabela 100:${NC}"
ip route show table 100 2>/dev/null || echo "(tabela vazia ou não existe)"
echo ""

# 7. Verificar FDB
echo -e "${YELLOW}7. FDB entries:${NC}"
bridge fdb show dev vxlan100 2>/dev/null | head -10 || echo "(sem entries)"
echo ""

# 8. Health check
echo -e "${YELLOW}8. Health check:${NC}"
curl -s http://127.0.0.1:9110/healthz && echo "" || echo -e "${RED}Health check falhou${NC}"
echo ""

echo -e "${GREEN}=== Teste concluído ===${NC}"
echo ""
echo "Para parar o daemon: sudo kill $DAEMON_PID"
echo "Para ver logs: sudo journalctl -f (se usando systemd)"
echo "Para testar ping entre VMs: ping -I br-nnet-100 <overlay-ip>"
