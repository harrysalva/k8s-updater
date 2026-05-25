package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"upgrade-guardian/internal/checker"
)

// checkKubespray inspects the user-provided Kubespray inventory and group_vars
// to verify the configured target k8s version matches what the user wants to
// upgrade to. It cannot run the full `ansible-playbook upgrade-cluster.yml --check`
// because the backend pod typically lacks Ansible and SSH credentials.
func checkKubespray(_ context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	if cfg.KubesprayConfig == nil || cfg.KubesprayConfig.GroupVarsPath == "" {
		return []checker.Finding{
			{
				CheckerName: Name,
				Severity:    checker.SeverityInfo,
				Blocker:     false,
				Title:       "Kubespray inventory not configured",
				Description: "No Kubespray group_vars path was provided. Configure X-Kubespray-Inventory and X-Kubespray-GroupVars headers to enable inventory pre-flight checks.",
				Remediation: "Set X-Kubespray-GroupVars to the path of group_vars/k8s_cluster/ (or k8s-cluster/) on the backend.",
				Source:      "kubespray",
			},
		}, map[string]string{"platform": "kubespray", "skip_reason": "no group_vars path"}, nil
	}

	versionFile, declaredVersion, err := findKubeVersion(cfg.KubesprayConfig.GroupVarsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: read group_vars: %w", Name, err)
	}

	var findings []checker.Finding
	warnings := 0

	// Compare with target.
	normTarget := normalizeMinor(cfg.TargetVersion)
	normDeclared := normalizeMinor(declaredVersion)
	if normDeclared == "" {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityHigh,
			Blocker:     true,
			Title:       "kube_version not found in Kubespray group_vars",
			Description: "Could not locate kube_version inside the provided group_vars directory. Kubespray needs this set to the new target before running upgrade-cluster.yml.",
			Remediation: fmt.Sprintf("Edit group_vars/k8s_cluster/k8s-cluster.yml and set kube_version: v%s", normTarget),
			Source:      "kubespray",
		})
		warnings++
	} else if normDeclared != normTarget {
		findings = append(findings, checker.Finding{
			CheckerName: Name,
			Severity:    checker.SeverityCritical,
			Blocker:     true,
			Title:       fmt.Sprintf("kube_version mismatch: inventory=%s, target=%s", normDeclared, normTarget),
			Description: fmt.Sprintf("Inventory at %s declares kube_version=%s but the requested upgrade target is %s. Running Kubespray now would not upgrade to the expected version.", versionFile, normDeclared, normTarget),
			Remediation: fmt.Sprintf("Update %s: set kube_version to v%s, then re-run validation.", versionFile, normTarget),
			Source:      "kubespray",
		})
		warnings++
	}

	meta := map[string]string{
		"platform":         "kubespray",
		"group_vars_path":  cfg.KubesprayConfig.GroupVarsPath,
		"declared_version": declaredVersion,
		"warnings":         "0",
		"errors":           "0",
	}
	if warnings > 0 {
		meta["errors"] = fmt.Sprintf("%d", warnings)
	}
	return findings, meta, nil
}

// findKubeVersion scans .yml/.yaml files under groupVarsPath for the line
// `kube_version: ...` and returns the file path and value of the first match.
func findKubeVersion(groupVarsPath string) (string, string, error) {
	var foundFile, foundVal string
	walkErr := filepath.WalkDir(groupVarsPath, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".yml") && !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "#") {
				continue
			}
			if strings.HasPrefix(trim, "kube_version:") {
				val := strings.TrimSpace(strings.TrimPrefix(trim, "kube_version:"))
				val = strings.Trim(val, "\"'")
				foundFile = p
				foundVal = val
				return filepath.SkipAll
			}
		}
		return nil
	})
	return foundFile, foundVal, walkErr
}

// normalizeMinor returns "1.34" given "v1.34.0", "1.34", or "v1.34".
func normalizeMinor(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}
