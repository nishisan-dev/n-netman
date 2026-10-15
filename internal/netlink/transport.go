package netlink

import (
	"context"
	"fmt"
	"net"
	"syscall"

	"github.com/nishisan-dev/n-netman/internal/config"
	nl "github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// ValidatePath checks source ownership and the actual FIB lookup without forcing
// an output device in the lookup (which could hide recursion via a backup).
func ValidatePath(device, local, remote string, direct bool) error {
	link, e := nl.LinkByName(device)
	if e != nil {
		return e
	}
	if link.Attrs().Flags&net.FlagUp == 0 {
		return fmt.Errorf("device %s is down", device)
	}
	src, dst := net.ParseIP(local), net.ParseIP(remote)
	addresses, e := nl.AddrList(link, nl.FAMILY_V4)
	if e != nil {
		return e
	}
	found, subnet := false, false
	for _, a := range addresses {
		if a.IP.Equal(src) {
			found = true
			subnet = a.IPNet.Contains(dst)
		}
	}
	if !found {
		return fmt.Errorf("source %s is not assigned to %s", local, device)
	}
	if direct && !subnet {
		return fmt.Errorf("peer %s is outside source subnet on %s", remote, device)
	}
	routes, e := nl.RouteGetWithOptions(dst, &nl.RouteGetOptions{SrcAddr: src})
	if e != nil {
		return e
	}
	if len(routes) != 1 || routes[0].LinkIndex != link.Attrs().Index || len(routes[0].MultiPath) != 0 || (direct && len(routes[0].Gw) != 0) {
		return fmt.Errorf("path to %s escapes %s or is recursive", remote, device)
	}
	return nil
}
func BindDevice(device string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var bindErr error
		e := raw.Control(func(fd uintptr) {
			bindErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, device)
		})
		if e != nil {
			return e
		}
		if bindErr != nil {
			return fmt.Errorf("SO_BINDTODEVICE %s: %w", device, bindErr)
		}
		return nil
	}
}
func DialPath(ctx context.Context, s config.PeerSession, address string) (net.Conn, error) {
	if e := ValidatePath(s.BindDevice, s.LocalIP, s.RemoteIP, true); e != nil {
		return nil, e
	}
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(s.LocalIP)}, Control: BindDevice(s.BindDevice)}
	return d.DialContext(ctx, "tcp4", address)
}

// ObjectChange is a concrete, read-only plan of network mutations.
type ObjectChange struct{ Action, Object, Detail string }

// PlanTransport validates existing objects before planning any mutation.
func PlanTransport(t config.Transport) ([]ObjectChange, error) {
	if !t.Managed {
		return []ObjectChange{{"preserve", t.Device(), "observe-only: master, MTU, addresses and ports unchanged"}}, ValidatePath(t.Device(), t.Source(), t.PeerIP, true)
	}
	if t.Type != "vxlan" || !t.Bridge.Managed {
		return nil, fmt.Errorf("transport is not explicitly managed")
	}
	if e := ValidatePath(t.Underlay.Interface, t.Underlay.LocalVTEPIP, t.Underlay.RemoteVTEPIP, false); e != nil {
		return nil, fmt.Errorf("underlay: %w", e)
	}
	var changes []ObjectChange
	bridge, be := nl.LinkByName(t.Bridge.Name)
	if be == nil {
		if _, ok := bridge.(*nl.Bridge); !ok {
			return nil, fmt.Errorf("%s is not a bridge", t.Bridge.Name)
		}
		if bridge.Attrs().MTU != t.MTU {
			return nil, fmt.Errorf("existing bridge MTU differs; adjust explicitly")
		}
		changes = append(changes, ObjectChange{"preserve", t.Bridge.Name, "existing bridge MTU and ports"})
		if bridge.Attrs().Flags&net.FlagUp == 0 {
			changes = append(changes, ObjectChange{"up-bridge", t.Bridge.Name, "set UP"})
		}
		ip, _, _ := net.ParseCIDR(t.Bridge.IPv4)
		addresses, e := nl.AddrList(bridge, nl.FAMILY_V4)
		if e != nil {
			return nil, e
		}
		found := false
		for _, a := range addresses {
			if a.IP.Equal(ip) {
				if a.IPNet.String() != t.Bridge.IPv4 {
					return nil, fmt.Errorf("existing bridge address mask differs")
				}
				found = true
			}
		}
		if !found {
			changes = append(changes, ObjectChange{"add-address", t.Bridge.Name, t.Bridge.IPv4})
		}
	} else {
		if _, ok := be.(nl.LinkNotFoundError); !ok {
			return nil, be
		}
		changes = append(changes, ObjectChange{"create-bridge", t.Bridge.Name, fmt.Sprintf("mtu=%d UP", t.MTU)}, ObjectChange{"add-address", t.Bridge.Name, t.Bridge.IPv4})
	}
	vx, ve := nl.LinkByName(t.Name)
	if ve == nil {
		v, ok := vx.(*nl.Vxlan)
		if !ok {
			return nil, fmt.Errorf("%s is not VXLAN", t.Name)
		}
		under, e := nl.LinkByName(t.Underlay.Interface)
		if e != nil {
			return nil, e
		}
		if v.VxlanId != t.VNI || !v.SrcAddr.Equal(net.ParseIP(t.Underlay.LocalVTEPIP)) || v.VtepDevIndex != under.Attrs().Index || v.Port != t.DstPort || (v.Attrs().MasterIndex != 0 && (bridge == nil || v.Attrs().MasterIndex != bridge.Attrs().Index)) {
			return nil, fmt.Errorf("existing VXLAN differs; refusing destructive replacement")
		}
		changes = append(changes, ObjectChange{"preserve", t.Name, "existing VNI, source VTEP, underlay and port"})
		if v.Attrs().MTU != t.MTU {
			changes = append(changes, ObjectChange{"mtu-vxlan", t.Name, fmt.Sprint(t.MTU)})
		}
		if v.Attrs().Flags&net.FlagUp == 0 {
			changes = append(changes, ObjectChange{"up-vxlan", t.Name, "set UP"})
		}
		if v.Attrs().MasterIndex == 0 {
			changes = append(changes, ObjectChange{"attach", t.Name, t.Bridge.Name})
		}
	} else {
		if _, ok := ve.(nl.LinkNotFoundError); !ok {
			return nil, ve
		}
		changes = append(changes, ObjectChange{"create-vxlan", t.Name, fmt.Sprintf("vni=%d src=%s underlay=%s port=%d mtu=%d bridge=%s UP", t.VNI, t.Underlay.LocalVTEPIP, t.Underlay.Interface, t.DstPort, t.MTU, t.Bridge.Name)})
	}
	found := false
	if ve == nil {
		entries, e := NewFDBManager().List(t.Name)
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if !isZeroMAC(entry.MAC) {
				continue
			}
			if entry.RemoteIP.String() == t.Underlay.RemoteVTEPIP {
				found = true
			} else {
				changes = append(changes, ObjectChange{"delete-fdb", t.Name, entry.RemoteIP.String()})
			}
		}
	}
	if !found {
		changes = append(changes, ObjectChange{"add-fdb", t.Name, t.Underlay.RemoteVTEPIP})
	}
	return changes, nil
}

// ReconcileTransport executes the same plan presented by apply --dry-run.
func ReconcileTransport(t config.Transport) error {
	changes, e := PlanTransport(t)
	if e != nil {
		return e
	}
	for _, c := range changes {
		switch c.Action {
		case "preserve":
			continue
		case "create-bridge":
			e = NewBridgeManager().Create(BridgeConfig{Name: t.Bridge.Name, MTU: t.MTU})
		case "add-address":
			e = NewBridgeManager().AddAddress(t.Bridge.Name, t.Bridge.IPv4)
		case "create-vxlan":
			e = NewVXLANManager().Create(VXLANConfig{Name: t.Name, VNI: t.VNI, DstPort: t.DstPort, MTU: t.MTU, Bridge: t.Bridge.Name, LocalIP: net.ParseIP(t.Underlay.LocalVTEPIP), VtepDev: t.Underlay.Interface})
		case "up-bridge", "up-vxlan":
			var link nl.Link
			link, e = nl.LinkByName(c.Object)
			if e == nil {
				e = nl.LinkSetUp(link)
			}
		case "mtu-vxlan":
			var link nl.Link
			link, e = nl.LinkByName(c.Object)
			if e == nil {
				e = nl.LinkSetMTU(link, t.MTU)
			}
		case "attach":
			e = NewVXLANManager().AttachToBridge(t.Name, t.Bridge.Name)
		case "add-fdb":
			e = NewFDBManager().AddPeer(t.Name, net.ParseIP(c.Detail))
		case "delete-fdb":
			e = NewFDBManager().DeletePeer(t.Name, net.ParseIP(c.Detail))
		default:
			return fmt.Errorf("unknown transport operation %s", c.Action)
		}
		if e != nil {
			return fmt.Errorf("%s %s: %w", c.Action, c.Object, e)
		}
	}
	return nil
}
