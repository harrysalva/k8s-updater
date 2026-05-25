package istio

import "testing"

func TestParseImageTag(t *testing.T) {
	cases := map[string]string{
		"docker.io/istio/pilot:1.24.0":             "1.24.0",
		"docker.io/istio/pilot:1.24.0-distroless":  "1.24.0",
		"gcr.io/istio-release/pilot:v1.24.0":       "1.24.0",
		"docker.io/istio/pilot:1.24.0@sha256:abc":  "1.24.0",
		"docker.io/istio/pilot":                    "",
	}
	for in, want := range cases {
		got := parseImageTag(in)
		if got != want {
			t.Errorf("parseImageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatrixIntegrity(t *testing.T) {
	for series, rng := range compatibilityMatrix {
		if rng.minMinor > rng.maxMinor {
			t.Errorf("series %s: minMinor(%d) > maxMinor(%d)", series, rng.minMinor, rng.maxMinor)
		}
		if rng.minMinor < 20 || rng.maxMinor > 50 {
			t.Errorf("series %s: range v1.%d-v1.%d looks wrong", series, rng.minMinor, rng.maxMinor)
		}
	}
}

func TestRecommendedUpgradeOnlySupportedVersions(t *testing.T) {
	// For k8s 1.31, the recommended Istio version must be both:
	//   1. In the supported k8s range
	//   2. An upstream-supported release (no EOL recommendations)
	got := recommendedUpgrade(31)
	// 1.27 is the lowest upstream-supported series whose range covers v1.31.
	if got != "v1.27.0+" {
		t.Errorf("recommendedUpgrade(31) = %q, want %q", got, "v1.27.0+")
	}
}

func TestRecommendedUpgradeFallback(t *testing.T) {
	got := recommendedUpgrade(99)
	if got != "latest supported" {
		t.Errorf("recommendedUpgrade(99) = %q, want %q", got, "latest supported")
	}
}

func TestSupportedReleasesSubset(t *testing.T) {
	// Every supported release should also be in the compatibility matrix.
	for series := range supportedReleases {
		if _, ok := compatibilityMatrix[series]; !ok {
			t.Errorf("series %s is in supportedReleases but not compatibilityMatrix", series)
		}
	}
}
