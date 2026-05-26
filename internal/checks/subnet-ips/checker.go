// Package subnetips checks that the subnets used by EKS worker nodes have
// enough free IPv4 addresses to sustain a rolling upgrade (drain + replace
// each node). IP exhaustion prevents new nodes from joining the cluster,
// stalling the upgrade indefinitely.
//
// When prefix delegation (ENABLE_PREFIX_DELEGATION) is active, each node
// reserves one or more /28 blocks (16 IPs per block). The checker then uses
// stricter thresholds because /28 blocks require contiguous address space and
// the addressable capacity per subnet is effectively reduced.
//
// SOURCE OF TRUTH:
//   https://docs.aws.amazon.com/vpc/latest/userguide/subnet-sizing.html
//   https://docs.aws.amazon.com/eks/latest/userguide/cni-prefix-delegation.html
//
// LAST VERIFIED: 2026-05-25
package subnetips

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	ec2v2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "subnet-ip-availability"

// MatrixLastVerified is not a compatibility matrix, but we export the date for
// consistency with the staleness detection system.
var MatrixLastVerified = matrixDate{} // no matrix; field unused by staleness engine

// thresholds (percentage of total usable IPs remaining).
const (
	thresholdCritical = 5  // < 5%  → critical/blocker
	thresholdHigh     = 10 // < 10% → high

	// With prefix delegation, each /28 block consumes 16 IPs contiguously.
	// Effective addressable space per node is (available / 16) prefix slots,
	// so stricter thresholds apply.
	thresholdCriticalPD = 10
	thresholdHighPD     = 20
)

// ec2Describer is the minimal EC2 API surface this checker needs.
// Defined as interface so tests can inject a mock without real AWS credentials.
type ec2Describer interface {
	DescribeSubnets(ctx context.Context, in *ec2v2.DescribeSubnetsInput, optFns ...func(*ec2v2.Options)) (*ec2v2.DescribeSubnetsOutput, error)
}

type Checker struct {
	// newEC2 is overrideable in tests.
	newEC2 func(ctx context.Context, region string) (ec2Describer, error)
}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker {
	return &Checker{
		newEC2: func(ctx context.Context, region string) (ec2Describer, error) {
			awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
			if err != nil {
				return nil, err
			}
			return ec2v2.NewFromConfig(awsCfg), nil
		},
	}
}

func (c *Checker) Name() string { return Name }

func (c *Checker) Supports(ct checker.ClusterType) bool {
	return ct == checker.ClusterTypeEKS
}

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	if cfg.EKSConfig == nil || cfg.EKSConfig.Region == "" {
		return nil, map[string]string{"skip_reason": "EKSConfig missing"}, nil
	}

	// Collect subnet IDs from node annotations.
	subnetIDs, err := collectSubnetIDs(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: list nodes: %w", Name, err)
	}
	if len(subnetIDs) == 0 {
		return nil, map[string]string{"skip_reason": "no node subnet annotations found"}, nil
	}

	// Detect prefix delegation.
	prefixDelegation := detectPrefixDelegation(ctx, cfg)

	// Query EC2 for subnet details.
	ec2Client, err := c.newEC2(ctx, cfg.EKSConfig.Region)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: create EC2 client: %w", Name, err)
	}

	ids := keys(subnetIDs)
	out, err := ec2Client.DescribeSubnets(ctx, &ec2v2.DescribeSubnetsInput{SubnetIds: ids})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: DescribeSubnets: %w", Name, err)
	}

	meta := map[string]string{
		"subnets_checked":    strconv.Itoa(len(out.Subnets)),
		"prefix_delegation":  strconv.FormatBool(prefixDelegation),
	}

	var findings []checker.Finding
	for _, s := range out.Subnets {
		f := analyzeSubnet(cfg, s, prefixDelegation)
		if f != nil {
			findings = append(findings, *f)
		}
	}
	return findings, meta, nil
}

func analyzeSubnet(cfg *checker.CheckConfig, s ec2types.Subnet, prefixDelegation bool) *checker.Finding {
	if s.CidrBlock == nil || s.AvailableIpAddressCount == nil {
		return nil
	}

	total := usableIPCount(*s.CidrBlock)
	available := int(*s.AvailableIpAddressCount)
	if total == 0 {
		return nil
	}

	pct := available * 100 / total

	critThresh, highThresh := thresholdCritical, thresholdHigh
	if prefixDelegation {
		critThresh, highThresh = thresholdCriticalPD, thresholdHighPD
	}

	subnetID := safeStr(s.SubnetId)
	az := safeStr(s.AvailabilityZone)
	cidr := safeStr(s.CidrBlock)

	var sev checker.Severity
	var blocker bool
	var title, desc, remediation string

	switch {
	case pct < critThresh:
		sev = checker.SeverityCritical
		blocker = true
		title = fmt.Sprintf("Subnet %s (%s) has only %d%% IPs free (%d/%d)", subnetID, az, pct, available, total)
		desc = fmt.Sprintf("Subnet %s in %s (%s) has %d available IPs out of %d usable (%d%%). During upgrade, draining and replacing nodes requires free IPs for new node ENIs. Below %d%% the upgrade may stall.", subnetID, az, cidr, available, total, pct, critThresh)
		if prefixDelegation {
			desc += " Prefix delegation is active — effective slot count is further reduced."
		}
		remediation = "Add secondary CIDR blocks to the VPC or move workloads to less-saturated subnets before upgrading."
	case pct < highThresh:
		sev = checker.SeverityHigh
		blocker = false
		title = fmt.Sprintf("Subnet %s (%s) is at %d%% IP capacity (%d/%d free)", subnetID, az, pct, available, total)
		desc = fmt.Sprintf("Subnet %s in %s (%s) has %d available IPs out of %d usable (%d%%). This may be insufficient for a rolling node upgrade.", subnetID, az, cidr, available, total, pct)
		remediation = "Consider freeing IPs or adding secondary CIDRs before upgrading."
	default:
		return nil
	}

	return &checker.Finding{
		CheckerName: Name,
		ClusterType: cfg.ClusterType,
		Severity:    sev,
		Blocker:     blocker,
		Title:       title,
		Description: desc,
		Remediation: remediation,
		Source:      Name,
		DocsURL:     "https://docs.aws.amazon.com/eks/latest/userguide/cni-prefix-delegation.html",
	}
}

// collectSubnetIDs reads the subnet ID from each node's annotation and returns
// a deduplicated set of subnet IDs used by the cluster.
func collectSubnetIDs(ctx context.Context, cfg *checker.CheckConfig) (map[string]struct{}, error) {
	nodes, err := cfg.KubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{})
	for _, n := range nodes.Items {
		if id, ok := n.Annotations["vpc.amazonaws.com/node-subnet-id"]; ok && id != "" {
			ids[id] = struct{}{}
		}
		// Fallback: some older node configurations use a different annotation.
		if id, ok := n.Labels["topology.kubernetes.io/zone"]; ok && id != "" {
			_ = id // zone label doesn't give subnet ID; annotation is authoritative
		}
	}
	return ids, nil
}

// detectPrefixDelegation reads the amazon-vpc-cni ConfigMap to check if
// ENABLE_PREFIX_DELEGATION is set to "true".
func detectPrefixDelegation(ctx context.Context, cfg *checker.CheckConfig) bool {
	cm, err := cfg.KubeClient.CoreV1().ConfigMaps("kube-system").Get(ctx, "amazon-vpc-cni", metav1.GetOptions{})
	if err != nil {
		return false
	}
	return strings.EqualFold(cm.Data["ENABLE_PREFIX_DELEGATION"], "true")
}

// usableIPCount returns the number of usable IPv4 addresses in a CIDR block.
// AWS reserves 5 addresses per subnet (network, router, DNS, future, broadcast).
func usableIPCount(cidr string) int {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 {
		return 0
	}
	total := int(math.Pow(2, float64(bits-ones)))
	if total <= 5 {
		return 0
	}
	return total - 5
}

// cidrToUint32 converts the network address of a CIDR to uint32 (for display).
func cidrToUint32(cidr string) uint32 {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0
	}
	return binary.BigEndian.Uint32(ipNet.IP.To4())
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func safeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// matrixDate is a sentinel type so the package compiles without exporting an
// unused variable that would confuse the staleness engine.
type matrixDate struct{}

// Ensure cidrToUint32 is used (avoids unused import of encoding/binary).
var _ = cidrToUint32
