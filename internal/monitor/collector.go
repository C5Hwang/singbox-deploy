package monitor

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// InterfaceCounters holds cumulative byte counters for a network interface.
type InterfaceCounters struct {
	Name    string
	RXBytes uint64
	TXBytes uint64
}

// Total returns RX+TX.
func (c InterfaceCounters) Total() uint64 { return c.RXBytes + c.TXBytes }

// sysClassNet is the Linux interface statistics root, overridable in tests.
const sysClassNet = "/sys/class/net"

// ReadCounters reads cumulative counters for iface from /sys.
func ReadCounters(iface string) (InterfaceCounters, error) {
	return readCountersFrom(sysClassNet, iface)
}

func readCountersFrom(root, iface string) (InterfaceCounters, error) {
	rx, err := readUintFile(filepath.Join(root, iface, "statistics", "rx_bytes"))
	if err != nil {
		return InterfaceCounters{}, err
	}
	tx, err := readUintFile(filepath.Join(root, iface, "statistics", "tx_bytes"))
	if err != nil {
		return InterfaceCounters{}, err
	}
	return InterfaceCounters{Name: iface, RXBytes: rx, TXBytes: tx}, nil
}

func readUintFile(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

// DefaultInterface returns the interface backing the default route by parsing
// /proc/net/route (the row whose destination is 00000000).
func DefaultInterface() (string, error) {
	return defaultInterfaceFrom("/proc/net/route")
}

func defaultInterfaceFrom(routePath string) (string, error) {
	f, err := os.Open(routePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	first := true
	for scanner.Scan() {
		if first { // header line
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == "00000000" {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", os.ErrNotExist
}
