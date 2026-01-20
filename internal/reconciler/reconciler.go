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

	// Step 1: Ensure bridge exists (if KVM mode with managed bridges)
	if err := r.reconcileBridge(ctx); err != nil {
		r.setError(err)
		return fmt.Errorf("bridge reconciliation failed: %w", err)
	}

	// Step 2: Ensure VXLAN interface exists and is attached to bridge
	if err := r.reconcileVXLAN(ctx); err != nil {
		r.setError(err)
		return fmt.Errorf("vxlan reconciliation failed: %w", err)
	}

	// Step 3: Sync FDB entries for peers
	if err := r.reconcileFDB(ctx); err != nil {
		r.setError(err)
		return fmt.Errorf("fdb reconciliation failed: %w", err)
	}

	r.logger.Debug("reconciliation complete")
	r.setError(nil)
	return nil
}

// reconcileBridge ensures the bridge exists and is configured correctly.
func (r *Reconciler) reconcileBridge(ctx context.Context) error {
	bridgeName := r.cfg.Overlay.VXLAN.Bridge

	// Check KVM config for bridge settings
	var bridgeCfg *config.BridgeDef
	for i := range r.cfg.KVM.Bridges {
		if r.cfg.KVM.Bridges[i].Name == bridgeName {
			bridgeCfg = &r.cfg.KVM.Bridges[i]
			break
		}
	}

	// If bridge is managed or doesn't exist in KVM config, create it
	mtu := 1450 // Default
	if r.cfg.Overlay.VXLAN.MTU > 0 {
		mtu = r.cfg.Overlay.VXLAN.MTU
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

	return nil
}

// reconcileVXLAN ensures the VXLAN interface exists and is attached to the bridge.
func (r *Reconciler) reconcileVXLAN(ctx context.Context) error {
	vxlanCfg := r.cfg.Overlay.VXLAN

	r.logger.Debug("ensuring vxlan interface",
		"name", vxlanCfg.Name,
		"vni", vxlanCfg.VNI,
		"bridge", vxlanCfg.Bridge,
	)

	// Determine local underlay IP
	// TODO: Implement proper underlay IP detection from netplan config
	var localIP net.IP

	cfg := nlink.VXLANConfig{
		Name:     vxlanCfg.Name,
		VNI:      vxlanCfg.VNI,
		DstPort:  vxlanCfg.DstPort,
		LocalIP:  localIP,
		MTU:      vxlanCfg.MTU,
		Learning: vxlanCfg.Learning,
		Bridge:   vxlanCfg.Bridge,
	}

	if err := r.vxlan.Create(cfg); err != nil {
		return fmt.Errorf("failed to create vxlan: %w", err)
	}

	return nil
}

// reconcileFDB syncs FDB entries with configured peers.
// Note: When VXLAN learning is enabled, the kernel manages FDB automatically
// and manual FDB entries are not needed.
func (r *Reconciler) reconcileFDB(ctx context.Context) error {
	vxlanName := r.cfg.Overlay.VXLAN.Name
	peers := r.cfg.Overlay.Peers

	// Skip FDB sync when learning is enabled - kernel manages it automatically
	if r.cfg.Overlay.VXLAN.Learning {
		r.logger.Debug("skipping fdb sync, vxlan learning is enabled", "vxlan", vxlanName)
		return nil
	}

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

	r.logger.Debug("syncing fdb entries", "vxlan", vxlanName, "peer_count", len(peerIPs))

	if err := r.fdb.SyncPeers(vxlanName, peerIPs); err != nil {
		return fmt.Errorf("failed to sync fdb: %w", err)
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
