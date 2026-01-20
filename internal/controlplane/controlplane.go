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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	pb "github.com/lucas/n-netman/api/v1"
	"github.com/lucas/n-netman/internal/config"
	"github.com/lucas/n-netman/internal/observability"
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

// Server is the gRPC server implementing the NNetMan service.
// It handles incoming route announcements and state exchange.
type Server struct {
	pb.UnimplementedNNetManServer

	cfg        *config.Config
	routeTable *RouteTable
	logger     *slog.Logger
	grpcServer *grpc.Server
	listener   net.Listener

	// Callback invoked when routes are received (for integrating with RouteManager)
	onRoutesReceived func(routes []Route)

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

// SetRoutesReceivedCallback sets the callback for when routes are received.
func (s *Server) SetRoutesReceivedCallback(fn func(routes []Route)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRoutesReceived = fn
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

	// Register the NNetMan service
	pb.RegisterNNetManServer(s.grpcServer, s)

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

// ExchangeState implements the ExchangeState RPC.
// Called when a peer connects to perform initial state synchronization.
func (s *Server) ExchangeState(ctx context.Context, req *pb.StateRequest) (*pb.StateResponse, error) {
	s.logger.Info("received state exchange request",
		"peer_id", req.NodeId,
		"route_count", len(req.Routes),
	)

	// Process incoming routes from the peer
	incomingRoutes := make([]Route, 0, len(req.Routes))
	for _, r := range req.Routes {
		route := Route{
			Prefix:       r.Prefix,
			NextHop:      r.NextHop,
			Metric:       r.Metric,
			LeaseSeconds: r.LeaseSeconds,
			Tags:         r.Tags,
			PeerID:       req.NodeId,
		}
		s.routeTable.Add(route)
		incomingRoutes = append(incomingRoutes, route)
	}

	// Invoke callback if set
	s.mu.RLock()
	callback := s.onRoutesReceived
	s.mu.RUnlock()
	if callback != nil && len(incomingRoutes) > 0 {
		callback(incomingRoutes)
	}

	s.logger.Info("processed peer routes",
		"peer_id", req.NodeId,
		"imported_count", len(incomingRoutes),
	)

	// Return our current routes to the peer
	ourRoutes := s.getExportableRoutes()
	pbRoutes := make([]*pb.Route, 0, len(ourRoutes))
	for _, r := range ourRoutes {
		pbRoutes = append(pbRoutes, &pb.Route{
			Prefix:       r.Prefix,
			NextHop:      r.NextHop,
			Metric:       r.Metric,
			LeaseSeconds: r.LeaseSeconds,
			Tags:         r.Tags,
		})
	}

	return &pb.StateResponse{
		NodeId:      s.cfg.Node.ID,
		Routes:      pbRoutes,
		TimestampMs: time.Now().UnixMilli(),
		Accepted:    true,
	}, nil
}

// AnnounceRoutes implements the AnnounceRoutes RPC.
// Called when a peer announces new or updated routes.
func (s *Server) AnnounceRoutes(ctx context.Context, req *pb.RouteAnnouncement) (*pb.RouteAck, error) {
	s.logger.Debug("received route announcement",
		"peer_id", req.NodeId,
		"route_count", len(req.Routes),
	)

	// Process incoming routes
	incomingRoutes := make([]Route, 0, len(req.Routes))
	for _, r := range req.Routes {
		route := Route{
			Prefix:       r.Prefix,
			NextHop:      r.NextHop,
			Metric:       r.Metric,
			LeaseSeconds: r.LeaseSeconds,
			Tags:         r.Tags,
			PeerID:       req.NodeId,
		}
		s.routeTable.Add(route)
		incomingRoutes = append(incomingRoutes, route)
	}

	// Invoke callback if set
	s.mu.RLock()
	callback := s.onRoutesReceived
	s.mu.RUnlock()
	if callback != nil && len(incomingRoutes) > 0 {
		callback(incomingRoutes)
	}

	s.logger.Info("processed route announcement",
		"peer_id", req.NodeId,
		"count", len(req.Routes),
	)

	return &pb.RouteAck{
		Accepted:        true,
		RoutesProcessed: uint32(len(req.Routes)),
	}, nil
}

// WithdrawRoutes implements the WithdrawRoutes RPC.
// Called when a peer withdraws routes.
func (s *Server) WithdrawRoutes(ctx context.Context, req *pb.RouteWithdrawal) (*pb.RouteAck, error) {
	s.logger.Info("received route withdrawal",
		"peer_id", req.NodeId,
		"prefix_count", len(req.Prefixes),
	)

	count := 0
	for _, prefix := range req.Prefixes {
		// Only remove if the route was from this peer
		if r, ok := s.routeTable.Get(prefix); ok && r.PeerID == req.NodeId {
			s.routeTable.Remove(prefix)
			count++
		}
	}

	s.logger.Info("processed route withdrawal",
		"peer_id", req.NodeId,
		"removed_count", count,
	)

	return &pb.RouteAck{
		Accepted:        true,
		RoutesProcessed: uint32(count),
	}, nil
}

// Keepalive implements the bidirectional Keepalive RPC.
func (s *Server) Keepalive(stream grpc.BidiStreamingServer[pb.KeepaliveRequest, pb.KeepaliveResponse]) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}

		s.logger.Debug("received keepalive",
			"peer_id", req.NodeId,
			"sequence", req.Sequence,
		)

		// Send response
		resp := &pb.KeepaliveResponse{
			NodeId:      s.cfg.Node.ID,
			Sequence:    req.Sequence,
			TimestampMs: time.Now().UnixMilli(),
			Health: &pb.PeerHealth{
				Healthy:       true,
				RouteCount:    uint32(len(s.routeTable.All())),
				UptimeSeconds: uint64(time.Since(s.startTime).Seconds()),
			},
		}

		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// getExportableRoutes returns routes that should be exported to peers.
// This would typically filter based on routing policies.
func (s *Server) getExportableRoutes() []Route {
	// For now, return all routes not learned from peers (i.e., local routes)
	all := s.routeTable.All()
	exportable := make([]Route, 0)
	for _, r := range all {
		if r.PeerID == "" { // Local route
			exportable = append(exportable, r)
		}
	}
	return exportable
}

// Client manages outbound connections to peer control-plane servers.
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
	client   pb.NNetManClient
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
	var firstErr error
	for _, peer := range c.cfg.Overlay.Peers {
		if err := c.connectPeer(ctx, peer); err != nil {
			c.logger.Warn("failed to connect to peer",
				"peer_id", peer.ID,
				"address", peer.Endpoint.Address,
				"error", err,
			)
			if firstErr == nil {
				firstErr = err
			}
			// Continue connecting to other peers even if one fails
		}
	}
	return firstErr
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

	client := pb.NewNNetManClient(conn)

	c.conns[peer.ID] = &peerConn{
		peerID:   peer.ID,
		address:  addr,
		conn:     conn,
		client:   client,
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

// ExchangeStateWithPeers performs initial state exchange with all connected peers.
func (c *Client) ExchangeStateWithPeers(ctx context.Context, localRoutes []Route) error {
	c.mu.RLock()
	peers := make([]*peerConn, 0, len(c.conns))
	for _, pc := range c.conns {
		if pc.healthy && pc.client != nil {
			peers = append(peers, pc)
		}
	}
	c.mu.RUnlock()

	// Convert routes to proto
	pbRoutes := make([]*pb.Route, 0, len(localRoutes))
	for _, r := range localRoutes {
		pbRoutes = append(pbRoutes, &pb.Route{
			Prefix:       r.Prefix,
			NextHop:      r.NextHop,
			Metric:       r.Metric,
			LeaseSeconds: r.LeaseSeconds,
			Tags:         r.Tags,
		})
	}

	req := &pb.StateRequest{
		NodeId:      c.cfg.Node.ID,
		Routes:      pbRoutes,
		TimestampMs: time.Now().UnixMilli(),
	}

	for _, pc := range peers {
		if err := c.exchangeWithPeer(ctx, pc, req); err != nil {
			c.logger.Warn("failed to exchange state with peer",
				"peer_id", pc.peerID,
				"error", err,
			)
			c.markPeerUnhealthy(pc.peerID)
		}
	}

	return nil
}

// exchangeWithPeer performs state exchange with a single peer.
func (c *Client) exchangeWithPeer(ctx context.Context, pc *peerConn, req *pb.StateRequest) error {
	resp, err := pc.client.ExchangeState(ctx, req)
	if err != nil {
		return fmt.Errorf("ExchangeState RPC failed: %w", err)
	}

	// Store received routes
	for _, r := range resp.Routes {
		route := Route{
			Prefix:       r.Prefix,
			NextHop:      r.NextHop,
			Metric:       r.Metric,
			LeaseSeconds: r.LeaseSeconds,
			Tags:         r.Tags,
			PeerID:       resp.NodeId,
		}
		c.routeTable.Add(route)
	}

	c.logger.Info("exchanged state with peer",
		"peer_id", pc.peerID,
		"routes_sent", len(req.Routes),
		"routes_received", len(resp.Routes),
		"accepted", resp.Accepted,
	)

	// Update peer health
	c.mu.Lock()
	if p, ok := c.conns[pc.peerID]; ok {
		p.healthy = true
		p.lastSeen = time.Now()
	}
	c.mu.Unlock()

	return nil
}

// AnnounceRoutes sends route announcements to all connected peers.
func (c *Client) AnnounceRoutes(ctx context.Context, routes []Route) error {
	c.mu.RLock()
	peers := make([]*peerConn, 0, len(c.conns))
	for _, pc := range c.conns {
		if pc.healthy && pc.client != nil {
			peers = append(peers, pc)
		}
	}
	c.mu.RUnlock()

	if len(peers) == 0 {
		return nil
	}

	// Convert routes to proto
	pbRoutes := make([]*pb.Route, 0, len(routes))
	for _, r := range routes {
		pbRoutes = append(pbRoutes, &pb.Route{
			Prefix:       r.Prefix,
			NextHop:      r.NextHop,
			Metric:       r.Metric,
			LeaseSeconds: r.LeaseSeconds,
			Tags:         r.Tags,
		})
	}

	req := &pb.RouteAnnouncement{
		NodeId:      c.cfg.Node.ID,
		Routes:      pbRoutes,
		TimestampMs: time.Now().UnixMilli(),
	}

	for _, pc := range peers {
		if err := c.announceToSinglePeer(ctx, pc, req); err != nil {
			c.logger.Warn("failed to announce routes to peer",
				"peer_id", pc.peerID,
				"error", err,
			)
			c.markPeerUnhealthy(pc.peerID)
		}
	}

	return nil
}

// announceToSinglePeer sends routes to a single peer.
func (c *Client) announceToSinglePeer(ctx context.Context, pc *peerConn, req *pb.RouteAnnouncement) error {
	resp, err := pc.client.AnnounceRoutes(ctx, req)
	if err != nil {
		return fmt.Errorf("AnnounceRoutes RPC failed: %w", err)
	}

	if !resp.Accepted {
		return fmt.Errorf("peer rejected routes: %s", resp.Error)
	}

	c.logger.Debug("announced routes to peer",
		"peer_id", pc.peerID,
		"route_count", len(req.Routes),
		"accepted", resp.Accepted,
	)

	// Update peer health
	c.mu.Lock()
	if p, ok := c.conns[pc.peerID]; ok {
		p.healthy = true
		p.lastSeen = time.Now()
	}
	c.mu.Unlock()

	return nil
}

// WithdrawRoutes sends route withdrawal to all connected peers.
func (c *Client) WithdrawRoutes(ctx context.Context, prefixes []string) error {
	c.mu.RLock()
	peers := make([]*peerConn, 0, len(c.conns))
	for _, pc := range c.conns {
		if pc.healthy && pc.client != nil {
			peers = append(peers, pc)
		}
	}
	c.mu.RUnlock()

	if len(peers) == 0 {
		return nil
	}

	req := &pb.RouteWithdrawal{
		NodeId:      c.cfg.Node.ID,
		Prefixes:    prefixes,
		TimestampMs: time.Now().UnixMilli(),
	}

	for _, pc := range peers {
		_, err := pc.client.WithdrawRoutes(ctx, req)
		if err != nil {
			c.logger.Warn("failed to withdraw routes from peer",
				"peer_id", pc.peerID,
				"error", err,
			)
			c.markPeerUnhealthy(pc.peerID)
		}
	}

	return nil
}

// markPeerUnhealthy marks a peer as unhealthy.
func (c *Client) markPeerUnhealthy(peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.conns[peerID]; ok {
		p.healthy = false
	}
}

// GetPeerStatus returns the connection status of all peers.
func (c *Client) GetPeerStatus() map[string]PeerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := make(map[string]PeerStatus)
	for id, pc := range c.conns {
		status[id] = PeerStatus{
			Connected: pc.conn != nil,
			Healthy:   pc.healthy,
			LastSeen:  pc.lastSeen,
			Address:   pc.address,
		}
	}
	return status
}

// PeerStatus contains the status of a peer connection.
type PeerStatus struct {
	Connected bool
	Healthy   bool
	LastSeen  time.Time
	Address   string
}

// IsHealthy returns true if at least one peer is healthy.
func (c *Client) IsHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, pc := range c.conns {
		if pc.healthy {
			return true
		}
	}
	return false
}

// CheckPeerHealth performs health checks on all peers.
func (c *Client) CheckPeerHealth(ctx context.Context) error {
	c.mu.RLock()
	peers := make([]*peerConn, 0, len(c.conns))
	for _, pc := range c.conns {
		if pc.client != nil {
			peers = append(peers, pc)
		}
	}
	c.mu.RUnlock()

	for _, pc := range peers {
		// Try a simple state exchange as health check
		req := &pb.StateRequest{
			NodeId:      c.cfg.Node.ID,
			Routes:      nil, // Empty for health check
			TimestampMs: time.Now().UnixMilli(),
		}

		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := pc.client.ExchangeState(ctx, req)
		cancel()

		c.mu.Lock()
		if p, ok := c.conns[pc.peerID]; ok {
			if err != nil {
				if status.Code(err) == codes.Unavailable {
					p.healthy = false
					c.logger.Warn("peer unreachable", "peer_id", pc.peerID, "error", err)
				}
			} else {
				p.healthy = true
				p.lastSeen = time.Now()
			}
		}
		c.mu.Unlock()
	}

	return nil
}

// GetPeerStatuses returns the status of all peers.
// This implements the observability.StatusProvider interface.
func (c *Client) GetPeerStatuses() map[string]observability.PeerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]observability.PeerStatus)

	// Start with configured peers
	for _, peer := range c.cfg.Overlay.Peers {
		ps := observability.PeerStatus{
			ID:       peer.ID,
			Endpoint: peer.Endpoint.Address,
			Status:   "disconnected",
			Routes:   0,
		}

		// Update with actual connection status
		if pc, ok := c.conns[peer.ID]; ok {
			if pc.healthy {
				ps.Status = "healthy"
				if !pc.lastSeen.IsZero() {
					ps.LastSeen = time.Since(pc.lastSeen).Round(time.Second).String()
				}
			} else if pc.conn != nil {
				ps.Status = "unhealthy"
			}
		}

		result[peer.ID] = ps
	}

	// Count routes per peer
	for _, route := range c.routeTable.All() {
		if route.PeerID != "" {
			if ps, ok := result[route.PeerID]; ok {
				ps.Routes++
				result[route.PeerID] = ps
			}
		}
	}

	return result
}

// GetRouteStats returns route statistics.
// This implements the observability.StatusProvider interface.
func (c *Client) GetRouteStats() observability.RouteStats {
	stats := observability.RouteStats{}

	// Exported routes from config
	stats.Exported = len(c.cfg.Routing.Export.Networks)

	// Installed routes (routes received from peers)
	for _, route := range c.routeTable.All() {
		if route.PeerID != "" {
			stats.Installed++
		}
	}

	return stats
}
