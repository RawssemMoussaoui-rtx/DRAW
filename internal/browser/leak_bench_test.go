package browser

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// countChromiumProcesses returns the OS-level count of running Chromium-based
// browser processes. On Windows it shells out to `tasklist` to count
// chrome.exe / chromium.exe. On non-Windows platforms it returns -1,
// signalling callers that OS-level enumeration is unavailable.
//
// tasklist may count Chromium processes from sources other than this
// benchmark (e.g. a user's open Chrome). The leak-detection signal is the
// *delta* (after - before), so a stable baseline is expected. However,
// external Chrome processes can start/stop independently, making the OS
// count noisy across multiple benchmark iterations. For this reason the
// OS count is reported as an informational metric; the assertion uses
// m.Stats().Active (the manager's own accounting) as the proxy, per plan
// §9.2 ("fall back to Active counter" when process enumeration is fragile).
func countChromiumProcesses() int {
	if runtime.GOOS != "windows" {
		return -1
	}
	total := 0
	for _, name := range []string{"chrome.exe", "chromium.exe"} {
		out, err := exec.Command("tasklist", "/fi", "imagename eq "+name, "/fo", "csv", "/nh").Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) != "" {
				total++
			}
		}
	}
	return total
}

// BenchmarkAcquireRelease_MemoryLeak (§0.13 #5) measures memory and process
// growth across N sequential Acquire->Capture->Release cycles.
//
// This is a read-only measurement harness only — Phase-J browser-context reuse
// (plan §9.2) is documented in chromedp.go and NOT implemented here.
//
// Design:
//   - Skips when no Chromium binary can be resolved (CI without Chromium).
//   - Snapshots runtime.ReadMemStats (Sys, Alloc, HeapInuse) + process
//     count before and after the loop, running GC before each snapshot.
//   - b.ReportMetric reports:
//       mem_sys_delta_bytes       delta of runtime MemStats.Sys
//       mem_heap_delta_bytes      delta of runtime MemStats.HeapInuse
//       mem_alloc_delta_bytes     delta of runtime MemStats.Alloc
//       proc_count_delta          manager Active counter delta (proxy)
//       proc_count_before         baseline Active count (should be 0)
//       proc_count_after          post-loop Active count (should be 0)
//       os_proc_count_before      OS-level chrome.exe/chromium.exe count
//       os_proc_count_after       OS-level count after loop
//       os_proc_count_delta       OS-level count delta (informational)
//   - Asserts:
//       1. proc_count_after == 0 — the manager's Active counter returns to
//          baseline (Stats().Active is the proxy per plan §9.2; OS-level
//          tasklist is reported but not asserted due to external Chrome noise).
//       2. heap delta < 10 MB for N tasks — retained allocators/contexts
//          indicate a leak. Threshold documented below.
//
// Threshold rationale: each Acquire->Release must fully tear down its
// chromedp exec-allocator + browser context. A heap delta exceeding 10 MB
// for any N indicates retained Go objects (pooled contexts, allocator state)
// — the leak §0.13 #5 is designed to surface for Phase-J reuse evaluation.
func BenchmarkAcquireRelease_MemoryLeak(b *testing.B) {
	// Skip fast if Chromium is not resolvable — mirrors
	// chromiumAvailable (chromedp_test.go:18) but adapted for *testing.B
	// (the helper takes *testing.T and does not share the testing.TB interface).
	if _, err := resolveChromiumPath(BrowserOpts{}); err != nil {
		b.Skipf("chromium not available: %v — §0.13 #5 harness ready but no browser to exercise", err)
	}

	m := NewBrowserManager(BrowserOpts{})
	defer m.Close()

	// procCount returns the manager's Active counter — an exact, in-process
	// proxy for "browser contexts spawned and not yet released". This is the
	// assertion metric per plan §9.2 (fall-back when OS enumeration is
	// fragile). The OS-level count is collected separately below as
	// informational metrics.
	procCount := func() int {
		return m.Stats().Active
	}

	// --- Before snapshot (outside timer) ---
	runtime.GC()
	var msBefore runtime.MemStats
	runtime.ReadMemStats(&msBefore)
	procBefore := procCount()
	osProcBefore := countChromiumProcesses()

	b.ReportMetric(float64(procBefore), "proc_count_before")
	if osProcBefore >= 0 {
		b.ReportMetric(float64(osProcBefore), "os_proc_count_before")
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		bc, err := m.Acquire(context.Background(), nil)
		if err != nil {
			_ = m.Release(bc)
			b.Fatalf("Acquire #%d: %v", i, err)
		}

		// about:blank is already proven in Acquire's fail-fast (chromedp.go:112).
		if _, err := bc.Capture("about:blank"); err != nil {
			_ = m.Release(bc)
			b.Fatalf("Capture #%d: %v", i, err)
		}

		if err := m.Release(bc); err != nil {
			b.Fatalf("Release #%d: %v", i, err)
		}

		// Confirm the Done channel fires — guarantees the cancel cascade
		// has triggered before the next iteration.
		select {
		case <-bc.Done():
		case <-time.After(5 * time.Second):
			b.Fatalf("bc.Done not closed after Release #%d", i)
		}
	}

	b.StopTimer()

	// Let OS-level Chromium processes terminate after the last Release
	// before snapshotting the process count.
	time.Sleep(500 * time.Millisecond)

	// --- After snapshot ---
	runtime.GC()
	var msAfter runtime.MemStats
	runtime.ReadMemStats(&msAfter)
	procAfter := procCount()
	osProcAfter := countChromiumProcesses()

	// --- Report metrics ---
	b.ReportMetric(float64(msAfter.Sys-msBefore.Sys), "mem_sys_delta_bytes")
	b.ReportMetric(float64(msAfter.HeapInuse-msBefore.HeapInuse), "mem_heap_delta_bytes")
	b.ReportMetric(float64(msAfter.Alloc-msBefore.Alloc), "mem_alloc_delta_bytes")
	b.ReportMetric(float64(procAfter), "proc_count_after")
	b.ReportMetric(float64(procAfter-procBefore), "proc_count_delta")
	if osProcBefore >= 0 && osProcAfter >= 0 {
		b.ReportMetric(float64(osProcAfter), "os_proc_count_after")
		b.ReportMetric(float64(osProcAfter-osProcBefore), "os_proc_count_delta")
	}

	// --- Assertions ---
	heapLimit := int64(10 * 1024 * 1024) // 10 MB — documented threshold
	b.Logf(
		"proc(before/after/delta)=%d/%d/%d | "+
			"heap delta=%d bytes (limit=%d) | sys delta=%d | alloc delta=%d",
		procBefore, procAfter, procAfter-procBefore,
		int64(msAfter.HeapInuse)-int64(msBefore.HeapInuse), heapLimit,
		int64(msAfter.Sys)-int64(msBefore.Sys),
		int64(msAfter.Alloc)-int64(msBefore.Alloc),
	)
	if osProcBefore >= 0 && osProcAfter >= 0 {
		b.Logf("OS procs (informational): before=%d after=%d delta=%d",
			osProcBefore, osProcAfter, osProcAfter-osProcBefore)
	}

	if procAfter != 0 {
		b.Errorf(
			"process count leak (Active proxy): before=%d after=%d — "+
				"manager did not release all browser contexts",
			procBefore, procAfter,
		)
	}

	heapDelta := int64(msAfter.HeapInuse) - int64(msBefore.HeapInuse)
	if heapDelta > heapLimit {
		b.Errorf(
			"heap leak: delta=%d bytes exceeds threshold=%d bytes — "+
				"retained chromedp allocators/contexts (gating Phase-J reuse)",
			heapDelta, heapLimit,
		)
	}
}
