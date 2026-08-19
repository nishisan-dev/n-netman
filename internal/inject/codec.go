package inject

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/nishisan-dev/n-netman/api/v1"
)

const (
	// ProtocolVersion is the wire version this build speaks.
	ProtocolVersion = 1

	// MaxDatagramSize caps a sealed envelope. It sits below the 1450-byte
	// overlay MTU so an advertisement never relies on IP fragmentation.
	MaxDatagramSize = 1400

	// ReplayWindow is how far an advertisement's timestamp may drift from the
	// receiver's clock. It also bounds how long a controller restart takes to
	// be distinguished from a replay.
	ReplayWindow = 30 * time.Second

	// replayStateTTL bounds how long a silent controller is remembered, so the
	// guard's map cannot grow without limit.
	replayStateTTL = time.Hour

	// domainSeparator binds the MAC to this protocol, so the same key can never
	// be made to authenticate bytes from another context.
	domainSeparator = "nnet-inject-v1"
)

// Errors returned by Open. They are distinct so receivers can label the reason
// an advertisement was dropped.
var (
	ErrMalformed      = errors.New("malformed inject datagram")
	ErrBadVersion     = errors.New("unsupported inject protocol version")
	ErrUnknownKey     = errors.New("advertisement signed with an unknown key id")
	ErrBadMAC         = errors.New("advertisement failed authentication")
	ErrStaleTimestamp = errors.New("advertisement timestamp outside the replay window")
	ErrReplay         = errors.New("advertisement replays a sequence already seen")
	ErrTooLarge       = errors.New("sealed advertisement exceeds the datagram cap")
)

// Seal serializes an advertisement into an authenticated datagram.
//
// It returns ErrTooLarge rather than dropping routes: silently truncating an
// advertisement would leave agents with a plausible but incomplete route set.
func Seal(adv *pb.Advertisement, psk []byte, keyID string) ([]byte, error) {
	if adv == nil {
		return nil, fmt.Errorf("seal: advertisement is nil")
	}
	if len(psk) == 0 {
		return nil, fmt.Errorf("seal: empty psk")
	}

	payload, err := proto.Marshal(adv)
	if err != nil {
		return nil, fmt.Errorf("seal: failed to marshal advertisement: %w", err)
	}

	envelope := &pb.InjectEnvelope{
		Version: ProtocolVersion,
		KeyId:   keyID,
		Payload: payload,
		Mac:     computeMAC(psk, ProtocolVersion, keyID, payload),
	}

	out, err := proto.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("seal: failed to marshal envelope: %w", err)
	}
	if len(out) > MaxDatagramSize {
		return nil, fmt.Errorf("%w: %d bytes, cap is %d", ErrTooLarge, len(out), MaxDatagramSize)
	}

	return out, nil
}

// Open authenticates a datagram and returns the advertisement it carries.
//
// The order matters: the MAC is verified before the payload is parsed and
// before the replay guard is touched, so an unauthenticated sender can neither
// exercise the protobuf decoder on arbitrary bytes nor poison replay state.
func Open(datagram []byte, keys *KeyRing, now time.Time, guard *ReplayGuard) (*pb.Advertisement, error) {
	if keys == nil {
		return nil, fmt.Errorf("open: nil key ring")
	}

	var envelope pb.InjectEnvelope
	if err := proto.Unmarshal(datagram, &envelope); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if envelope.GetVersion() != ProtocolVersion {
		return nil, fmt.Errorf("%w: got %d, expected %d", ErrBadVersion, envelope.GetVersion(), ProtocolVersion)
	}

	psk, ok := keys.Key(envelope.GetKeyId())
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, envelope.GetKeyId())
	}

	want := computeMAC(psk, envelope.GetVersion(), envelope.GetKeyId(), envelope.GetPayload())
	if !hmac.Equal(want, envelope.GetMac()) {
		return nil, ErrBadMAC
	}

	var adv pb.Advertisement
	if err := proto.Unmarshal(envelope.GetPayload(), &adv); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if adv.GetControllerId() == "" {
		return nil, fmt.Errorf("%w: advertisement has no controller_id", ErrMalformed)
	}

	if guard != nil {
		if err := guard.Admit(adv.GetControllerId(), adv.GetSequence(), adv.GetTimestampMs(), now); err != nil {
			return nil, err
		}
	}

	return &adv, nil
}

// computeMAC derives the authentication tag over a domain-separated preimage.
//
// key_id is length-prefixed so that a key id and payload cannot be re-split at
// a different boundary to produce the same preimage.
func computeMAC(psk []byte, version uint32, keyID string, payload []byte) []byte {
	mac := hmac.New(sha256.New, psk)
	mac.Write([]byte(domainSeparator))

	var scratch [4]byte
	binary.BigEndian.PutUint32(scratch[:], version)
	mac.Write(scratch[:])

	binary.BigEndian.PutUint32(scratch[:], uint32(len(keyID)))
	mac.Write(scratch[:])
	mac.Write([]byte(keyID))

	mac.Write(payload)
	return mac.Sum(nil)
}

// KeyRing holds the key an agent or controller uses on one segment, plus the
// set of key ids it is willing to accept.
//
// KeyRing is read-only after construction and safe for concurrent use.
type KeyRing struct {
	psk         []byte
	acceptedIDs map[string]struct{} // empty means any key id is accepted
}

// NewKeyRing builds a key ring. An empty acceptedKeyIDs list accepts any key id,
// which is the common case; listing ids pins the ring during a key rotation.
func NewKeyRing(psk []byte, acceptedKeyIDs []string) (*KeyRing, error) {
	if len(psk) == 0 {
		return nil, fmt.Errorf("key ring: empty psk")
	}
	ring := &KeyRing{psk: psk}
	if len(acceptedKeyIDs) > 0 {
		ring.acceptedIDs = make(map[string]struct{}, len(acceptedKeyIDs))
		for _, id := range acceptedKeyIDs {
			ring.acceptedIDs[id] = struct{}{}
		}
	}
	return ring, nil
}

// Key returns the key material for a key id, and whether the id is accepted.
func (k *KeyRing) Key(keyID string) ([]byte, bool) {
	if k.acceptedIDs != nil {
		if _, ok := k.acceptedIDs[keyID]; !ok {
			return nil, false
		}
	}
	return k.psk, true
}

// ReplayGuard rejects advertisements that repeat a sequence already seen.
//
// State is kept per controller because a single L2 segment spans every host in
// the overlay, so an agent legitimately hears from several controllers at once.
//
// ReplayGuard is safe for concurrent use.
type ReplayGuard struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]replayState
}

type replayState struct {
	lastSeq uint64
	lastTs  int64 // Unix millis
}

// NewReplayGuard creates a guard. A zero window falls back to ReplayWindow.
func NewReplayGuard(window time.Duration) *ReplayGuard {
	if window <= 0 {
		window = ReplayWindow
	}
	return &ReplayGuard{
		window: window,
		seen:   make(map[string]replayState),
	}
}

// Admit records an advertisement and reports whether it should be accepted.
//
// An advertisement is admitted when its timestamp is inside the window and
// either its sequence advances, or its timestamp jumped a whole window ahead.
// The second case is what lets a restarted controller — whose sequence resets
// to zero — be told apart from an attacker replaying old datagrams.
func (g *ReplayGuard) Admit(controllerID string, seq uint64, timestampMs int64, now time.Time) error {
	nowMs := now.UnixMilli()
	windowMs := g.window.Milliseconds()

	drift := nowMs - timestampMs
	if drift < 0 {
		drift = -drift
	}
	if drift > windowMs {
		return fmt.Errorf("%w: drift %dms exceeds %dms", ErrStaleTimestamp, drift, windowMs)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.pruneLocked(nowMs)

	if prev, ok := g.seen[controllerID]; ok {
		restarted := timestampMs > prev.lastTs+windowMs
		if seq <= prev.lastSeq && !restarted {
			return fmt.Errorf("%w: sequence %d not after %d", ErrReplay, seq, prev.lastSeq)
		}
	}

	next := replayState{lastSeq: seq, lastTs: timestampMs}
	if prev, ok := g.seen[controllerID]; ok && prev.lastTs > timestampMs {
		next.lastTs = prev.lastTs
	}
	g.seen[controllerID] = next

	return nil
}

// Forget drops the state for a controller, used when its lease expires.
func (g *ReplayGuard) Forget(controllerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.seen, controllerID)
}

// pruneLocked evicts controllers that have been silent long enough that their
// old sequences can no longer be replayed inside the window.
func (g *ReplayGuard) pruneLocked(nowMs int64) {
	ttlMs := replayStateTTL.Milliseconds()
	for id, st := range g.seen {
		if nowMs-st.lastTs > ttlMs {
			delete(g.seen, id)
		}
	}
}
