// Copyright (c) 2024 Blacknon. All rights reserved.
// Use of this source code is governed by an MIT license
// that can be found in the LICENSE file.

package monitor

import (
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	sshproc "github.com/blacknon/go-sshproc"
	sshrun "github.com/blacknon/lssh/ssh"
	"github.com/c9s/goprocinfo/linux"
)

var fstype = map[string]bool{
	"ext2":     true,
	"ext3":     true,
	"ext4":     true,
	"btrfs":    true,
	"xfs":      true,
	"vfat":     true,
	"ntfs":     true,
	"exfat":    true,
	"reiserfs": true,
	"jfs":      true,
	"zfs":      true,
	"udev":     true,
	"tmpfs":    true,
}

// CPUUsage is monitoring cpu struct. cpu is CPUStatAll only
type CPUUsage struct {
	linux.CPUStat
	Detail    []linux.CPUStat
	Timestamp time.Time
}

type CPUUsageTop struct {
	Low    float64
	Normal float64
	Kernel float64
	Guest  float64
	Total  float64
}

// MemoryUsage is monitoring memory struct
type MemoryUsage struct {
	*linux.MemInfo
	timestamp time.Time
}

type DiskUsage struct {
	MountPoint   string
	FSType       string
	Device       string
	All          uint64
	Used         uint64
	Free         uint64
	ReadIOBytes  []int64
	WriteIOBytes []int64
}

type DiskIO struct {
	Device     string
	ReadIOs    uint64
	ReadBytes  int64
	WriteIOs   uint64
	WriteBytes int64
}

type NetworkUsage struct {
	Device      string
	IPv4Address string
	IPv6Address string
	RXBytes     []uint64
	TXBytes     []uint64
	RXPackets   []uint64
	TXPackets   []uint64
}

type NetworkIO struct {
	Device    string
	RXPackets uint64
	RXBytes   uint64
	TXPackets uint64
	TXBytes   uint64
	sync.RWMutex
}

// Node is monitoring node struct
type Node struct {
	ServerName string

	con *sshproc.ConnectWithProc

	// Path
	PathProcStat      string
	PathProcCpuinfo   string
	PathProcMeminfo   string
	PathProcUptime    string
	PathProcLoadavg   string
	PathProcMounts    string
	PathProcDiskStats string

	// CPU Usage
	cpuUsage      []CPUUsage
	cpuUsageLimit int

	// DiskIO
	DiskIOs          map[string][]*DiskIO
	DiskIOsLimit     int
	DiskReadIOBytes  []int64
	DiskWriteIOBytes []int64

	// NetworkIO
	NetworkIOs       map[string][]*NetworkIO
	NetworkIOsLimit  int
	NetworkRXBytes   []uint64
	NetworkRXPackets []uint64
	NetworkTXBytes   []uint64
	NetworkTXPackets []uint64

	// Process
	LatestProcessLists []*linux.Process

	// Top
	NodeTop *NodeTop

	sync.RWMutex
}

// NewNode is create new Node struct.
// with set default values
func NewNode(name string) *Node {
	//
	procConnect := &sshproc.ConnectWithProc{Connect: nil}

	node := &Node{
		ServerName: name,

		con: procConnect,

		// set default path
		PathProcStat:      "/proc/stat",
		PathProcCpuinfo:   "/proc/cpuinfo",
		PathProcMeminfo:   "/proc/meminfo",
		PathProcUptime:    "/proc/uptime",
		PathProcLoadavg:   "/proc/loadavg",
		PathProcMounts:    "/proc/mounts",
		PathProcDiskStats: "/proc/diskstats",

		// CPU Usage
		cpuUsage:      []CPUUsage{},
		cpuUsageLimit: 480,

		// DiskIO
		DiskIOs:      map[string][]*DiskIO{},
		DiskIOsLimit: 480,

		// NetworkIO
		NetworkIOs:      map[string][]*NetworkIO{},
		NetworkIOsLimit: 480,
	}

	// Create Top
	_ = node.CreateNodeTop()

	return node
}

func (n *Node) CheckClientAlive() bool {
	if n.con.Connect == nil {
		return false
	}

	return n.con.CheckSftpClient()
}

func (n *Node) Connect(r *sshrun.Run) (err error) {
	// Create *sshlib.Connect
	con, err := r.CreateSshConnect(n.ServerName)
	if err != nil {
		log.Printf("CreateSshConnect %s Error: %s", n.ServerName, err)
		n.con.Connect = nil
		return
	}

	// Create Session and run KeepAlive
	con.SetLog("/dev/null", false)
	con.SendKeepAliveInterval = 1

	procCon := &sshproc.ConnectWithProc{Connect: con}
	err = procCon.CreateSftpClient()
	if err != nil {
		con.Client.Close()
		return
	}

	n.con = procCon

	session, err := con.CreateSession()
	if err != nil {
		procCon.Client.Close()
		procCon.CloseSftpClient()
		con.Client.Close()
		return
	}

	// send keepalive
	go func() {
		log.Println("Start KeepAlive. Server:", n.ServerName)
		con.SendKeepAlive(session)
	}()

	return
}

// GetCPUCore is get cpu core num
func (n *Node) GetCPUCore() (cn int, err error) {
	if !n.CheckClientAlive() {
		return
	}

	cpuinfo, err := n.con.ReadCPUInfo(n.PathProcCpuinfo)
	if err != nil {
		return
	}

	cn = cpuinfo.NumCPU()
	return
}

func (n *Node) GetCPUUsage() (usage float64, err error) {
	if !n.CheckClientAlive() {
		return
	}

	usage = 0.0
	if len(n.cpuUsage) >= 2 {
		lUsage := n.cpuUsage[len(n.cpuUsage)-1]
		pUsage := n.cpuUsage[len(n.cpuUsage)-2]

		// Get total usage
		lUsageTotal := sumFloat64(
			float64(lUsage.User),
			float64(lUsage.Nice),
			float64(lUsage.System),
			float64(lUsage.Idle),
			float64(lUsage.IOWait),
			float64(lUsage.IRQ),
			float64(lUsage.SoftIRQ),
			float64(lUsage.Steal),
			float64(lUsage.Guest),
			float64(lUsage.GuestNice),
		)

		pUsageTotal := sumFloat64(
			float64(pUsage.User),
			float64(pUsage.Nice),
			float64(pUsage.System),
			float64(pUsage.Idle),
			float64(pUsage.IOWait),
			float64(pUsage.IRQ),
			float64(pUsage.SoftIRQ),
			float64(pUsage.Steal),
			float64(pUsage.Guest),
			float64(pUsage.GuestNice),
		)

		// Get idle
		lIdle := float64(lUsage.Idle)
		pIdle := float64(pUsage.Idle)

		// Get diff total
		totalDiff := lUsageTotal - pUsageTotal
		idleDiff := lIdle - pIdle

		usage = (totalDiff - idleDiff) / totalDiff * 100
	}

	return
}

func (n *Node) GetCPUUsageWithSparkline() (usage float64, sparkline string, err error) {
	if !n.CheckClientAlive() {
		return
	}

	usages := []float64{}
	sparklineNums := 11

	for i := 1; i < sparklineNums; i++ {
		l := i
		p := i + 1

		if len(n.cpuUsage) < p+1 {
			usages = append(usages, 0.0)
			continue
		}

		lUsage := n.cpuUsage[len(n.cpuUsage)-l]
		pUsage := n.cpuUsage[len(n.cpuUsage)-p]

		// Get total usage
		lUsageTotal := sumFloat64(
			float64(lUsage.User),
			float64(lUsage.Nice),
			float64(lUsage.System),
			float64(lUsage.Idle),
			float64(lUsage.IOWait),
			float64(lUsage.IRQ),
			float64(lUsage.SoftIRQ),
			float64(lUsage.Steal),
			float64(lUsage.Guest),
			float64(lUsage.GuestNice),
		)

		pUsageTotal := sumFloat64(
			float64(pUsage.User),
			float64(pUsage.Nice),
			float64(pUsage.System),
			float64(pUsage.Idle),
			float64(pUsage.IOWait),
			float64(pUsage.IRQ),
			float64(pUsage.SoftIRQ),
			float64(pUsage.Steal),
			float64(pUsage.Guest),
			float64(pUsage.GuestNice),
		)

		// Get idle
		lIdle := float64(lUsage.Idle)
		pIdle := float64(pUsage.Idle)

		// Get diff total
		totalDiff := lUsageTotal - pUsageTotal
		idleDiff := lIdle - pIdle

		usages = append(usages, (totalDiff-idleDiff)/totalDiff*100)
	}

	usage = usages[0]
	sparkline = ""
	if len(usages) > 2 {
		graph := Graph{
			Data: usages,
			Max:  100,
			Min:  0,
		}

		sparkline = strings.Join(graph.Sparkline(), "")
	}

	return
}

func (n *Node) GetCPUUsageWithBrailleLine() (usage float64, brailleLine string, err error) {
	if !n.CheckClientAlive() {
		return
	}

	usages := []float64{}
	for i := 1; i < 22; i++ {
		l := i
		p := i + 1

		if len(n.cpuUsage) < p+1 {
			usages = append(usages, 0.0)
			continue
		}

		lUsage := n.cpuUsage[len(n.cpuUsage)-l]
		pUsage := n.cpuUsage[len(n.cpuUsage)-p]

		// Get total usage
		lUsageTotal := sumFloat64(
			float64(lUsage.User),
			float64(lUsage.Nice),
			float64(lUsage.System),
			float64(lUsage.Idle),
			float64(lUsage.IOWait),
			float64(lUsage.IRQ),
			float64(lUsage.SoftIRQ),
			float64(lUsage.Steal),
			float64(lUsage.Guest),
			float64(lUsage.GuestNice),
		)

		pUsageTotal := sumFloat64(
			float64(pUsage.User),
			float64(pUsage.Nice),
			float64(pUsage.System),
			float64(pUsage.Idle),
			float64(pUsage.IOWait),
			float64(pUsage.IRQ),
			float64(pUsage.SoftIRQ),
			float64(pUsage.Steal),
			float64(pUsage.Guest),
			float64(pUsage.GuestNice),
		)

		// Get idle
		lIdle := float64(lUsage.Idle)
		pIdle := float64(pUsage.Idle)

		// Get diff total
		totalDiff := lUsageTotal - pUsageTotal
		idleDiff := lIdle - pIdle

		usages = append(usages, (totalDiff-idleDiff)/totalDiff*100)
	}

	usage = usages[0]
	brailleLine = ""
	if len(usages) > 0 {
		graph := Graph{
			Data: usages,
			Max:  100,
			Min:  0,
		}

		brailleLine = strings.Join(graph.BrailleLine(), "")
	}

	return
}

func (n *Node) GetCPUCoreUsage() (usages []CPUUsageTop, err error) {
	if !n.CheckClientAlive() {
		return
	}

	usages = []CPUUsageTop{}
	if len(n.cpuUsage) >= 2 {
		lUsage := n.cpuUsage[len(n.cpuUsage)-1]
		pUsage := n.cpuUsage[len(n.cpuUsage)-2]

		for i := 0; i < len(lUsage.Detail); i++ {
			l := lUsage.Detail[i]
			p := pUsage.Detail[i]

			// Get total usage
			lUsageTotal := sumFloat64(
				float64(l.User),
				float64(l.Nice),
				float64(l.System),
				float64(l.Idle),
				float64(l.IOWait),
				float64(l.IRQ),
				float64(l.SoftIRQ),
				float64(l.Steal),
				float64(l.Guest),
				float64(l.GuestNice),
			)
			pUsageTotal := sumFloat64(
				float64(p.User),
				float64(p.Nice),
				float64(p.System),
				float64(p.Idle),
				float64(p.IOWait),
				float64(p.IRQ),
				float64(p.SoftIRQ),
				float64(p.Steal),
				float64(p.Guest),
				float64(p.GuestNice),
			)

			// Get idle
			lIdle := float64(l.Idle)
			pIdle := float64(p.Idle)

			// Get diff total
			totalDiff := lUsageTotal - pUsageTotal
			idleDiff := lIdle - pIdle

			usage := CPUUsageTop{
				Low:    float64(l.Nice-p.Nice) / totalDiff,
				Normal: float64(l.User-p.User) / totalDiff,
				Kernel: float64(l.System-p.System) / totalDiff,
				Guest:  float64(l.Guest-p.Guest) / totalDiff,
				Total:  float64((totalDiff - idleDiff) / totalDiff),
			}

			usages = append(usages, usage)
		}
	}

	return
}

// GetMemoryUsage is get memory usage. return size is byte.
func (n *Node) GetMemoryUsage() (memUsed, memTotal, swapUsed, swapTotal uint64, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	meminfo, err := n.con.ReadMemInfo(n.PathProcMeminfo)
	if err != nil {
		return
	}

	// memory
	memUsed = (meminfo.MemTotal - meminfo.MemFree - meminfo.Buffers - meminfo.Cached) * 1024
	memTotal = (meminfo.MemTotal) * 1024

	// swap
	swapUsed = (meminfo.SwapTotal - meminfo.SwapFree) * 1024
	swapTotal = (meminfo.SwapTotal) * 1024

	return
}

func (n *Node) GetMemInfo() (memInfo *linux.MemInfo, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	memInfo, err = n.con.ReadMemInfo(n.PathProcMeminfo)
	return
}

func (n *Node) GetKernelVersion() (version string, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	data, err := n.con.ReadData("/proc/version")
	if err != nil {
		return
	}

	versionSlice := strings.Split(data, " ")
	if len(versionSlice) < 3 {
		err = fmt.Errorf("Kernel Version is not found")
		return
	}

	version = strings.Join(versionSlice[:3], " ")

	return
}

func (n *Node) GetUptime() (uptime *linux.Uptime, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	uptime, err = n.con.ReadUptime(n.PathProcUptime)
	return
}

func (n *Node) GetTaskCounts() (tasks uint64, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	processList, err := n.con.ListInPID("/proc")
	if err != nil {
		return
	}

	tasks = uint64(len(processList))

	return
}

func (n *Node) GetLoadAvg() (loadavg *linux.LoadAvg, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	loadavg, err = n.con.ReadLoadAvg(n.PathProcLoadavg)

	return
}

func (n *Node) GetDiskUsage() (diskUsages []*DiskUsage, err error) {
	if n.con.Connect == nil {
		return
	}

	if !n.CheckClientAlive() {
		return
	}

	mounts, err := n.con.ReadMounts(n.PathProcMounts)
	if err != nil {
		return
	}

	for _, m := range mounts.Mounts {
		disk, err := n.con.ReadDisk(m.MountPoint)
		if err != nil {
			continue
		}

		if fstype[m.FSType] {
			n.getDiskIOBytes(m.Device)

			diskUsage := &DiskUsage{
				MountPoint:   m.MountPoint,
				FSType:       m.FSType,
				Device:       m.Device,
				All:          disk.All,
				Used:         disk.Used,
				Free:         disk.Free,
				ReadIOBytes:  n.DiskReadIOBytes,
				WriteIOBytes: n.DiskWriteIOBytes,
			}

			diskUsages = append(diskUsages, diskUsage)
		}
	}

	return
}

func (n *Node) GetNetworkUsage() (networkUsages []*NetworkUsage, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	if n.con.Connect == nil {
		return
	}

	// Get Network stats
	if len(n.NetworkIOs) == 0 {
		err = fmt.Errorf("NetworkIOs is not found")
		return
	}

	networkIOs := make(map[string][]*NetworkIO)
	n.Lock()
	for device, networkIO := range n.NetworkIOs {
		networkIOs[device] = networkIO
	}
	n.Unlock()
	for device, networkIO := range networkIOs {
		if len(networkIO) > 1 {
			n.getNetworkIO(device)

			networkUsage := &NetworkUsage{
				Device:    device,
				RXBytes:   n.NetworkRXBytes,
				TXBytes:   n.NetworkTXBytes,
				RXPackets: n.NetworkRXPackets,
				TXPackets: n.NetworkTXPackets,
			}

			networkUsages = append(networkUsages, networkUsage)
		}
	}

	for _, networkUsage := range networkUsages {
		ipv4, err := n.GetIPv4()
		if err != nil {
			continue
		}

		ipv6, err := n.GetIPV6()
		if err != nil {
			continue
		}

		for _, ip := range ipv4 {
			if ip.Interface == networkUsage.Device {
				ipAddress := ip.IPAddress
				netMask, _ := ip.Netmask.Size()
				networkUsage.IPv4Address = fmt.Sprintf("%s/%d", ipAddress, netMask)
				break
			}
		}

		for _, ip := range ipv6 {
			if ip.Interface == networkUsage.Device {
				ipAddress := ip.IPAddress
				prefix := ip.Prefix
				networkUsage.IPv6Address = fmt.Sprintf("%s/%s", ipAddress.String(), prefix)
				break
			}
		}
	}

	return
}

func (n *Node) GetIPv4() (ipv4 []sshproc.IPv4, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	ipv4, err = n.con.ReadFibTrie("/proc/net/fib_trie", "/proc/net/route")
	if err != nil {
		return
	}

	return
}

func (n *Node) GetIPV6() (ipv6 []sshproc.IPv6, err error) {
	if !n.CheckClientAlive() {
		err = fmt.Errorf("Node is not connected")
		return
	}

	ipv6, err = n.con.ReadIfInet6("/proc/net/if_inet6")
	if err != nil {
		return
	}

	return
}

func (n *Node) MonitoringCPUUsage() {
	if !n.CheckClientAlive() {
		// reset cpuUsage
		n.Lock()
		n.cpuUsage = []CPUUsage{}
		n.Unlock()

		return
	}

	timestamp := time.Now()
	stat, err := n.con.ReadStat(n.PathProcStat)
	if err != nil {
		return
	}
	stats := stat.CPUStats

	cpuUsage := CPUUsage{
		stat.CPUStatAll,
		stats,
		timestamp,
	}

	n.cpuUsage = append(n.cpuUsage, cpuUsage)
	if len(n.cpuUsage) > n.cpuUsageLimit {
		n.Lock()
		n.cpuUsage = n.cpuUsage[1:]
		n.Unlock()
	}
}

func (n *Node) MonitoringDiskIO() {
	if !n.CheckClientAlive() {
		n.Lock()
		n.DiskIOs = map[string][]*DiskIO{}
		n.Unlock()
		return
	}

	// Get Disk stats
	stats, err := n.con.ReadDiskStats(n.PathProcDiskStats)
	if err != nil {
		return
	}

	// Get Disk IO
	for _, stat := range stats {
		device := ""

		// if mapper device, get /dev path
		match, _ := regexp.MatchString("^md-", device)
		if match {
			sysDeviceName := fmt.Sprintf("/sys/block/%s/dm/name", stat.Name)
			mapperDeviceName, err := n.con.ReadData(sysDeviceName)
			if err != nil {
				continue
			}

			// overwrite device
			device = filepath.Join("/dev/mapper", mapperDeviceName)
		} else {
			device = fmt.Sprintf("/dev/%s", stat.Name)
		}

		// Get Disk IO
		diskIO := DiskIO{
			Device:     device,
			ReadIOs:    stat.ReadIOs,
			ReadBytes:  stat.GetReadBytes(),
			WriteIOs:   stat.WriteIOs,
			WriteBytes: stat.GetWriteBytes(),
		}

		n.Lock()
		n.DiskIOs[device] = append(n.DiskIOs[device], &diskIO)
		n.Unlock()

		if len(n.DiskIOs[device]) > n.DiskIOsLimit {
			n.Lock()
			n.DiskIOs[device] = n.DiskIOs[device][1:]
			n.Unlock()
		}
	}
}

func (n *Node) MonitoringNetworkIO() (err error) {
	if !n.CheckClientAlive() {
		n.Lock()
		n.NetworkIOs = map[string][]*NetworkIO{}
		n.Unlock()
		return
	}

	// Get Network stats
	stats, err := n.con.ReadNetworkStat("/proc/net/dev")
	if err != nil {
		return
	}

	// Get Network IO
	for _, stat := range stats {
		networkIO := NetworkIO{
			Device:    stat.Iface,
			RXPackets: stat.RxPackets,
			RXBytes:   stat.RxBytes,
			TXPackets: stat.TxPackets,
			TXBytes:   stat.TxBytes,
		}

		n.Lock()
		n.NetworkIOs[stat.Iface] = append(n.NetworkIOs[stat.Iface], &networkIO)
		n.Unlock()

		if len(n.NetworkIOs[stat.Iface]) > n.NetworkIOsLimit {
			n.Lock()
			n.NetworkIOs[stat.Iface] = n.NetworkIOs[stat.Iface][1:]
			n.Unlock()
		}
	}
	return
}

func (n *Node) StartMonitoring() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		n.MonitoringCPUUsage()
		n.MonitoringDiskIO()
		n.MonitoringNetworkIO()
	}
}

func (n *Node) getDiskIOBytes(device string) {
	n.Lock()
	defer n.Unlock()

	if len(device) == 0 {
		return
	}

	diskIO := n.DiskIOs[device]
	if len(diskIO) > 1 {
		var readIOBytes int64
		var writeIOBytes int64

		preReadIOBytes := diskIO[len(diskIO)-2].ReadBytes
		preWriteIOBytes := diskIO[len(diskIO)-2].WriteBytes

		readIOBytes = diskIO[len(diskIO)-1].ReadBytes - preReadIOBytes
		writeIOBytes = diskIO[len(diskIO)-1].WriteBytes - preWriteIOBytes

		if len(n.DiskReadIOBytes) == 0 {
			n.DiskReadIOBytes = append(n.DiskReadIOBytes, 0)
		} else {
			n.DiskReadIOBytes = append(n.DiskReadIOBytes, readIOBytes)
		}

		if len(n.DiskWriteIOBytes) == 0 {
			n.DiskWriteIOBytes = append(n.DiskWriteIOBytes, 0)
		} else {
			n.DiskWriteIOBytes = append(n.DiskWriteIOBytes, writeIOBytes)
		}
	}

	if len(n.DiskReadIOBytes) > n.DiskIOsLimit {
		n.DiskReadIOBytes = n.DiskReadIOBytes[1:]
	}

	if len(n.DiskWriteIOBytes) > n.DiskIOsLimit {
		n.DiskWriteIOBytes = n.DiskWriteIOBytes[1:]
	}

	return
}

func (n *Node) getNetworkIO(device string) {
	n.Lock()
	defer n.Unlock()

	if len(device) == 0 {
		return
	}

	networkIO := n.NetworkIOs[device]
	if len(networkIO) > 1 {
		var rxBytes uint64
		var txBytes uint64
		var rxPackets uint64
		var txPackets uint64

		preRXBytes := networkIO[len(networkIO)-2].RXBytes
		preRXPackets := networkIO[len(networkIO)-2].RXPackets
		preTXBytes := networkIO[len(networkIO)-2].TXBytes
		preTXPackets := networkIO[len(networkIO)-2].TXPackets

		rxBytes = networkIO[len(networkIO)-1].RXBytes - preRXBytes
		rxPackets = networkIO[len(networkIO)-1].RXPackets - preRXPackets
		txBytes = networkIO[len(networkIO)-1].TXBytes - preTXBytes
		txPackets = networkIO[len(networkIO)-1].TXPackets - preTXPackets

		if len(n.NetworkRXBytes) == 0 {
			n.NetworkRXBytes = append(n.NetworkRXBytes, 0)
		} else {
			n.NetworkRXBytes = append(n.NetworkRXBytes, uint64(rxBytes))
		}

		if len(n.NetworkRXPackets) == 0 {
			n.NetworkRXPackets = append(n.NetworkRXPackets, 0)
		} else {
			n.NetworkRXPackets = append(n.NetworkRXPackets, uint64(rxPackets))
		}

		if len(n.NetworkTXBytes) == 0 {
			n.NetworkTXBytes = append(n.NetworkTXBytes, 0)
		} else {
			n.NetworkTXBytes = append(n.NetworkTXBytes, uint64(txBytes))
		}

		if len(n.NetworkTXPackets) == 0 {
			n.NetworkTXPackets = append(n.NetworkTXPackets, 0)
		} else {
			n.NetworkTXPackets = append(n.NetworkTXPackets, uint64(txPackets))
		}

		if len(n.NetworkRXBytes) > n.NetworkIOsLimit {
			n.NetworkRXBytes = n.NetworkRXBytes[1:]
		}

		if len(n.NetworkTXBytes) > n.NetworkIOsLimit {
			n.NetworkTXBytes = n.NetworkTXBytes[1:]
		}

		if len(n.NetworkRXBytes) > n.NetworkIOsLimit {
			n.NetworkRXBytes = n.NetworkRXBytes[1:]
		}

		if len(n.NetworkRXPackets) > n.NetworkIOsLimit {
			n.NetworkRXPackets = n.NetworkRXPackets[1:]
		}

	}

	return
}
