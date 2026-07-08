package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	mdnsGroup = "224.0.0.251"
	mdnsPort  = 5353
)

type liveOptions struct {
	Interface string
	Timeout   time.Duration
	RemoteSKI string
}

type liveDiscovery struct {
	Records      int
	SHIP         int
	ServiceRef   string
	InterfaceRef string
}

func runLiveVR940fSmoke(ctx context.Context, opts liveOptions) caseResult {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	discovery, err := probeSHIP(ctx, opts.Interface, opts.Timeout)
	if err != nil {
		return caseResult{
			ID:       caseLive,
			Status:   resultBlocked,
			Evidence: []string{"live-vr940f-mdns-probe-attempted"},
			Error:    "mdns_probe_unavailable",
		}
	}
	if discovery.SHIP == 0 {
		return caseResult{
			ID:     caseLive,
			Status: resultBlocked,
			Evidence: []string{
				"live-vr940f-mdns-probe-attempted",
				"live-vr940f-no-ship-service-visible",
			},
			Details: map[string]string{
				"records_ref":   countRef("records", discovery.Records),
				"interface_ref": discovery.InterfaceRef,
			},
			Error: "no_visible_ship_service",
		}
	}
	if opts.RemoteSKI == "" {
		return caseResult{
			ID:     caseLive,
			Status: resultBlocked,
			Evidence: []string{
				"live-vr940f-ship-service-visible",
				"live-vr940f-pairing-not-attempted-without-remote-ski",
			},
			Details: map[string]string{
				"service_ref":   discovery.ServiceRef,
				"interface_ref": discovery.InterfaceRef,
			},
			Error: "remote_ski_required_for_pairing_smoke",
		}
	}
	return caseResult{
		ID:     caseLive,
		Status: resultBlocked,
		Evidence: []string{
			"live-vr940f-ship-service-visible",
			"live-vr940f-pairing-runner-not-yet-promoted-to-pass",
		},
		Details: map[string]string{
			"service_ref":    discovery.ServiceRef,
			"remote_ski_ref": digestRef(opts.RemoteSKI),
			"interface_ref":  discovery.InterfaceRef,
		},
		Error: "pairing_session_feature_graph_reconnect_not_collected",
	}
}

func probeSHIP(ctx context.Context, iface string, timeout time.Duration) (liveDiscovery, error) {
	if iface == "" {
		iface = defaultLANInterface()
	}
	localIP, err := interfaceIPv4(iface)
	if err != nil {
		return liveDiscovery{}, err
	}
	interfaceRef := refLabel("iface", iface)
	conn, err := listenMDNSPacket(ctx)
	if err != nil {
		return liveDiscovery{}, err
	}
	defer conn.Close()

	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return liveDiscovery{}, errors.New("not a udp conn")
	}
	if err := udp.SetReadBuffer(64 * 1024); err != nil {
		return liveDiscovery{}, err
	}
	if err := joinMDNSGroup(udp, localIP); err != nil {
		return liveDiscovery{}, err
	}

	deadline := time.Now().Add(timeout)
	query := mdnsPTRQuery("_ship._tcp.local.")
	out := liveDiscovery{InterfaceRef: interfaceRef}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		_, _ = udp.WriteTo(query, &net.UDPAddr{IP: net.ParseIP(mdnsGroup), Port: mdnsPort})
		_ = udp.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		for {
			buf := make([]byte, 9000)
			n, _, err := udp.ReadFrom(buf)
			if err != nil {
				break
			}
			records := parseMDNSRecords(buf[:n])
			out.Records += len(records)
			for _, record := range records {
				if record.Name == "_ship._tcp.local." || stringsHasSHIP(record.Name) || stringsHasSHIP(record.Value) {
					out.SHIP++
					if out.ServiceRef == "" {
						out.ServiceRef = refLabel("ship-service", record.Name+"|"+record.Value)
					}
				}
			}
		}
	}
	return out, nil
}

func listenMDNSPacket(ctx context.Context) (net.PacketConn, error) {
	listener := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var controlErr error
			if err := c.Control(func(fd uintptr) {
				controlErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				if controlErr != nil {
					return
				}
				controlErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return controlErr
		},
	}
	return listener.ListenPacket(ctx, "udp4", ":5353")
}

func joinMDNSGroup(conn *net.UDPConn, localIP net.IP) error {
	if localIP == nil {
		return errors.New("invalid local ip")
	}
	packetConn := ipv4.NewPacketConn(conn)
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip.Equal(localIP) {
				if err := packetConn.SetMulticastInterface(&iface); err != nil {
					return err
				}
				if err := packetConn.SetMulticastTTL(255); err != nil {
					return err
				}
				return packetConn.JoinGroup(&iface, &net.UDPAddr{IP: net.ParseIP(mdnsGroup)})
			}
		}
	}
	return errors.New("interface for local ip not found")
}

type mdnsRecord struct {
	Name  string
	Type  uint16
	Value string
}

func mdnsPTRQuery(name string) []byte {
	query := make([]byte, 12, 64)
	query[5] = 1
	query = append(query, encodeDNSName(name)...)
	query = binary.BigEndian.AppendUint16(query, 12)
	query = binary.BigEndian.AppendUint16(query, 1)
	return query
}

func encodeDNSName(name string) []byte {
	var out []byte
	for _, part := range splitDNSName(name) {
		out = append(out, byte(len(part)))
		out = append(out, []byte(part)...)
	}
	return append(out, 0)
}

func splitDNSName(name string) []string {
	var parts []string
	start := 0
	for i, r := range name {
		if r == '.' {
			if i > start {
				parts = append(parts, name[start:i])
			}
			start = i + 1
		}
	}
	if start < len(name) {
		parts = append(parts, name[start:])
	}
	return parts
}

func parseMDNSRecords(packet []byte) []mdnsRecord {
	if len(packet) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(packet[4:6]))
	an := int(binary.BigEndian.Uint16(packet[6:8]))
	ns := int(binary.BigEndian.Uint16(packet[8:10]))
	ar := int(binary.BigEndian.Uint16(packet[10:12]))
	offset := 12
	for i := 0; i < qd; i++ {
		_, next, ok := readDNSName(packet, offset)
		if !ok || next+4 > len(packet) {
			return nil
		}
		offset = next + 4
	}
	records := make([]mdnsRecord, 0, an+ns+ar)
	for i := 0; i < an+ns+ar; i++ {
		name, next, ok := readDNSName(packet, offset)
		if !ok || next+10 > len(packet) {
			return records
		}
		typ := binary.BigEndian.Uint16(packet[next : next+2])
		rdlen := int(binary.BigEndian.Uint16(packet[next+8 : next+10]))
		rstart := next + 10
		rend := rstart + rdlen
		if rend > len(packet) {
			return records
		}
		value := ""
		switch typ {
		case 12:
			if v, _, ok := readDNSName(packet, rstart); ok {
				value = v
			}
		case 33:
			if rdlen >= 6 {
				if v, _, ok := readDNSName(packet, rstart+6); ok {
					value = v
				}
			}
		}
		records = append(records, mdnsRecord{Name: name, Type: typ, Value: value})
		offset = rend
	}
	return records
}

func readDNSName(packet []byte, offset int) (string, int, bool) {
	labels := make([]string, 0, 8)
	next := offset
	jumped := false
	for depth := 0; depth < 32; depth++ {
		if offset >= len(packet) {
			return "", 0, false
		}
		size := int(packet[offset])
		if size&0xc0 == 0xc0 {
			if offset+1 >= len(packet) {
				return "", 0, false
			}
			ptr := ((size & 0x3f) << 8) | int(packet[offset+1])
			if !jumped {
				next = offset + 2
			}
			offset = ptr
			jumped = true
			continue
		}
		offset++
		if size == 0 {
			if !jumped {
				next = offset
			}
			return joinDNSLabels(labels), next, true
		}
		if offset+size > len(packet) {
			return "", 0, false
		}
		labels = append(labels, string(packet[offset:offset+size]))
		offset += size
	}
	return "", 0, false
}

func joinDNSLabels(labels []string) string {
	if len(labels) == 0 {
		return "."
	}
	out := ""
	for i, label := range labels {
		if i > 0 {
			out += "."
		}
		out += label
	}
	return out + "."
}

func stringsHasSHIP(value string) bool {
	return len(value) >= len("._ship._tcp.local.") && contains(value, "._ship._tcp.local.")
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func interfaceIPv4(name string) (net.IP, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
			return ip4, nil
		}
	}
	return nil, errors.New("interface has no non-loopback IPv4")
}

func defaultLANInterface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if _, err := interfaceIPv4(iface.Name); err == nil {
			return iface.Name
		}
	}
	return ""
}

func countRef(prefix string, count int) string {
	return fmt.Sprintf("%s-%d", prefix, count)
}
