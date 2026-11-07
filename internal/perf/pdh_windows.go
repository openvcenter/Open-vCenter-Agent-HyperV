//go:build windows

package perf

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modpdh                           = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW                = modpdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW        = modpdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData          = modpdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterArrayW = modpdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery                = modpdh.NewProc("PdhCloseQuery")

	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
)

const (
	pdhFmtDouble        = 0x00000200
	pdhFmtNoCap100      = 0x00008000
	pdhMoreData         = 0x800007D2
	pdhCStatusValidData = 0x0
	pdhCStatusNewData   = 0x1
)

// pdhFmtCounterValueItem mirrors PDH_FMT_COUNTERVALUE_ITEM_W with a double
// value (szName, then PDH_FMT_COUNTERVALUE{CStatus, union}).
type pdhFmtCounterValueItem struct {
	name    *uint16
	cStatus uint32
	value   float64
}

// memoryStatusEx mirrors MEMORYSTATUSEX.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// pdhSource is the production Source: one PDH query holding every counter in
// counterPaths, collected once per Sample call.
type pdhSource struct {
	query    uintptr
	counters map[string]uintptr
	buf      []byte
}

// NewSource opens the PDH query and primes it, so the first Sample already has
// rate counters (they need two collections).
func NewSource() (Source, error) {
	var query uintptr
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&query))); r != 0 {
		return nil, fmt.Errorf("PdhOpenQuery failed: 0x%08x", r)
	}
	s := &pdhSource{query: query, counters: make(map[string]uintptr)}
	for _, c := range counterPaths {
		path, err := windows.UTF16PtrFromString(c.path)
		if err != nil {
			continue
		}
		var h uintptr
		r, _, _ := procPdhAddEnglishCounterW.Call(query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&h)))
		if r == 0 {
			s.counters[c.key] = h
		}
	}
	if len(s.counters) == 0 {
		s.Close()
		return nil, fmt.Errorf("no performance counters could be added")
	}
	procPdhCollectQueryData.Call(query)
	return s, nil
}

func (s *pdhSource) Sample() (*Sample, error) {
	if r, _, _ := procPdhCollectQueryData.Call(s.query); r != 0 {
		return nil, fmt.Errorf("PdhCollectQueryData failed: 0x%08x", r)
	}
	smp := &Sample{At: time.Now(), Counters: make(map[string]map[string]float64, len(s.counters))}
	for key, h := range s.counters {
		if values := s.read(h); len(values) > 0 {
			smp.Counters[key] = values
		}
	}

	ms := memoryStatusEx{}
	ms.length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		smp.MemTotalBytes = float64(ms.totalPhys)
		smp.MemAvailBytes = float64(ms.availPhys)
	}
	return smp, nil
}

// read returns every valid instance of a (possibly wildcard) counter. Duplicate
// instance names get a "#n" suffix, the way perfmon shows them.
func (s *pdhSource) read(counter uintptr) map[string]float64 {
	const format = pdhFmtDouble | pdhFmtNoCap100
	var size, count uint32
	r, _, _ := procPdhGetFormattedCounterArrayW.Call(counter, format,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != pdhMoreData || size == 0 {
		return nil
	}
	if cap(s.buf) < int(size) {
		s.buf = make([]byte, size)
	}
	buf := s.buf[:size]
	r, _, _ = procPdhGetFormattedCounterArrayW.Call(counter, format,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 || count == 0 {
		return nil
	}

	items := unsafe.Slice((*pdhFmtCounterValueItem)(unsafe.Pointer(&buf[0])), count)
	out := make(map[string]float64, count)
	for _, it := range items {
		if it.cStatus != pdhCStatusValidData && it.cStatus != pdhCStatusNewData {
			continue
		}
		name := windows.UTF16PtrToString(it.name)
		if _, dup := out[name]; dup {
			for n := 1; ; n++ {
				if _, taken := out[fmt.Sprintf("%s#%d", name, n)]; !taken {
					name = fmt.Sprintf("%s#%d", name, n)
					break
				}
			}
		}
		out[name] = it.value
	}
	return out
}

func (s *pdhSource) Close() {
	if s.query != 0 {
		procPdhCloseQuery.Call(s.query)
		s.query = 0
	}
}
