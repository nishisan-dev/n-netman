// Package controlplane implements the gRPC control plane for route exchange.
package controlplane

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/lucas/n-netman/internal/config"
)

// Route represents a network route for exchange between peers.
type Route struct {
	Prefix       string
	NextHop      string
	Metric       uint32
	LeaseSeconds uint32
	Tags         []string
	ReceivedAt   time.Time
	ExpiresAt    time.Time
	PeerID       string
}

// RouteTable stores learned routes from peers.
type RouteTable struct {
	mu     sync.RWMutex
	routes map[string]Route // key: prefix
}

// NewRouteTable creates a new route table.
func NewRouteTable() *RouteTable {
	return &RouteTable{
		routes: make(map[string]Route),
	}
}

// Add adds or updates a route.
func (rt *RouteTable) Add(r Route) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	r.ReceivedAt = time.Now()
	if r.LeaseSeconds > 0 {
		r.ExpiresAt = r.ReceivedAt.Add(time.Duration(r.LeaseSeconds) * time.Second)
	}
	rt.routes[r.Prefix] = r
}

// Remove removes a route by prefix.
func (rt *RouteTable) Remove(prefix string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.routes, prefix)
}

// RemoveByPeer removes all routes from a specific peer.
func (rt *RouteTable) RemoveByPeer(peerID string) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	count := 0
	for prefix, r := range rt.routes {
		if r.PeerID == peerID {
			delete(rt.routes, prefix)
			count++
		}
	}
	return count
}

// Get returns a route by prefix.
func (rt *RouteTable) Get(prefix string) (Route, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	r, ok := rt.routes[prefix]
	return r, ok
}

// All returns all routes.
func (rt *RouteTable) All() []Route {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	routes := make([]Route, 0, len(rt.routes))
	for _, r := range rt.routes {
		routes = append(routes, r)
	}
	return routes
}

// ExpireStale removes routes that have exceeded their lease.
func (rt *RouteTable) ExpireStale() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	count := 0
	for prefix, r := range rt.routes {
		if !r.ExpiresAt.IsZero() && r.ExpiresAt.Before(now) {
			delete(rt.routes, prefix)
			count++
		}
	}
	return count
}

// Server is the gRPC server for receiving route announcements.
type Server struct {
	cfg        *config.Config
	routeTable *RouteTable
	logger     *slog.Logger
	grpcServer *grpc.Server
	listener   net.Listener

	mu        sync.RWMutex
	started   bool
	startTime time.Time
}

// NewServer creates a new control plane server.
func NewServer(cfg *config.Config, routeTable *RouteTable, logger *slog.Logger) *Server {
	return &Server{
		cfg:        cfg,
		routeTable: routeTable,
		logger:     logger,
	}
}

// Start starts the gRPC server.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("server already started")
	}
	s.mu.Unlock()

	addr := fmt.Sprintf("%s:%d",
		s.cfg.Security.ControlPlane.Listen.Address,
		s.cfg.Security.ControlPlane.Listen.Port,
	)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.listener = listener

	// TODO: Add TLS support when cfg.Security.ControlPlane.TLS.Enabled
	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	}

	s.grpcServer = grpc.NewServer(opts...)

	// Register our service implementation
	// Note: We're using a simple RPC pattern instead of generated proto
	// The actual service registration would use the generated code

	s.mu.Lock()
	s.started = true
	s.startTime = time.Now()
	s.mu.Unlock()

	s.logger.Info("control plane server started", "address", addr)

	// Serve in a goroutine
	go func() {
		if err := s.grpcServer.Serve(listener); err != nil {
			s.logger.Error("grpc server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully stops the server.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return
	}

	s.grpcServer.GracefulStop()
	s.started = false
	s.logger.Info("control plane server stopped")
}

// Client manages connections to peer control plane servers.
type Client struct {
	cfg        *config.Config
	routeTable *RouteTable
	logger     *slog.Logger

	mu    sync.RWMutex
	conns map[string]*peerConn // key: peer ID
}

// peerConn represents a connection to a peer.
type peerConn struct {
	peerID   string
	address  string
	conn     *grpc.ClientConn
	healthy  bool
	lastSeen time.Time
}

// NewClient creates a new control plane client.
func NewClient(cfg *config.Config, routeTable *RouteTable, logger *slog.Logger) *Client {
	return &Client{
		cfg:        cfg,
		routeTable: routeTable,
		logger:     logger,
		conns:      make(map[string]*peerConn),
	}
}

// ConnectToPeers establishes connections to all configured peers.
func (c *Client) ConnectToPeers(ctx context.Context) error {
	for _, peer := range c.cfg.Overlay.Peers {
		if err := c.connectPeer(ctx, peer); err != nil {
			c.logger.Warn("failed to connect to peer",
				"peer_id", peer.ID,
				"address", peer.Endpoint.Address,
				"error", err,
			)
			// Continue connecting to other peers even if one fails
		}
	}
	return nil
}

// connectPeer establishes a connection to a single peer.
func (c *Client) connectPeer(ctx context.Context, peer config.PeerConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if already connected
	if existing, ok := c.conns[peer.ID]; ok && existing.conn != nil {
		return nil
	}

	addr := fmt.Sprintf("%s:%d", peer.Endpoint.Address, c.cfg.Security.ControlPlane.Listen.Port)

	c.logger.Debug("connecting to peer", "peer_id", peer.ID, "address", addr)

	// TODO: Add TLS support
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
	}

	conn, err := grpc.DialContext(ctx, addr, opts...)
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	c.conns[peer.ID] = &peerConn{
		peerID:   peer.ID,
		address:  addr,
		conn:     conn,
		healthy:  true,
		lastSeen: time.Now(),
	}

	c.logger.Info("connected to peer", "peer_id", peer.ID, "address", addr)
	return nil
}

// Disconnect closes all peer connections.
func (c *Client) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, pc := range c.conns {
		if pc.conn != nil {
			pc.conn.Close()
		}
		delete(c.conns, id)
	}

	c.logger.Info("disconnected from all peers")
}

// AnnounceRoutes sends route announcements to all connected peers.
func (c *Client) AnnounceRoutes(ctx context.Context, routes []Route) error {
	c.mu.RLock()
	peers := make([]*peerConn, 0, len(c.conns))
	for _, pc := range c.conns {
		if pc.healthy {
			peers = append(peers, pc)
		}
	}
	c.mu.RUnlock()

	for _, pc := range peers {
		if err := c.announceToSinglePeer(ctx, pc, routes); err != nil {
			c.logger.Warn("failed to announce routes to peer",
				"peer_id", pc.peerID,
				"error", err,
			)
			// Mark peer as unhealthy
			c.mu.Lock()
			if p, ok := c.conns[pc.peerID]; ok {
				p.healthy = false
			}
			c.mu.Unlock()
		}
	}

	return nil
}

// announceToSinglePeer sends routes to a single peer.
func (c *Client) announceToSinglePeer(ctx context.Context, pc *peerConn, routes []Route) error {
	// TODO: Implement actual gRPC call using generated client
	// For now, this is a placeholder that logs the intent
	c.logger.Debug("would announce routes to peer",
		"peer_id", pc.peerID,
		"route_count", len(routes),
	)
	return nil
}

// GetPeerStatus returns the connection status of all peers.
func (c *Client) GetPeerStatus() map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := make(map[string]bool)
	for id, pc := range c.conns {
		status[id] = pc.healthy
	}
	return status
}
