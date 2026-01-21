// Package reconciler implements the reconciliation loop that synchronizes
// the desired state (from config) with the actual state (on the system).
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/lucas/n-netman/internal/config"
	nlink "github.com/lucas/n-netman/internal/netlink"
)

// Reconciler manages the reconciliation loop.
type Reconciler struct {
	cfg    *config.Config
	vxlan  *nlink.VXLANManager
	bridge *nlink.BridgeManager
	fdb    *nlink.FDBManager
	route  *nlink.RouteManager

	interval time.Duration
	logger   *slog.Logger

	mu      sync.RWMutex
	running bool
	lastErr error
	lastRun time.Time
}

// New creates a new Reconciler with the given configuration.
func New(cfg *config.Config, opts ...Option) *Reconciler {
	r := &Reconciler{
		cfg:      cfg,
		vxlan:    nlink.NewVXLANManager(),
		bridge:   nlink.NewBridgeManager(),
		fdb:      nlink.NewFDBManager(),
		route:    nlink.NewRouteManager(),
		interval: 10 * time.Second,
		logger:   slog.Default(),
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// Option is a functional option for configuring the Reconciler.
type Option func(*Reconciler)

// WithInterval sets the reconciliation interval.
func WithInterval(d time.Duration) Option {
	return func(r *Reconciler) {
		r.interval = d
	}
}

// WithLogger sets the logger for the reconciler.
func WithLogger(l *slog.Logger) Option {
	return func(r *Reconciler) {
		r.logger = l
	}
}

// Run starts the reconciliation loop. It blocks until the context is cancelled.
func (r *Reconciler) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return fmt.Errorf("reconciler already running")
	}
	r.running = true
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	r.logger.Info("starting reconciler loop", "interval", r.interval)

	// Initial reconciliation
	if err := r.Reconcile(ctx); err != nil {
		r.logger.Error("initial reconciliation failed", "error", err)
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconciler loop stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := r.Reconcile(ctx); err != nil {
				r.logger.Error("reconciliation failed", "error", err)
			}
		}
	}
}

// Reconcile performs a single reconciliation cycle.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	r.lastRun = time.Now()
	r.mu.Unlock()

	r.logger.Debug("starting reconciliation")

	// Get all overlays (works for both v1 and v2 configs)
	overlays := r.cfg.GetOverlays()
	if len(overlays) == 0 {
		r.logger.Warn("no overlays configured, skipping reconciliation")
		return nil
	}

	// Reconcile each overlay
	for _, overlay := range overlays {
		if err := r.reconcileOverlay(ctx, overlay); err != nil {
			r.setError(err)
			return fmt.Errorf("overlay %s (VNI %d) reconciliation failed: %w", overlay.Name, overlay.VNI, err)
		}
	}

	r.logger.Debug("reconciliation complete", "overlay_count", len(overlays))
	r.setError(nil)
	return nil
}

// reconcileOverlay reconciles a single overlay (bridge, VXLAN, FDB).
func (r *Reconciler) reconcileOverlay(ctx context.Context, overlay config.OverlayDef) error {
	r.logger.Debug("reconciling overlay", "name", overlay.Name, "vni", overlay.VNI, "bridge", overlay.Bridge)

	// Step 1: Ensure bridge exists
	if err := r.reconcileBridgeForOverlay(ctx, overlay); err != nil {
		return fmt.Errorf("bridge reconciliation failed: %w", err)
	}

	// Step 2: Ensure VXLAN interface exists and is attached to bridge
	if err := r.reconcileVXLANForOverlay(ctx, overlay); err != nil {
		return fmt.Errorf("vxlan reconciliation failed: %w", err)
	}

	// Step 3: Sync FDB entries for peers
	if err := r.reconcileFDBForOverlay(ctx, overlay); err != nil {
		return fmt.Errorf("fdb reconciliation failed: %w", err)
	}

	return nil
}

// reconcileBridgeForOverlay ensures the bridge for an overlay exists and is configured correctly.
func (r *Reconciler) reconcileBridgeForOverlay(ctx context.Context, overlay config.OverlayDef) error {
	bridgeName := overlay.Bridge.Name

	// Check KVM config for bridge settings
	var bridgeCfg *config.BridgeDef
	for i := range r.cfg.KVM.Bridges {
		if r.cfg.KVM.Bridges[i].Name == bridgeName {
			bridgeCfg = &r.cfg.KVM.Bridges[i]
			break
		}
	}

	// Determine MTU from overlay or defaults
	mtu := 1450 // Default
	if overlay.MTU > 0 {
		mtu = overlay.MTU
	}

	if bridgeCfg != nil && bridgeCfg.Manage {
		stp := bridgeCfg.STP
		if bridgeCfg.MTU > 0 {
			mtu = bridgeCfg.MTU
		}

		r.logger.Debug("ensuring managed bridge", "name", bridgeName, "mtu", mtu, "stp", stp)

		if err := r.bridge.Create(nlink.BridgeConfig{
			Name: bridgeName,
			STP:  stp,
			MTU:  mtu,
		}); err != nil {
			return fmt.Errorf("failed to create bridge %s: %w", bridgeName, err)
		}
	} else if !r.bridge.Exists(bridgeName) {
		// Bridge doesn't exist and isn't managed - create with defaults
		r.logger.Debug("creating unmanaged bridge", "name", bridgeName)

		if err := r.bridge.Create(nlink.BridgeConfig{
			Name: bridgeName,
			MTU:  mtu,
		}); err != nil {
			return fmt.Errorf("failed to create bridge %s: %w", bridgeName, err)
		}
	}

	// Add IP address to bridge if configured (for overlay routing)
	if overlay.Bridge.IPv4 != "" {
		r.logger.Debug("adding IPv4 address to bridge", "bridge", bridgeName, "address", overlay.Bridge.IPv4)
		if err := r.bridge.AddAddress(bridgeName, overlay.Bridge.IPv4); err != nil {
			return fmt.Errorf("failed to add IPv4 to bridge %s: %w", bridgeName, err)
		}
	}
	if overlay.Bridge.IPv6 != "" {
		r.logger.Debug("adding IPv6 address to bridge", "bridge", bridgeName, "address", overlay.Bridge.IPv6)
		if err := r.bridge.AddAddress(bridgeName, overlay.Bridge.IPv6); err != nil {
			return fmt.Errorf("failed to add IPv6 to bridge %s: %w", bridgeName, err)
		}
	}

	return nil
}

// reconcileVXLANForOverlay ensures the VXLAN interface for an overlay exists and is attached to the bridge.
func (r *Reconciler) reconcileVXLANForOverlay(ctx context.Context, overlay config.OverlayDef) error {
	r.logger.Debug("ensuring vxlan interface",
		"name", overlay.Name,
		"vni", overlay.VNI,
		"bridge", overlay.Bridge,
		"underlay_interface", overlay.UnderlayInterface,
	)

	// Determine local underlay IP
	// TODO: Implement proper underlay IP detection based on overlay.UnderlayInterface
	var localIP net.IP

	// Use default DstPort if not specified
	dstPort := overlay.DstPort
	if dstPort == 0 {
		dstPort = 4789
	}

	cfg := nlink.VXLANConfig{
		Name:     overlay.Name,
		VNI:      overlay.VNI,
		DstPort:  dstPort,
		LocalIP:  localIP,
		MTU:      overlay.MTU,
		Learning: overlay.Learning,
		Bridge:   overlay.Bridge.Name,
	}

	if err := r.vxlan.Create(cfg); err != nil {
		return fmt.Errorf("failed to create vxlan %s: %w", overlay.Name, err)
	}

	return nil
}

// reconcileFDBForOverlay syncs FDB entries with configured peers for an overlay.
// Note: When VXLAN learning is enabled, the kernel manages FDB automatically
// and manual FDB entries are not needed.
func (r *Reconciler) reconcileFDBForOverlay(ctx context.Context, overlay config.OverlayDef) error {
	// Skip FDB sync when learning is enabled - kernel manages it automatically
	if overlay.Learning {
		r.logger.Debug("skipping fdb sync, vxlan learning is enabled", "vxlan", overlay.Name)
		return nil
	}

	// Get peers from legacy config (for now, v2 will need per-overlay peers)
	peers := r.cfg.GetPeers()

	// Build list of peer IPs
	var peerIPs []net.IP
	for _, peer := range peers {
		ip := net.ParseIP(peer.Endpoint.Address)
		if ip == nil {
			r.logger.Warn("invalid peer IP, skipping", "peer_id", peer.ID, "address", peer.Endpoint.Address)
			continue
		}
		peerIPs = append(peerIPs, ip)
	}

	r.logger.Debug("syncing fdb entries", "vxlan", overlay.Name, "peer_count", len(peerIPs))

	if err := r.fdb.SyncPeers(overlay.Name, peerIPs); err != nil {
		return fmt.Errorf("failed to sync fdb for %s: %w", overlay.Name, err)
	}

	return nil
}

func (r *Reconciler) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastErr = err
}

// Status returns the current status of the reconciler.
func (r *Reconciler) Status() ReconcilerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return ReconcilerStatus{
		Running: r.running,
		LastRun: r.lastRun,
		LastErr: r.lastErr,
	}
}

// ReconcilerStatus contains the current status of the reconciler.
type ReconcilerStatus struct {
	Running bool
	LastRun time.Time
	LastErr error
}

// RunOnce performs a single reconciliation without starting the loop.
func (r *Reconciler) RunOnce(ctx context.Context) error {
	return r.Reconcile(ctx)
}
