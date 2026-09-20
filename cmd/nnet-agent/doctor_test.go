package main

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckRoutePrivileges(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps unix.CapUserData
		err  error
		want bool
	}{
		{name: "no capabilities"},
		{name: "unrelated effective capability", caps: unix.CapUserData{Effective: 1 << unix.CAP_NET_RAW}},
		{name: "permitted but not effective", caps: unix.CapUserData{Permitted: 1 << unix.CAP_NET_ADMIN}},
		{name: "inheritable but not effective", caps: unix.CapUserData{Inheritable: 1 << unix.CAP_NET_ADMIN}},
		{name: "effective NET_ADMIN", caps: unix.CapUserData{Effective: 1 << unix.CAP_NET_ADMIN}, want: true},
		{name: "capability query failed", err: unix.EPERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := checkRoutePrivileges(func(header *unix.CapUserHeader, data *unix.CapUserData) error {
				if header.Version != unix.LINUX_CAPABILITY_VERSION_3 || header.Pid != 0 {
					t.Fatalf("expected a query of the current thread using capability ABI v3: %+v", header)
				}
				*data = tc.caps
				return tc.err
			})
			if ok != tc.want {
				t.Fatalf("checkRoutePrivileges() = %v (%s), want %v", ok, msg, tc.want)
			}
			if tc.err != nil {
				if !strings.Contains(msg, tc.err.Error()) {
					t.Fatalf("diagnostic does not explain the query failure: %s", msg)
				}
			} else if !strings.Contains(msg, "CAP_NET_ADMIN") {
				t.Fatalf("diagnostic does not name the required capability: %s", msg)
			}
		})
	}
}
