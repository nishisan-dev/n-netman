package inject

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// filePSKPrefix is the reference scheme shared with peers[].auth.psk_ref.
const filePSKPrefix = "file:"

// minPSKBytes rejects keys too short to carry meaningful entropy.
const minPSKBytes = 16

// LoadPSK resolves a psk_ref into key material.
//
// Only the "file:/path" form is accepted; a bare path is rejected rather than
// silently guessed, so a malformed reference fails loudly at startup instead of
// producing advertisements nobody can verify.
//
// Surrounding whitespace is trimmed, because keys are routinely created with
// shell redirection that appends a newline. Both sides trim identically.
func LoadPSK(ref string) ([]byte, error) {
	if ref == "" {
		return nil, fmt.Errorf("psk_ref is empty")
	}
	if !strings.HasPrefix(ref, filePSKPrefix) {
		return nil, fmt.Errorf("psk_ref %q must use the %q scheme", ref, filePSKPrefix)
	}
	path := strings.TrimPrefix(ref, filePSKPrefix)
	if path == "" {
		return nil, fmt.Errorf("psk_ref %q has no path", ref)
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat psk file %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("psk path %s is a directory", path)
	}
	// The key authenticates every advertisement on the segment; a world- or
	// group-readable file is treated as a configuration error, matching how
	// internal/pki protects private keys.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("psk file %s has permissions %#o; it must not be group or world accessible (chmod 600)", path, perm)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read psk file %s: %w", path, err)
	}

	key := bytes.TrimSpace(raw)
	if len(key) < minPSKBytes {
		return nil, fmt.Errorf("psk file %s holds %d bytes; at least %d are required", path, len(key), minPSKBytes)
	}

	return key, nil
}
