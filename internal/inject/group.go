// Package inject implements the multicast channel that carries route
// information from a controller to agents running inside VMs attached to a
// tagged bridge.
//
// The channel is deliberately not gRPC: a controller does not know which VMs
// exist on its bridges, so advertisements are published to a per-VNI multicast
// group scoped to a single L2 segment (TTL 1) and authenticated with a shared
// key rather than a per-peer identity.
package inject

import (
	"fmt"
	"net"
)

// GroupForVNI derives the multicast group of an overlay from a base address.
//
// The low 16 bits of the VNI are OR-ed into the last two octets of the base,
// which is why callers must supply a base ending in 0.0 (enforced by config
// validation). Because only those two octets change, the result can never
// leave the multicast range the base already sits in.
//
// With the default base 239.8.0.0, VNI 100 maps to 239.8.0.100.
func GroupForVNI(base net.IP, vni uint32) (net.IP, error) {
	v4 := base.To4()
	if v4 == nil {
		return nil, fmt.Errorf("inject group base %q must be IPv4", base)
	}
	if !base.IsMulticast() {
		return nil, fmt.Errorf("inject group base %q is not multicast", base)
	}
	if v4[2] != 0 || v4[3] != 0 {
		return nil, fmt.Errorf("inject group base %q must end in 0.0; the last two octets carry the VNI", base)
	}

	out := net.IPv4(v4[0], v4[1], byte((vni>>8)&0xFF), byte(vni&0xFF))
	return out.To4(), nil
}
