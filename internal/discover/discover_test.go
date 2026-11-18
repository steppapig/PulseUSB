package discover

import (
	"net"
	"testing"
	"time"
)

func TestDiscover(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go Serve(conn, "sim rig", 3240, nil)
	found, err := QueryAddrs(300*time.Millisecond, []*net.UDPAddr{conn.LocalAddr().(*net.UDPAddr)})
	if err != nil || len(found) != 1 || found[0].Name != "sim-rig" || found[0].Addr != "127.0.0.1:3240" {
		t.Fatalf("%v %+v", err, found)
	}

	deny, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer deny.Close()
	go Serve(deny, "hidden", 3240, func(net.Addr) bool { return false })
	if found, _ := QueryAddrs(200*time.Millisecond, []*net.UDPAddr{deny.LocalAddr().(*net.UDPAddr)}); len(found) != 0 {
		t.Fatalf("server answered a client outside its allow list: %+v", found)
	}
}
