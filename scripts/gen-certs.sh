#!/bin/bash
# gen-certs.sh - Gera CA e certificados self-signed para lab n-netman
# Uso: ./scripts/gen-certs.sh [output_dir]
#
# Gera certificados para mTLS entre hosts do cluster Vagrant.
# NÃO use estes certificados em produção!

set -euo pipefail

OUTPUT_DIR="${1:-/tmp/n-netman-certs}"
HOSTS="${2:-host-a host-b host-c}"
VALIDITY_DAYS=365

echo "🔐 Gerando certificados n-netman em: $OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

# ============================================
# 1. Gerar CA (Certificate Authority)
# ============================================
echo "📜 Gerando CA..."
openssl genrsa -out "$OUTPUT_DIR/ca.key" 4096 2>/dev/null
openssl req -x509 -new -nodes \
    -key "$OUTPUT_DIR/ca.key" \
    -sha256 \
    -days "$VALIDITY_DAYS" \
    -out "$OUTPUT_DIR/ca.crt" \
    -subj "/CN=n-netman-ca/O=n-netman" \
    2>/dev/null

echo "   ✅ CA gerada: $OUTPUT_DIR/ca.crt"

# ============================================
# 2. Gerar certificados por host
# ============================================
for HOST in $HOSTS; do
    echo "📜 Gerando certificado para: $HOST"
    
    # Gerar chave privada
    openssl genrsa -out "$OUTPUT_DIR/$HOST.key" 2048 2>/dev/null
    
    # Criar CSR (Certificate Signing Request)
    openssl req -new \
        -key "$OUTPUT_DIR/$HOST.key" \
        -out "$OUTPUT_DIR/$HOST.csr" \
        -subj "/CN=$HOST/O=n-netman" \
        2>/dev/null
    
    # Criar arquivo de extensões para SAN (Subject Alternative Name)
    cat > "$OUTPUT_DIR/$HOST.ext" <<EOF
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth, clientAuth
subjectAltName = DNS:$HOST, DNS:localhost, IP:127.0.0.1
EOF
    
    # Assinar com a CA
    openssl x509 -req \
        -in "$OUTPUT_DIR/$HOST.csr" \
        -CA "$OUTPUT_DIR/ca.crt" \
        -CAkey "$OUTPUT_DIR/ca.key" \
        -CAcreateserial \
        -out "$OUTPUT_DIR/$HOST.crt" \
        -days "$VALIDITY_DAYS" \
        -sha256 \
        -extfile "$OUTPUT_DIR/$HOST.ext" \
        2>/dev/null
    
    # Limpar arquivos temporários
    rm -f "$OUTPUT_DIR/$HOST.csr" "$OUTPUT_DIR/$HOST.ext"
    
    echo "   ✅ $HOST.crt + $HOST.key"
done

# ============================================
# 3. Definir permissões seguras
# ============================================
chmod 644 "$OUTPUT_DIR"/*.crt
chmod 600 "$OUTPUT_DIR"/*.key

# ============================================
# 4. Resumo
# ============================================
echo ""
echo "✨ Certificados gerados com sucesso!"
echo ""
echo "📁 Arquivos em $OUTPUT_DIR:"
ls -la "$OUTPUT_DIR"
echo ""
echo "📋 Uso no n-netman.yaml:"
echo "   security:"
echo "     control_plane:"
echo "       tls:"
echo "         enabled: true"
echo "         cert_file: \"$OUTPUT_DIR/\$HOSTNAME.crt\""
echo "         key_file: \"$OUTPUT_DIR/\$HOSTNAME.key\""
echo "         ca_file: \"$OUTPUT_DIR/ca.crt\""
echo ""
echo "💡 Para copiar para as VMs Vagrant:"
echo "   vagrant ssh host-a -c 'sudo mkdir -p /etc/n-netman/tls'"
echo "   vagrant scp $OUTPUT_DIR/host-a.* host-a:/tmp/"
echo "   vagrant scp $OUTPUT_DIR/ca.crt host-a:/tmp/"
echo "   vagrant ssh host-a -c 'sudo mv /tmp/host-a.* /tmp/ca.crt /etc/n-netman/tls/'"
