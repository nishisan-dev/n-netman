package inject

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/nishisan-dev/n-netman/api/inject/v1"
)

var testPSK = []byte("0123456789abcdef0123456789abcdef")

func sampleAdvertisement() *pb.Advertisement {
	return &pb.Advertisement{
		ControllerId: "host-a",
		Vni:          100,
		Segment:      "vxlan-prod",
		Tags:         []string{"it", "external"},
		TimestampMs:  time.Now().UnixMilli(),
		Sequence:     1,
		LeaseSeconds: 30,
		Routes: []*pb.InjectedRoute{
			{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100},
		},
	}
}

func mustKeyRing(t *testing.T, ids ...string) *KeyRing {
	t.Helper()
	ring, err := NewKeyRing(testPSK, ids)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	return ring
}

func TestSealOpen_Roundtrip(t *testing.T) {
	adv := sampleAdvertisement()
	datagram, err := Seal(adv, testPSK, "k1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	got, err := Open(datagram, mustKeyRing(t), time.Now(), NewReplayGuard(ReplayWindow))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if got.GetControllerId() != "host-a" || got.GetVni() != 100 {
		t.Fatalf("identity did not survive the roundtrip: %+v", got)
	}
	if len(got.GetRoutes()) != 1 || got.GetRoutes()[0].GetPrefix() != "172.16.10.0/24" {
		t.Fatalf("routes did not survive the roundtrip: %+v", got.GetRoutes())
	}
	if len(got.GetTags()) != 2 {
		t.Fatalf("expected 2 tags, got %v", got.GetTags())
	}
}

func TestOpen_RejectsTamperedPayload(t *testing.T) {
	datagram, err := Seal(sampleAdvertisement(), testPSK, "k1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Flip a byte in the middle, which lands inside the serialized payload.
	tampered := append([]byte(nil), datagram...)
	tampered[len(tampered)/2] ^= 0xFF

	_, err = Open(tampered, mustKeyRing(t), time.Now(), NewReplayGuard(ReplayWindow))
	if err == nil {
		t.Fatal("expected tampered datagram to be rejected")
	}
	// Either the envelope no longer parses or the MAC fails; both are refusals,
	// and neither may be a silent accept.
	if !errors.Is(err, ErrBadMAC) && !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrBadVersion) {
		t.Fatalf("expected an authentication or parse failure, got: %v", err)
	}
}

func TestOpen_RejectsWrongKey(t *testing.T) {
	datagram, err := Seal(sampleAdvertisement(), []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "k1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = Open(datagram, mustKeyRing(t), time.Now(), NewReplayGuard(ReplayWindow))
	if !errors.Is(err, ErrBadMAC) {
		t.Fatalf("expected ErrBadMAC, got: %v", err)
	}
}

func TestOpen_RejectsUnacceptedKeyID(t *testing.T) {
	datagram, err := Seal(sampleAdvertisement(), testPSK, "k2")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = Open(datagram, mustKeyRing(t, "k1"), time.Now(), NewReplayGuard(ReplayWindow))
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey, got: %v", err)
	}
}

func TestOpen_RejectsUnknownVersion(t *testing.T) {
	adv := sampleAdvertisement()
	payload, envelope := sealParts(t, adv, testPSK, "k1")
	envelope.Version = ProtocolVersion + 1
	envelope.Mac = computeMAC(testPSK, envelope.Version, "k1", payload)

	_, err := Open(marshal(t, envelope), mustKeyRing(t), time.Now(), NewReplayGuard(ReplayWindow))
	if !errors.Is(err, ErrBadVersion) {
		t.Fatalf("expected ErrBadVersion, got: %v", err)
	}
}

func TestOpen_RejectsMissingControllerID(t *testing.T) {
	adv := sampleAdvertisement()
	adv.ControllerId = ""
	datagram, err := Seal(adv, testPSK, "k1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = Open(datagram, mustKeyRing(t), time.Now(), NewReplayGuard(ReplayWindow))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("expected ErrMalformed, got: %v", err)
	}
}

func TestSeal_RejectsOversizedAdvertisement(t *testing.T) {
	adv := sampleAdvertisement()
	// Far more routes than fit in one datagram.
	for i := 0; i < 500; i++ {
		adv.Routes = append(adv.Routes, &pb.InjectedRoute{
			Prefix:  "172.16.10.0/24",
			NextHop: "10.100.0.1",
			Metric:  100,
		})
	}

	if _, err := Seal(adv, testPSK, "k1"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got: %v", err)
	}
}

func TestReplayGuard(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ms := now.UnixMilli()

	t.Run("first advertisement is admitted", func(t *testing.T) {
		g := NewReplayGuard(ReplayWindow)
		if err := g.Admit("host-a", 1, ms, now); err != nil {
			t.Fatalf("expected admit, got: %v", err)
		}
	})

	t.Run("advancing sequence is admitted", func(t *testing.T) {
		g := NewReplayGuard(ReplayWindow)
		mustAdmit(t, g, "host-a", 1, ms, now)
		if err := g.Admit("host-a", 2, ms, now); err != nil {
			t.Fatalf("expected admit, got: %v", err)
		}
	})

	t.Run("repeated sequence is rejected", func(t *testing.T) {
		g := NewReplayGuard(ReplayWindow)
		mustAdmit(t, g, "host-a", 5, ms, now)
		if err := g.Admit("host-a", 5, ms, now); !errors.Is(err, ErrReplay) {
			t.Fatalf("expected ErrReplay, got: %v", err)
		}
		if err := g.Admit("host-a", 4, ms, now); !errors.Is(err, ErrReplay) {
			t.Fatalf("expected ErrReplay for an older sequence, got: %v", err)
		}
	})

	t.Run("stale timestamp is rejected", func(t *testing.T) {
		g := NewReplayGuard(ReplayWindow)
		old := now.Add(-2 * ReplayWindow).UnixMilli()
		if err := g.Admit("host-a", 1, old, now); !errors.Is(err, ErrStaleTimestamp) {
			t.Fatalf("expected ErrStaleTimestamp, got: %v", err)
		}
		future := now.Add(2 * ReplayWindow).UnixMilli()
		if err := g.Admit("host-a", 1, future, now); !errors.Is(err, ErrStaleTimestamp) {
			t.Fatalf("expected ErrStaleTimestamp for a future timestamp, got: %v", err)
		}
	})

	t.Run("restarted controller with reset sequence is admitted", func(t *testing.T) {
		g := NewReplayGuard(ReplayWindow)
		mustAdmit(t, g, "host-a", 42, ms, now)

		// The controller restarts: sequence goes back to 1, but the clock has
		// moved more than a window forward.
		later := now.Add(2 * ReplayWindow)
		if err := g.Admit("host-a", 1, later.UnixMilli(), later); err != nil {
			t.Fatalf("expected a restarted controller to be admitted, got: %v", err)
		}
	})

	t.Run("controllers are tracked independently", func(t *testing.T) {
		g := NewReplayGuard(ReplayWindow)
		mustAdmit(t, g, "host-a", 10, ms, now)
		// host-b starting at a lower sequence must not be mistaken for a replay.
		if err := g.Admit("host-b", 1, ms, now); err != nil {
			t.Fatalf("expected independent state per controller, got: %v", err)
		}
	})

	t.Run("forget clears state", func(t *testing.T) {
		g := NewReplayGuard(ReplayWindow)
		mustAdmit(t, g, "host-a", 10, ms, now)
		g.Forget("host-a")
		if err := g.Admit("host-a", 1, ms, now); err != nil {
			t.Fatalf("expected admit after Forget, got: %v", err)
		}
	})
}

func TestGroupForVNI(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		vni     uint32
		want    string
		wantErr bool
	}{
		{name: "default base, vni 100", base: "239.8.0.0", vni: 100, want: "239.8.0.100"},
		{name: "vni spanning both octets", base: "239.8.0.0", vni: 300, want: "239.8.1.44"},
		{name: "max 16-bit vni", base: "239.8.0.0", vni: 65535, want: "239.8.255.255"},
		{name: "alternate base", base: "239.9.0.0", vni: 200, want: "239.9.0.200"},
		{name: "non-multicast base", base: "10.0.0.0", vni: 100, wantErr: true},
		{name: "base with non-zero low octets", base: "239.8.1.1", vni: 100, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GroupForVNI(parseIP(t, tc.base), tc.vni)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("expected %s, got %s", tc.want, got)
			}
		})
	}
}

func TestLoadPSK(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.key")
	// Trailing newline is the normal result of shell redirection and must be
	// trimmed identically on both sides.
	if err := os.WriteFile(good, append(testPSK, '\n'), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	key, err := LoadPSK("file:" + good)
	if err != nil {
		t.Fatalf("LoadPSK: %v", err)
	}
	if string(key) != string(testPSK) {
		t.Fatalf("expected the trimmed key, got %q", key)
	}

	t.Run("rejects a bare path", func(t *testing.T) {
		if _, err := LoadPSK(good); err == nil {
			t.Fatal("expected a bare path to be rejected")
		}
	})

	t.Run("rejects loose permissions", func(t *testing.T) {
		loose := filepath.Join(dir, "loose.key")
		if err := os.WriteFile(loose, testPSK, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := LoadPSK("file:" + loose)
		if err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("expected a permissions error, got: %v", err)
		}
	})

	t.Run("rejects a short key", func(t *testing.T) {
		short := filepath.Join(dir, "short.key")
		if err := os.WriteFile(short, []byte("tooshort"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadPSK("file:" + short); err == nil {
			t.Fatal("expected a short key to be rejected")
		}
	})

	t.Run("reports a missing file", func(t *testing.T) {
		if _, err := LoadPSK("file:" + filepath.Join(dir, "absent.key")); err == nil {
			t.Fatal("expected a missing file to be reported")
		}
	})
}

// --- helpers ---

func mustAdmit(t *testing.T, g *ReplayGuard, id string, seq uint64, ts int64, now time.Time) {
	t.Helper()
	if err := g.Admit(id, seq, ts, now); err != nil {
		t.Fatalf("setup admit failed: %v", err)
	}
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("invalid test IP %q", s)
	}
	return ip
}

// sealParts builds an envelope by hand so tests can alter fields before the
// MAC is recomputed.
func sealParts(t *testing.T, adv *pb.Advertisement, psk []byte, keyID string) ([]byte, *pb.InjectEnvelope) {
	t.Helper()
	payload, err := proto.Marshal(adv)
	if err != nil {
		t.Fatalf("marshal advertisement: %v", err)
	}
	return payload, &pb.InjectEnvelope{
		Version: ProtocolVersion,
		KeyId:   keyID,
		Payload: payload,
		Mac:     computeMAC(psk, ProtocolVersion, keyID, payload),
	}
}

func marshal(t *testing.T, env *pb.InjectEnvelope) []byte {
	t.Helper()
	out, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return out
}
