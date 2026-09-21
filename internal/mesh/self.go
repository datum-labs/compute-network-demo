package mesh

import (
	"net"
	"net/netip"

	"github.com/datum-labs/compute-network-demo/internal/datum"
)

// LocalAddrs lists the addresses assigned to this machine's interfaces.
func LocalAddrs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(ipnet.IP); ok {
			out = append(out, ip.Unmap())
		}
	}
	return out
}

// FindSelf works out which Instance this process is, since the platform does
// not tell a container its instance name. An exact address match wins. If the
// interface holds a different address inside the reported prefix, the single
// instance whose prefix contains it is chosen. As a last resort the hostname
// is compared with instance names.
func FindSelf(instances []datum.Instance, local []netip.Addr, hostname string) string {
	for _, inst := range instances {
		if !inst.PrivateIP.IsValid() {
			continue
		}
		for _, a := range local {
			if a == inst.PrivateIP {
				return inst.Name
			}
		}
	}

	match := ""
	for _, inst := range instances {
		// A host route carries no containment information beyond equality,
		// and very wide prefixes would match unrelated addresses.
		if !inst.Prefix.IsValid() || inst.Prefix.Bits() == inst.Prefix.Addr().BitLen() || inst.Prefix.Bits() < 64 && inst.Prefix.Addr().Is6() {
			continue
		}
		for _, a := range local {
			if a.IsLoopback() || a.IsLinkLocalUnicast() {
				continue
			}
			if inst.Prefix.Masked().Contains(a) {
				if match != "" && match != inst.Name {
					return hostnameMatch(instances, hostname)
				}
				match = inst.Name
			}
		}
	}
	if match != "" {
		return match
	}
	return hostnameMatch(instances, hostname)
}

func hostnameMatch(instances []datum.Instance, hostname string) string {
	if hostname == "" {
		return ""
	}
	for _, inst := range instances {
		if inst.Name == hostname {
			return inst.Name
		}
	}
	return ""
}
