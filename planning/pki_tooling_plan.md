# PKI Tooling Implementation Plan

## Goal
Replace the external `scripts/gen-certs.sh` shell script with a native Go implementation integrated into the `nnet` CLI. This improves portability, removes dependencies (openssl), and provides a better user experience for setting up mTLS.

## User Review Required
- **Breaking Change**: None for existing users, but changes the recommended workflow for new setups.
- **Security**: The generated keys will be stored in the file system. Default permissions must be secure (600 for keys).

## Proposed Changes

### 1. New Package: `internal/pki`
This package will encapsulate all crypto operations, using Go's standard `crypto/x509` library.

#### [NEW] `internal/pki/ca.go`
- `GenerateCA(outputDir string, validityDays int) error`
- Generates a self-signed Root CA certificate and private key (RSA 4096).
- Saves to `ca.crt` and `ca.key`.

#### [NEW] `internal/pki/host.go`
- `GenerateHostCert(outputDir, caCertPath, caKeyPath, hostname string, ips []net.IP, validityDays int) error`
- Generates a private key (RSA 2048) for the host.
- Creates a CSR.
- Signs the CSR using the provided CA to create the host certificate.
- Adds Subject Alternative Names (SANs) for DNS (hostname, localhost) and IPs (loopback + provided IPs).

### 2. New CLI Command: `cmd/nnet/cert.go`
Implement the `nnet cert` subcommand structure using `cobra`.

#### `nnet cert init-ca`
- Flags: `--output-dir`, `--days`
- Calls `pki.GenerateCA`.

#### `nnet cert gen-host`
- Flags: `--output-dir`, `--ca-cert`, `--ca-key`, `--host`, `--ip` (array), `--days`
- Calls `pki.GenerateHostCert`.

### 3. CLI Integration: `cmd/nnet/main.go`
- Register `certCmd()` in the root command.

## Verification Plan

### Automated Tests
- Unit tests for `internal/pki` to ensure certificates are valid and correctly signed.
- Verify key permissions are set to 0600.

### Manual Verification
1. Run `go run ./cmd/nnet cert init-ca --output-dir /tmp/certs`
2. Run `go run ./cmd/nnet cert gen-host --host node-1 --ip 192.168.1.10 --output-dir /tmp/certs --ca-cert /tmp/certs/ca.crt --ca-key /tmp/certs/ca.key`
3. Inspect generated files using `openssl x509 -text -noout -in /tmp/certs/node-1.crt` to verify SANs and Issuer.
