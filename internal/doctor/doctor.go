package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type CheckResult struct {
	Name   string
	OK     bool
	Detail string
}

// RunChecks performs runtime preflight checks and returns results.
// role should be "client" or "server".
func RunChecks(role string) []CheckResult {
	var results []CheckResult

	// OS/Arch
	results = append(results, CheckResult{
		Name:   "platform",
		OK:     true,
		Detail: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	})

	// PortAudio (client only)
	if role == "client" {
		results = append(results, checkLib("libportaudio"))
	}

	// Opus
	results = append(results, checkLib("libopus"))

	// JACK (optional)
	if role == "client" {
		jack := checkLib("libjack")
		if !jack.OK {
			jack.Detail = "not found (optional, PortAudio may work without it)"
			jack.OK = true
		}
		results = append(results, jack)
	}

	// ONNX Runtime + Moonshine (server only)
	if role == "server" {
		results = append(results, checkLib("libonnxruntime"))
		results = append(results, checkLib("libmoonshine"))
	}

	// zstd command (server bundle)
	if role == "server" {
		results = append(results, checkCommand("zstd"))
	}

	// wl-copy (client, optional)
	if role == "client" {
		wl := checkCommand("wl-copy")
		if !wl.OK {
			wl.Detail = "not found (optional, needed for --clipboard)"
			wl.OK = true
		}
		results = append(results, wl)
	}

	return results
}

// PrintResults prints check results and returns true if all passed.
func PrintResults(results []CheckResult) bool {
	allOK := true
	for _, r := range results {
		status := "✅"
		if !r.OK {
			status = "❌"
			allOK = false
		}
		fmt.Fprintf(os.Stderr, "  %s %-20s %s\n", status, r.Name, r.Detail)
	}

	if !allOK {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Install missing dependencies:")
		if isDebian() {
			fmt.Fprintln(os.Stderr, "  sudo apt install -y libportaudio2 libportaudio-dev libopus-dev libopusfile-dev zstd")
		} else {
			fmt.Fprintln(os.Stderr, "  sudo dnf install -y portaudio-devel opus-devel opusfile-devel pipewire-jack-audio-connection-kit-devel zstd")
		}
	}

	return allOK
}

func checkLib(name string) CheckResult {
	// Try ldconfig first
	out, err := exec.Command("ldconfig", "-p").Output()
	if err == nil {
		soName := name + ".so"
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, soName) {
				parts := strings.SplitN(line, "=>", 2)
				path := strings.TrimSpace(parts[len(parts)-1])
				return CheckResult{Name: name, OK: true, Detail: path}
			}
		}
	}

	// Try common PipeWire JACK paths
	if name == "libjack" {
		for _, p := range []string{
			"/usr/lib64/pipewire-0.3/jack/libjack.so",
			"/usr/lib/aarch64-linux-gnu/pipewire-0.3/jack/libjack.so",
			"/usr/lib/x86_64-linux-gnu/pipewire-0.3/jack/libjack.so",
		} {
			if _, err := os.Stat(p); err == nil {
				return CheckResult{Name: name, OK: true, Detail: p + " (pipewire)"}
			}
		}
	}

	// Check LD_LIBRARY_PATH
	for _, dir := range strings.Split(os.Getenv("LD_LIBRARY_PATH"), ":") {
		if dir == "" {
			continue
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), name+".so") {
				return CheckResult{Name: name, OK: true, Detail: dir + "/" + e.Name()}
			}
		}
	}

	return CheckResult{Name: name, OK: false, Detail: "not found"}
}

func checkCommand(name string) CheckResult {
	path, err := exec.LookPath(name)
	if err != nil {
		return CheckResult{Name: name, OK: false, Detail: "not found"}
	}
	return CheckResult{Name: name, OK: true, Detail: path}
}

func isDebian() bool {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	s := strings.ToLower(string(data))
	return strings.Contains(s, "debian") || strings.Contains(s, "ubuntu") || strings.Contains(s, "raspbian")
}
