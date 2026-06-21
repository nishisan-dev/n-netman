package netlink

import (
	"net"
	"testing"
)

func TestIsZeroMAC(t *testing.T) {
	cases := []struct {
		name string
		mac  net.HardwareAddr
		want bool
	}{
		{"empty", net.HardwareAddr{}, true},
		{"all zeros", net.HardwareAddr{0, 0, 0, 0, 0, 0}, true},
		{"real mac", net.HardwareAddr{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}, false},
		{"one nonzero byte", net.HardwareAddr{0, 0, 0, 0, 0, 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isZeroMAC(tc.mac); got != tc.want {
				t.Fatalf("isZeroMAC(%v) = %v, want %v", tc.mac, got, tc.want)
			}
		})
	}
}
