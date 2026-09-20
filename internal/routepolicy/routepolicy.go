// Package routepolicy evaluates which prefixes a node accepts.
//
// It is deliberately a leaf package depending only on the standard library.
// Both the controller (via internal/routing) and nnet-agent apply the same
// admission rules, and the agent must not pull the gRPC control plane into a
// VM just to decide whether a prefix is allowed.
package routepolicy

import "net"

// Policy is an allow/deny list over prefixes, in the shape both configuration
// schemas expose.
type Policy struct {
	// AcceptAll admits everything not explicitly denied.
	AcceptAll bool
	// Allow lists supernets a route must fall within to be admitted.
	Allow []string
	// Deny lists prefixes that reject a route on any overlap.
	Deny []string
}

// Admits reports whether a route prefix in CIDR form is accepted.
// An unparseable prefix is rejected.
func (p Policy) Admits(prefix string) bool {
	_, routeNet, err := net.ParseCIDR(prefix)
	if err != nil {
		return false
	}
	return p.AdmitsNet(routeNet)
}

// AdmitsNet reports whether a parsed route network is accepted.
//
// Deny is evaluated first and matches on overlap in either direction, so a
// broader announced prefix that swallows a denied subnet is still rejected.
// Allow matches only on containment: admitting a route broader than the allowed
// supernet would leak more than intended. With neither accept_all nor a
// matching allow entry, the route is denied — the secure default.
func (p Policy) AdmitsNet(routeNet *net.IPNet) bool {
	if routeNet == nil {
		return false
	}

	for _, denyPrefix := range p.Deny {
		if overlaps(routeNet, denyPrefix) {
			return false
		}
	}

	if p.AcceptAll {
		return true
	}

	for _, allowPrefix := range p.Allow {
		if within(routeNet, allowPrefix) {
			return true
		}
	}

	return false
}

// within reports whether routeNet is contained in (a subset of) policyPrefix.
func within(routeNet *net.IPNet, policyPrefix string) bool {
	_, policyNet, err := net.ParseCIDR(policyPrefix)
	if err != nil {
		return false
	}
	if !policyNet.Contains(routeNet.IP) {
		return false
	}
	policyOnes, _ := policyNet.Mask.Size()
	routeOnes, _ := routeNet.Mask.Size()
	return policyOnes <= routeOnes
}

// overlaps reports whether routeNet and policyPrefix intersect at all.
func overlaps(routeNet *net.IPNet, policyPrefix string) bool {
	_, policyNet, err := net.ParseCIDR(policyPrefix)
	if err != nil {
		return false
	}
	return policyNet.Contains(routeNet.IP) || routeNet.Contains(policyNet.IP)
}
