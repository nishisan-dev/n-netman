package netlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/sys/unix"
)

// CandidateBackend permits exercising ownership/conflict logic without privileges.
type CandidateBackend interface {
	List(int) ([]RouteInfo, error)
	Add(RouteConfig) error
	Delete(RouteConfig) error
}
type PathRoute struct {
	Peer      string
	Session   string
	Transport string
	Route     RouteConfig
}

func (p PathRoute) Key() string {
	return fmt.Sprintf("%s/%s/%s/%s", p.Peer, p.Session, p.Transport, routeKey(p.Route))
}
func routeKey(r RouteConfig) string {
	return fmt.Sprintf("%d/%s/%s/%s/%d/%d", r.Table, r.Destination, r.Gateway, r.Device, r.Metric, r.Protocol)
}
func routeSlot(r RouteConfig) string {
	return fmt.Sprintf("%d/%s/%d", r.Table, r.Destination, r.Metric)
}
func infoConfig(r RouteInfo) RouteConfig {
	return RouteConfig{Destination: r.Destination, Gateway: r.Gateway, Device: r.Device, Table: r.Table, Metric: r.Metric, Protocol: r.Protocol}
}

type RouteChange struct {
	Action    string
	Candidate PathRoute
}

// CandidateManager owns only entries in its durable journal, never proto 99 alone.
// The journal must be private to this daemon and preserved across restarts.
type CandidateManager struct {
	mu      sync.Mutex
	backend CandidateBackend
	file    string
	owned   map[string]PathRoute
}

func NewCandidateManager(backend CandidateBackend, file string) (*CandidateManager, error) {
	m := &CandidateManager{backend: backend, file: file, owned: map[string]PathRoute{}}
	b, e := os.ReadFile(file)
	if e == nil {
		var entries []PathRoute
		if e = json.Unmarshal(b, &entries); e != nil {
			return nil, e
		}
		for _, p := range entries {
			if p.Route.Destination == nil || p.Route.Table <= 0 || p.Route.Protocol != RouteProtocolNNetMan || p.Route.Device == "" {
				return nil, fmt.Errorf("invalid ownership journal")
			}
			m.owned[p.Key()] = p
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	return m, nil
}
func (m *CandidateManager) save() error {
	entries := make([]PathRoute, 0, len(m.owned))
	for _, p := range m.owned {
		entries = append(entries, p)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key() < entries[j].Key() })
	b, e := json.MarshalIndent(entries, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(m.file), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(m.file), ".path-routes-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(f.Name(), m.file); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(m.file))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func (m *CandidateManager) plan(desired []PathRoute) ([]RouteChange, error) {
	want := map[string]PathRoute{}
	tables := map[int]bool{}
	slots := map[string]bool{}
	for _, p := range desired {
		r := p.Route
		if r.Destination == nil || r.Table <= 0 || r.Protocol != RouteProtocolNNetMan || r.Device == "" || r.Metric <= 0 {
			return nil, fmt.Errorf("invalid candidate")
		}
		if slots[routeSlot(r)] {
			return nil, fmt.Errorf("duplicate kernel route slot")
		}
		slots[routeSlot(r)] = true
		want[p.Key()] = p
		tables[r.Table] = true
	}
	for _, p := range m.owned {
		tables[p.Route.Table] = true
	}
	current := map[string]RouteConfig{}
	occupied := map[string][]RouteConfig{}
	for table := range tables {
		routes, e := m.backend.List(table)
		if e != nil {
			return nil, e
		}
		for _, r := range routes {
			cfg := infoConfig(r)
			current[routeKey(cfg)] = cfg
			occupied[routeSlot(cfg)] = append(occupied[routeSlot(cfg)], cfg)
		}
	}
	var changes []RouteChange
	var conflicts []error
	for k, p := range m.owned {
		if _, ok := want[k]; !ok {
			if _, exists := current[routeKey(p.Route)]; exists {
				changes = append(changes, RouteChange{"delete", p})
			} else {
				changes = append(changes, RouteChange{"forget", p})
			}
		}
	}
	for k, p := range want {
		_, owned := m.owned[k]
		if _, exists := current[routeKey(p.Route)]; exists && owned {
			continue
		}
		if routes := occupied[routeSlot(p.Route)]; len(routes) > 0 {
			conflicts = append(conflicts, fmt.Errorf("route conflict for %s: existing %s; refusing takeover", p.Key(), routeKey(routes[0])))
			continue
		}
		changes = append(changes, RouteChange{"add", p})
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Action != changes[j].Action {
			return changes[i].Action > changes[j].Action
		}
		return changes[i].Candidate.Key() < changes[j].Candidate.Key()
	})
	return changes, errors.Join(conflicts...)
}
func (m *CandidateManager) Plan(desired []PathRoute) ([]RouteChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.plan(desired)
}
func (m *CandidateManager) SyncCandidates(desired []PathRoute) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	changes, planErr := m.plan(desired)
	var e error
	if len(changes) == 0 {
		return planErr
	}
	for _, change := range changes {
		p := change.Candidate
		key := p.Key()
		switch change.Action {
		case "delete":
			if e = m.backend.Delete(p.Route); e != nil {
				return e
			}
			delete(m.owned, key)
			if e = m.save(); e != nil {
				m.owned[key] = p
				return e
			}
		case "forget":
			delete(m.owned, key)
			if e = m.save(); e != nil {
				m.owned[key] = p
				return e
			}
		case "add":
			// Write intent before the kernel write, so a crash cannot leave an untracked route.
			_, wasOwned := m.owned[key]
			m.owned[key] = p
			if e = m.save(); e != nil {
				if !wasOwned {
					delete(m.owned, key)
				}
				return e
			}
			if e = m.backend.Add(p.Route); e != nil {
				if !wasOwned {
					delete(m.owned, key)
					if se := m.save(); se != nil {
						return errors.Join(e, se)
					}
				}
				return e
			}
		}
	}
	return planErr
}

// Lock prevents two daemon instances from using the same ownership journal.
func (m *CandidateManager) Lock() (func(), error) {
	if e := os.MkdirAll(filepath.Dir(m.file), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(m.file+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("ownership journal already in use: %w", e)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}

// Current returns only journaled candidates that still match the actual FIB.
func (m *CandidateManager) Current() ([]PathRoute, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tables := map[int]bool{}
	for _, p := range m.owned {
		tables[p.Route.Table] = true
	}
	actual := map[string]bool{}
	for table := range tables {
		routes, e := m.backend.List(table)
		if e != nil {
			return nil, e
		}
		for _, r := range routes {
			actual[routeKey(infoConfig(r))] = true
		}
	}
	var out []PathRoute
	for _, p := range m.owned {
		if actual[routeKey(p.Route)] {
			out = append(out, p)
		}
	}
	return out, nil
}
