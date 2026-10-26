// Package plan holds the pure logic: inventory sync planning, the coverage-gap diff, status values and the
// enrichment note. It does no I/O.
package plan

// Device is what Forward holds for one network device.
type Device struct {
	Name       string
	Site       string
	Vendor     string
	Model      string
	OS         string
	OSVersion  string
	DeviceType string
	IPs        []string
	Tags       []string
	Collected  string
}

// Link is one direct connection from a device to a peer.
type Link struct {
	Iface     string
	Peer      string
	PeerIface string
}
