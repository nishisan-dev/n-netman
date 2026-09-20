package inject

import (
	"errors"
	"net"
	"testing"
)

func TestNewListener_Validation(t *testing.T) {
	ring := mustKeyRing(t)
	group := net.ParseIP("239.8.0.100")

	cases := []struct {
		name string
		cfg  ListenerConfig
	}{
		{name: "no interface", cfg: ListenerConfig{Group: group, Keys: ring}},
		{name: "no group", cfg: ListenerConfig{Interface: "ens3", Keys: ring}},
		{name: "non-multicast group", cfg: ListenerConfig{Interface: "ens3", Group: net.ParseIP("10.0.0.1"), Keys: ring}},
		{name: "no key ring", cfg: ListenerConfig{Interface: "ens3", Group: group}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewListener(tc.cfg); err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}

	valid := ListenerConfig{Interface: "nnet-absent-iface", Group: group, Port: 4790, Keys: ring}
	if _, err := NewListener(valid); err != nil {
		t.Fatalf("expected construction to succeed without touching the interface, got: %v", err)
	}
}

// Reject reasons become metric labels, so the mapping must be stable.
func TestRejectReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrBadMAC, "bad_mac"},
		{ErrUnknownKey, "unknown_key"},
		{ErrBadVersion, "bad_version"},
		{ErrReplay, "replay"},
		{ErrStaleTimestamp, "stale_timestamp"},
		{ErrMalformed, "malformed"},
		{errors.New("something else"), "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			// Wrapped errors must map the same way, since the codec wraps.
			wrapped := errors.Join(errors.New("context"), tc.err)
			if got := rejectReason(wrapped); got != tc.want {
				t.Fatalf("rejectReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestHasAllTags(t *testing.T) {
	cases := []struct {
		name string
		have []string
		want []string
		out  bool
	}{
		{name: "no expectation accepts anything", have: []string{"it"}, want: nil, out: true},
		{name: "no expectation accepts an untagged segment", have: nil, want: nil, out: true},
		{name: "single expected tag present", have: []string{"it", "external"}, want: []string{"it"}, out: true},
		{name: "all expected tags present", have: []string{"it", "external", "public"}, want: []string{"it", "external"}, out: true},
		{name: "one expected tag missing", have: []string{"it"}, want: []string{"it", "external"}},
		{name: "untagged segment against an expectation", have: nil, want: []string{"it"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAllTags(tc.have, tc.want); got != tc.out {
				t.Fatalf("hasAllTags(%v, %v) = %v, want %v", tc.have, tc.want, got, tc.out)
			}
		})
	}
}
