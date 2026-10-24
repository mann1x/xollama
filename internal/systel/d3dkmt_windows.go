package systel

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ollama/ollama/api"
)

// The GPUs NVML does not cover — AMD, Intel, and NVIDIA without its driver's
// library — from the kernel graphics interface (D3DKMT) and the GPU
// performance counters behind Task Manager's graphs: which adapters there
// are, where each sits on the bus and its dedicated memory; then how much of
// that memory is in use and how busy its busiest engine is.
var (
	gdi32                    = windows.NewLazySystemDLL("gdi32.dll")
	procEnumAdapters2        = gdi32.NewProc("D3DKMTEnumAdapters2")
	procQueryAdapterInfo     = gdi32.NewProc("D3DKMTQueryAdapterInfo")
	procCloseAdapter         = gdi32.NewProc("D3DKMTCloseAdapter")
	pdh                      = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery         = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollect           = pdh.NewProc("PdhCollectQueryData")
	procPdhGetArray          = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

const (
	kmtqaiGetSegmentSize    = 3
	kmtqaiAdapterAddress    = 6
	kmtqaiAdapterRegistry   = 8
	kmtqaiPhysAdapterDevIDs = 31
	pdhFmtDouble            = 0x00000200
	pdhFmtNoCap100          = 0x00008000
	pdhMoreData             = 0x800007D2
	pdhCStatusValidData     = 0
	pdhCStatusNewData       = 1
)

type kmtAdapter struct {
	handle     uint32
	luid       string // as the counters name it: 0x%08X_0x%08X, high then low
	vendor     uint32
	name       string
	pci        string
	vram       uint64 // dedicated video memory (DedicatedVideoMemorySize)
	shared     uint64 // shared system memory the GPU may address (SharedSystem)
	integrated bool   // an iGPU whose real memory is the shared pool
}

type d3dkmtReader struct {
	query      uintptr
	engines    uintptr // \GPU Engine(*)\Utilization Percentage
	dedicated  uintptr // \GPU Adapter Memory(*)\Dedicated Usage
	collected  bool    // a rate needs two collections
	adapters   []kmtAdapter
	enumerated bool
}

func (r *d3dkmtReader) reset() { r.collected = false }

func (r *d3dkmtReader) read(s *Snapshot) {
	if !r.enumerated {
		r.adapters, r.enumerated = enumAdapters(), true
	}
	var mine []kmtAdapter
	for _, a := range r.adapters {
		if a.vendor == 0x10de && nvmlOpen() != nil {
			continue // NVML lists it, with everything
		}
		if a.vendor == 0x1414 {
			continue // Microsoft's software adapter
		}
		mine = append(mine, a)
	}
	if len(mine) == 0 {
		return
	}
	util, used := r.counters()
	for _, a := range mine {
		d := api.SystemDevice{Vendor: vendorName(a.vendor), Name: a.name, PCIID: a.pci, Source: "d3dkmt", Integrated: a.integrated}
		// An integrated GPU's dedicated segment is only its UMA carve-out; the
		// memory it can actually use is the shared system pool, which is what
		// /api/xollama/devices reports. Report dedicated + shared for an iGPU so
		// the two endpoints agree (bug-320: a Radeon iGPU showed 509 MB here and
		// 33.4 GB on /devices).
		total := a.vram
		if a.integrated {
			total = a.vram + a.shared
		}
		if total > 0 {
			d.TotalMemory = ptr(total)
		}
		if u, ok := used[a.luid]; ok {
			d.UsedMemory = ptr(u)
			if total >= u && total > 0 {
				d.FreeMemory = ptr(total - u)
			}
		}
		// No engine counter for an adapter means no process has work on it:
		// its utilization is 0, once there is a rate at all.
		if util != nil {
			d.UtilPct = ptr(min(util[a.luid], 100))
		}
		s.Devices = append(s.Devices, d)
	}
}

func vendorName(v uint32) string {
	switch v {
	case 0x1002:
		return "amd"
	case 0x8086:
		return "intel"
	case 0x10de:
		return "nvidia"
	}
	return fmt.Sprintf("0x%04x", v)
}

// enumAdapters lists the adapters once: they do not change while the server
// runs, short of a driver reinstall.
func enumAdapters() []kmtAdapter {
	// D3DKMT_ENUMADAPTERS2: NumAdapters, then a pointer to the array.
	var req struct {
		n    uint32
		_    uint32
		list uintptr
	}
	if r, _, _ := procEnumAdapters2.Call(uintptr(unsafe.Pointer(&req))); r != 0 || req.n == 0 {
		return nil
	}
	// D3DKMT_ADAPTERINFO: hAdapter, AdapterLuid (low, high), NumOfSources,
	// bPrecisePresentRegionsPreferred: 20 bytes.
	info := make([]byte, 20*req.n)
	req.list = uintptr(unsafe.Pointer(&info[0]))
	if r, _, _ := procEnumAdapters2.Call(uintptr(unsafe.Pointer(&req))); r != 0 {
		return nil
	}
	var out []kmtAdapter
	for i := range req.n {
		e := info[20*i:]
		a := kmtAdapter{
			handle: *(*uint32)(unsafe.Pointer(&e[0])),
			luid:   fmt.Sprintf("0x%08x_0x%08x", *(*uint32)(unsafe.Pointer(&e[8])), *(*uint32)(unsafe.Pointer(&e[4]))),
		}
		var addr [3]uint32 // BusNumber, DeviceNumber, FunctionNumber
		if queryAdapter(a.handle, kmtqaiAdapterAddress, unsafe.Pointer(&addr), unsafe.Sizeof(addr)) {
			a.pci = fmt.Sprintf("0000:%02x:%02x.%x", addr[0], addr[1], addr[2])
		}
		// D3DKMT_QUERY_DEVICE_IDS: PhysicalAdapterIndex, then VendorID,
		// DeviceID, SubVendorID, SubSystemID, RevisionID, BusType.
		var ids [7]uint32
		if queryAdapter(a.handle, kmtqaiPhysAdapterDevIDs, unsafe.Pointer(&ids), unsafe.Sizeof(ids)) {
			a.vendor = ids[1]
		}
		var seg [3]uint64 // DedicatedVideoMemorySize, DedicatedSystem, SharedSystem
		if queryAdapter(a.handle, kmtqaiGetSegmentSize, unsafe.Pointer(&seg), unsafe.Sizeof(seg)) {
			a.vram = seg[0]
			a.shared = seg[2]
			a.integrated = isIntegratedUMA(a.vram, a.shared)
		}
		var reg [4 * 260]uint16 // AdapterString, BiosString, DacType, ChipType
		if queryAdapter(a.handle, kmtqaiAdapterRegistry, unsafe.Pointer(&reg), unsafe.Sizeof(reg)) {
			a.name = strings.TrimSpace(windows.UTF16ToString(reg[:260]))
		}
		closeAdapter(a.handle)
		out = append(out, a)
	}
	return out
}

func queryAdapter(h uint32, typ uint32, data unsafe.Pointer, size uintptr) bool {
	// D3DKMT_QUERYADAPTERINFO: hAdapter, Type, pPrivateDriverData,
	// PrivateDriverDataSize.
	q := struct {
		h    uint32
		typ  uint32
		data unsafe.Pointer
		size uint32
		_    uint32
	}{h, typ, data, uint32(size), 0}
	r, _, _ := procQueryAdapterInfo.Call(uintptr(unsafe.Pointer(&q)))
	return r == 0
}

func closeAdapter(h uint32) {
	c := struct{ h uint32 }{h}
	procCloseAdapter.Call(uintptr(unsafe.Pointer(&c))) //nolint:errcheck
}

// counters collects the GPU counters: each adapter's busiest engine type (the
// figure Task Manager shows), summed over the processes using it, and its
// dedicated memory in use. Utilization is a rate, so the first collection
// after a reset gives none.
func (r *d3dkmtReader) counters() (util map[string]float64, used map[string]uint64) {
	if r.query == 0 {
		var q uintptr
		if rc, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&q))); rc != 0 {
			return nil, nil
		}
		r.query = q
		r.engines = addCounter(q, `\GPU Engine(*)\Utilization Percentage`)
		r.dedicated = addCounter(q, `\GPU Adapter Memory(*)\Dedicated Usage`)
	}
	if rc, _, _ := procPdhCollect.Call(r.query); rc != 0 {
		return nil, nil
	}
	used = map[string]uint64{}
	for name, v := range counterArray(r.dedicated) {
		if luid := luidOf(name); luid != "" {
			used[luid] += uint64(v)
		}
	}
	if !r.collected {
		r.collected = true
		return nil, used
	}
	byType := map[string]float64{} // luid|engtype -> summed over processes
	for name, v := range counterArray(r.engines) {
		luid := luidOf(name)
		_, typ, ok := strings.Cut(name, "engtype_")
		if luid == "" || !ok {
			continue
		}
		byType[luid+"|"+typ] += v
	}
	util = map[string]float64{}
	for k, v := range byType {
		luid, _, _ := strings.Cut(k, "|")
		if v > util[luid] {
			util[luid] = v
		}
	}
	return util, used
}

func addCounter(q uintptr, path string) uintptr {
	p, _ := windows.UTF16PtrFromString(path)
	var c uintptr
	if rc, _, _ := procPdhAddEnglishCounter.Call(q, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&c))); rc != 0 {
		return 0
	}
	return c
}

// counterArray reads every instance of a wildcard counter.
func counterArray(c uintptr) map[string]float64 {
	if c == 0 {
		return nil
	}
	var size, count uint32
	rc, _, _ := procPdhGetArray.Call(c, pdhFmtDouble|pdhFmtNoCap100, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(rc) != pdhMoreData || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	if rc, _, _ = procPdhGetArray.Call(c, pdhFmtDouble|pdhFmtNoCap100, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0]))); rc != 0 {
		return nil
	}
	// PDH_FMT_COUNTERVALUE_ITEM_W: szName, then CStatus, padding and the
	// double: 24 bytes. The names point into buf.
	out := make(map[string]float64, count)
	for i := range count {
		e := buf[24*i:]
		status := *(*uint32)(unsafe.Pointer(&e[8]))
		if status != pdhCStatusValidData && status != pdhCStatusNewData {
			continue
		}
		name := windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&e[0])))
		out[name] += *(*float64)(unsafe.Pointer(&e[16]))
	}
	return out
}

// luidOf finds "luid_0x…_0x…" in a counter instance name.
func luidOf(name string) string {
	_, rest, ok := strings.Cut(strings.ToLower(name), "luid_")
	if !ok || len(rest) < 21 {
		return ""
	}
	return rest[:21]
}
