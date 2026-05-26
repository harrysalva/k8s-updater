package irsa

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	eksv2 "github.com/aws/aws-sdk-go-v2/service/eks"
	eksv2types "github.com/aws/aws-sdk-go-v2/service/eks/types"
	iamv2 "github.com/aws/aws-sdk-go-v2/service/iam"
	iamv2types "github.com/aws/aws-sdk-go-v2/service/iam/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"upgrade-guardian/internal/checker"
)

const testOIDCIssuer = "https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLEID"
const testOIDCHost = "oidc.eks.us-east-1.amazonaws.com/id/EXAMPLEID"
const testOIDCProviderARN = "arn:aws:iam::123456789012:oidc-provider/" + testOIDCHost

func strPtr(s string) *string { return &s }

// mockEKS returns a fixed OIDC issuer.
type mockEKS struct {
	issuer string
	err    error
}

func (m *mockEKS) DescribeCluster(_ context.Context, _ *eksv2.DescribeClusterInput, _ ...func(*eksv2.Options)) (*eksv2.DescribeClusterOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &eksv2.DescribeClusterOutput{
		Cluster: &eksv2types.Cluster{
			Identity: &eksv2types.Identity{
				Oidc: &eksv2types.OIDC{Issuer: &m.issuer},
			},
		},
	}, nil
}

// mockIAM allows configuring list providers and role responses.
type mockIAM struct {
	providers []iamv2types.OpenIDConnectProviderListEntry
	roles     map[string]string // roleName → URL-encoded trust policy JSON
	listErr   error
	getRoleErr error
}

func (m *mockIAM) ListOpenIDConnectProviders(_ context.Context, _ *iamv2.ListOpenIDConnectProvidersInput, _ ...func(*iamv2.Options)) (*iamv2.ListOpenIDConnectProvidersOutput, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return &iamv2.ListOpenIDConnectProvidersOutput{OpenIDConnectProviderList: m.providers}, nil
}

func (m *mockIAM) GetRole(_ context.Context, in *iamv2.GetRoleInput, _ ...func(*iamv2.Options)) (*iamv2.GetRoleOutput, error) {
	if m.getRoleErr != nil {
		return nil, m.getRoleErr
	}
	doc, ok := m.roles[*in.RoleName]
	if !ok {
		return nil, fmt.Errorf("no such role: %s", *in.RoleName)
	}
	return &iamv2.GetRoleOutput{
		Role: &iamv2types.Role{
			RoleName:                 in.RoleName,
			AssumeRolePolicyDocument: strPtr(doc),
		},
	}, nil
}

func trustPolicy(oidcHost string) string {
	raw := fmt.Sprintf(`{"Statement":[{"Principal":{"Federated":"arn:aws:iam::123:oidc-provider/%s"}}]}`, oidcHost)
	return url.QueryEscape(raw)
}

func baseConfig(saName, saNamespace, roleARN string) *checker.CheckConfig {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        saName,
			Namespace:   saNamespace,
			Annotations: map[string]string{"eks.amazonaws.com/role-arn": roleARN},
		},
	}
	return &checker.CheckConfig{
		ClusterType:   checker.ClusterTypeEKS,
		TargetVersion: "1.28",
		KubeClient:    fake.NewSimpleClientset(sa),
		EKSConfig:     &checker.EKSConfig{ClusterName: "my-cluster", Region: "us-east-1"},
	}
}

func makeChecker(eks iamEKSDescriber, iam iamDescriber) *Checker {
	return &Checker{
		newClients: func(_ context.Context, _ string) (*awsClients, error) {
			return &awsClients{eks: eks, iam: iam}, nil
		},
	}
}

func TestArnToRoleName(t *testing.T) {
	cases := map[string]string{
		"arn:aws:iam::123456789012:role/my-role":           "my-role",
		"arn:aws:iam::123456789012:role/path/to/role":      "path/to/role",
		"not-an-arn":                                       "",
		"arn:aws:iam::123:policy/something":                "",
	}
	for arn, want := range cases {
		got := arnToRoleName(arn)
		if got != want {
			t.Errorf("arnToRoleName(%q) = %q, want %q", arn, got, want)
		}
	}
}

// Happy path: OIDC registered, trust policy correct → no findings.
func TestNoFindings_HappyPath(t *testing.T) {
	roleARN := "arn:aws:iam::123456789012:role/my-app-role"
	cfg := baseConfig("my-app", "default", roleARN)

	eks := &mockEKS{issuer: testOIDCIssuer}
	iam := &mockIAM{
		providers: []iamv2types.OpenIDConnectProviderListEntry{{Arn: strPtr(testOIDCProviderARN)}},
		roles:     map[string]string{"my-app-role": trustPolicy(testOIDCHost)},
	}
	c := makeChecker(eks, iam)

	findings, meta, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %v", findings)
	}
	if meta["irsa_service_accounts"] != "1" {
		t.Errorf("expected 1 IRSA SA, got %s", meta["irsa_service_accounts"])
	}
}

// OIDC provider not registered in IAM → critical/blocker.
func TestOIDCProviderNotRegistered(t *testing.T) {
	roleARN := "arn:aws:iam::123456789012:role/my-app-role"
	cfg := baseConfig("my-app", "default", roleARN)

	eks := &mockEKS{issuer: testOIDCIssuer}
	iam := &mockIAM{
		providers: []iamv2types.OpenIDConnectProviderListEntry{}, // empty
		roles:     map[string]string{"my-app-role": trustPolicy(testOIDCHost)},
	}
	c := makeChecker(eks, iam)

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
		t.Errorf("expected critical/blocker for missing OIDC provider, got %v", findings)
	}
}

// Trust policy references wrong OIDC host → high finding per SA.
func TestWrongOIDCInTrustPolicy(t *testing.T) {
	roleARN := "arn:aws:iam::123456789012:role/my-app-role"
	cfg := baseConfig("my-app", "default", roleARN)

	eks := &mockEKS{issuer: testOIDCIssuer}
	iam := &mockIAM{
		providers: []iamv2types.OpenIDConnectProviderListEntry{{Arn: strPtr(testOIDCProviderARN)}},
		// trust policy references a DIFFERENT OIDC host
		roles: map[string]string{"my-app-role": trustPolicy("oidc.eks.eu-west-1.amazonaws.com/id/WRONGID")},
	}
	c := makeChecker(eks, iam)

	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range findings {
		if f.Severity == checker.SeverityHigh && !f.Blocker {
			found = true
		}
	}
	if !found {
		t.Errorf("expected high finding for wrong OIDC in trust policy, got %v", findings)
	}
}

// SA without the annotation → ignored.
func TestSAWithoutAnnotation(t *testing.T) {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "no-irsa", Namespace: "default"},
	}
	cfg := &checker.CheckConfig{
		ClusterType:   checker.ClusterTypeEKS,
		TargetVersion: "1.28",
		KubeClient:    fake.NewSimpleClientset(sa),
		EKSConfig:     &checker.EKSConfig{ClusterName: "my-cluster", Region: "us-east-1"},
	}
	eks := &mockEKS{issuer: testOIDCIssuer}
	iam := &mockIAM{
		providers: []iamv2types.OpenIDConnectProviderListEntry{{Arn: strPtr(testOIDCProviderARN)}},
		roles:     map[string]string{},
	}
	c := makeChecker(eks, iam)

	findings, meta, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %v", findings)
	}
	if meta["irsa_service_accounts"] != "0" {
		t.Errorf("expected 0 IRSA SAs, got %s", meta["irsa_service_accounts"])
	}
}

// No OIDC issuer on cluster → high finding.
func TestNoOIDCIssuer(t *testing.T) {
	cfg := baseConfig("my-app", "default", "arn:aws:iam::123:role/role")
	eks := &mockEKS{issuer: ""} // empty issuer
	c := makeChecker(eks, &mockIAM{})

	findings, _, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Error("expected finding when OIDC issuer is missing")
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
