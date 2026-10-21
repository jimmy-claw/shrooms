package lanproxy

// physicalLink reports whether an interface is a LAN: a network device the
// kernel has hardware behind (sysfs gives it a "device"), or a bridge, bond or
// VLAN with such a device beneath it. A home server's br0 over its Ethernet
// is a LAN; docker0, whose ports are veths, is not; neither is a tun, a
// WireGuard interface or a veth.
func physicalLink(name string) bool { return physicalUnder("/sys/class/net", name, 0) }
