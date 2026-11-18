// Package usbids names USB devices from their vendor and product IDs using
// the public usb.ids database (http://www.linux-usb.org/usb.ids, maintained
// by Stephen J. Gowdy; licensed GPL-2.0-or-later or BSD-3-Clause; used here under BSD-3-Clause).
package usbids

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strconv"
	"strings"
	"sync"
)

//go:embed usb.ids.gz
var packed []byte

var (
	once     sync.Once
	vendors  map[uint16]string
	products map[uint32]string
)

func load() {
	vendors, products = map[uint16]string{}, map[uint32]string{}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return
	}
	var vendor uint16
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "C ") {
			break // device class tables follow the vendor list
		}
		if line == "" || line[0] == '#' || strings.HasPrefix(line, "\t\t") {
			continue
		}
		entry := strings.TrimPrefix(line, "\t")
		id, name, ok := strings.Cut(entry, "  ")
		n, err := strconv.ParseUint(id, 16, 16)
		if !ok || err != nil {
			continue
		}
		if entry == line {
			vendor = uint16(n)
			vendors[vendor] = strings.TrimSpace(name)
		} else {
			products[uint32(vendor)<<16|uint32(n)] = strings.TrimSpace(name)
		}
	}
}

// Lookup returns the vendor and product names, empty where unknown.
func Lookup(vendor, product uint16) (vendorName, productName string) {
	once.Do(load)
	return vendors[vendor], products[uint32(vendor)<<16|uint32(product)]
}
