package pki

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// GenerateCA generates a self-signed Root CA certificate and private key.
func GenerateCA(outputDir string, validityDays int) error {
	// 1. Generate private key (RSA 4096)
	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// 2. Create Certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"n-netman"},
			CommonName:   "n-netman-ca",
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Duration(validityDays) * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	// 3. Self-sign the certificate
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// 4. Save to files
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Save Certificate (PEM)
	caCertPath := filepath.Join(outputDir, "ca.crt")
	caCertFile, err := os.Create(caCertPath)
	if err != nil {
		return fmt.Errorf("failed to create ca.crt: %w", err)
	}
	defer caCertFile.Close()

	if err := pem.Encode(caCertFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return fmt.Errorf("failed to encode ca.crt: %w", err)
	}

	// Save Private Key (PEM) - permissions 0600
	caKeyPath := filepath.Join(outputDir, "ca.key")
	caKeyFile, err := os.OpenFile(caKeyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create ca.key: %w", err)
	}
	defer caKeyFile.Close()

	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	if err := pem.Encode(caKeyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes}); err != nil {
		return fmt.Errorf("failed to encode ca.key: %w", err)
	}

	return nil
}
