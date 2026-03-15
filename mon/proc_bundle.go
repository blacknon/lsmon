package monitor

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"

	sshproc "github.com/blacknon/go-sshproc"
	"github.com/c9s/goprocinfo/linux"
)

const (
	procBundleMarker     = "__LSMON_SECTION__:"
	networkMetaCacheTTL  = time.Minute
	reconnectWarmupDelay = 1500 * time.Millisecond
)

type procBundle struct {
	Stat        *linux.Stat
	MemInfo     *linux.MemInfo
	Uptime      *linux.Uptime
	LoadAvg     *linux.LoadAvg
	DiskStats   []linux.DiskStat
	NetworkStat []linux.NetworkStat
	CPUCount    int
	TaskCount   *uint64
}

func (n *Node) runWarmupStages() {
	_ = n.MonitoringBundle()

	delay := reconnectWarmupDelay
	if n.monitorInterval > 0 && n.monitorInterval/2 < delay {
		delay = n.monitorInterval / 2
	}
	if delay > 0 {
		time.Sleep(delay)
	}

	if n.CheckClientAlive() {
		_ = n.refreshNetworkMetadata()
	}
}

func (n *Node) monitorJitter() time.Duration {
	if n.monitorInterval <= time.Second {
		return 0
	}

	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(n.ServerName))
	window := n.monitorInterval / 2
	if window <= 0 {
		return 0
	}

	return time.Duration(hasher.Sum32()%uint32(window/time.Millisecond)) * time.Millisecond
}

func (n *Node) MonitoringBundle() error {
	if !n.CheckClientAlive() {
		n.resetMonitoringCaches()
		return nil
	}

	bundle, err := n.readProcBundle(!n.cpuCoreCached, n.shouldRefreshTaskCount())
	if err != nil {
		return err
	}

	n.applyProcBundle(bundle)
	return nil
}

func (n *Node) shouldRefreshTaskCount() bool {
	n.RLock()
	defer n.RUnlock()

	return n.taskCountUpdatedAt.IsZero() || time.Since(n.taskCountUpdatedAt) >= taskCountTTL
}

func (n *Node) resetMonitoringCaches() {
	n.Lock()
	defer n.Unlock()

	n.cpuUsage = nil
	n.DiskIOs = map[string][]*DiskIO{}
	n.NetworkIOs = map[string][]*NetworkIO{}
	n.baseSnapshot = BaseSnapshot{}
	n.taskCountCache = 0
	n.taskCountUpdatedAt = time.Time{}
	n.ipv4Cache = nil
	n.ipv6Cache = nil
	n.networkMetaUpdatedAt = time.Time{}
}

func (n *Node) refreshNetworkMetadata() error {
	if !n.CheckClientAlive() {
		return fmt.Errorf("node is not connected")
	}

	ipv4, err := n.con.ReadFibTrie("/proc/net/fib_trie", "/proc/net/route")
	if err != nil {
		return err
	}
	ipv6, err := n.con.ReadIfInet6("/proc/net/if_inet6")
	if err != nil {
		return err
	}

	n.Lock()
	n.ipv4Cache = append([]sshproc.IPv4(nil), ipv4...)
	n.ipv6Cache = append([]sshproc.IPv6(nil), ipv6...)
	n.networkMetaUpdatedAt = time.Now()
	n.Unlock()

	return nil
}

func (n *Node) readProcBundle(includeCPUInfo, includeTaskCount bool) (*procBundle, error) {
	if n.con == nil || n.con.Connect == nil {
		return nil, fmt.Errorf("node is not connected")
	}

	command := n.buildProcBundleCommand(includeCPUInfo, includeTaskCount)
	session, err := n.con.Connect.CreateSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	output, err := session.Output(command)
	if err != nil {
		return nil, err
	}

	return parseProcBundle(string(output))
}

func (n *Node) buildProcBundleCommand(includeCPUInfo, includeTaskCount bool) string {
	sections := []struct {
		name string
		path string
	}{
		{name: "stat", path: n.PathProcStat},
		{name: "meminfo", path: n.PathProcMeminfo},
		{name: "uptime", path: n.PathProcUptime},
		{name: "loadavg", path: n.PathProcLoadavg},
		{name: "diskstats", path: n.PathProcDiskStats},
		{name: "network", path: "/proc/net/dev"},
	}

	if includeCPUInfo {
		sections = append(sections, struct {
			name string
			path string
		}{name: "cpuinfo", path: n.PathProcCpuinfo})
	}

	var commands []string
	for _, section := range sections {
		commands = append(commands,
			fmt.Sprintf("printf '%s%s\\n'", procBundleMarker, section.name),
			fmt.Sprintf("cat %s", shellQuote(section.path)),
		)
	}

	if includeTaskCount {
		commands = append(commands,
			fmt.Sprintf("printf '%s%s\\n'", procBundleMarker, "tasks"),
			"find /proc -maxdepth 1 -type d -name '[0-9]*' | wc -l",
		)
	}

	return strings.Join(commands, "; ")
}

func (n *Node) applyProcBundle(bundle *procBundle) {
	if bundle == nil {
		return
	}

	if bundle.Stat != nil {
		n.appendCPUUsage(bundle.Stat)
	}
	if len(bundle.DiskStats) > 0 {
		n.updateDiskIO(bundle.DiskStats)
	}
	if len(bundle.NetworkStat) > 0 {
		n.updateNetworkIO(bundle.NetworkStat)
	}

	now := time.Now()
	n.Lock()
	if bundle.MemInfo != nil {
		n.baseSnapshot.MemInfo = bundle.MemInfo
	}
	if bundle.Uptime != nil {
		n.baseSnapshot.Uptime = bundle.Uptime
	}
	if bundle.LoadAvg != nil {
		n.baseSnapshot.LoadAvg = bundle.LoadAvg
	}
	if bundle.MemInfo != nil || bundle.Uptime != nil || bundle.LoadAvg != nil {
		n.baseSnapshot.UpdatedAt = now
	}
	if bundle.CPUCount > 0 {
		n.cpuCoreCache = bundle.CPUCount
		n.cpuCoreCached = true
	}
	if bundle.TaskCount != nil {
		n.taskCountCache = *bundle.TaskCount
		n.taskCountUpdatedAt = now
	}
	n.Unlock()
}

func (n *Node) appendCPUUsage(stat *linux.Stat) {
	if stat == nil {
		return
	}

	cpuUsage := CPUUsage{
		CPUStat:   stat.CPUStatAll,
		Detail:    stat.CPUStats,
		Timestamp: time.Now(),
	}

	n.Lock()
	n.cpuUsage = append(n.cpuUsage, cpuUsage)
	if len(n.cpuUsage) > n.cpuUsageLimit {
		n.cpuUsage = n.cpuUsage[1:]
	}
	n.Unlock()
}

func (n *Node) updateDiskIO(stats []linux.DiskStat) {
	n.Lock()
	defer n.Unlock()

	if n.DiskIOs == nil {
		n.DiskIOs = map[string][]*DiskIO{}
	}

	for _, stat := range stats {
		device := fmt.Sprintf("/dev/%s", stat.Name)
		diskIO := DiskIO{
			Device:     device,
			ReadIOs:    stat.ReadIOs,
			ReadBytes:  stat.GetReadBytes(),
			WriteIOs:   stat.WriteIOs,
			WriteBytes: stat.GetWriteBytes(),
		}

		n.DiskIOs[device] = append(n.DiskIOs[device], &diskIO)
		if len(n.DiskIOs[device]) > n.DiskIOsLimit {
			n.DiskIOs[device] = n.DiskIOs[device][1:]
		}
	}
}

func (n *Node) updateNetworkIO(stats []linux.NetworkStat) {
	n.Lock()
	defer n.Unlock()

	if n.NetworkIOs == nil {
		n.NetworkIOs = map[string][]*NetworkIO{}
	}

	for _, stat := range stats {
		networkIO := NetworkIO{
			Device:    stat.Iface,
			RXPackets: stat.RxPackets,
			RXBytes:   stat.RxBytes,
			TXPackets: stat.TxPackets,
			TXBytes:   stat.TxBytes,
		}

		n.NetworkIOs[stat.Iface] = append(n.NetworkIOs[stat.Iface], &networkIO)
		if len(n.NetworkIOs[stat.Iface]) > n.NetworkIOsLimit {
			n.NetworkIOs[stat.Iface] = n.NetworkIOs[stat.Iface][1:]
		}
	}
}

func parseProcBundle(raw string) (*procBundle, error) {
	sections := map[string]string{}

	var current string
	var builder strings.Builder

	flush := func() {
		if current != "" {
			sections[current] = strings.TrimSuffix(builder.String(), "\n")
			builder.Reset()
		}
	}

	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, procBundleMarker) {
			flush()
			current = strings.TrimPrefix(line, procBundleMarker)
			continue
		}

		if current != "" {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	flush()

	bundle := &procBundle{}
	var err error

	if content, ok := sections["stat"]; ok {
		bundle.Stat, err = parseStatContent(content)
		if err != nil {
			return nil, err
		}
	}
	if content, ok := sections["meminfo"]; ok {
		bundle.MemInfo, err = parseMemInfoContent(content)
		if err != nil {
			return nil, err
		}
	}
	if content, ok := sections["uptime"]; ok {
		bundle.Uptime, err = parseUptimeContent(content)
		if err != nil {
			return nil, err
		}
	}
	if content, ok := sections["loadavg"]; ok {
		bundle.LoadAvg, err = parseLoadAvgContent(content)
		if err != nil {
			return nil, err
		}
	}
	if content, ok := sections["diskstats"]; ok {
		bundle.DiskStats, err = parseDiskStatsContent(content)
		if err != nil {
			return nil, err
		}
	}
	if content, ok := sections["network"]; ok {
		bundle.NetworkStat, err = parseNetworkStatContent(content)
		if err != nil {
			return nil, err
		}
	}
	if content, ok := sections["cpuinfo"]; ok {
		bundle.CPUCount = parseCPUCount(content)
	}
	if content, ok := sections["tasks"]; ok {
		taskCount, taskErr := parseTaskCountContent(content)
		if taskErr != nil {
			return nil, taskErr
		}
		bundle.TaskCount = &taskCount
	}

	return bundle, nil
}

func parseStatContent(content string) (*linux.Stat, error) {
	lines := strings.Split(content, "\n")
	result := &linux.Stat{}

	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		switch {
		case strings.HasPrefix(fields[0], "cpu"):
			cpuStat := createCPUStat(fields)
			if cpuStat == nil {
				continue
			}
			if index == 0 {
				result.CPUStatAll = *cpuStat
			} else {
				result.CPUStats = append(result.CPUStats, *cpuStat)
			}
		case fields[0] == "intr":
			result.Interrupts, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "ctxt":
			result.ContextSwitches, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "btime":
			seconds, _ := strconv.ParseInt(fields[1], 10, 64)
			result.BootTime = time.Unix(seconds, 0)
		case fields[0] == "processes":
			result.Processes, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "procs_running":
			result.ProcsRunning, _ = strconv.ParseUint(fields[1], 10, 64)
		case fields[0] == "procs_blocked":
			result.ProcsBlocked, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}

	return result, nil
}

func createCPUStat(fields []string) *linux.CPUStat {
	stat := &linux.CPUStat{Id: fields[0]}
	for i := 1; i < len(fields); i++ {
		value, _ := strconv.ParseUint(fields[i], 10, 64)
		switch i {
		case 1:
			stat.User = value
		case 2:
			stat.Nice = value
		case 3:
			stat.System = value
		case 4:
			stat.Idle = value
		case 5:
			stat.IOWait = value
		case 6:
			stat.IRQ = value
		case 7:
			stat.SoftIRQ = value
		case 8:
			stat.Steal = value
		case 9:
			stat.Guest = value
		case 10:
			stat.GuestNice = value
		}
	}

	return stat
}

func parseMemInfoContent(content string) (*linux.MemInfo, error) {
	info := &linux.MemInfo{}
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, err
		}

		switch parts[0] {
		case "MemTotal":
			info.MemTotal = value
		case "MemFree":
			info.MemFree = value
		case "Buffers":
			info.Buffers = value
		case "Cached":
			info.Cached = value
		case "SwapTotal":
			info.SwapTotal = value
		case "SwapFree":
			info.SwapFree = value
		case "MemAvailable":
			info.MemAvailable = value
		}
	}

	return info, nil
}

func parseUptimeContent(content string) (*linux.Uptime, error) {
	fields := strings.Fields(content)
	if len(fields) < 2 {
		return nil, fmt.Errorf("cannot parse uptime: %q", content)
	}

	total, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, err
	}
	idle, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, err
	}

	return &linux.Uptime{Total: total, Idle: idle}, nil
}

func parseLoadAvgContent(content string) (*linux.LoadAvg, error) {
	fields := strings.Fields(strings.TrimSpace(content))
	if len(fields) < 5 {
		return nil, fmt.Errorf("cannot parse loadavg: %q", content)
	}

	processFields := strings.Split(fields[3], "/")
	if len(processFields) != 2 {
		return nil, fmt.Errorf("cannot parse loadavg process fields: %q", content)
	}

	load := &linux.LoadAvg{}
	var err error
	load.Last1Min, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, err
	}
	load.Last5Min, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return nil, err
	}
	load.Last15Min, err = strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return nil, err
	}
	load.ProcessRunning, err = strconv.ParseUint(processFields[0], 10, 64)
	if err != nil {
		return nil, err
	}
	load.ProcessTotal, err = strconv.ParseUint(processFields[1], 10, 64)
	if err != nil {
		return nil, err
	}
	load.LastPID, err = strconv.ParseUint(fields[4], 10, 64)
	if err != nil {
		return nil, err
	}

	return load, nil
}

func parseDiskStatsContent(content string) ([]linux.DiskStat, error) {
	lines := strings.Split(content, "\n")
	results := make([]linux.DiskStat, 0, len(lines))

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}

		var stat linux.DiskStat
		major, err := strconv.ParseInt(fields[0], 10, strconv.IntSize)
		if err != nil {
			return nil, err
		}
		minor, err := strconv.ParseInt(fields[1], 10, strconv.IntSize)
		if err != nil {
			return nil, err
		}

		stat.Major = int(major)
		stat.Minor = int(minor)
		stat.Name = fields[2]
		stat.ReadIOs, _ = strconv.ParseUint(fields[3], 10, 64)
		stat.ReadMerges, _ = strconv.ParseUint(fields[4], 10, 64)
		stat.ReadSectors, _ = strconv.ParseUint(fields[5], 10, 64)
		stat.ReadTicks, _ = strconv.ParseUint(fields[6], 10, 64)
		stat.WriteIOs, _ = strconv.ParseUint(fields[7], 10, 64)
		stat.WriteMerges, _ = strconv.ParseUint(fields[8], 10, 64)
		stat.WriteSectors, _ = strconv.ParseUint(fields[9], 10, 64)
		stat.WriteTicks, _ = strconv.ParseUint(fields[10], 10, 64)
		stat.InFlight, _ = strconv.ParseUint(fields[11], 10, 64)
		stat.IOTicks, _ = strconv.ParseUint(fields[12], 10, 64)
		stat.TimeInQueue, _ = strconv.ParseUint(fields[13], 10, 64)
		results = append(results, stat)
	}

	return results, nil
}

func parseNetworkStatContent(content string) ([]linux.NetworkStat, error) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		return nil, nil
	}

	results := make([]linux.NetworkStat, 0, len(lines)-2)
	for _, line := range lines[2:] {
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}

		fields := strings.Fields(line[colon+1:])
		if len(fields) < 16 {
			continue
		}

		var stat linux.NetworkStat
		stat.Iface = strings.ReplaceAll(line[:colon], " ", "")
		stat.RxBytes, _ = strconv.ParseUint(fields[0], 10, 64)
		stat.RxPackets, _ = strconv.ParseUint(fields[1], 10, 64)
		stat.RxErrs, _ = strconv.ParseUint(fields[2], 10, 64)
		stat.RxDrop, _ = strconv.ParseUint(fields[3], 10, 64)
		stat.RxFifo, _ = strconv.ParseUint(fields[4], 10, 64)
		stat.RxFrame, _ = strconv.ParseUint(fields[5], 10, 64)
		stat.RxCompressed, _ = strconv.ParseUint(fields[6], 10, 64)
		stat.RxMulticast, _ = strconv.ParseUint(fields[7], 10, 64)
		stat.TxBytes, _ = strconv.ParseUint(fields[8], 10, 64)
		stat.TxPackets, _ = strconv.ParseUint(fields[9], 10, 64)
		stat.TxErrs, _ = strconv.ParseUint(fields[10], 10, 64)
		stat.TxDrop, _ = strconv.ParseUint(fields[11], 10, 64)
		stat.TxFifo, _ = strconv.ParseUint(fields[12], 10, 64)
		stat.TxColls, _ = strconv.ParseUint(fields[13], 10, 64)
		stat.TxCarrier, _ = strconv.ParseUint(fields[14], 10, 64)
		stat.TxCompressed, _ = strconv.ParseUint(fields[15], 10, 64)
		results = append(results, stat)
	}

	return results, nil
}

func parseCPUCount(content string) int {
	count := 0
	re := regexp.MustCompile(`(?m)^processor\s*:`)
	matches := re.FindAllStringIndex(content, -1)
	count = len(matches)
	return count
}

func parseTaskCountContent(content string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(content), 10, 64)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
