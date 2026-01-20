# -*- mode: ruby -*-
# vi: set ft=ruby :

# n-netman Lab - 3 VMs para testar overlay VXLAN
#
# Uso:
#   vagrant up
#   vagrant ssh host-a
#
# Rede underlay: 192.168.56.0/24 (VirtualBox host-only)
#   host-a: 192.168.56.11
#   host-b: 192.168.56.12
#   host-c: 192.168.56.13

NODES = [
  { name: "host-a", ip: "192.168.56.11", overlay_net: "172.16.10.0/24" },
  { name: "host-b", ip: "192.168.56.12", overlay_net: "172.16.20.0/24" },
  { name: "host-c", ip: "192.168.56.13", overlay_net: "172.16.30.0/24" },
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
      
      # Rede host-only para underlay
      vm.vm.network "private_network", ip: node[:ip]

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
        address: "#{p[:ip]}"
      health:
        keepalive_interval_ms: 1500
        dead_after_ms: 6000
    PEER
  end.join("\n")

  <<-SHELL
cat > /etc/n-netman/n-netman.yaml << 'EOF'
version: 1

node:
  id: "#{node[:name]}"
  hostname: "#{node[:name]}"
  tags:
    - "vagrant-lab"

overlay:
  vxlan:
    vni: 100
    name: "vxlan100"
    dstport: 4789
    mtu: 1450
    learning: true
    bridge: "br-nnet-100"

  peers:
#{peers_yaml}

routing:
  enabled: true
  export:
    networks:
      - "#{node[:overlay_net]}"
    metric: 100
  import:
    accept_all: true
    install:
      table: 100
      flush_on_peer_down: true
      route_lease_seconds: 30

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

echo "Config gerada para #{node[:name]}"
cat /etc/n-netman/n-netman.yaml
  SHELL
end
