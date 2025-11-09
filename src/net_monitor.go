package downloader

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

type NetTop struct {
	Process  string `json:"proc_name"`
	PID      int    `json:"pid"`
	BytesIn  int64  `json:"bytes_in"`
	BytesOut int64  `json:"bytes_out"`
}

type NetStats struct {
	mu             sync.Mutex
	prev           int64
	first          bool
	prevTotalBytes int64
	totalBytes     int64
	counter        int64
}

var netStats = NetStats{}

// Assigns netStat func according to os
func assignNetStatFunc() func(d *Downloader) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("nettop"); err != nil {
			fmt.Println("netop is not installed")
			return nil
		}
		return macOs
	case "linux":
		fmt.Println("Linux detected", runtime.GOOS)
		return linux
	default:
		return nil
	}
}

// Calls assigned net stats function every 20ticks (1 second).
func (d *Downloader) startNetReporting() {
	if d.networkStats == nil {
		return
	}
	go func() {
		ch, unsubscribe := d.ticker.Subscribe()
		defer unsubscribe()
		var count int
		for range ch {
			count++
			if count == 20 {
				count = 0
				d.networkStats(d)
			}
		}
	}()
}

// =======================================
// MacOS
// =======================================

// Parses netStats from macOscNettop and compiles along with chunk read and total read
func macOs(d *Downloader) {
	go func() {
		netStats.counter++

		net := netTopMacOs()
		netStats.mu.Lock()
		defer netStats.mu.Unlock()

		if netStats.first {
			netStats.prev = 0
			netStats.first = false
		}

		if net.BytesIn-netStats.prev == 0 {
			return
		}

		currentTotalBytes := d.totalReadGlobal.Load()
		average := d.GetAverageRead() // read speed

		netStats.totalBytes += net.BytesIn - netStats.prev
		averageNet := netStats.totalBytes / netStats.counter

		d.log("info", "Network Activity:", map[string]any{
			"Network Activity": fmt.Sprintf(
				"%s (PID %d): bytes in: %s, bytes out: %s, Average: %s",
				net.Process, net.PID, d.bytesToHuman(net.BytesIn-netStats.prev), d.bytesToHuman(net.BytesOut), d.bytesToHuman(averageNet),
			),
			"Read:":              d.bytesToHuman(currentTotalBytes - netStats.prevTotalBytes),
			"Average Read Speed": d.bytesToHuman(average),
			"Rate Limit":         d.bytesToHuman(int64(d.rateLimiter.Limit())),
		})

		netStats.prev = net.BytesIn
		netStats.prevTotalBytes = currentTotalBytes

	}()
}

// Returns stats for proc 'wget' and 'test'.
func netTopMacOs() *NetTop {
	process := &NetTop{}
	cmd := exec.Command("nettop", "-P", "-L", "1", "-J", "bytes_in,bytes_out")
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("Error running nettop:", err)
		return process
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	var stats []NetTop
	firstLine := true

	// Read stats CSV
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// skip header line (starts with a comma)
		if firstLine {
			firstLine = false
			continue
		}

		// example line: "Dropbox.564,346876,437679,"
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}

		// separate process name and PID
		procParts := strings.SplitN(parts[0], ".", 2)
		if len(procParts) != 2 {
			continue
		}
		name := procParts[0]
		var pid int
		fmt.Sscanf(procParts[1], "%d", &pid)

		var bytesIn, bytesOut int64
		fmt.Sscanf(parts[1], "%d", &bytesIn)
		fmt.Sscanf(parts[2], "%d", &bytesOut)

		stats = append(stats, NetTop{
			Process:  name,
			PID:      pid,
			BytesIn:  bytesIn,
			BytesOut: bytesOut,
		})
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Scan error:", err)
		return process
	}

	for _, s := range stats {
		if strings.Contains(s.Process, "test") || strings.Contains(s.Process, "wget") {
			process = &s
			// fmt.Println("proc Name", s.Process)
			return process
		}
	}
	return process
}

// TODO: UNTESTED functions

// ================================================
// Linux
// ================================================

func linux(d *Downloader) {
	go func() {
		netStats.counter++
		net := netTopLinux()
		if net == nil {
			return
		}

		netStats.mu.Lock()
		defer netStats.mu.Unlock()

		if netStats.first {
			netStats.prev = 0
			netStats.first = false
		}

		if net.BytesIn-netStats.prev == 0 {
			return
		}

		currentTotalBytes := d.totalReadGlobal.Load()
		average := d.GetAverageRead()

		netStats.totalBytes += net.BytesIn - netStats.prev
		averageNet := netStats.totalBytes / netStats.counter

		d.log("network", "Network Activity:", map[string]any{
			"Network Activity": fmt.Sprintf(
				"%s (PID %d): bytes in: %s, bytes out: %s, Average: %s",
				net.Process, net.PID, d.bytesToHuman(net.BytesIn-netStats.prev), d.bytesToHuman(net.BytesOut), d.bytesToHuman(averageNet),
			),
			"Read:":              d.bytesToHuman(currentTotalBytes - netStats.prevTotalBytes),
			"Average Read Speed": d.bytesToHuman(average),
			"Rate Limit":         d.bytesToHuman(int64(d.rateLimiter.Limit())),
		})

		// fmt.Println("network", "Network Activity:",
		// 	"Network Activity", fmt.Sprintf(
		// 		"%s (PID %d): bytes in: %s, bytes out: %s, Average: %s",
		// 		net.Process, net.PID, d.BytesToHuman(net.BytesIn-netStats.prev), d.BytesToHuman(net.BytesOut), d.BytesToHuman(averageNet),
		// 	),
		// 	"Read:", d.BytesToHuman(currentTotalBytes-netStats.prevTotalBytes),
		// 	"Average Read Speed", d.BytesToHuman(average),
		// 	"Rate Limit", d.BytesToHuman(int64(d.rateLimiter.Limit())),
		// )

		netStats.prev = net.BytesIn
		netStats.prevTotalBytes = currentTotalBytes
	}()
}

func netTopLinux() *NetTop {
	process := &NetTop{}

	// nethogs must be run as root or with CAP_NET_ADMIN
	cmd := exec.Command("nethogs", "-t", "-c", "1") // -t: trace mode, -c 1: one iteration
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("Error running nethogs:", err)
		return process
	}

	// fmt.Println("nethogs output", string(output))

	// Example nethogs -t output line:
	// eth0       PID    Program name  Sent(kB/s)  Received(kB/s)
	// eth0       1234   wget          12.345      67.890
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "Refreshing") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		iface := fields[0]
		pidStr := fields[1]
		proc := fields[2]
		sentStr := fields[3]
		recvStr := fields[4]

		if !strings.Contains(proc, "wget") && !strings.Contains(proc, "test") {
			continue
		}

		var pid int
		fmt.Sscanf(pidStr, "%d", &pid)

		var sentKB, recvKB float64
		fmt.Sscanf(sentStr, "%f", &sentKB)
		fmt.Sscanf(recvStr, "%f", &recvKB)

		process = &NetTop{
			Process:  fmt.Sprintf("%s (%s)", proc, iface),
			PID:      pid,
			BytesIn:  int64(recvKB * 1024),
			BytesOut: int64(sentKB * 1024),
		}
		break
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading nethogs output:", err)
	}

	return process
}
