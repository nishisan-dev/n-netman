# -*- mode: ruby -*-
# vi: set ft=ruby :

# n-netman Lab - 3 VMs para testar multi-overlay VXLAN
#
# Uso:
#   vagrant up
#   vagrant ssh host-a
#
# Redes underlay:
#   Produção:   192.168.56.0/24 (VirtualBox host-only)
#   Management: 192.168.57.0/24 (VirtualBox host-only)

NODES = [
  { 
    name: "host-a", 
    prod_ip: "192.168.56.11", 
    mgmt_ip: "192.168.57.11",
    prod_net: "172.16.10.0/24",   # Overlay network for production
    mgmt_net: "10.200.10.0/24"    # Overlay network for management
  },
  { 
    name: "host-b", 
    prod_ip: "192.168.56.12", 
    mgmt_ip: "192.168.57.12",
    prod_net: "172.16.20.0/24",
    mgmt_net: "10.200.20.0/24"
  },
  { 
    name: "host-c", 
    prod_ip: "192.168.56.13", 
    mgmt_ip: "192.168.57.13",
    prod_net: "172.16.30.0/24",
    mgmt_net: "10.200.30.0/24"
  },
]

Vagrant.configure("2") do |config|
  config.vm.box = "ubuntu/jammy64"
  config.vm.box_check_update = false

  # Configuração comum para todas as VMs
  config.vm.provider "virtualbox" do |vb|
    vb.memory = "512"
    vb.cpus = 1
    vb.linked_clone = true
  end

  NODES.each do |node|
    config.vm.define node[:name] do |vm|
      vm.vm.hostname = node[:name]
      
      # Rede 1: Produção (underlay for VNI 100)
      vm.vm.network "private_network", ip: node[:prod_ip]
      
      # Rede 2: Management (underlay for VNI 200)
      vm.vm.network "private_network", ip: node[:mgmt_ip]

      # Provisioning: instalar Go e compilar n-netman
      vm.vm.provision "shell", inline: <<-SHELL
        set -e
        
        # Instalar dependências
        apt-get update
        apt-get install -y build-essential iproute2 bridge-utils

        # Instalar Go 1.23
        if ! command -v go &> /dev/null; then
          wget -q https://go.dev/dl/go1.23.0.linux-amd64.tar.gz
          rm -rf /usr/local/go
          tar -C /usr/local -xzf go1.23.0.linux-amd64.tar.gz
          rm go1.23.0.linux-amd64.tar.gz
          echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
        fi
        export PATH=$PATH:/usr/local/go/bin

        # Carregar módulos do kernel
        modprobe vxlan
        modprobe bridge
        echo "vxlan" >> /etc/modules
        echo "bridge" >> /etc/modules

        # Criar diretório de config
        mkdir -p /etc/n-netman
      SHELL

      # Copiar código fonte (synced folder)
      vm.vm.synced_folder ".", "/home/vagrant/n-netman"

      # Provisioning: build e config específica do nó
      vm.vm.provision "shell", inline: <<-SHELL
        set -e
        export PATH=$PATH:/usr/local/go/bin
        
        cd /home/vagrant/n-netman
        make build
        cp bin/nnetd /usr/local/bin/
        cp bin/nnet /usr/local/bin/
        
        echo "n-netman instalado em #{node[:name]}"
      SHELL

      # Gerar config YAML para este nó
      vm.vm.provision "shell", inline: generate_config(node, NODES)
    end
  end
end

def generate_config(node, all_nodes)
  peers = all_nodes.reject { |n| n[:name] == node[:name] }
  
  peers_yaml = peers.map do |p|
    <<-PEER
    - id: "#{p[:name]}"
      endpoint:
        address: "#{p[:prod_ip]}"
      health:
        keepalive_interval_ms: 1500
        dead_after_ms: 6000
    PEER
  end.join("\n")

  <<-SHELL
cat > /etc/n-netman/n-netman.yaml << 'EOF'
version: 2

node:
  id: "#{node[:name]}"
  hostname: "#{node[:name]}"
  tags:
    - "vagrant-lab"
    - "multi-overlay"

# Multi-Overlay Configuration (v2)
# VNI 100: Production (via eth1/enp0s8)
# VNI 200: Management (via eth2/enp0s9)
overlays:
  - vni: 100
    name: "vxlan-prod"
    dstport: 4789
    mtu: 1450
    learning: true
    bridge: "br-prod"
    underlay_interface: "enp0s8"
    routing:
      export:
        networks:
          - "#{node[:prod_net]}"
        metric: 100
      import:
        accept_all: true
        install:
          table: 100
          flush_on_peer_down: true
          route_lease_seconds: 30

  - vni: 200
    name: "vxlan-mgmt"
    dstport: 4789
    mtu: 1450
    learning: true
    bridge: "br-mgmt"
    underlay_interface: "enp0s9"
    routing:
      export:
        networks:
          - "#{node[:mgmt_net]}"
        metric: 200
      import:
        accept_all: true
        install:
          table: 200
          flush_on_peer_down: true
          route_lease_seconds: 30

# Legacy peers section (required for FDB sync)
overlay:
  peers:
#{peers_yaml}

topology:
  mode: "direct-preferred"
  transit: "deny"

security:
  control_plane:
    transport: "grpc"
    listen:
      address: "0.0.0.0"
      port: 9898

observability:
  logging:
    level: "debug"
    format: "json"
  metrics:
    enabled: true
    listen:
      address: "0.0.0.0"
      port: 9109
  healthcheck:
    enabled: true
    listen:
      address: "0.0.0.0"
      port: 9110
EOF

echo "Config v2 multi-overlay gerada para #{node[:name]}"
cat /etc/n-netman/n-netman.yaml
  SHELL
end

