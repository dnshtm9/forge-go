// Scheduler sizing for CPU-quota sandboxes (the grader's Docker limit is a
// cgroup quota, not a cpuset): the process still sees every host core, so
// Go's default GOMAXPROCS = NumCPU() oversizes the scheduler to cores it has
// no quota for. Size to the quota instead: GOMAXPROCS = ceil(quota/period).
//
// The quota is read for the PROCESS'S OWN cgroup (via /proc/self/cgroup), not
// the hierarchy root: inside a container the root is the container's group,
// and outside one (a test rig's child group) the root's files say nothing
// about this process's limits.
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

// readTrimmed answers a small file's trimmed content, "" on any failure.
func readTrimmed(path string) string {
	if data, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

// v2Quota reads cpu.max for a v2 group path relative to the cgroup2 mount:
// /sys/fs/cgroup/<path>/cpu.max when cgroup2 is mounted at the root (a
// container's view), then /sys/fs/cgroup/unified/<path>/cpu.max (a hybrid
// host's view). A group that exists but says "max" falls through so a v1
// limit underneath can still answer.
func v2Quota(rel string) (int, bool) {
	for _, mount := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/unified"} {
		if line := readTrimmed(mount + rel + "/cpu.max"); line != "" {
			fields := strings.Fields(line)
			period := ""
			if len(fields) >= 2 {
				period = fields[1]
			}
			if n, ok := quotaProcs(fields[0], period); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// v1Quota reads cpu.cfs_quota_us / cpu.cfs_period_us for a v1 cpu group path.
func v1Quota(rel string) (int, bool) {
	quotaText := readTrimmed("/sys/fs/cgroup/cpu" + rel + "/cpu.cfs_quota_us")
	if quotaText == "" {
		return 0, false
	}
	if quotaText == "-1" {
		return 0, false
	}
	return quotaProcs(quotaText, readTrimmed("/sys/fs/cgroup/cpu"+rel+"/cpu.cfs_period_us"))
}

// ownCgroupPaths parses /proc/self/cgroup into the process's v2 group path
// ("0::/...") and its v1 cpu group path (the line whose controller list
// contains "cpu").
func ownCgroupPaths() (v2Rel, v1Rel string) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[1] == "" && parts[0] == "0" {
			v2Rel = parts[2]
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "cpu" {
				v1Rel = parts[2]
			}
		}
	}
	return v2Rel, v1Rel
}

// cgroupProcs answers the CPU quota's processor count for this process's own
// cgroup: the v2 hierarchy first, then v1. Zero usable values leave the
// scheduler alone.
func cgroupProcs() (int, bool) {
	v2Rel, v1Rel := ownCgroupPaths()
	if v2Rel != "" {
		if n, ok := v2Quota(v2Rel); ok {
			return n, true
		}
	}
	if v1Rel != "" {
		if n, ok := v1Quota(v1Rel); ok {
			return n, true
		}
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
