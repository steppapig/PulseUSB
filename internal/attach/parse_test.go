package attach

import "testing"

func TestParsePorts(t *testing.T) {
	out := "Imported USB devices\n====================\nPort 01: device in use at High Speed(480Mbps)\r\n" +
		"         unknown vendor : unknown product (1234:5678)\r\n           -> usbip://rig:3240/1-1.4\r\n" +
		"           -> remote bus/dev: 001/005\r\nPort 03: device in use at Full Speed(12Mbps)\r\n" +
		"           -> usbip://rig:3240/1-1.2\r\n"
	have := parsePorts(out)
	if len(have) != 2 || have[Key("rig", "1-1.4")] != 1 || have[Key("rig:3240", "1-1.2")] != 3 {
		t.Fatalf("%v", have)
	}
}
