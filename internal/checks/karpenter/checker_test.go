package karpenter

import "testing"

func TestParseImageTag(t *testing.T) {
	cases := map[string]string{
		"public.ecr.aws/karpenter/controller:v1.2.0":       "1.2.0",
		"public.ecr.aws/karpenter/controller:1.2.0":        "1.2.0",
		"public.ecr.aws/karpenter/controller:v1.2.0@sha256:abc": "1.2.0",
		"karpenter:v0.37.0":                                "0.37.0",
		"registry:5000/karpenter":                          "", // no tag, port style
		"karpenter":                                        "",
	}
	for in, want := range cases {
		got := parseImageTag(in)
		if got != want {
			t.Errorf("parseImageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMajorMinor(t *testing.T) {
	cases := map[string]string{
		"1.2.0":  "1.2",
		"v1.2.0": "1.2",
		"1.2":    "1.2",
		"0.37.0": "0.37",
	}
	for in, want := range cases {
		got := majorMinor(in)
		if got != want {
			t.Errorf("majorMinor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMatrixIntegrity verifies that the compatibility matrix is monotonic:
// each successive Karpenter series should support a k8s range that is later
// than or overlapping the previous one.
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

// TestRecommendedUpgrade ensures the function returns a version that actually
// supports the target.
func TestRecommendedUpgrade(t *testing.T) {
	cases := []struct {
		targetMinor int
		expectIn    []string // any of these is acceptable
	}{
		{29, []string{"v0.34.0+", "v0.37.0+", "v1.0.0+", "v1.2.0+", "v1.5.0+", "v1.6.0+", "v1.9.0+"}},
		{30, []string{"v0.37.0+", "v1.0.0+", "v1.2.0+", "v1.5.0+", "v1.6.0+", "v1.9.0+"}},
		{33, []string{"v1.5.0+", "v1.6.0+", "v1.9.0+"}},
		{34, []string{"v1.6.0+", "v1.9.0+"}},
		{35, []string{"v1.9.0+"}},
		{99, []string{"latest"}},
	}
	for _, tc := range cases {
		got := recommendedUpgrade(tc.targetMinor)
		match := false
		for _, ok := range tc.expectIn {
			if got == ok {
				match = true
				break
			}
		}
		if !match {
			t.Errorf("recommendedUpgrade(%d) = %q, want one of %v", tc.targetMinor, got, tc.expectIn)
		}
	}
}

// TestSeriesLess covers numeric "MAJOR.MINOR" string comparison.
func TestSeriesLess(t *testing.T) {
	cases := []struct{ a, b string; want bool }{
		{"0.37", "1.0", true},
		{"1.0", "0.37", false},
		{"1.10", "1.2", false}, // 1.10 > 1.2 numerically
		{"1.2", "1.10", true},
		{"1.2", "1.2", false},
	}
	for _, tc := range cases {
		got := seriesLess(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("seriesLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
