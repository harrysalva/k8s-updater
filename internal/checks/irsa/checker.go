// Package irsa validates IAM Roles for Service Accounts (IRSA) configuration
// before a Kubernetes upgrade. IRSA relies on the cluster's OIDC provider; if
// the trust policy references the wrong OIDC URL, or if the provider is not
// registered in IAM, pods lose AWS API access immediately after the upgrade —
// often silently, since the pod starts but all AWS calls return 401/403.
//
// Checks performed:
//  1. OIDC provider is registered in IAM for this cluster.
//  2. Each ServiceAccount with eks.amazonaws.com/role-arn has a trust policy
//     that references the cluster's OIDC issuer URL.
//
// SOURCE OF TRUTH:
//
//	https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html
//
// LAST VERIFIED: 2026-05-25
package irsa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	eksv2 "github.com/aws/aws-sdk-go-v2/service/eks"
	iamv2 "github.com/aws/aws-sdk-go-v2/service/iam"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "irsa-oidc"

// awsClients groups the AWS API surfaces needed by this checker.
type awsClients struct {
	eks iamEKSDescriber
	iam iamDescriber
}

type iamEKSDescriber interface {
	DescribeCluster(ctx context.Context, in *eksv2.DescribeClusterInput, optFns ...func(*eksv2.Options)) (*eksv2.DescribeClusterOutput, error)
}

type iamDescriber interface {
	ListOpenIDConnectProviders(ctx context.Context, in *iamv2.ListOpenIDConnectProvidersInput, optFns ...func(*iamv2.Options)) (*iamv2.ListOpenIDConnectProvidersOutput, error)
	GetRole(ctx context.Context, in *iamv2.GetRoleInput, optFns ...func(*iamv2.Options)) (*iamv2.GetRoleOutput, error)
}

type Checker struct {
	newClients func(ctx context.Context, region string) (*awsClients, error)
}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker {
	return &Checker{
		newClients: func(ctx context.Context, region string) (*awsClients, error) {
			awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
			if err != nil {
				return nil, err
			}
			return &awsClients{
				eks: eksv2.NewFromConfig(awsCfg),
				iam: iamv2.NewFromConfig(awsCfg),
			}, nil
		},
	}
}

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(ct checker.ClusterType) bool {
	return ct == checker.ClusterTypeEKS
}

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	if cfg.EKSConfig == nil || cfg.EKSConfig.ClusterName == "" {
		return nil, map[string]string{"skip_reason": "EKSConfig missing"}, nil
	}

	clients, err := c.newClients(ctx, cfg.EKSConfig.Region)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: create AWS clients: %w", Name, err)
	}

	// Step 1: get the cluster OIDC issuer URL.
	oidcIssuer, err := getOIDCIssuer(ctx, clients.eks, cfg.EKSConfig.ClusterName)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: describe cluster: %w", Name, err)
	}
	if oidcIssuer == "" {
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityHigh,
			Blocker:     false,
			Title:       "EKS cluster has no OIDC issuer configured",
			Description: "No OIDC issuer URL found for this EKS cluster. IRSA will not work — any ServiceAccount with eks.amazonaws.com/role-arn will fail to obtain AWS credentials.",
			Remediation: "Enable the OIDC provider for your cluster: eksctl utils associate-iam-oidc-provider --cluster <name> --approve",
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/enable-iam-roles-for-service-accounts.html",
		}}, map[string]string{"oidc_issuer": "none"}, nil
	}

	meta := map[string]string{"oidc_issuer": oidcIssuer}

	var findings []checker.Finding

	// Step 2: verify OIDC provider is registered in IAM.
	if f := checkOIDCRegistered(ctx, clients.iam, oidcIssuer, cfg); f != nil {
		findings = append(findings, *f)
		// If the provider is missing, trust policy checks are still useful
		// because the provider can be re-added without changing roles.
	}

	// Step 3: scan all ServiceAccounts for IRSA annotations and validate trust policies.
	saFindings, saCount, err := checkServiceAccounts(ctx, cfg, clients.iam, oidcIssuer)
	if err != nil {
		return findings, meta, fmt.Errorf("%s: scan service accounts: %w", Name, err)
	}
	meta["irsa_service_accounts"] = fmt.Sprintf("%d", saCount)
	findings = append(findings, saFindings...)

	return findings, meta, nil
}

func getOIDCIssuer(ctx context.Context, client iamEKSDescriber, clusterName string) (string, error) {
	out, err := client.DescribeCluster(ctx, &eksv2.DescribeClusterInput{Name: &clusterName})
	if err != nil {
		return "", err
	}
	if out.Cluster == nil || out.Cluster.Identity == nil ||
		out.Cluster.Identity.Oidc == nil || out.Cluster.Identity.Oidc.Issuer == nil {
		return "", nil
	}
	return *out.Cluster.Identity.Oidc.Issuer, nil
}

func checkOIDCRegistered(ctx context.Context, client iamDescriber, issuer string, cfg *checker.CheckConfig) *checker.Finding {
	out, err := client.ListOpenIDConnectProviders(ctx, &iamv2.ListOpenIDConnectProvidersInput{})
	if err != nil {
		return &checker.Finding{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityMedium,
			Blocker:     false,
			Title:       "Cannot verify OIDC provider registration (IAM ListOpenIDConnectProviders failed)",
			Description: err.Error(),
			Remediation: "Verify that the IAM credentials have iam:ListOpenIDConnectProviders permission.",
			Source:      Name,
			DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/enable-iam-roles-for-service-accounts.html",
		}
	}

	// The OIDC issuer URL is like: https://oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE
	// IAM provider ARNs look like: arn:aws:iam::123:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE
	// Strip https:// to compare.
	issuerHost := strings.TrimPrefix(issuer, "https://")
	for _, p := range out.OpenIDConnectProviderList {
		if p.Arn != nil && strings.HasSuffix(*p.Arn, issuerHost) {
			return nil // found
		}
	}

	return &checker.Finding{
		CheckerName: Name,
		ClusterType: cfg.ClusterType,
		Severity:    checker.SeverityCritical,
		Blocker:     true,
		Title:       "OIDC provider not registered in IAM for this cluster",
		Description: fmt.Sprintf("The cluster OIDC issuer %s is not listed in IAM OpenID Connect providers. All IRSA-based pods will lose AWS credentials after upgrade.", issuer),
		Remediation: "Re-associate the OIDC provider:\n  eksctl utils associate-iam-oidc-provider --cluster <name> --approve",
		Source:      Name,
		DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/enable-iam-roles-for-service-accounts.html",
	}
}

func checkServiceAccounts(ctx context.Context, cfg *checker.CheckConfig, client iamDescriber, oidcIssuer string) ([]checker.Finding, int, error) {
	// List ServiceAccounts across all namespaces.
	saList, err := cfg.KubeClient.CoreV1().ServiceAccounts("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, 0, err
	}

	issuerHost := strings.TrimPrefix(oidcIssuer, "https://")
	var findings []checker.Finding
	irsaCount := 0

	for _, sa := range saList.Items {
		roleARN, ok := sa.Annotations["eks.amazonaws.com/role-arn"]
		if !ok || roleARN == "" {
			continue
		}
		irsaCount++

		roleName := arnToRoleName(roleARN)
		if roleName == "" {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityMedium,
				Blocker:     false,
				Title:       fmt.Sprintf("ServiceAccount %s/%s has malformed role ARN", sa.Namespace, sa.Name),
				Description: fmt.Sprintf("eks.amazonaws.com/role-arn = %q is not a valid IAM role ARN.", roleARN),
				Remediation: "Correct the annotation to a valid ARN (arn:aws:iam::<account>:role/<name>).",
				Source:      Name,
				Resource:    &checker.Resource{Kind: "ServiceAccount", Namespace: sa.Namespace, Name: sa.Name},
				DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html",
			})
			continue
		}

		roleOut, err := client.GetRole(ctx, &iamv2.GetRoleInput{RoleName: &roleName})
		if err != nil {
			// Role not found or no permission — medium, non-blocker.
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityMedium,
				Blocker:     false,
				Title:       fmt.Sprintf("Cannot retrieve IAM role %s for ServiceAccount %s/%s", roleName, sa.Namespace, sa.Name),
				Description: fmt.Sprintf("GetRole failed: %v. Cannot verify that the trust policy references the correct OIDC provider.", err),
				Remediation: "Ensure the IAM credentials have iam:GetRole on this role.",
				Source:      Name,
				Resource:    &checker.Resource{Kind: "ServiceAccount", Namespace: sa.Namespace, Name: sa.Name},
				DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html",
			})
			continue
		}

		if roleOut.Role == nil || roleOut.Role.AssumeRolePolicyDocument == nil {
			continue
		}

		// Trust policy is URL-encoded JSON.
		doc, err := url.QueryUnescape(*roleOut.Role.AssumeRolePolicyDocument)
		if err != nil {
			continue
		}

		if !strings.Contains(doc, issuerHost) {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityHigh,
				Blocker:     false,
				Title:       fmt.Sprintf("IAM role %s trust policy does not reference the cluster OIDC provider", roleName),
				Description: fmt.Sprintf("ServiceAccount %s/%s references role %s, but its trust policy does not include %s. After upgrade, pods using this SA will receive 403 from AWS.", sa.Namespace, sa.Name, roleName, issuerHost),
				Remediation: fmt.Sprintf("Update the trust policy of role %s to include:\n  \"Federated\": \"arn:aws:iam::<account>:oidc-provider/%s\"", roleName, issuerHost),
				Source:      Name,
				Resource:    &checker.Resource{Kind: "ServiceAccount", Namespace: sa.Namespace, Name: sa.Name},
				DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html",
			})
		}
	}

	return findings, irsaCount, nil
}

// arnToRoleName extracts the role name from arn:aws:iam::<account>:role/<name>.
func arnToRoleName(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return ""
	}
	resource := parts[5]
	if !strings.HasPrefix(resource, "role/") {
		return ""
	}
	return strings.TrimPrefix(resource, "role/")
}

// trustPolicyDoc is used only for JSON unmarshalling in tests.
type trustPolicyDoc struct {
	Statement []struct {
		Principal struct {
			Federated string `json:"Federated"`
		} `json:"Principal"`
	} `json:"Statement"`
}

// Ensure json is used (avoids unused import if struct is only in test file).
var _ = json.Marshal
