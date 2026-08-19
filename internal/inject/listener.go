package inject

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"golang.org/x/net/ipv4"

	pb "github.com/nishisan-dev/n-netman/api/v1"
)

// readBufferSize is larger than MaxDatagramSize so an oversized datagram is
// observed and rejected rather than silently truncated into something that
// might still parse.
const readBufferSize = 4096

// reopenDelay is how long the listener waits before retrying a socket it could
// not open, e.g. because the interface has not appeared yet.
const reopenDelay = 5 * time.Second

// ListenerConfig describes one segment an agent listens on.
type ListenerConfig struct {
	Interface string
	Group     net.IP
	Port      int
	Keys      *KeyRing
	// ExpectTags requires the advertised segment to carry every listed tag.
	// Empty accepts any segment reachable on the interface.
	ExpectTags []string
}

// Handler receives an authenticated advertisement.
type Handler func(*pb.Advertisement)

// RejectHandler reports a dropped datagram and why, for metrics and logs.
type RejectHandler func(reason string, err error)

// Listener receives advertisements on one interface.
//
// One Listener owns one socket and is driven by a single Run goroutine; it is
// not safe to call Run concurrently on the same Listener.
type Listener struct {
	cfg      ListenerConfig
	logger   *slog.Logger
	onReject RejectHandler

	raw  net.PacketConn
	conn *ipv4.PacketConn
}

// ListenerOption configures a Listener.
type ListenerOption func(*Listener)

// WithListenerLogger sets the logger.
func WithListenerLogger(l *slog.Logger) ListenerOption {
	return func(x *Listener) { x.logger = l }
}

// WithRejectHandler registers a callback for dropped datagrams.
func WithRejectHandler(h RejectHandler) ListenerOption {
	return func(x *Listener) { x.onReject = h }
}

// NewListener creates a listener. The socket is opened by Run.
func NewListener(cfg ListenerConfig, opts ...ListenerOption) (*Listener, error) {
	if cfg.Interface == "" {
		return nil, fmt.Errorf("listener: no interface")
	}
	if cfg.Group == nil || !cfg.Group.IsMulticast() {
		return nil, fmt.Errorf("listener: %v is not a multicast group", cfg.Group)
	}
	if cfg.Keys == nil {
		return nil, fmt.Errorf("listener: no key ring")
	}

	l := &Listener{cfg: cfg, logger: slog.Default()}
	for _, opt := range opts {
		opt(l)
	}
	return l, nil
}

// Run receives advertisements until the context is cancelled.
//
// The socket is opened here, with retries, because the NIC may not be up when
// the agent starts — netplan and the agent race at boot, and losing that race
// must not be fatal.
func (l *Listener) Run(ctx context.Context, guard *ReplayGuard, handle Handler) error {
	for {
		if err := l.open(); err != nil {
			l.logger.Warn("waiting to join the inject group",
				"interface", l.cfg.Interface, "group", l.cfg.Group, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reopenDelay):
				continue
			}
		}

		l.logger.Info("listening for inject advertisements",
			"interface", l.cfg.Interface, "group", l.cfg.Group, "port", l.cfg.Port)

		err := l.receive(ctx, guard, handle)
		l.Close()

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// The socket failed for a reason other than shutdown; rebuild it.
		l.logger.Warn("inject listener stopped, reopening",
			"interface", l.cfg.Interface, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reopenDelay):
		}
	}
}

func (l *Listener) receive(ctx context.Context, guard *ReplayGuard, handle Handler) error {
	// Closing the socket is what unblocks the read on shutdown.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			l.Close()
		case <-done:
		}
	}()

	buf := make([]byte, readBufferSize)
	for {
		n, _, _, err := l.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read failed on %s: %w", l.cfg.Interface, err)
		}

		if n > MaxDatagramSize {
			l.reject("oversized", fmt.Errorf("datagram of %d bytes exceeds the %d cap", n, MaxDatagramSize))
			continue
		}

		adv, err := Open(buf[:n], l.cfg.Keys, time.Now(), guard)
		if err != nil {
			l.reject(rejectReason(err), err)
			continue
		}

		// A NIC cabled to the wrong segment is the failure this catches: the
		// advertisement authenticates, but it does not describe the segment
		// this interface was configured for.
		if !hasAllTags(adv.GetTags(), l.cfg.ExpectTags) {
			l.reject("tag_mismatch", fmt.Errorf("segment %q carries %v, expected %v",
				adv.GetSegment(), adv.GetTags(), l.cfg.ExpectTags))
			continue
		}

		handle(adv)
	}
}

// Close releases the socket. It is safe to call more than once.
func (l *Listener) Close() error {
	if l.raw == nil {
		return nil
	}
	err := l.raw.Close()
	l.raw = nil
	l.conn = nil
	return err
}

func (l *Listener) open() error {
	if l.raw != nil {
		return nil
	}

	iface, err := net.InterfaceByName(l.cfg.Interface)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", l.cfg.Interface, err)
	}

	// Binding to the group address makes the kernel deliver only datagrams for
	// that group, so segments sharing a port stay separated.
	raw, err := net.ListenPacket("udp4", fmt.Sprintf("%s:%d", l.cfg.Group, l.cfg.Port))
	if err != nil {
		return fmt.Errorf("failed to bind %s:%d: %w", l.cfg.Group, l.cfg.Port, err)
	}

	conn := ipv4.NewPacketConn(raw)
	if err := conn.JoinGroup(iface, &net.UDPAddr{IP: l.cfg.Group}); err != nil {
		raw.Close()
		return fmt.Errorf("failed to join %s on %s: %w", l.cfg.Group, l.cfg.Interface, err)
	}
	// Membership is per interface, so an agent with several NICs never picks up
	// a neighbouring segment's advertisements by accident.
	if err := conn.SetMulticastInterface(iface); err != nil {
		raw.Close()
		return fmt.Errorf("failed to pin the group to %s: %w", l.cfg.Interface, err)
	}

	l.raw = raw
	l.conn = conn
	return nil
}

func (l *Listener) reject(reason string, err error) {
	if l.onReject != nil {
		l.onReject(reason, err)
	}
	l.logger.Debug("rejected inject advertisement",
		"interface", l.cfg.Interface, "reason", reason, "error", err)
}

// rejectReason maps a codec error to a stable metric label.
func rejectReason(err error) string {
	switch {
	case errors.Is(err, ErrBadMAC):
		return "bad_mac"
	case errors.Is(err, ErrUnknownKey):
		return "unknown_key"
	case errors.Is(err, ErrBadVersion):
		return "bad_version"
	case errors.Is(err, ErrReplay):
		return "replay"
	case errors.Is(err, ErrStaleTimestamp):
		return "stale_timestamp"
	case errors.Is(err, ErrMalformed):
		return "malformed"
	default:
		return "unknown"
	}
}

// hasAllTags reports whether have contains every tag in want (AND), matching
// how the controller selects bridges.
func hasAllTags(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}
