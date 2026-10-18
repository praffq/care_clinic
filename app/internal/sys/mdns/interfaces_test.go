package mdns

import (
	"errors"
	"maps"
	"net"
	"slices"
	"testing"
)

const vzBridge = `bridge100: flags=8a63<UP,BROADCAST,SMART,RUNNING,ALLMULTI,SIMPLEX,MULTICAST> mtu 1500
	inet 192.168.64.1 netmask 0xffffff00 broadcast 192.168.64.255
	Configuration:
		ipfilter disabled flags 0x0
	member: vmenet0 flags=20803<LEARNING,DISCOVER,PRIVATE,VIRTIO>
	        ifmaxaddr 0 port 25 priority 0 path cost 0
	Address cache:
		8a:19:1c:98:50:45 Vlan1 vmenet0 1197 flags=0<>
	status: active
`

const thunderboltBridge = `bridge0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500
	member: en1 flags=3<LEARNING,DISCOVER>
	        ifmaxaddr 0 port 10 priority 0 path cost 0
	member: en2 flags=3<LEARNING,DISCOVER>
	        ifmaxaddr 0 port 11 priority 0 path cost 0
	status: inactive
`

func TestParseBridgeMembers(t *testing.T) {
	if got := parseBridgeMembers(vzBridge); !slices.Equal(got, []string{"vmenet0"}) {
		t.Fatalf("VZ bridge members = %v", got)
	}
	if got := parseBridgeMembers(thunderboltBridge); !slices.Equal(got, []string{"en1", "en2"}) {
		t.Fatalf("Thunderbolt bridge members = %v", got)
	}
	if got := parseBridgeMembers("en0: flags=8863<UP>\n\tinet 192.168.1.20\n"); got != nil {
		t.Fatalf("non-bridge members = %v", got)
	}
}

func TestLANInterfacesSkipsVirtualLinks(t *testing.T) {
	const lan = net.FlagUp | net.FlagBroadcast | net.FlagMulticast | net.FlagRunning
	ifaces := map[string]string{
		"lo0":       "127.0.0.1/8",
		"en0":       "192.168.1.20/24", // Wi-Fi
		"en7":       "10.1.0.5/16",     // USB Ethernet
		"bridge0":   "10.9.0.1/24",     // Thunderbolt Bridge with a static address
		"bridge100": "192.168.64.1/24", // engine VM NAT: vmenet0 only
		"bridge101": "192.168.65.1/24", // NAT bridge after its VM stopped
		"bridge102": "192.168.2.1/24",  // wired Internet Sharing: en5 + vmenet1
		"bridge103": "192.168.66.1/24", // members unreadable
		"utun3":     "100.64.0.2/32",   // VPN
		"awdl0":     "192.168.70.1/24", // Apple peer-to-peer links never carry the LAN
		"llw0":      "192.168.71.1/24",
		"anpi0":     "192.168.72.1/24", //
		"ap1":       "192.168.73.1/24",
		"nan0":      "192.168.74.1/24",  //
		"en9":       "169.254.10.10/16", // link-local only
	}
	members := map[string][]string{
		"bridge0":   parseBridgeMembers(thunderboltBridge),
		"bridge100": parseBridgeMembers(vzBridge),
		"bridge101": nil,
		"bridge102": {"en5", "vmenet1"},
	}
	var list []net.Interface
	for i, name := range slices.Sorted(maps.Keys(ifaces)) {
		flags := lan
		if name == "lo0" {
			flags = net.FlagUp | net.FlagLoopback | net.FlagMulticast
		}
		list = append(list, net.Interface{Index: i + 1, Name: name, Flags: flags})
	}
	src := ifaceSource{
		interfaces: func() ([]net.Interface, error) { return list, nil },
		addrs: func(ifi net.Interface) ([]net.Addr, error) {
			ip, network, err := net.ParseCIDR(ifaces[ifi.Name])
			if err != nil {
				return nil, err
			}
			network.IP = ip
			return []net.Addr{network}, nil
		},
		bridgeMembers: func(name string) ([]string, error) {
			m, ok := members[name]
			if !ok {
				return nil, errors.New("ifconfig failed")
			}
			return m, nil
		},
	}

	links, err := src.lanInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range links {
		got = append(got, l.iface.Name)
	}
	if want := []string{"bridge0", "bridge102", "en0", "en7"}; !slices.Equal(got, want) {
		t.Fatalf("advertised on %v, want %v", got, want)
	}

	// Without a bridge lookup (Linux, Windows) bridgeN is an ordinary name.
	src.bridgeMembers = nil
	links, err = src.lanInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 7 {
		t.Fatalf("without bridge filtering got %d links, want 7", len(links))
	}
}
