package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	shipmdns "github.com/enbility/ship-go/mdns"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

const (
	mdnsGroup = "224.0.0.251"
	mdnsPort  = 5353
)

type liveOptions struct {
	Interface        string
	Timeout          time.Duration
	Port             int
	RemoteSKI        string
	PairingWindow    bool
	OperatorProofRef string
	RepoBranch       string
	RepoCommit       string
	ChallengeWriter  io.Writer
}

type liveDiscovery struct {
	Records         int
	SHIP            int
	ExpectedActive  int
	ExpectedGoodbye int
}

type lanSHIPPublisher struct {
	provider    lanSHIPMDNSProvider
	serviceFQDN string
}

type lanSHIPMDNSProvider interface {
	Announce(serviceName string, port int, txt []string) error
	Shutdown()
}

var lanSHIPInterfaceByName = net.InterfaceByName

var newLANSHIPMDNSProvider = func(ifaces []net.Interface) lanSHIPMDNSProvider {
	return shipmdns.NewZeroconfProvider(ifaces)
}

func startLANSHIPPublisher(ifaceName string, port int, ski, shipID string, pairingWindow bool) (*lanSHIPPublisher, error) {
	ifaceName = strings.TrimSpace(ifaceName)
	ski = normalizeSKI(ski)
	shipID = strings.TrimSpace(shipID)
	if ifaceName == "" || port < 1 || port > 65535 || !validSKI(ski) || shipID == "" {
		return nil, errors.New("LAN SHIP publisher configuration invalid")
	}
	iface, err := lanSHIPInterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("LAN SHIP publisher interface unavailable: %w", err)
	}
	txt := []string{
		"txtvers=1",
		"path=/ship/",
		"id=" + shipID,
		"ski=" + ski,
		"brand=Helianthus",
		"model=RawProbe",
		"type=EnergyManagementSystem",
		fmt.Sprintf("register=%t", pairingWindow),
	}
	provider := newLANSHIPMDNSProvider([]net.Interface{*iface})
	if err := provider.Announce(liveServiceName, port, txt); err != nil {
		return nil, fmt.Errorf("LAN SHIP publisher start failed: %w", err)
	}
	return &lanSHIPPublisher{provider: provider, serviceFQDN: liveServiceName + "._ship._tcp.local."}, nil
}

func (p *lanSHIPPublisher) shutdown() {
	if p == nil || p.provider == nil {
		return
	}
	p.provider.Shutdown()
	p.provider = nil
}

func runLiveVR940fSmoke(ctx context.Context, opts liveOptions) caseResult {
	result := runLiveVR940fProof(ctx, opts)
	for _, item := range result.Cases {
		if item.ID == caseLive {
			return item
		}
	}
	return caseResult{ID: caseLive, Status: resultFail, Evidence: []string{"g17-runner-returned-no-result"}, Error: "g17_result_missing"}
}

func probeSHIP(ctx context.Context, iface string, timeout time.Duration) (liveDiscovery, error) {
	return probeSHIPService(ctx, iface, timeout, "")
}

func probeSHIPService(ctx context.Context, iface string, timeout time.Duration, expectedService string) (out liveDiscovery, resultErr error) {
	iface = strings.TrimSpace(iface)
	expectedService = strings.TrimSpace(expectedService)
	if iface == "" {
		iface = defaultLANInterface()
	}
	if timeout <= 0 {
		return liveDiscovery{}, errors.New("mDNS probe timeout must be positive")
	}
	udp, err := openMDNSObserver(ctx, iface)
	if err != nil {
		return liveDiscovery{}, err
	}
	defer func() {
		if closeErr := udp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("mDNS observer close failed: %w", closeErr))
		}
	}()

	deadline := time.Now().Add(timeout)
	query := mdnsPTRQuery("_ship._tcp.local.")
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		if err := writeMDNSQuery(udp, query); err != nil {
			return out, err
		}
		readDeadline := time.Now().Add(400 * time.Millisecond)
		if readDeadline.After(deadline) {
			readDeadline = deadline
		}
		if err := udp.SetReadDeadline(readDeadline); err != nil {
			return out, fmt.Errorf("mDNS read deadline failed: %w", err)
		}
		for {
			buf := make([]byte, 9000)
			n, _, err := udp.ReadFrom(buf)
			if err != nil {
				if isTimeoutError(err) {
					break
				}
				return out, fmt.Errorf("mDNS read failed: %w", err)
			}
			accountMDNSRecords(&out, parseMDNSRecords(buf[:n]), expectedService)
		}
	}
	return out, nil
}

func observeSHIPWithdrawal(ctx context.Context, iface string, timeout time.Duration, expectedService string, withdrawAdvertisement func() error) (out liveDiscovery, resultErr error) {
	iface = strings.TrimSpace(iface)
	expectedService = strings.TrimSpace(expectedService)
	if iface == "" || timeout <= 0 || expectedService == "" || withdrawAdvertisement == nil {
		return liveDiscovery{}, errors.New("mDNS withdrawal observer configuration invalid")
	}
	udp, err := openMDNSObserver(ctx, iface)
	if err != nil {
		return liveDiscovery{}, err
	}
	defer func() {
		if closeErr := udp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("mDNS withdrawal observer close failed: %w", closeErr))
		}
	}()
	if err := writeMDNSQuery(udp, mdnsPTRQuery("_ship._tcp.local.")); err != nil {
		return liveDiscovery{}, err
	}

	if err := withdrawAdvertisement(); err != nil {
		return liveDiscovery{}, fmt.Errorf("mDNS withdrawal action failed: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		readDeadline := time.Now().Add(400 * time.Millisecond)
		if readDeadline.After(deadline) {
			readDeadline = deadline
		}
		if err := udp.SetReadDeadline(readDeadline); err != nil {
			return out, fmt.Errorf("mDNS withdrawal deadline failed: %w", err)
		}
		buf := make([]byte, 9000)
		n, _, readErr := udp.ReadFrom(buf)
		if readErr != nil {
			if isTimeoutError(readErr) {
				continue
			}
			return out, fmt.Errorf("mDNS withdrawal read failed: %w", readErr)
		}
		accountMDNSRecords(&out, parseMDNSRecords(buf[:n]), expectedService)
	}
	return out, nil
}

func openMDNSObserver(ctx context.Context, iface string) (*net.UDPConn, error) {
	localIP, err := interfaceIPv4(iface)
	if err != nil {
		return nil, err
	}
	conn, err := listenMDNSPacket(ctx)
	if err != nil {
		return nil, err
	}
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("mDNS listener is not UDP")
	}
	if err := udp.SetReadBuffer(64 * 1024); err != nil {
		_ = udp.Close()
		return nil, fmt.Errorf("mDNS read buffer setup failed: %w", err)
	}
	if err := joinMDNSGroup(udp, iface, localIP); err != nil {
		_ = udp.Close()
		return nil, err
	}
	return udp, nil
}

func writeMDNSQuery(udp *net.UDPConn, query []byte) error {
	group := net.ParseIP(mdnsGroup)
	if group == nil {
		return errors.New("mDNS group address invalid")
	}
	written, err := udp.WriteTo(query, &net.UDPAddr{IP: group, Port: mdnsPort})
	if err != nil {
		return fmt.Errorf("mDNS query write failed: %w", err)
	}
	if written != len(query) {
		return io.ErrShortWrite
	}
	return nil
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func accountMDNSRecords(out *liveDiscovery, records []mdnsRecord, expectedService string) {
	out.Records += len(records)
	for _, record := range records {
		if strings.EqualFold(record.Name, "_ship._tcp.local.") || stringsHasSHIP(record.Name) || stringsHasSHIP(record.Value) {
			out.SHIP++
		}
		if expectedService == "" || record.Type != 12 || !strings.EqualFold(record.Name, "_ship._tcp.local.") || !strings.EqualFold(record.Value, expectedService) {
			continue
		}
		if record.TTL == 0 {
			out.ExpectedGoodbye++
		} else {
			out.ExpectedActive++
		}
	}
}

func listenMDNSPacket(ctx context.Context) (net.PacketConn, error) {
	listener := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var controlErr error
			if err := c.Control(func(fd uintptr) {
				controlErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
				if controlErr != nil {
					return
				}
				controlErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return controlErr
		},
	}
	return listener.ListenPacket(ctx, "udp4", ":5353")
}

func joinMDNSGroup(conn *net.UDPConn, ifaceName string, localIP net.IP) error {
	if localIP == nil {
		return errors.New("invalid local ip")
	}
	packetConn := ipv4.NewPacketConn(conn)
	iface, err := net.InterfaceByName(strings.TrimSpace(ifaceName))
	if err != nil {
		return err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return err
	}
	found := false
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip.Equal(localIP) {
			found = true
			break
		}
	}
	if !found {
		return errors.New("interface for local ip not found")
	}
	if err := packetConn.SetMulticastInterface(iface); err != nil {
		return err
	}
	if err := packetConn.SetMulticastTTL(255); err != nil {
		return err
	}
	group := net.ParseIP(mdnsGroup)
	if group == nil {
		return errors.New("mDNS group address invalid")
	}
	return packetConn.JoinGroup(iface, &net.UDPAddr{IP: group})
}

type mdnsRecord struct {
	Name  string
	Type  uint16
	TTL   uint32
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
		ttl := binary.BigEndian.Uint32(packet[next+4 : next+8])
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
		records = append(records, mdnsRecord{Name: name, Type: typ, TTL: ttl, Value: value})
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
