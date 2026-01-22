// Package controlplane implements the gRPC control plane for route exchange.
// This file contains TLS configuration helpers for secure peer communication.
package controlplane

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"

	"github.com/nishisan-dev/n-netman/internal/config"
)

// LoadServerTLSConfig creates TLS credentials for the gRPC server.
// It configures mTLS (mutual TLS) when a CA file is provided, requiring
// clients to present valid certificates signed by the same CA.
func LoadServerTLSConfig(cfg *config.TLSConfig) (credentials.TransportCredentials, error) {
	// Load server certificate and key
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// If CA file is provided, enable mTLS (require and verify client certs)
	if cfg.CAFile != "" {
		caPool, err := loadCAPool(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.ClientCAs = caPool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return credentials.NewTLS(tlsConfig), nil
}

// LoadClientTLSConfig creates TLS credentials for the gRPC client.
// It configures the client to verify the server certificate using the CA,
// and presents its own certificate for mTLS authentication.
func LoadClientTLSConfig(cfg *config.TLSConfig) (credentials.TransportCredentials, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Load client certificate if provided (for mTLS)
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	// Load CA for server verification
	if cfg.CAFile != "" {
		caPool, err := loadCAPool(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.RootCAs = caPool
	} else {
		// No CA provided - this is insecure, only for development
		tlsConfig.InsecureSkipVerify = true
	}

	return credentials.NewTLS(tlsConfig), nil
}

// loadCAPool loads a certificate authority file into a certificate pool.
func loadCAPool(caFile string) (*x509.CertPool, error) {
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA file %s: %w", caFile, err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caData) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", caFile)
	}

	return caPool, nil
}
