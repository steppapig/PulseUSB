// Package desc decodes USB descriptors.
package desc

import (
	"fmt"

	"pulseusb/internal/proto"
)

// USB endpoint transfer types (bmAttributes & 3).
const (
	Control   = 0
	Iso       = 1
	Bulk      = 2
	Interrupt = 3
)

// ClassHub is the device class of USB hubs.
const ClassHub = 9

// Config is what we need to know about a device's active configuration.
type Config struct {
	Value         uint8
	Ifaces        []uint8           // interface numbers
	IfaceInfo     []proto.Interface // class triple of each interface (altsetting 0)
	Endpoints     map[uint8]uint8   // endpoint address -> transfer type
	EndpointIface map[uint8]uint8   // endpoint address -> interface number
}

// Parse decodes a blob as found in sysfs "descriptors" (device
// descriptor followed by every configuration descriptor). active selects
// the configuration to describe; if it is not present the first one is used
// for the interface listing but nothing is considered claimable.
func Parse(blob []byte, active uint8) (*proto.Device, *Config, error) {
	if len(blob) < 18 || blob[0] < 18 || blob[1] != 1 {
		return nil, nil, fmt.Errorf("bad device descriptor")
	}
	le16 := func(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
	dev := &proto.Device{
		Class:      blob[4],
		SubClass:   blob[5],
		Protocol:   blob[6],
		VendorID:   le16(blob[8:]),
		ProductID:  le16(blob[10:]),
		BcdDevice:  le16(blob[12:]),
		NumConfigs: blob[17],
	}
	var first, found *Config
	rest := blob[18:]
	for len(rest) >= 9 && rest[1] == 2 {
		total := int(le16(rest[2:]))
		if total < 9 || total > len(rest) {
			total = len(rest)
		}
		c := parseConfig(rest[:total])
		if first == nil {
			first = c
		}
		if c.Value == active && active != 0 {
			found = c
		}
		rest = rest[total:]
	}
	cfg := found
	if cfg == nil {
		cfg = &Config{Endpoints: map[uint8]uint8{}, EndpointIface: map[uint8]uint8{}}
		if first != nil {
			cfg.IfaceInfo = first.IfaceInfo
		}
	}
	dev.ConfigValue = cfg.Value
	dev.Interfaces = cfg.IfaceInfo
	return dev, cfg, nil
}

func parseConfig(b []byte) *Config {
	c := &Config{Value: b[5], Endpoints: map[uint8]uint8{}, EndpointIface: map[uint8]uint8{}}
	seen := map[uint8]bool{}
	cur := uint8(0)
	for len(b) >= 2 {
		l := int(b[0])
		if l < 2 || l > len(b) {
			break
		}
		switch {
		case b[1] == 4 && l >= 9: // interface
			cur = b[2]
			if n := b[2]; !seen[n] {
				seen[n] = true
				c.Ifaces = append(c.Ifaces, n)
				c.IfaceInfo = append(c.IfaceInfo, proto.Interface{Class: b[5], SubClass: b[6], Protocol: b[7]})
			}
		case b[1] == 5 && l >= 7: // endpoint
			c.Endpoints[b[2]] = b[3] & 3
			c.EndpointIface[b[2]] = cur
		}
		b = b[l:]
	}
	return c
}
