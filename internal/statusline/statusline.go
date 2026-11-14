// Package statusline renders the status line from Claude's JSON payload (design section 11).
// It never fails: a segment that errors is dropped.
package statusline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wonsong82/claude-hub/internal/config"
	"github.com/wonsong82/claude-hub/internal/fsutil"
	"github.com/wonsong82/claude-hub/internal/xdg"
)

// DefaultSegments is the segment order when none is configured.
var DefaultSegments = []string{"profile", "cpu", "ram", "disks", "append"}

var colorRE = regexp.MustCompile(`^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$`)

// Icon renders an icon: "#rrggbb" as a 24-bit colour block.
func Icon(s string) string {
	if s == "" {
		return "⬜"
	}
	if m := colorRE.FindStringSubmatch(s); m != nil {
		r, _ := strconv.ParseUint(m[1], 16, 8)
		g, _ := strconv.ParseUint(m[2], 16, 8)
		b, _ := strconv.ParseUint(m[3], 16, 8)
		return fmt.Sprintf("\033[38;2;%d;%d;%dm██\033[0m", r, g, b)
	}
	return s
}

func pct(v int) string {
	switch {
	case v >= 90:
		return fmt.Sprintf("\033[31m%d%%\033[0m", v)
	case v >= 70:
		return fmt.Sprintf("\033[33m%d%%\033[0m", v)
	}
	return fmt.Sprintf("%d%%", v)
}

// Render builds the line. c may be nil (no config).
func Render(c *config.Config, p xdg.Paths, profile string, payload []byte) string {
	segs := DefaultSegments
	var disks []config.Disk
	var apps []config.AppendCmd
	if c != nil && c.Statusline != nil {
		if len(c.Statusline.Segments) > 0 {
			segs = c.Statusline.Segments
		}
		disks = c.Statusline.Disks
		apps = c.Statusline.Append
	} else {
		disks = defaultDisks()
	}
	var parts []string
	var sample *metrics
	get := func() *metrics {
		if sample == nil {
			sample = loadMetrics(p)
		}
		return sample
	}
	for _, s := range segs {
		func() {
			defer func() { _ = recover() }()
			switch s {
			case "profile":
				name, icon := "default", "⬜"
				if profile != "" {
					name = profile
					if c != nil && c.Profiles[profile] != nil {
						icon = Icon(c.Profiles[profile].Icon)
					}
				}
				parts = append(parts, icon+" "+name)
			case "cpu":
				if m := get(); m != nil && m.CPU >= 0 {
					parts = append(parts, "CPU "+pct(m.CPU))
				}
			case "ram":
				if m := get(); m != nil && m.RAMTotal > 0 {
					used := float64(m.RAMUsed) / (1 << 30)
					total := m.RAMTotal / (1 << 30)
					parts = append(parts, fmt.Sprintf("RAM %.1f/%dG %s", used, total, pct(int(float64(m.RAMUsed)*100/float64(m.RAMTotal)+0.5))))
				}
			case "disks":
				for _, d := range disks {
					if seg := disk(c, d); seg != "" {
						parts = append(parts, seg)
					}
				}
			case "append":
				for _, a := range apps {
					if seg := runAppend(c, a, payload); seg != "" {
						parts = append(parts, seg)
					}
				}
			}
		}()
	}
	return strings.Join(parts, " · ")
}

func defaultDisks() []config.Disk {
	if runtime.GOOS == "darwin" {
		return []config.Disk{{Label: "Storage", Path: "/System/Volumes/Data"}}
	}
	return []config.Disk{{Label: "Storage", Path: "/"}}
}

func disk(c *config.Config, d config.Disk) string {
	path := d.Path
	if c != nil {
		path = c.Expand(path)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return ""
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return ""
	}
	bs := uint64(st.Bsize)
	total, free, avail := uint64(st.Blocks)*bs, uint64(st.Bfree)*bs, uint64(st.Bavail)*bs
	used := total - free
	if used+avail == 0 {
		return ""
	}
	p := int(float64(used)*100/float64(used+avail) + 0.5)
	return fmt.Sprintf("%s %s %.0fG free", d.Label, pct(p), float64(avail)/(1<<30))
}

func runAppend(c *config.Config, a config.AppendCmd, payload []byte) string {
	cmdline := a.Command
	if c != nil {
		cmdline = c.Expand(cmdline)
	}
	if cmdline == "" {
		return ""
	}
	to := a.TimeoutMS
	if to <= 0 {
		to = 1000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(to)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil && out.Len() == 0 {
		return ""
	}
	sc := bufio.NewScanner(&out)
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}

// metrics is a cached system sample.
type metrics struct {
	At       int64  `json:"at"`
	CPU      int    `json:"cpu"`
	RAMUsed  uint64 `json:"ram_used"`
	RAMTotal uint64 `json:"ram_total"`
	// Linux /proc/stat counters for the delta.
	Busy  uint64 `json:"busy,omitempty"`
	Total uint64 `json:"total,omitempty"`
}

func loadMetrics(p xdg.Paths) *metrics {
	cache := filepath.Join(p.StateDir, "statusline-cache.json")
	var prev metrics
	if b, err := os.ReadFile(cache); err == nil {
		_ = json.Unmarshal(b, &prev)
		if time.Now().UnixMilli()-prev.At < 2000 && prev.At > 0 {
			return &prev
		}
	}
	m := sample(&prev)
	if m == nil {
		return nil
	}
	m.At = time.Now().UnixMilli()
	if b, err := json.Marshal(m); err == nil {
		_ = fsutil.WriteFileAtomic(cache, b, 0o600)
	}
	return m
}

func sample(prev *metrics) *metrics {
	switch runtime.GOOS {
	case "darwin":
		return sampleDarwin()
	case "linux":
		return sampleLinux(prev)
	}
	return nil
}

func output(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(b)
}

func sampleDarwin() *metrics {
	m := &metrics{CPU: -1}
	ncpu, _ := strconv.Atoi(strings.TrimSpace(output("/usr/sbin/sysctl", "-n", "hw.ncpu")))
	if ncpu > 0 {
		var sum float64
		for _, l := range strings.Split(output("/bin/ps", "-A", "-o", "%cpu="), "\n") {
			if v, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(l, ",", ".")), 64); err == nil {
				sum += v
			}
		}
		v := sum / float64(ncpu)
		if v > 100 {
			v = 100
		}
		m.CPU = int(v + 0.5)
	}
	total, _ := strconv.ParseUint(strings.TrimSpace(output("/usr/sbin/sysctl", "-n", "hw.memsize")), 10, 64)
	vm := output("/usr/bin/vm_stat")
	var ps, anon, purge, wired, comp uint64
	for _, l := range strings.Split(vm, "\n") {
		f := strings.Fields(l)
		num := func(i int) uint64 {
			if i >= len(f) {
				return 0
			}
			v, _ := strconv.ParseUint(strings.TrimSuffix(f[i], "."), 10, 64)
			return v
		}
		switch {
		case strings.Contains(l, "page size of"):
			ps = num(7)
		case strings.HasPrefix(l, "Anonymous pages"):
			anon = num(2)
		case strings.HasPrefix(l, "Pages purgeable"):
			purge = num(2)
		case strings.HasPrefix(l, "Pages wired down"):
			wired = num(3)
		case strings.HasPrefix(l, "Pages occupied by compressor"):
			comp = num(4)
		}
	}
	if total > 0 && ps > 0 {
		// Activity Monitor's "Memory Used": app (anonymous - purgeable) + wired + compressed.
		u := anon
		if purge < u {
			u -= purge
		}
		m.RAMUsed = (u + wired + comp) * ps
		m.RAMTotal = total
	}
	return m
}

func sampleLinux(prev *metrics) *metrics {
	m := &metrics{CPU: -1}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		line := strings.SplitN(string(b), "\n", 2)[0]
		f := strings.Fields(line)
		if len(f) > 4 && f[0] == "cpu" {
			var total, idle uint64
			for i, x := range f[1:] {
				v, _ := strconv.ParseUint(x, 10, 64)
				total += v
				if i == 3 || i == 4 { // idle, iowait
					idle += v
				}
			}
			m.Total, m.Busy = total, total-idle
			if prev != nil && prev.Total > 0 && total > prev.Total {
				m.CPU = int(float64(m.Busy-prev.Busy)*100/float64(total-prev.Total) + 0.5)
			} else if total > 0 {
				m.CPU = int(float64(m.Busy)*100/float64(total) + 0.5)
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail uint64
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				total = v * 1024
			case "MemAvailable:":
				avail = v * 1024
			}
		}
		if total > 0 {
			m.RAMTotal, m.RAMUsed = total, total-avail
		}
	}
	return m
}
