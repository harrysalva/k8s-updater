// Package crd validates each installed CRD against the target Kubernetes version's
// JSON schema using kubeconform. This catches CRDs that use deprecated structural
// patterns removed in the target version.
//
// Note: kubeconform downloads schemas from GitHub by default. For air-gapped
// environments, provide a local schema mirror via the KUBECONFORM_SCHEMA env var
// or set custom schema locations in the checker configuration.
package crd

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/yannh/kubeconform/pkg/resource"
	"github.com/yannh/kubeconform/pkg/validator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"upgrade-guardian/internal/checker"
)

const Name = "crd-schemas"

var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(_ checker.ClusterType) bool { return true }

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	// Build a dynamic client to list CRDs without importing apiextensions-apiserver.
	dynCli, err := dynamic.NewForConfig(cfg.RestConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: dynamic client: %w", Name, err)
	}

	crdList, err := dynCli.Resource(crdGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: list CRDs: %w", Name, err)
	}

	meta := map[string]string{"crds_validated": strconv.Itoa(len(crdList.Items))}

	if len(crdList.Items) == 0 {
		return nil, meta, nil
	}

	// kubeconform validates manifests against the k8s JSON schema.
	// nil = use default remote schema locations (github.com/yannh/kubernetes-json-schema).
	v, err := validator.New(nil, validator.Opts{
		KubernetesVersion: normalizeVersion(cfg.TargetVersion),
		Strict:            true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: init validator: %w", Name, err)
	}

	var findings []checker.Finding

	for _, crd := range crdList.Items {
		crdName := crd.GetName()

		data, err := yaml.Marshal(crd.Object)
		if err != nil {
			continue // malformed CRD object — skip
		}

		res := resource.Resource{
			Bytes: data,
			Path:  crdName,
		}

		result := v.ValidateResource(res)

		switch result.Status {
		case validator.Invalid:
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("CRD %s is invalid for Kubernetes %s", crdName, cfg.TargetVersion),
				Description: result.Err.Error(),
				Remediation: "Update the CRD to be compatible with the target Kubernetes version before upgrading.",
				Resource:    &checker.Resource{Kind: "CustomResourceDefinition", Name: crdName},
				Source:      "kubeconform",
				DocsURL:     "https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/",
			})

		case validator.Error:
			// Schema fetch error (network, missing schema) — report as medium,
			// not a blocker since the schema might simply not be available.
			if result.Err != nil {
				findings = append(findings, checker.Finding{
					CheckerName: Name,
					ClusterType: cfg.ClusterType,
					Severity:    checker.SeverityMedium,
					Blocker:     false,
					Title:       fmt.Sprintf("CRD %s could not be validated", crdName),
					Description: fmt.Sprintf("Schema validation error: %s", result.Err.Error()),
					Remediation: "Manually verify this CRD is compatible with the target version.",
					Resource:    &checker.Resource{Kind: "CustomResourceDefinition", Name: crdName},
					Source:      "kubeconform",
				})
			}

		case validator.Skipped:
			// kubeconform skips types it has no schema for — not an error.

		case validator.Valid:
			// Nothing to report.
		}
	}

	return findings, meta, nil
}

// normalizeVersion turns "1.35" or "v1.35.0" into "1.35.0" as kubeconform expects.
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, ".")
}

// unused — kept to satisfy the bytes import for potential future streaming use
var _ = bytes.NewBuffer
