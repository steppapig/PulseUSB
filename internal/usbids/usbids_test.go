package usbids

import "testing"

func TestLookup(t *testing.T) {
	v, p := Lookup(0x046d, 0xc52b)
	if v != "Logitech, Inc." || p != "Unifying Receiver" {
		t.Fatalf("%q %q", v, p)
	}
	if v, p := Lookup(0x046d, 0xfffe); v == "" || p != "" {
		t.Fatalf("unknown product: %q %q", v, p)
	}
	if v, _ := Lookup(0xfff0, 1); v != "" {
		t.Fatalf("unknown vendor: %q", v)
	}
	if len(vendors) < 3000 || len(products) < 15000 {
		t.Fatalf("database looks truncated: %d vendors, %d products", len(vendors), len(products))
	}
}
