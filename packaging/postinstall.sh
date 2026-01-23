#!/bin/bash
set -e

# Reload systemd
systemctl daemon-reload

echo "n-netman installed successfully!"
echo ""
echo "Next steps:"
echo "  1. Copy config:  sudo cp /etc/n-netman/n-netman.yaml.example /etc/n-netman/n-netman.yaml"
echo "  2. Edit config:  sudo nano /etc/n-netman/n-netman.yaml"
echo "  3. Start:        sudo systemctl start n-netman"
echo "  4. Enable:       sudo systemctl enable n-netman"
