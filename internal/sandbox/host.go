package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"aegis/internal/config"
)

// hostMemoryMB returns the machine's total memory (Linux and macOS).
func hostMemoryMB() (int, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, err
		}
		b, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return int(b / (1 << 20)), err
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.Atoi(fields[1])
			return kb / 1024, err
		}
	}
	return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
}

// fits reports whether the configured sandbox concurrency fits in total memory.
func fits(s config.Sandbox, totalMB int) error {
	need := s.MaxConcurrent*s.MemoryMB + s.HostReserveMB
	if need > totalMB {
		return fmt.Errorf("max_concurrent %d × memory_mb %d + host_reserve_mb %d = %d MB, but the host has %d MB; lower max_concurrent or use a larger instance",
			s.MaxConcurrent, s.MemoryMB, s.HostReserveMB, need, totalMB)
	}
	return nil
}

// CheckHostFits refuses limits that could exhaust the host's memory.
func CheckHostFits(s config.Sandbox) error {
	total, err := hostMemoryMB()
	if err != nil {
		return fmt.Errorf("read host memory: %w", err)
	}
	return fits(s, total)
}
