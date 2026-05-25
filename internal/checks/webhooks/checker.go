// Package webhooks validates that admission webhooks (Validating & Mutating)
// are reachable and have valid certificates. Broken webhooks with
// failurePolicy=Fail are a top cause of stuck upgrades — the apiserver
// can't create the new system pods if the mutator can't be reached.
package webhooks

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"strconv"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "webhooks-health"

// caExpiryWarnDays is how many days before CA expiry we start flagging as critical.
const caExpiryWarnDays = 30

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	var findings []checker.Finding

	validating, vCAExpiring, vUnreachable := c.checkValidating(ctx, cfg, &findings)
	mutating, mCAExpiring, mUnreachable := c.checkMutating(ctx, cfg, &findings)

	meta := map[string]string{
		"validating":       strconv.Itoa(validating),
		"mutating":         strconv.Itoa(mutating),
		"ca_expiring_soon": strconv.Itoa(vCAExpiring + mCAExpiring),
		"unreachable":      strconv.Itoa(vUnreachable + mUnreachable),
	}
	return findings, meta, nil
}

func (c *Checker) checkValidating(ctx context.Context, cfg *checker.CheckConfig, findings *[]checker.Finding) (count, caExpiring, unreachable int) {
	list, err := cfg.KubeClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, 0
	}
	count = len(list.Items)
	for _, cfgItem := range list.Items {
		for _, wh := range cfgItem.Webhooks {
			f := analyzeWebhook(ctx, cfg, "ValidatingWebhookConfiguration", cfgItem.Name, wh.Name,
				wh.ClientConfig, wh.FailurePolicy, wh.TimeoutSeconds)
			if f.caExpiring {
				caExpiring++
			}
			if f.unreachable {
				unreachable++
			}
			*findings = append(*findings, f.findings...)
		}
	}
	return count, caExpiring, unreachable
}

func (c *Checker) checkMutating(ctx context.Context, cfg *checker.CheckConfig, findings *[]checker.Finding) (count, caExpiring, unreachable int) {
	list, err := cfg.KubeClient.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, 0
	}
	count = len(list.Items)
	for _, cfgItem := range list.Items {
		for _, wh := range cfgItem.Webhooks {
			f := analyzeWebhook(ctx, cfg, "MutatingWebhookConfiguration", cfgItem.Name, wh.Name,
				wh.ClientConfig, wh.FailurePolicy, wh.TimeoutSeconds)
			if f.caExpiring {
				caExpiring++
			}
			if f.unreachable {
				unreachable++
			}
			*findings = append(*findings, f.findings...)
		}
	}
	return count, caExpiring, unreachable
}

type webhookCheck struct {
	findings    []checker.Finding
	caExpiring  bool
	unreachable bool
}

// analyzeWebhook inspects one webhook entry within a Configuration and returns
// findings + flags for meta accounting.
func analyzeWebhook(
	ctx context.Context,
	cfg *checker.CheckConfig,
	kind, configName, webhookName string,
	cc admissionv1.WebhookClientConfig,
	failurePolicy *admissionv1.FailurePolicyType,
	timeoutSeconds *int32,
) webhookCheck {
	res := webhookCheck{}

	fail := failurePolicy != nil && *failurePolicy == admissionv1.Fail
	resource := &checker.Resource{Kind: kind, Name: configName, APIGroup: "admissionregistration.k8s.io/v1"}

	// 1. CA bundle validation.
	if len(cc.CABundle) > 0 {
		expiry, parseErr := parseCABundleExpiry(cc.CABundle)
		if parseErr == nil {
			daysLeft := int(time.Until(expiry).Hours() / 24)
			if daysLeft < 0 {
				res.caExpiring = true
				res.findings = append(res.findings, checker.Finding{
					CheckerName: Name,
					Severity:    checker.SeverityCritical,
					Blocker:     true,
					Title:       fmt.Sprintf("%s: webhook %s CA expired %d days ago", configName, webhookName, -daysLeft),
					Description: fmt.Sprintf("CA bundle expired on %s. Apiserver rejects this webhook's TLS handshake, which can block upgrades.", expiry.Format("2006-01-02")),
					Remediation: "Re-issue the webhook CA bundle and patch the webhook configuration.",
					Resource:    resource,
					Source:      "webhooks",
				})
			} else if daysLeft < caExpiryWarnDays {
				res.caExpiring = true
				sev := checker.SeverityHigh
				if daysLeft < 7 {
					sev = checker.SeverityCritical
				}
				res.findings = append(res.findings, checker.Finding{
					CheckerName: Name,
					Severity:    sev,
					Blocker:     daysLeft < 7,
					Title:       fmt.Sprintf("%s: webhook %s CA expires in %d days", configName, webhookName, daysLeft),
					Description: fmt.Sprintf("CA bundle expires on %s. After expiry the apiserver will reject this webhook.", expiry.Format("2006-01-02")),
					Remediation: "Rotate the webhook CA before the upgrade window.",
					Resource:    resource,
					Source:      "webhooks",
				})
			}
		}
	}

	// 2. Reachability — only for Service-backed webhooks. URL-backed ones are typically external.
	if cc.Service != nil {
		reachable, reason := serviceReachable(ctx, cfg, cc.Service)
		if !reachable {
			res.unreachable = true
			sev := checker.SeverityHigh
			blocker := false
			if fail {
				sev = checker.SeverityCritical
				blocker = true
			}
			res.findings = append(res.findings, checker.Finding{
				CheckerName: Name,
				Severity:    sev,
				Blocker:     blocker,
				Title:       fmt.Sprintf("%s: webhook %s service %s/%s unreachable", configName, webhookName, cc.Service.Namespace, cc.Service.Name),
				Description: fmt.Sprintf("Backing service is not reachable (%s). failurePolicy=%s.", reason, failurePolicyStr(failurePolicy)),
				Remediation: "Verify the backing Deployment is running and the Service selector matches its pods. Consider setting failurePolicy=Ignore if this webhook is non-critical.",
				Resource:    resource,
				Source:      "webhooks",
			})
		}
	}

	// 3. Generous timeout — upgrade rolls may stall.
	if timeoutSeconds != nil && *timeoutSeconds > 10 {
		res.findings = append(res.findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityMedium,
			Blocker:     false,
			Title:       fmt.Sprintf("%s: webhook %s has timeoutSeconds=%d", configName, webhookName, *timeoutSeconds),
			Description: "Slow webhooks compound during a rolling upgrade. Each blocked admission delays node drain and pod scheduling.",
			Remediation: "Reduce timeoutSeconds to <=10 (Kubernetes default is 10) once the webhook is verified fast.",
			Resource:    resource,
			Source:      "webhooks",
		})
	}

	return res
}

func failurePolicyStr(p *admissionv1.FailurePolicyType) string {
	if p == nil {
		return "Fail"
	}
	return string(*p)
}

// parseCABundleExpiry parses the first cert in a PEM bundle and returns NotAfter.
func parseCABundleExpiry(caBundle []byte) (time.Time, error) {
	block, _ := pem.Decode(caBundle)
	if block == nil {
		// Some tools store the bundle DER-encoded directly, no PEM wrapping.
		cert, err := x509.ParseCertificate(caBundle)
		if err != nil {
			return time.Time{}, fmt.Errorf("decode CA bundle: %w", err)
		}
		return cert.NotAfter, nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

// serviceReachable confirms the backing service exists and has at least one ready endpoint.
// Reaching the actual port over TLS would require apiserver-equivalent network access,
// which we don't have from the backend pod. Endpoint existence is a strong proxy.
func serviceReachable(ctx context.Context, cfg *checker.CheckConfig, ref *admissionv1.ServiceReference) (bool, string) {
	svc, err := cfg.KubeClient.CoreV1().Services(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, "service not found"
	}
	if err != nil {
		return false, fmt.Sprintf("service lookup error: %v", err)
	}
	_ = svc // existence check only

	// EndpointSlices is the modern API. Fall back to legacy Endpoints if needed.
	eps, err := cfg.KubeClient.CoreV1().Endpoints(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return false, "endpoints lookup error"
	}
	for _, subset := range eps.Subsets {
		if len(subset.Addresses) > 0 {
			return true, ""
		}
	}
	return false, "no ready endpoints"
}

// Reference imports to ensure net.Dial type stays available for future TCP probe.
var _ = net.Dial
