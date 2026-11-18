// Package discover finds pulseusb servers on the local network. A client
// broadcasts a small UDP question and every server answers with its name
// and TCP port. Because the client asks first, the replies pass a default
// Windows firewall without any rule being added.
package discover

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"pulseusb/internal/proto"
)

const (
	query = "USBETH-DISCOVER 1"
	reply = "USBETH-SERVER 1"
)

// Server is one answer to a discovery question.
type Server struct {
	Name string `json:"name"`
	Addr string `json:"addr"` // host:port to connect to
}

// Serve answers discovery questions arriving on conn until it is closed.
// allowed may be nil; otherwise it filters who gets an answer.
func Serve(conn net.PacketConn, name string, tcpPort int, allowed func(net.Addr) bool) {
	name = strings.Join(strings.Fields(name), "-")
	buf := make([]byte, 256)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if strings.TrimSpace(string(buf[:n])) != query || (allowed != nil && !allowed(from)) {
			continue
		}
		conn.WriteTo([]byte(fmt.Sprintf("%s %d %s\n", reply, tcpPort, name)), from)
	}
}

// broadcastAddrs returns the limited broadcast address plus the directed
// broadcast address of every active IPv4 interface.
func broadcastAddrs() []net.IP {
	out := []net.IP{net.IPv4bcast}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			ip := n.IP.To4()
			if !ok || ip == nil || len(n.Mask) != 4 {
				continue
			}
			b := make(net.IP, 4)
			for i := range b {
				b[i] = ip[i] | ^n.Mask[i]
			}
			out = append(out, b)
		}
	}
	return out
}

// Query looks for servers for the given time.
func Query(wait time.Duration) ([]Server, error) {
	port, _ := strconv.Atoi(proto.DefaultPort)
	var targets []*net.UDPAddr
	for _, ip := range broadcastAddrs() {
		targets = append(targets, &net.UDPAddr{IP: ip, Port: port})
	}
	return QueryAddrs(wait, targets)
}

// QueryAddrs sends the question to specific addresses.
func QueryAddrs(wait time.Duration, targets []*net.UDPAddr) ([]Server, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	sent := false
	for _, t := range targets {
		if _, err := conn.WriteToUDP([]byte(query+"\n"), t); err == nil {
			sent = true
		}
	}
	if !sent {
		return nil, fmt.Errorf("could not send a discovery broadcast")
	}
	conn.SetReadDeadline(time.Now().Add(wait))
	seen := map[string]bool{}
	var out []Server
	buf := make([]byte, 512)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		f := strings.Fields(string(buf[:n]))
		if len(f) < 4 || f[0]+" "+f[1] != reply {
			continue
		}
		p, err := strconv.Atoi(f[2])
		if err != nil || p < 1 || p > 65535 || len(f[3]) > 64 {
			continue
		}
		s := Server{Name: f[3], Addr: net.JoinHostPort(from.IP.String(), f[2])}
		if !seen[s.Addr] {
			seen[s.Addr] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name+out[i].Addr < out[j].Name+out[j].Addr })
	return out, nil
}
