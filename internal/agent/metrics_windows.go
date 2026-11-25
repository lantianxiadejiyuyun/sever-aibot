package agent

import (
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"server-aibot/internal/model"
)

var (
	metricsKernel      = windows.NewLazySystemDLL("kernel32.dll")
	getSystemTimes     = metricsKernel.NewProc("GetSystemTimes")
	globalMemoryStatus = metricsKernel.NewProc("GlobalMemoryStatusEx")
)

// MEMORYSTATUSEX has DWORD length/load followed by seven DWORDLONG fields.
type memoryStatus struct {
	Length, Load                                       uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual   uint64
}

func (c *metricsCollector) collectNative(m *model.Metrics) {
	for _, key := range []string{"cpu", "memory", "network", "diskCapacity"} {
		m.Status[key] = "unavailable"
	}
	var idle, kernel, user windows.Filetime
	if ok, _, _ := getSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); ok != 0 {
		ticks := []uint64{filetimeTicks(idle), filetimeTicks(kernel), filetimeTicks(user)}
		m.Status["cpu"] = "warming_up"
		if len(c.previous) == 3 {
			if ticks[0] >= c.previous[0] && ticks[1] >= c.previous[1] && ticks[2] >= c.previous[2] {
				total := ticks[1] - c.previous[1] + ticks[2] - c.previous[2]
				idleDelta := ticks[0] - c.previous[0]
				if total > 0 && idleDelta <= total {
					v := 100 * float64(total-idleDelta) / float64(total)
					m.CPUPercent = &v
					m.Status["cpu"] = "ok"
				}
			}
		}
		c.previous = ticks
	} else {
		c.previous = nil
	}
	mem := memoryStatus{}
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ok, _, _ := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&mem))); ok != 0 && mem.TotalPhys > 0 && mem.AvailPhys <= mem.TotalPhys {
		used := mem.TotalPhys - mem.AvailPhys
		m.MemTotal = &mem.TotalPhys
		m.MemUsed = &used
		m.Status["memory"] = "ok"
	}
	// SystemDrive is the root filesystem equivalent on Windows.
	if drive := os.Getenv("SystemDrive"); drive != "" {
		if path, err := windows.UTF16PtrFromString(drive + `\`); err == nil {
			var available, total, free uint64
			if windows.GetDiskFreeSpaceEx(path, &available, &total, &free) == nil {
				setDiskCapacity(m, total, free)
			}
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return
	}
	rx, tx := map[string]uint64{}, map[string]uint64{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		row := windows.MibIfRow2{InterfaceIndex: uint32(iface.Index)}
		if windows.GetIfEntry2Ex(0, &row) != nil {
			return
		}
		rx[iface.Name], tx[iface.Name] = row.InOctets, row.OutOctets
	}
	m.NetworkRXBytes, m.NetworkTXBytes = rx, tx
	m.Status["network"] = "ok"
}

func filetimeTicks(v windows.Filetime) uint64 {
	return uint64(v.HighDateTime)<<32 | uint64(v.LowDateTime)
}
