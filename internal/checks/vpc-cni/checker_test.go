package vpccni

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"upgrade-guardian/internal/checker"
)

func TestParseImageTag(t *testing.T) {
	cases := map[string]string{
		"602401143452.dkr.ecr.us-west-2.amazonaws.com/amazon-k8s-cni:v1.18.1-eksbuild.1": "1.18.1-eksbuild.1",
		"amazon-k8s-cni:v1.18.1":              "1.18.1",
		"amazon-k8s-cni":                      "",
		"amazon-k8s-cni:v1.18.1@sha256:abc":   "1.18.1",
	}
	for in, want := range cases {
		got := parseImageTag(in)
		if got != want {
			t.Errorf("parseImageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	cases := []struct{ a, b string; want int }{
		{"1.18.1", "1.17.1", 1},
		{"1.11.4", "1.11.4", 0},
		{"1.10.0", "1.11.0", -1},
		{"1.19.0", "1.11.4", 1},
		{"1.18.1-eksbuild.1", "1.18.1", 0}, // suffix stripped
	}
	for _, tc := range cases {
		got := versionCompare(tc.a, tc.b)
		if (got < 0 && tc.want >= 0) || (got == 0 && tc.want != 0) || (got > 0 && tc.want <= 0) {
			t.Errorf("versionCompare(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStripEKSBuildSuffix(t *testing.T) {
	cases := map[string]string{
		"1.18.1-eksbuild.1": "1.18.1",
		"1.18.1":            "1.18.1",
		"v1.18.1":           "1.18.1",
	}
	for in, want := range cases {
		got := stripEKSBuildSuffix(in)
		if got != want {
			t.Errorf("stripEKSBuildSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func makeEKSConfig() *checker.CheckConfig {
	return &checker.CheckConfig{
		ClusterType:    checker.ClusterTypeEKS,
		CurrentVersion: "1.27",
		TargetVersion:  "1.28",
		EKSConfig: &checker.EKSConfig{
			ClusterName: "my-cluster",
			Region:      "us-east-1",
		},
	}
}

func fakeClientWithAWSNode(version string) *fake.Clientset {
	image := "602401143452.dkr.ecr.us-west-2.amazonaws.com/amazon-k8s-cni:v" + version
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-node", Namespace: "kube-system"},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "aws-node", Image: image}},
				},
			},
		},
	}
	return fake.NewSimpleClientset(ds)
}

func TestVPCCNITooOld(t *testing.T) {
	cfg := makeEKSConfig()
	cfg.KubeClient = fakeClientWithAWSNode("1.14.1") // too old for k8s 1.28 (min 1.16.0)

	c := New()
	findings, _, err := c.Check(context.Background(), cfg)
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
		t.Errorf("expected critical/blocker finding for outdated VPC CNI, got %v", findings)
	}
}

func TestVPCCNISufficientVersion(t *testing.T) {
	cfg := makeEKSConfig()
	cfg.KubeClient = fakeClientWithAWSNode("1.16.0") // exactly minimum for k8s 1.28

	c := New()
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Blocker {
			t.Errorf("unexpected blocker finding: %s", f.Title)
		}
	}
}

func TestPrefixDelegationWithOldCNI(t *testing.T) {
	cfg := makeEKSConfig()
	// VPC CNI version sufficient for k8s but too old for prefix delegation
	image := "amazon-k8s-cni:v1.10.0"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-node", Namespace: "kube-system"},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "aws-node", Image: image}},
				},
			},
		},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "amazon-vpc-cni", Namespace: "kube-system"},
		Data:       map[string]string{"ENABLE_PREFIX_DELEGATION": "true"},
	}
	cfg.KubeClient = fake.NewSimpleClientset(ds, cm)
	cfg.TargetVersion = "1.28"

	c := New()
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var pdFinding bool
	for _, f := range findings {
		if strings.Contains(f.Title, "Prefix delegation") || strings.Contains(f.Title, "prefix delegation") {
			pdFinding = true
		}
	}
	if !pdFinding {
		t.Errorf("expected prefix delegation finding, got %v", findings)
	}
}

func TestNotInstalled(t *testing.T) {
	cfg := makeEKSConfig()
	cfg.KubeClient = fake.NewSimpleClientset() // no aws-node

	c := New()
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Error("expected a finding when aws-node is missing")
	}
}

func TestSupportsOnlyEKS(t *testing.T) {
	c := New()
	if c.Supports(checker.ClusterTypeUpstream) {
		t.Error("should not support upstream clusters")
	}
	if !c.Supports(checker.ClusterTypeEKS) {
		t.Error("should support EKS clusters")
	}
}
