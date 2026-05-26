package subnetips

import (
	"context"
	"testing"

	ec2v2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"upgrade-guardian/internal/checker"
)

// mockEC2 implements ec2Describer and returns a fixed set of subnets.
type mockEC2 struct {
	subnets []ec2types.Subnet
	err     error
}

func (m *mockEC2) DescribeSubnets(_ context.Context, _ *ec2v2.DescribeSubnetsInput, _ ...func(*ec2v2.Options)) (*ec2v2.DescribeSubnetsOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &ec2v2.DescribeSubnetsOutput{Subnets: m.subnets}, nil
}

func int32Ptr(n int32) *int32 { return &n }
func strPtr(s string) *string  { return &s }

func subnet(id, cidr, az string, available int32) ec2types.Subnet {
	return ec2types.Subnet{
		SubnetId:                strPtr(id),
		CidrBlock:               strPtr(cidr),
		AvailabilityZone:        strPtr(az),
		AvailableIpAddressCount: int32Ptr(available),
	}
}

func makeConfig(subnetID string, prefixDelegation bool) *checker.CheckConfig {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "node-1",
			Annotations: map[string]string{"vpc.amazonaws.com/node-subnet-id": subnetID},
		},
	}
	objects := []interface{}{node}
	if prefixDelegation {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "amazon-vpc-cni", Namespace: "kube-system"},
			Data:       map[string]string{"ENABLE_PREFIX_DELEGATION": "true"},
		}
		objects = append(objects, cm)
	}
	return &checker.CheckConfig{
		ClusterType:   checker.ClusterTypeEKS,
		TargetVersion: "1.28",
		KubeClient:    fake.NewSimpleClientset(node),
		EKSConfig:     &checker.EKSConfig{ClusterName: "my-cluster", Region: "us-east-1"},
	}
}

// makeConfigFull creates a config with optional prefix delegation ConfigMap.
func makeConfigFull(subnetID string, prefixDelegation bool) *checker.CheckConfig {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "node-1",
			Annotations: map[string]string{"vpc.amazonaws.com/node-subnet-id": subnetID},
		},
	}
	if prefixDelegation {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "amazon-vpc-cni", Namespace: "kube-system"},
			Data:       map[string]string{"ENABLE_PREFIX_DELEGATION": "true"},
		}
		return &checker.CheckConfig{
			ClusterType:   checker.ClusterTypeEKS,
			TargetVersion: "1.28",
			KubeClient:    fake.NewSimpleClientset(node, cm),
			EKSConfig:     &checker.EKSConfig{ClusterName: "my-cluster", Region: "us-east-1"},
		}
	}
	return &checker.CheckConfig{
		ClusterType:   checker.ClusterTypeEKS,
		TargetVersion: "1.28",
		KubeClient:    fake.NewSimpleClientset(node),
		EKSConfig:     &checker.EKSConfig{ClusterName: "my-cluster", Region: "us-east-1"},
	}
}

func checkerWithMock(subnets []ec2types.Subnet) *Checker {
	mock := &mockEC2{subnets: subnets}
	return &Checker{
		newEC2: func(_ context.Context, _ string) (ec2Describer, error) {
			return mock, nil
		},
	}
}

func TestUsableIPCount(t *testing.T) {
	cases := map[string]int{
		"10.0.0.0/24": 251, // 256 - 5
		"10.0.0.0/25": 123, // 128 - 5
		"10.0.0.0/28": 11,  // 16  - 5
		"10.0.0.0/16": 65531,
	}
	for cidr, want := range cases {
		got := usableIPCount(cidr)
		if got != want {
			t.Errorf("usableIPCount(%q) = %d, want %d", cidr, got, want)
		}
	}
}

// /24 subnet: 251 usable, 3% free → critical/blocker
func TestCriticalThreshold(t *testing.T) {
	cfg := makeConfigFull("subnet-abc", false)
	c := checkerWithMock([]ec2types.Subnet{
		subnet("subnet-abc", "10.0.0.0/24", "us-east-1a", 7), // 7/251 = 2.8% < 5%
	})
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("expected findings, got none")
	}
	f := findings[0]
	if f.Severity != checker.SeverityCritical || !f.Blocker {
		t.Errorf("expected critical/blocker, got severity=%s blocker=%v", f.Severity, f.Blocker)
	}
}

// /24 subnet: 251 usable, 7% free → high (not blocker)
func TestHighThreshold(t *testing.T) {
	cfg := makeConfigFull("subnet-abc", false)
	c := checkerWithMock([]ec2types.Subnet{
		subnet("subnet-abc", "10.0.0.0/24", "us-east-1a", 18), // 18/251 = 7.2%
	})
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("expected findings, got none")
	}
	f := findings[0]
	if f.Severity != checker.SeverityHigh || f.Blocker {
		t.Errorf("expected high/non-blocker, got severity=%s blocker=%v", f.Severity, f.Blocker)
	}
}

// /24 subnet: 50% free → no findings
func TestHealthySubnet(t *testing.T) {
	cfg := makeConfigFull("subnet-abc", false)
	c := checkerWithMock([]ec2types.Subnet{
		subnet("subnet-abc", "10.0.0.0/24", "us-east-1a", 125), // 49.8%
	})
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings for healthy subnet, got %v", findings)
	}
}

// With prefix delegation: stricter threshold. 12% → high (normal would be OK at > 10%)
func TestPrefixDelegationStricterThreshold(t *testing.T) {
	cfg := makeConfigFull("subnet-abc", true)
	c := checkerWithMock([]ec2types.Subnet{
		// 30/251 = 11.9% — above normal high threshold (10%) but below PD high threshold (20%)
		subnet("subnet-abc", "10.0.0.0/24", "us-east-1a", 30),
	})
	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Error("expected finding with prefix delegation stricter threshold, got none")
	}
}

// No node annotations → no subnets → skip
func TestNoAnnotations(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	cfg := &checker.CheckConfig{
		ClusterType:   checker.ClusterTypeEKS,
		TargetVersion: "1.28",
		KubeClient:    fake.NewSimpleClientset(node),
		EKSConfig:     &checker.EKSConfig{ClusterName: "my-cluster", Region: "us-east-1"},
	}
	c := checkerWithMock(nil)
	findings, meta, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %v", findings)
	}
	if meta["skip_reason"] == "" {
		t.Error("expected skip_reason in meta")
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
