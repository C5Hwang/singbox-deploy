//go:build linux

package monitor

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// ResourceReading holds computed resource metrics from a single collection.
type ResourceReading struct {
	CPUPct         float64
	MemPct         float64
	MemUsedBytes   uint64
	MemTotalBytes  uint64
	DiskUsedPct    float64
	DiskUsedBytes  uint64
	DiskTotalBytes uint64
	DIOReadDelta   uint64
	DIOWriteDelta  uint64
	Valid          bool
}

type cpuSample struct {
	idle  uint64
	total uint64
}

// ResourceCollector reads system resource metrics and tracks previous state
// for delta-based metrics (CPU, disk IO).
type ResourceCollector struct {
	prevCPU      cpuSample
	havePrevCPU  bool
	prevDIORead  uint64
	prevDIOWrite uint64
	havePrevDIO  bool
	diskPath     string
}

// NewResourceCollector returns a collector that reads disk capacity from path.
func NewResourceCollector(diskPath string) *ResourceCollector {
	if diskPath == "" {
		diskPath = "/"
	}
	return &ResourceCollector{diskPath: diskPath}
}

// Collect reads current resource metrics. The first call returns Valid=false
// because CPU and disk IO require two readings to compute a delta.
func (rc *ResourceCollector) Collect() (ResourceReading, error) {
	cpu, err := readProcStat()
	if err != nil {
		return ResourceReading{}, err
	}
	var cpuPct float64
	if rc.havePrevCPU {
		deltaIdle := cpu.idle - rc.prevCPU.idle
		deltaTotal := cpu.total - rc.prevCPU.total
		if deltaTotal > 0 {
			cpuPct = (1 - float64(deltaIdle)/float64(deltaTotal)) * 100
			if cpuPct < 0 {
				cpuPct = 0
			}
		}
	}
	rc.prevCPU = cpu

	memPct, memUsed, memTotal, err := readMemoryInfo()
	if err != nil {
		return ResourceReading{}, err
	}

	diskPct, diskUsed, diskTotal, err := readDiskInfo(rc.diskPath)
	if err != nil {
		return ResourceReading{}, err
	}

	readBytes, writeBytes, err := readDiskIOBytes()
	if err != nil {
		return ResourceReading{}, err
	}

	var dioReadDelta, dioWriteDelta uint64
	if rc.havePrevDIO {
		dioReadDelta = Delta(rc.prevDIORead, readBytes)
		dioWriteDelta = Delta(rc.prevDIOWrite, writeBytes)
	}
	rc.prevDIORead = readBytes
	rc.prevDIOWrite = writeBytes

	valid := rc.havePrevCPU && rc.havePrevDIO
	rc.havePrevCPU = true
	rc.havePrevDIO = true

	return ResourceReading{
		CPUPct:         cpuPct,
		MemPct:         memPct,
		MemUsedBytes:   memUsed,
		MemTotalBytes:  memTotal,
		DiskUsedPct:    diskPct,
		DiskUsedBytes:  diskUsed,
		DiskTotalBytes: diskTotal,
		DIOReadDelta:   dioReadDelta,
		DIOWriteDelta:  dioWriteDelta,
		Valid:          valid,
	}, nil
}

func readProcStat() (cpuSample, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuSample{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		var total, idle uint64
		for i := 1; i < len(fields); i++ {
			v, _ := strconv.ParseUint(fields[i], 10, 64)
			total += v
			if i == 4 || i == 5 { // idle + iowait
				idle += v
			}
		}
		return cpuSample{idle: idle, total: total}, nil
	}
	return cpuSample{}, scanner.Err()
}

func readMemoryInfo() (pct float64, usedBytes, totalBytes uint64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	var memTotal, memAvailable uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			memTotal = parseMemInfoKB(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			memAvailable = parseMemInfoKB(line)
		}
		if memTotal > 0 && memAvailable > 0 {
			break
		}
	}
	if memTotal == 0 {
		return 0, 0, 0, nil
	}
	totalBytes = memTotal * 1024
	usedBytes = (memTotal - memAvailable) * 1024
	pct = (1 - float64(memAvailable)/float64(memTotal)) * 100
	return pct, usedBytes, totalBytes, nil
}

func parseMemInfoKB(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(fields[1], 10, 64)
	return v
}

func readDiskInfo(path string) (pct float64, usedBytes, totalBytes uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0, err
	}
	totalBytes = stat.Blocks * uint64(stat.Bsize)
	avail := stat.Bavail * uint64(stat.Bsize)
	if totalBytes == 0 {
		return 0, 0, 0, nil
	}
	usedBytes = totalBytes - avail
	pct = (1 - float64(avail)/float64(totalBytes)) * 100
	return pct, usedBytes, totalBytes, nil
}

var wholeDiskRE = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|nvme\d+n\d+)$`)

func readDiskIOBytes() (readBytes, writeBytes uint64, err error) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}
		devName := fields[2]
		if !wholeDiskRE.MatchString(devName) {
			continue
		}
		rd, _ := strconv.ParseUint(fields[5], 10, 64)
		wr, _ := strconv.ParseUint(fields[9], 10, 64)
		readBytes += rd * 512
		writeBytes += wr * 512
	}
	return readBytes, writeBytes, scanner.Err()
}
