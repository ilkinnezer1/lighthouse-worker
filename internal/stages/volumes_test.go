package stages

import (
	"strings"
	"testing"
)

func TestVolumeNameIsEnvironmentScoped(t *testing.T) {
	a := VolumeName("env-a", "uploads")
	b := VolumeName("env-b", "uploads")
	if a == b {
		t.Fatalf("the same volume name in two environments must not collide: %q", a)
	}
	if !strings.HasPrefix(a, EnvVolumePrefix("env-a")) {
		t.Fatalf("%q must carry its environment prefix so teardown can find it", a)
	}
	// A redeploy must resolve to the identical volume — that is what persists data.
	if VolumeName("env-a", "uploads") != a {
		t.Fatal("volume name must be stable across deploys")
	}
}

func TestValidateVolume(t *testing.T) {
	if err := validateVolume(volumeMount{Name: "uploads", Path: "/app/storage"}); err != nil {
		t.Fatalf("valid volume rejected: %v", err)
	}
	if err := validateVolume(volumeMount{Name: "data-1_v.2", Path: "/data"}); err != nil {
		t.Fatalf("docker-safe name rejected: %v", err)
	}

	for _, bad := range []volumeMount{
		{Name: "", Path: "/data"},
		{Name: "has space", Path: "/data"},
		{Name: "-leading", Path: "/data"},
		{Name: "semi;colon", Path: "/data"},
		{Name: "ok", Path: "relative/path"},
		{Name: "ok", Path: "/"},
		{Name: "ok", Path: "/app/../../etc"},
	} {
		if err := validateVolume(bad); err == nil {
			t.Fatalf("expected %+v to be rejected", bad)
		}
	}
}

func TestParseBuildOverridesReadsVolumes(t *testing.T) {
	o := parseBuildOverrides([]byte(`{"volumes":[{"name":"uploads","path":"/app/storage/uploads"}]}`))
	if len(o.Volumes) != 1 {
		t.Fatalf("volumes = %+v", o.Volumes)
	}
	if o.Volumes[0].Name != "uploads" || o.Volumes[0].Path != "/app/storage/uploads" {
		t.Fatalf("volume parsed as %+v", o.Volumes[0])
	}
}
