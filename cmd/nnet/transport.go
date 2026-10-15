package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"text/tabwriter"
	"time"

	"github.com/nishisan-dev/n-netman/internal/config"
	nl "github.com/nishisan-dev/n-netman/internal/netlink"
	"github.com/nishisan-dev/n-netman/internal/observability"
	"github.com/nishisan-dev/n-netman/internal/reconciler"
	"github.com/spf13/cobra"
)

func peersCmd() *cobra.Command {
	return &cobra.Command{Use: "peers", Short: "Show transport sessions", RunE: func(cmd *cobra.Command, args []string) error {
		c, e := loadConfig()
		if e != nil {
			return e
		}
		if c.Version != 3 {
			return fmt.Errorf("peers requires version 3; use status for legacy peers")
		}
		return transportStatus(c)
	}}
}
func transportStatus(c *config.Config) error {
	status := getDaemonStatus(c)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "PEER\tSESSION\tTRANSPORT\tSTATE\tSOURCE\tREMOTE\tDEVICE\tRTT\tUNDERLAY/INTERFACE\tERROR")
	for _, p := range c.Peers {
		for _, s := range p.Sessions {
			ps := observability.PeerStatus{Status: "daemon unavailable"}
			if status != nil {
				if live, ok := status.Peers[p.ID+"/"+s.ID]; ok {
					ps = live
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.3fms\t%t/%t\t%s\n", p.ID, s.ID, s.Transport, ps.Status, s.LocalIP, s.RemoteIP, s.BindDevice, ps.RTTSeconds*1000, ps.UnderlayReachable, ps.OverlayReady, ps.Error)
		}
	}
	return nil
}
func transportRoutes(c *config.Config) error {
	rm := nl.NewRouteManager()
	manager, e := nl.NewCandidateManager(rm, c.StateFile)
	if e != nil {
		return e
	}
	owned, e := manager.Current()
	if e != nil {
		return e
	}
	cache := map[int][]nl.RouteInfo{}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "PREFIX\tTABLE\tPEER/SESSION\tVIA\tDEVICE\tMETRIC\tORIGIN\tSTATE")
	for _, policy := range c.RoutePolicies {
		table := int(policy.Table)
		routes, ok := cache[table]
		if !ok {
			var e error
			routes, e = rm.List(table)
			if e != nil {
				return e
			}
			cache[table] = routes
		}
		best := int(^uint(0) >> 1)
		for _, r := range routes {
			if r.Destination != nil && r.Destination.String() == policy.Prefix && r.Metric < best {
				best = r.Metric
			}
		}
		for _, cand := range policy.Candidates {
			state := "INELIGIBLE"
			for _, entry := range owned {
				r := entry.Route
				if entry.Peer == policy.Peer && entry.Session == cand.Session && r.Destination != nil && r.Destination.String() == policy.Prefix && r.Gateway.Equal(net.ParseIP(cand.Via)) && r.Device == cand.Dev && r.Metric == cand.Metric && r.Protocol == nl.RouteProtocolNNetMan {
					state = "INSTALLED / STANDBY"
					if r.Metric == best {
						state = "INSTALLED / PREFERRED"
					}
				}
			}
			fmt.Fprintf(w, "%s\t%d\t%s/%s\t%s\t%s\t%d\tproto 99\t%s\n", policy.Prefix, table, policy.Peer, cand.Session, cand.Via, cand.Dev, cand.Metric, state)
		}
	}
	return nil
}
func transportDoctor(c *config.Config) error {
	failed := false
	for _, t := range c.Transports {
		e := nl.ValidatePath(t.Device(), t.Source(), t.PeerIP, true)
		if e != nil {
			fmt.Printf("transport %s: %v\n", t.ID, e)
			failed = true
		} else {
			fmt.Printf("transport %s: source and direct egress OK\n", t.ID)
		}
		if t.Type == "vxlan" && t.Managed {
			if e := nl.ValidatePath(t.Underlay.Interface, t.Underlay.LocalVTEPIP, t.Underlay.RemoteVTEPIP, false); e != nil {
				fmt.Printf("underlay %s: %v\n", t.ID, e)
				failed = true
			}
		}
	}
	for _, p := range c.Peers {
		for _, s := range p.Sessions {
			// Exercise the real bind option without opening a listening socket or
			// requiring the peer to be online.
			lc := net.ListenConfig{Control: nl.BindDevice(s.BindDevice)}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			lis, e := lc.Listen(ctx, "tcp4", net.JoinHostPort(s.LocalIP, "0"))
			cancel()
			if e != nil {
				fmt.Printf("bind %s/%s: %v\n", p.ID, s.ID, e)
				failed = true
			} else {
				lis.Close()
				fmt.Printf("bind %s/%s: OK\n", p.ID, s.ID)
			}
		}
	}
	manager, e := nl.NewCandidateManager(nl.NewRouteManager(), c.StateFile)
	if e != nil {
		return e
	}
	// Check candidate slots without installing unhealthy routes.
	var candidates []nl.PathRoute
	for _, p := range c.RoutePolicies {
		for _, cand := range p.Candidates {
			_, dst, _ := net.ParseCIDR(p.Prefix)
			var transport string
			for _, peer := range c.Peers {
				if peer.ID == p.Peer {
					for _, s := range peer.Sessions {
						if s.ID == cand.Session {
							transport = s.Transport
						}
					}
				}
			}
			candidates = append(candidates, nl.PathRoute{Peer: p.Peer, Session: cand.Session, Transport: transport, Route: nl.RouteConfig{Destination: dst, Gateway: net.ParseIP(cand.Via), Device: cand.Dev, Metric: cand.Metric, Table: int(p.Table), Protocol: 99}})
		}
	}
	if _, e := manager.Plan(candidates); e != nil {
		fmt.Printf("candidate conflicts: %v\n", e)
		failed = true
	}
	if failed {
		return fmt.Errorf("transport diagnostics failed")
	}
	return nil
}
func transportApply(c *config.Config, dryRun bool) error {
	if dryRun {
		for _, t := range c.Transports {
			changes, e := nl.PlanTransport(t)
			for _, change := range changes {
				fmt.Printf("%s %s: %s\n", change.Action, change.Object, change.Detail)
			}
			if e != nil {
				return fmt.Errorf("transport %s: %w", t.ID, e)
			}
		}
		for _, o := range c.GetOverlays() {
			fmt.Printf("ENSURE explicit legacy overlay %s bridge=%s VNI=%d\n", o.Name, o.Bridge.Name, o.VNI)
		}
		fmt.Println("ROUTES: no changes by apply; daemon alone owns health-dependent candidates.")
		for _, p := range c.RoutePolicies {
			for _, r := range p.Candidates {
				fmt.Printf("CONDITIONAL table=%d prefix=%s via=%s dev=%s metric=%d peer=%s session=%s: add only after valid snapshot + stable ACKs; remove on DOWN/withdraw/lease expiry\n", p.Table, p.Prefix, r.Via, r.Dev, r.Metric, p.Peer, r.Session)
			}
		}
		manager, e := nl.NewCandidateManager(nl.NewRouteManager(), c.StateFile)
		if e != nil {
			return e
		}
		changes, e := manager.Plan(nil)
		if e != nil {
			return e
		}
		for _, change := range changes {
			fmt.Printf("DAEMON RESTART %s %s\n", change.Action, change.Candidate.Key())
		}
		return nil
	}
	return reconciler.New(c).RunOnce(context.Background())
}
