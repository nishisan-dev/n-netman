#!/bin/bash
set -e

# Reload systemd
systemctl daemon-reload

echo "n-netman-agent installed successfully!"
echo ""
echo "Next steps:"
echo "  1. Copy config:  sudo cp /etc/n-netman/agent.yaml.example /etc/n-netman/agent.yaml"
echo "  2. Edit config:  sudo nano /etc/n-netman/agent.yaml"
echo "  3. Install key:  copy the segment's shared key to /etc/n-netman/psk/inject.key"
echo "                   and run: sudo chmod 600 /etc/n-netman/psk/inject.key"
echo "  4. Check:        sudo nnet-agent doctor"
echo "  5. Start:        sudo systemctl start n-netman-agent"
echo "  6. Enable:       sudo systemctl enable n-netman-agent"
