package pki

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// GenerateHostCert generates a host certificate signed by the provided CA.
func GenerateHostCert(outputDir, caCertPath, caKeyPath, hostname string, ips []net.IP, validityDays int) error {
	// 1. Load CA Certificate and Key
	caCert, caKey, err := loadCA(caCertPath, caKeyPath)
	if err != nil {
		return fmt.Errorf("failed to load CA: %w", err)
	}

	// 2. Generate Host Private Key (RSA 2048)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate host key: %w", err)
	}

	// 3. Prepare Certificate Template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"n-netman"},
			CommonName:   hostname,
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Duration(validityDays) * 24 * time.Hour),

		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},

		DNSNames:    []string{hostname, "localhost"},
		IPAddresses: append([]net.IP{net.ParseIP("127.0.0.1")}, ips...),
	}

	// 4. Sign the certificate with the CA
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("failed to create host certificate: %w", err)
	}

	// 5. Save to files
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Save Host Certificate
	hostCertPath := filepath.Join(outputDir, fmt.Sprintf("%s.crt", hostname))
	hostCertFile, err := os.Create(hostCertPath)
	if err != nil {
		return fmt.Errorf("failed to create host cert file: %w", err)
	}
	defer hostCertFile.Close()

	if err := pem.Encode(hostCertFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return fmt.Errorf("failed to encode host cert: %w", err)
	}

	// Save Host Key
	hostKeyPath := filepath.Join(outputDir, fmt.Sprintf("%s.key", hostname))
	hostKeyFile, err := os.OpenFile(hostKeyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create host key file: %w", err)
	}
	defer hostKeyFile.Close()

	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	if err := pem.Encode(hostKeyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes}); err != nil {
		return fmt.Errorf("failed to encode host key: %w", err)
	}

	return nil
}

// loadCA loads the CA certificate and private key from files.
func loadCA(certPath, keyPath string) (*x509.Certificate, *rsa.PrivateKey, error) {
	// Read Cert
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read CA cert %s: %w", certPath, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("failed to decode CA cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA cert: %w", err)
	}

	// Read Key
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read CA key %s: %w", keyPath, err)
	}
	block, _ = pem.Decode(keyPEM)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		return nil, nil, fmt.Errorf("failed to decode CA key PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA key: %w", err)
	}

	return cert, key, nil
}
