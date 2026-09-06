//go:build windows

package orchestrator

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// golang.org/x/sys v0.47.0 does not expose GetSystemTimes or
// GlobalMemoryStatusEx as typed exports, so we invoke the kernel32 entry
// points directly via the windows lazy-proc mechanism from the same package.
var (
	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = modkernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
)

const (
	// sysSamplerInterval is the ideal spacing between two CPU counter
	// samples used to compute a delta ratio.
	sysSamplerInterval = 1 * time.Second
	// sysSamplerReuse is the minimum age of the cached sample below which
	// a caller reuses the previously computed ratio instead of sleeping.
	sysSamplerReuse = 500 * time.Millisecond
)

// sysSampler implements ResourceSampler using Windows kernel32 APIs.
type sysSampler struct {
	mu      sync.Mutex
	prev    systemTimes
	prevT   time.Time
	lastCPU float64
	have    bool
}

// systemTimes holds the three cumulative FILETIME counters returned by
// GetSystemTimes. They are 100ns ticks since boot and monotonically
// increasing (kernel includes idle time).
type systemTimes struct {
	idle   uint64
	kernel uint64
	user   uint64
}

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX struct layout.
type memoryStatusEx struct {
	Length                 uint32
	MemoryLoad             uint32
	TotalPhys              uint64
	AvailPhys              uint64
	TotalPage              uint64
	AvailPageFiles         uint64
	TotalVirtual           uint64
	AvailVirtual           uint64
	AvailExtendedPageFiles uint64
}

func filetimeToUint64(ft windows.Filetime) uint64 {
	return (uint64(ft.HighDateTime) << 32) | uint64(ft.LowDateTime)
}

func readSystemTimes() (systemTimes, error) {
	var idle, kernel, user windows.Filetime
	r1, _, e1 := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r1 == 0 {
		return systemTimes{}, e1
	}
	return systemTimes{
		idle:   filetimeToUint64(idle),
		kernel: filetimeToUint64(kernel),
		user:   filetimeToUint64(user),
	}, nil
}

// readRAM returns the fraction of physical memory in use, in [0, 1].
func readRAM() (float64, error) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r1, _, e1 := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r1 == 0 {
		return 0, e1
	}
	if m.TotalPhys == 0 {
		return float64(m.MemoryLoad) / 100.0, nil
	}
	if m.AvailPhys > m.TotalPhys {
		m.AvailPhys = m.TotalPhys
	}
	return float64(m.TotalPhys-m.AvailPhys) / float64(m.TotalPhys), nil
}

// cpuRatio computes the busy fraction (0..1) between two sampled times.
// kernel time includes idle, so busy = (kernelDelta + userDelta) - idleDelta.
func cpuRatio(prev, cur systemTimes) float64 {
	total := float64(cur.kernel-prev.kernel) + float64(cur.user-prev.user)
	if total <= 0 {
		return 0
	}
	idle := float64(cur.idle - prev.idle)
	busy := total - idle
	if busy < 0 {
		busy = 0
	}
	if busy > total {
		busy = total
	}
	return busy / total
}

// NewSysSampler returns a ResourceSampler backed by Windows kernel APIs.
func NewSysSampler() ResourceSampler {
	return &sysSampler{}
}

// Load samples CPU and RAM utilisation.
//
// CPU is computed from a pair of GetSystemTimes readings. To avoid blocking
// the EffectiveCaps (~1s) cadence on every call, the previous sample is cached
// behind a mutex: when the cache is at least sysSamplerReuse old a fresh delta
// is derived from the cached counters (no sleep); when the cache is younger
// than sysSamplerReuse the last computed ratio is reused without sleeping.
// Only the very first call blocks ~sysSamplerInterval to seed the cache.
func (s *sysSampler) Load() (cpu, ram float64, err error) {
	ram, err = readRAM()
	if err != nil {
		return 0, 0, err
	}

	s.mu.Lock()
	now := time.Now()
	cur, cerr := readSystemTimes()
	if cerr != nil {
		s.mu.Unlock()
		return 0, 0, cerr
	}
	// Cache is old enough: derive a real delta from cached counters, no sleep.
	if s.have && now.Sub(s.prevT) >= sysSamplerReuse {
		cpu = cpuRatio(s.prev, cur)
		s.prev = cur
		s.prevT = now
		s.lastCPU = cpu
		s.mu.Unlock()
		return cpu, ram, nil
	}
	// Within the reuse window: recompute from cached counters, no sleep.
	if s.have {
		cpu = s.lastCPU
		s.mu.Unlock()
		return cpu, ram, nil
	}
	s.mu.Unlock()

	// First sample: take two real readings sysSamplerInterval apart.
	time.Sleep(sysSamplerInterval)
	cur2, cerr := readSystemTimes()
	if cerr != nil {
		return 0, ram, cerr
	}
	cpu = cpuRatio(cur, cur2)

	s.mu.Lock()
	s.prev = cur2
	s.prevT = time.Now()
	s.lastCPU = cpu
	s.have = true
	s.mu.Unlock()
	return cpu, ram, nil
}
