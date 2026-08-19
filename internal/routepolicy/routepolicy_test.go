package routepolicy

import "testing"

func TestPolicy_Admits(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
		prefix string
		want   bool
	}{
		{
			name:   "empty policy denies by default",
			policy: Policy{},
			prefix: "172.16.10.0/24",
		},
		{
			name:   "accept_all admits anything not denied",
			policy: Policy{AcceptAll: true},
			prefix: "172.16.10.0/24",
			want:   true,
		},
		{
			name:   "allow supernet admits a contained route",
			policy: Policy{Allow: []string{"172.16.0.0/16"}},
			prefix: "172.16.10.0/24",
			want:   true,
		},
		{
			name:   "a route broader than allow is not admitted",
			policy: Policy{Allow: []string{"172.16.10.0/24"}},
			prefix: "172.16.0.0/16",
		},
		{
			name:   "deny beats accept_all",
			policy: Policy{AcceptAll: true, Deny: []string{"0.0.0.0/0"}},
			prefix: "172.16.10.0/24",
		},
		{
			name:   "deny matches on overlap, not just containment",
			policy: Policy{AcceptAll: true, Deny: []string{"10.0.0.0/8"}},
			prefix: "0.0.0.0/0",
		},
		{
			name:   "deny is evaluated before allow",
			policy: Policy{Allow: []string{"172.16.0.0/16"}, Deny: []string{"172.16.10.0/24"}},
			prefix: "172.16.10.0/24",
		},
		{
			name:   "ipv6 within allow is admitted",
			policy: Policy{Allow: []string{"2001:db8::/32"}},
			prefix: "2001:db8:10::/64",
			want:   true,
		},
		{
			name:   "an unparseable prefix is rejected",
			policy: Policy{AcceptAll: true},
			prefix: "not-a-cidr",
		},
		{
			name:   "an unparseable policy entry does not admit",
			policy: Policy{Allow: []string{"garbage"}},
			prefix: "172.16.10.0/24",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.Admits(tc.prefix); got != tc.want {
				t.Fatalf("Admits(%q) = %v, want %v", tc.prefix, got, tc.want)
			}
		})
	}
}

func TestPolicy_AdmitsNet_NilIsRejected(t *testing.T) {
	p := Policy{AcceptAll: true}
	if p.AdmitsNet(nil) {
		t.Fatal("a nil network must never be admitted")
	}
}
