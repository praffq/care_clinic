package mdns

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Advertiser struct {
	name    string
	links   []lanInterface
	mu      sync.Mutex
	servers []*responder
}

func Advertise(name string, logf func(string)) (*Advertiser, error) {
	name = Label(name)
	if err := ValidateLabel(name); err != nil {
		return nil, err
	}
	links, err := lanInterfaces()
	if err != nil {
		return nil, err
	}
	if err := checkAvailable(name+".local.", links, nil, probeConflict); err != nil {
		return nil, err
	}
	a := &Advertiser{name: name, links: links}
	for _, link := range links {
		server, err := newResponder(name, link, logf)
		if err != nil {
			a.Stop()
			return nil, fmt.Errorf("advertise on %s: %w", link.iface.Name, err)
		}
		a.servers = append(a.servers, server)
	}
	return a, nil
}

func (a *Advertiser) Name() string { return a.name }

func (a *Advertiser) Stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, server := range a.servers {
		server.Shutdown()
	}
	a.servers = nil
}

func (a *Advertiser) IPsChanged() (bool, error) {
	cur, err := lanInterfaces()
	if err != nil {
		return false, err
	}
	return !sameLinks(a.links, cur), nil
}

func (a *Advertiser) Resolves() error {
	return a.resolves(probeHostname)
}

func (a *Advertiser) resolves(query func(context.Context, string, lanInterface) error) error {
	if a == nil || a.stopped() {
		return fmt.Errorf("name responder is stopped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan error, len(a.links))
	for _, link := range a.links {
		go func() {
			if err := query(ctx, a.name+".local.", link); err != nil {
				results <- fmt.Errorf("%s: %w", link.iface.Name, err)
			} else {
				results <- nil
			}
		}()
	}
	for range a.links {
		select {
		case err := <-results:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if a.stopped() {
		return fmt.Errorf("name responder stopped during query")
	}
	return nil
}

func (a *Advertiser) stopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.servers) == 0
}

type NameStatus struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func Label(name string) string {
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
	name = strings.TrimSuffix(name, ".local")
	return strings.Trim(name, ".")
}

var labelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func ValidateLabel(name string) error {
	label := strings.ToLower(Label(name))
	switch {
	case label == "":
		return fmt.Errorf("enter a name, for example care")
	case len(label) > 63:
		return fmt.Errorf("name is too long (63 characters at most)")
	case !labelRe.MatchString(label):
		return fmt.Errorf("use lowercase letters, numbers and hyphens only, for example care-test")
	}
	return nil
}

var virtualIface = []string{"docker", "br-", "veth", "virbr", "vboxnet", "vmnet", "utun", "tun", "tap"}

// Apple's link-local and peer-to-peer links: AWDL, low-latency WLAN,
// Wi-Fi Aware, the access-point side of Internet Sharing and the
// Apple-silicon private link to attached devices. None of them carry the LAN.
var appleVirtualIface = regexp.MustCompile(`^(awdl|llw|nan|ap|anpi)[0-9]+$`)

var (
	macBridge   = regexp.MustCompile(`^bridge[0-9]+$`)
	physicalNIC = regexp.MustCompile(`^en[0-9]+$`)
)

// ifaceSource reads the host's interfaces; tests replace it with fakes.
type ifaceSource struct {
	interfaces func() ([]net.Interface, error)
	addrs      func(net.Interface) ([]net.Addr, error)
	// bridgeMembers lists the member ports of a macOS bridgeN interface.
	// Nil disables the bridge check (other systems name bridges freely).
	bridgeMembers func(name string) ([]string, error)
}

var system = ifaceSource{
	interfaces: net.Interfaces,
	addrs:      func(ifi net.Interface) ([]net.Addr, error) { return ifi.Addrs() },
}

func init() {
	if runtime.GOOS == "darwin" {
		system.bridgeMembers = ifconfigBridgeMembers
	}
}

func lanInterfaces() ([]lanInterface, error) { return system.lanInterfaces() }

func (src ifaceSource) lanInterfaces() ([]lanInterface, error) {
	ifaces, err := src.interfaces()
	if err != nil {
		return nil, err
	}
	var links []lanInterface
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 ||
			ifi.Flags&net.FlagMulticast == 0 ||
			ifi.Flags&net.FlagLoopback != 0 ||
			ifi.Flags&net.FlagPointToPoint != 0 ||
			isVirtual(ifi.Name) {
			continue
		}
		addrs, err := src.addrs(ifi)
		if err != nil {
			return nil, fmt.Errorf("read addresses on %s: %w", ifi.Name, err)
		}
		link := lanInterface{iface: ifi}
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if network.IP.To4() == nil && !network.IP.IsLoopback() && !network.IP.IsUnspecified() {
				link.ipv6 = true
				link.ips6 = append(link.ips6, network.IP)
			}
			if ip4 := network.IP.To4(); ip4 != nil && ip4.IsGlobalUnicast() && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
				link.ips = append(link.ips, ip4)
				link.networks = append(link.networks, network)
			}
		}
		if len(link.ips) > 0 && !src.hostOnlyBridge(ifi.Name) {
			links = append(links, link)
		}
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("no multicast-capable LAN IPv4 interface found")
	}
	return links, nil
}

// hostOnlyBridge reports whether a macOS bridge has no physical port.
// Virtualization.framework and vmnet NAT networks (the engine VM's
// 192.168.64.1, Docker Desktop, UTM, ...) are bridgeN interfaces whose only
// members are vmenetN, so only the host and its VMs can reach that address.
// Thunderbolt Bridge and wired Internet Sharing bridge real enN ports, so
// devices on those segments can reach the host through the bridge address.
// A bridge whose members cannot be read is treated as host-only: missing a
// rare real bridge costs less than advertising an unreachable address.
func (src ifaceSource) hostOnlyBridge(name string) bool {
	if src.bridgeMembers == nil || !macBridge.MatchString(name) {
		return false
	}
	members, err := src.bridgeMembers(name)
	if err != nil {
		return true
	}
	for _, m := range members {
		if physicalNIC.MatchString(m) {
			return false
		}
	}
	return true
}

func ifconfigBridgeMembers(name string) ([]string, error) {
	out, err := exec.Command("/sbin/ifconfig", name).Output()
	if err != nil {
		return nil, err
	}
	return parseBridgeMembers(string(out)), nil
}

// parseBridgeMembers reads the "member: <port> flags=..." lines of ifconfig.
func parseBridgeMembers(ifconfig string) []string {
	var members []string
	for line := range strings.Lines(ifconfig) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "member:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				members = append(members, f[0])
			}
		}
	}
	return members
}

func isVirtual(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range virtualIface {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return appleVirtualIface.MatchString(lower)
}

func sameLinks(a, b []lanInterface) bool {
	snapshot := func(links []lanInterface) map[string]bool {
		keys := make(map[string]bool)
		for _, link := range links {
			for _, network := range link.networks {
				keys[fmt.Sprintf("%d/%s/%t/%s", link.iface.Index, link.iface.Name, link.ipv6, network)] = true
			}
		}
		return keys
	}
	return maps.Equal(snapshot(a), snapshot(b))
}
