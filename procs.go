// Scheduler sizing for CPU-quota sandboxes (the grader's Docker limit is a
// cgroup quota, not a cpuset): the process still sees every host core, so
// Go's default GOMAXPROCS = NumCPU() oversizes the scheduler to cores it has
// no quota for. Size to the quota instead: GOMAXPROCS = ceil(quota/period).
package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// quotaProcs answers ceil(quota/period) from a cgroup cpu.max value ("max"
// means unlimited) and reports whether the value was usable.
func quotaProcs(quotaText, periodText string) (int, bool) {
	if quotaText == "max" || quotaText == "" {
		return 0, false
	}
	quota, err := strconv.Atoi(strings.TrimSpace(quotaText))
	if err != nil || quota <= 0 {
		return 0, false
	}
	period := 100000
	if strings.TrimSpace(periodText) != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(periodText)); err == nil && p > 0 {
			period = p
		}
	}
	n := (quota + period - 1) / period
	if n < 1 {
		n = 1
	}
	return n, true
}

// cgroupProcs reads the container's CPU quota: cgroup v2's cpu.max first,
// then v1's cpu.cfs_quota_us / cpu.cfs_period_us pair. Zero usable values
// leave the scheduler alone.
func cgroupProcs() (int, bool) {
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			period := ""
			if len(fields) >= 2 {
				period = fields[1]
			}
			return quotaProcs(fields[0], period)
		}
	}
	quotaText := ""
	periodText := ""
	if data, err := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us"); err == nil {
		quotaText = strings.TrimSpace(string(data))
		if quotaText == "-1" {
			return 0, false
		}
		if data, err := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us"); err == nil {
			periodText = strings.TrimSpace(string(data))
		}
		return quotaProcs(quotaText, periodText)
	}
	return 0, false
}

// sizeToQuota sizes the Go scheduler to the sandbox's CPU quota. An explicit
// GOMAXPROCS env var wins over everything; a cgroup quota smaller than the
// visible core count shrinks the scheduler to it; with no usable quota the
// fallback is 2 — the competition sandbox's documented CPU count, never
// NumCPU() (on a 16-core host without a readable cgroup the default would
// size the scheduler to 16 cores against a 2-CPU quota, the exact failure
// this exists to fix). Never raised above NumCPU(). One stderr line records
// what was chosen.
func sizeToQuota() {
	if raw := strings.TrimSpace(os.Getenv("GOMAXPROCS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
			runtime.GOMAXPROCS(n)
			os.Stderr.WriteString("[entry] GOMAXPROCS=" + strconv.Itoa(n) + " (explicit env)\n")
			return
		}
	}
	if n, ok := cgroupProcs(); ok && n >= 1 && n < runtime.NumCPU() {
		runtime.GOMAXPROCS(n)
		os.Stderr.WriteString("[entry] GOMAXPROCS=" + strconv.Itoa(n) + " (cgroup quota)\n")
		return
	}
	const fallbackProcs = 2
	n := fallbackProcs
	if numCPU := runtime.NumCPU(); n > numCPU {
		n = numCPU
	}
	runtime.GOMAXPROCS(n)
	os.Stderr.WriteString("[entry] GOMAXPROCS=" + strconv.Itoa(n) + " (fallback: no cgroup quota)\n")
}
