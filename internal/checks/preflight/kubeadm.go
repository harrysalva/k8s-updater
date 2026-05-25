package preflight

import (
	"context"

	"upgrade-guardian/internal/checker"
)

// checkKubeadm validates a static kubeadm upgrade plan via SSH.
//
// Real execution of `kubeadm upgrade plan <version>` requires SSH access to a
// control-plane node, which the backend pod does not have in most deployments.
// This first cut returns an informational finding describing what to run
// manually; future iterations can wire up an SSH executor.
func checkKubeadm(_ context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	findings := []checker.Finding{
		{
			CheckerName: Name,
			Severity:    checker.SeverityInfo,
			Blocker:     false,
			Title:       "Run `kubeadm upgrade plan` on a control-plane node",
			Description: "kubeadm is the source of truth for upstream upgrades. The backend cannot invoke it remotely. Run the command yourself and review the output before proceeding.",
			Remediation: "ssh to a control-plane node and run:\n  sudo kubeadm upgrade plan " + cfg.TargetVersion + "\nLook for the line \"the table below shows the components that will be updated\". Address every warning.",
			Source:      "kubeadm",
			DocsURL:     "https://kubernetes.io/docs/tasks/administer-cluster/kubeadm/kubeadm-upgrade/",
		},
	}
	return findings, map[string]string{
		"platform":        "upstream",
		"insights_checked": "0",
		"errors":          "0",
		"warnings":        "0",
		"note":            "remote execution not implemented",
	}, nil
}
