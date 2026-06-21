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
// It always verifies the server certificate against the configured CA (server
// verification is never skipped) and presents its own certificate for mTLS.
// serverName is the expected identity of the peer (its endpoint address or
// hostname); it must appear in the peer certificate's SANs.
func LoadClientTLSConfig(cfg *config.TLSConfig, serverName string) (credentials.TransportCredentials, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
	}

	// Load client certificate if provided (for mTLS)
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	// A CA is mandatory: without it we cannot authenticate the server, and
	// silently skipping verification would defeat the purpose of enabling TLS.
	if cfg.CAFile == "" {
		return nil, fmt.Errorf("control_plane.tls.ca_file is required when TLS is enabled (refusing to skip server verification)")
	}
	caPool, err := loadCAPool(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	tlsConfig.RootCAs = caPool

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
