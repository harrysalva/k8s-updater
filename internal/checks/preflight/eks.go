package preflight

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	eksv2 "github.com/aws/aws-sdk-go-v2/service/eks"
	eksv2types "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"upgrade-guardian/internal/checker"
)

// checkEKSInsights uses the EKS Insights API to surface upgrade-blocking issues.
// Insights are AWS's own checks (deprecated APIs, addon versions, IAM, etc.) —
// running them via the API is the canonical "dry run" for EKS upgrades.
func checkEKSInsights(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	if cfg.EKSConfig == nil || cfg.EKSConfig.ClusterName == "" {
		return nil, map[string]string{
			"platform":    "eks",
			"skip_reason": "EKSConfig missing — provide X-AWS-Region header and ensure the kubeconfig context exposes the cluster name",
		}, nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.EKSConfig.Region))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: load AWS config: %w", Name, err)
	}
	client := eksv2.NewFromConfig(awsCfg)

	out, err := client.ListInsights(ctx, &eksv2.ListInsightsInput{
		ClusterName: &cfg.EKSConfig.ClusterName,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: ListInsights: %w", Name, err)
	}

	var findings []checker.Finding
	errCount, warnCount := 0, 0
	for _, summary := range out.Insights {
		switch summary.InsightStatus.Status {
		case eksv2types.InsightStatusValueError:
			errCount++
			findings = append(findings, eksInsightFinding(summary, checker.SeverityCritical, true))
		case eksv2types.InsightStatusValueWarning:
			warnCount++
			findings = append(findings, eksInsightFinding(summary, checker.SeverityHigh, false))
		}
	}

	return findings, metaWithCounts("eks", len(out.Insights), errCount, warnCount), nil
}

func eksInsightFinding(s eksv2types.InsightSummary, sev checker.Severity, blocker bool) checker.Finding {
	title := "EKS Insight: " + safeString(s.Name)
	desc := safeString(s.Description)
	if s.InsightStatus != nil && s.InsightStatus.Reason != nil {
		desc = *s.InsightStatus.Reason
	}
	return checker.Finding{
		CheckerName: Name,
		Severity:    sev,
		Blocker:     blocker,
		Title:       title,
		Description: desc,
		Remediation: "Open the EKS console → Upgrade insights for actionable remediation steps.",
		Source:      "eks-insights",
		DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/cluster-insights.html",
	}
}

func safeString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
