package eksaddons

import (
	"context"
	"testing"

	eksv2 "github.com/aws/aws-sdk-go-v2/service/eks"
	eksv2types "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"upgrade-guardian/internal/checker"
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) bool      { return b }

type mockEKS struct {
	addons           []string
	installedVersion map[string]string
	compatVersions   map[string][]eksv2types.AddonVersionInfo // addonName → versions
}

func (m *mockEKS) ListAddons(_ context.Context, _ *eksv2.ListAddonsInput, _ ...func(*eksv2.Options)) (*eksv2.ListAddonsOutput, error) {
	return &eksv2.ListAddonsOutput{Addons: m.addons}, nil
}

func (m *mockEKS) DescribeAddon(_ context.Context, in *eksv2.DescribeAddonInput, _ ...func(*eksv2.Options)) (*eksv2.DescribeAddonOutput, error) {
	ver := m.installedVersion[*in.AddonName]
	return &eksv2.DescribeAddonOutput{
		Addon: &eksv2types.Addon{
			AddonName:    in.AddonName,
			AddonVersion: strPtr(ver),
		},
	}, nil
}

func (m *mockEKS) DescribeAddonVersions(_ context.Context, in *eksv2.DescribeAddonVersionsInput, _ ...func(*eksv2.Options)) (*eksv2.DescribeAddonVersionsOutput, error) {
	versions := m.compatVersions[*in.AddonName]
	return &eksv2.DescribeAddonVersionsOutput{
		Addons: []eksv2types.AddonInfo{{
			AddonName:    in.AddonName,
			AddonVersions: versions,
		}},
	}, nil
}

func addonVersion(ver string, isDefault bool) eksv2types.AddonVersionInfo {
	return eksv2types.AddonVersionInfo{
		AddonVersion:    strPtr(ver),
		Compatibilities: []eksv2types.Compatibility{{DefaultVersion: isDefault}},
	}
}

func makeConfig() *checker.CheckConfig {
	return &checker.CheckConfig{
		ClusterType:    checker.ClusterTypeEKS,
		CurrentVersion: "1.27",
		TargetVersion:  "1.28",
		EKSConfig:      &checker.EKSConfig{ClusterName: "my-cluster", Region: "us-east-1"},
	}
}

func checkerWithMock(mock *mockEKS) *Checker {
	return &Checker{
		newEKS: func(_ context.Context, _ string) (eksClient, error) { return mock, nil },
	}
}

// Installed version is in compatible set → no findings (or info if not default).
func TestCompatibleAddon(t *testing.T) {
	mock := &mockEKS{
		addons:           []string{"vpc-cni"},
		installedVersion: map[string]string{"vpc-cni": "v1.18.1-eksbuild.1"},
		compatVersions: map[string][]eksv2types.AddonVersionInfo{
			"vpc-cni": {
				addonVersion("v1.18.1-eksbuild.1", false),
				addonVersion("v1.19.0-eksbuild.1", true),
			},
		},
	}
	cfg := makeConfig()
	c := checkerWithMock(mock)
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Blocker {
			t.Errorf("expected no blockers, got %v", f)
		}
	}
}

// Installed version matches the default → no findings at all.
func TestInstalledIsDefault(t *testing.T) {
	mock := &mockEKS{
		addons:           []string{"coredns"},
		installedVersion: map[string]string{"coredns": "v1.11.1-eksbuild.4"},
		compatVersions: map[string][]eksv2types.AddonVersionInfo{
			"coredns": {addonVersion("v1.11.1-eksbuild.4", true)},
		},
	}
	c := checkerWithMock(mock)
	findings, _, err := c.Check(context.Background(), makeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %v", findings)
	}
}

// Installed version is not in compatible set → critical/blocker.
func TestIncompatibleAddon(t *testing.T) {
	mock := &mockEKS{
		addons:           []string{"kube-proxy"},
		installedVersion: map[string]string{"kube-proxy": "v1.26.2-eksbuild.1"},
		compatVersions: map[string][]eksv2types.AddonVersionInfo{
			"kube-proxy": {
				addonVersion("v1.28.2-eksbuild.1", true),
				addonVersion("v1.27.6-eksbuild.2", false),
			},
		},
	}
	c := checkerWithMock(mock)
	findings, _, err := c.Check(context.Background(), makeConfig())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range findings {
		if f.Blocker && f.Severity == checker.SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Errorf("expected critical/blocker for incompatible addon, got %v", findings)
	}
}

// No managed add-ons → skip.
func TestNoAddons(t *testing.T) {
	mock := &mockEKS{addons: []string{}}
	c := checkerWithMock(mock)
	findings, meta, err := c.Check(context.Background(), makeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %v", findings)
	}
	if meta["addons_found"] != "0" {
		t.Errorf("expected addons_found=0, got %s", meta["addons_found"])
	}
}

func TestSupportsOnlyEKS(t *testing.T) {
	c := New()
	if c.Supports(checker.ClusterTypeUpstream) {
		t.Error("should not support upstream")
	}
	if !c.Supports(checker.ClusterTypeEKS) {
		t.Error("should support EKS")
	}
}

func TestMinorStr(t *testing.T) {
	cases := map[string]string{
		"1.28":   "28",
		"v1.28.0": "28",
		"1.30":   "30",
	}
	for in, want := range cases {
		got := minorStr(in)
		if got != want {
			t.Errorf("minorStr(%q) = %q, want %q", in, got, want)
		}
	}
}
