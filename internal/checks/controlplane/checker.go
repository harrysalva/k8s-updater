// Package controlplane checks control plane component health.
//
// For EKS clusters: uses AWS EKS Insights API (requires aws-sdk-go-v2/service/eks).
// For Upstream/Kubespray: checks /healthz, /readyz on kube-apiserver, controller-manager,
// scheduler, and validates certificate expiry via x509 parsing.
package controlplane

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

// TODO (EKS): after `go get github.com/aws/aws-sdk-go-v2/service/eks`:
//
//	import eksv2 "github.com/aws/aws-sdk-go-v2/service/eks"
//
//	eksClient := eksv2.NewFromConfig(awsCfg)
//	insights, _ := eksClient.ListInsights(ctx, &eksv2.ListInsightsInput{
//	    ClusterName: &cfg.EKSConfig.ClusterName,
//	    Filter:      &types.InsightsFilter{Categories: []types.InsightCategoryType{types.InsightCategoryTypeUpgradeReadiness}},
//	})
//	for _, insight := range insights.Insights {
//	    if insight.InsightStatus.Status == types.InsightStatusValueError {
//	        findings = append(findings, toFinding(insight, cfg.ClusterType))
//	    }
//	}

const Name = "control-plane"

// certWarnDays triggers a high-severity finding when a cert expires within this window.
const certWarnDays = 30

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	switch cfg.ClusterType {
	case checker.ClusterTypeEKS:
		return c.checkEKS(ctx, cfg)
	default:
		return c.checkUpstream(ctx, cfg)
	}
}

func (c *Checker) checkEKS(_ context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	// TODO: call EKS Insights API (see package doc above).
	_ = cfg
	return nil, nil, fmt.Errorf("%s (EKS): not yet implemented — run scripts/setup.sh first", Name)
}

func (c *Checker) checkUpstream(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	var findings []checker.Finding

	// Fetch API server version.
	apiVersion := "unknown"
	if sv, err := cfg.KubeClient.Discovery().ServerVersion(); err == nil {
		apiVersion = sv.GitVersion
	}

	// Check API server health endpoints.
	for _, endpoint := range []string{"/healthz", "/readyz", "/livez"} {
		if err := c.probeEndpoint(ctx, cfg, endpoint); err != nil {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("API server %s unhealthy", endpoint),
				Description: err.Error(),
				Remediation: "Investigate kube-apiserver logs before upgrading.",
				Source:      Name,
			})
		}
	}

	// Check component statuses (deprecated in newer k8s but still readable).
	cs, err := cfg.KubeClient.CoreV1().ComponentStatuses().List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, comp := range cs.Items {
			for _, cond := range comp.Conditions {
				if cond.Type == "Healthy" && cond.Status != "True" {
					findings = append(findings, checker.Finding{
						CheckerName: Name,
						ClusterType: cfg.ClusterType,
						Severity:    checker.SeverityCritical,
						Blocker:     true,
						Title:       fmt.Sprintf("Component %s is unhealthy", comp.Name),
						Description: cond.Message,
						Remediation: "Restore component health before upgrading.",
						Source:      Name,
					})
				}
			}
		}
	}

	// Certificate expiry check via TLS handshake.
	if expiry, cn, err := c.checkAPICertExpiry(cfg); err == nil {
		daysLeft := int(time.Until(expiry).Hours() / 24)
		if daysLeft < certWarnDays {
			sev := checker.SeverityHigh
			blocker := false
			if daysLeft <= 0 {
				sev = checker.SeverityCritical
				blocker = true
			}
			desc := fmt.Sprintf("Certificate CN=%s expires on %s (%d days remaining).", cn, expiry.Format("2006-01-02"), daysLeft)
			if daysLeft <= 0 {
				desc = fmt.Sprintf("Certificate CN=%s expired on %s. The cluster may be unreachable after upgrade.", cn, expiry.Format("2006-01-02"))
			}
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    sev,
				Blocker:     blocker,
				Title:       fmt.Sprintf("API server TLS cert expires in %d days (%s)", daysLeft, expiry.Format("2006-01-02")),
				Description: desc,
				Remediation: "Rotate certificates before upgrading: kubeadm certs renew all",
				Source:      Name,
				DocsURL:     "https://kubernetes.io/docs/tasks/administer-cluster/kubeadm/kubeadm-certs/",
			})
		}
	}

	meta := map[string]string{
		"api_version":       apiVersion,
		"endpoints_checked": "3",
	}
	return findings, meta, nil
}

func (c *Checker) probeEndpoint(_ context.Context, cfg *checker.CheckConfig, path string) error {
	host := cfg.RestConfig.Host
	url := host + path
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // health probe only
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Checker) checkAPICertExpiry(cfg *checker.CheckConfig) (time.Time, string, error) {
	host := cfg.RestConfig.Host
	conn, err := tls.Dial("tcp", stripScheme(host), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	if err != nil {
		return time.Time{}, "", err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, "", fmt.Errorf("no peer certificates")
	}
	return certs[0].NotAfter, certs[0].Subject.CommonName, nil
}

func stripScheme(host string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(host) > len(prefix) && host[:len(prefix)] == prefix {
			return host[len(prefix):]
		}
	}
	return host
}
