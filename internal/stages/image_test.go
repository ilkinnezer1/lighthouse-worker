package stages

import "testing"

func TestResourceLimits(t *testing.T) {
	if res := resourceLimits(buildOverrides{}); res.NanoCPUs != 0 || res.Memory != 0 {
		t.Fatalf("unset limits must leave the container unconstrained, got %+v", res)
	}

	res := resourceLimits(buildOverrides{CPULimit: 0.5, MemoryMB: 512})
	if res.NanoCPUs != 500_000_000 {
		t.Fatalf("0.5 cores = %d nanocpus", res.NanoCPUs)
	}
	if res.Memory != 512*1024*1024 {
		t.Fatalf("512MB = %d bytes", res.Memory)
	}

	if res := resourceLimits(buildOverrides{CPULimit: 2}); res.NanoCPUs != 2_000_000_000 || res.Memory != 0 {
		t.Fatalf("a cpu-only limit must not set memory, got %+v", res)
	}
}

func TestImageStrategyPortDefaultsToWebPort(t *testing.T) {
	if got := resolvePort(0, defaultPort); got != defaultPort {
		t.Fatalf("a prebuilt image with no port override must route to %d, got %d", defaultPort, got)
	}
	if got := resolvePort(8080, defaultPort); got != 8080 {
		t.Fatalf("explicit port = %d", got)
	}
}

func TestParseBuildOverridesReadsImageAndLimits(t *testing.T) {
	o := parseBuildOverrides([]byte(`{"strategy":"image","image":"nginx:1.27","port":80,"cpuLimit":1.5,"memoryMb":256}`))
	if o.Strategy != StrategyImage {
		t.Fatalf("strategy = %q", o.Strategy)
	}
	if o.Image != "nginx:1.27" {
		t.Fatalf("image = %q", o.Image)
	}
	if o.Port != 80 || o.CPULimit != 1.5 || o.MemoryMB != 256 {
		t.Fatalf("port/cpu/mem = %d/%v/%d", o.Port, o.CPULimit, o.MemoryMB)
	}
}
