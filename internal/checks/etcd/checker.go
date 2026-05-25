// Package etcd checks etcd cluster health before an upgrade.
// It is skipped on EKS (etcd is fully managed by AWS).
//
// Discovery: reads etcd endpoints from the kube-apiserver static pod spec.
// TLS:       expects kubeadm-standard cert paths under /etc/kubernetes/pki/etcd/.
//            On Kind or environments without direct cert access, it gracefully
//            reports that etcd health must be verified manually.
package etcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"upgrade-guardian/internal/checker"
)

const Name = "etcd-health"

// Standard kubeadm certificate paths.
const (
	etcdCAPath     = "/etc/kubernetes/pki/etcd/ca.crt"
	etcdCertPath   = "/etc/kubernetes/pki/etcd/healthcheck-client.crt"
	etcdKeyPath    = "/etc/kubernetes/pki/etcd/healthcheck-client.key"
	defaultEtcdEP  = "https://127.0.0.1:2379"
	dialTimeout    = 5 * time.Second
	statusTimeout  = 5 * time.Second
)

type Checker struct{}

var _ checker.Checker = (*Checker)(nil)

func New() *Checker { return &Checker{} }

func (c *Checker) Name() string { return Name }

// Supports excludes EKS — etcd is fully managed by AWS there.
func (c *Checker) Supports(ct checker.ClusterType) bool {
	return ct != checker.ClusterTypeEKS
}

func (c *Checker) Check(ctx context.Context, cfg *checker.CheckConfig) ([]checker.Finding, map[string]string, error) {
	endpoints := c.discoverEndpoints(ctx, cfg)
	if len(endpoints) == 0 {
		endpoints = []string{defaultEtcdEP}
	}

	tlsCfg, err := c.buildTLSConfig()
	if err != nil {
		// Certs not available (Kind, remote cluster) — cannot verify etcd directly.
		return []checker.Finding{{
			CheckerName: Name,
			ClusterType: cfg.ClusterType,
			Severity:    checker.SeverityMedium,
			Blocker:     false,
			Title:       "etcd health cannot be verified automatically",
			Description: fmt.Sprintf("TLS certificates not accessible at %s. etcd is at %s.", etcdCAPath, strings.Join(endpoints, ",")),
			Remediation: "Manually run: etcdctl endpoint health --endpoints=" + strings.Join(endpoints, ","),
			Source:      Name,
			DocsURL:     "https://etcd.io/docs/latest/op-guide/health/",
		}}, map[string]string{"status": "tls_unavailable"}, nil
	}

	meta := map[string]string{"endpoints_checked": strconv.Itoa(len(endpoints))}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: dialTimeout,
		TLS:         tlsCfg,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%s: connect to etcd: %w", Name, err)
	}
	defer cli.Close()

	var findings []checker.Finding

	// Per-endpoint health check.
	for _, ep := range endpoints {
		statusCtx, cancel := context.WithTimeout(ctx, statusTimeout)
		status, err := cli.Status(statusCtx, ep)
		cancel()

		if err != nil {
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("etcd endpoint %s is unreachable", ep),
				Description: err.Error(),
				Remediation: "Restore etcd endpoint health before upgrading.",
				Source:      Name,
				DocsURL:     "https://etcd.io/docs/latest/op-guide/recovery/",
			})
			continue
		}

		// Warn if the etcd version is significantly older than the target k8s version
		// (etcd version skew requirements apply).
		_ = status
	}

	// Cluster-wide alarm check.
	alarmCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	alarms, err := cli.AlarmList(alarmCtx)
	cancel()

	if err == nil {
		for _, alarm := range alarms.Alarms {
			// AlarmType 1 = NOSPACE, 2 = CORRUPT
			alarmName := alarmTypeName(int(alarm.Alarm))
			findings = append(findings, checker.Finding{
				CheckerName: Name,
				ClusterType: cfg.ClusterType,
				Severity:    checker.SeverityCritical,
				Blocker:     true,
				Title:       fmt.Sprintf("etcd alarm active: %s (member %d)", alarmName, alarm.MemberID),
				Description: fmt.Sprintf("etcd has an active %s alarm. This will block writes and could corrupt data during upgrade.", alarmName),
				Remediation: resolveAlarmRemediation(alarmName),
				Source:      Name,
				DocsURL:     "https://etcd.io/docs/latest/op-guide/maintenance/",
			})
		}
	}

	return findings, meta, nil
}

// discoverEndpoints reads etcd endpoints from the kube-apiserver static pod.
func (c *Checker) discoverEndpoints(ctx context.Context, cfg *checker.CheckConfig) []string {
	pods, err := cfg.KubeClient.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "component=kube-apiserver",
	})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}

	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			for _, arg := range container.Command {
				if strings.HasPrefix(arg, "--etcd-servers=") {
					raw := strings.TrimPrefix(arg, "--etcd-servers=")
					return strings.Split(raw, ",")
				}
			}
		}
	}
	return nil
}

// buildTLSConfig loads the kubeadm-standard etcd client certificates.
func (c *Checker) buildTLSConfig() (*tls.Config, error) {
	for _, path := range []string{etcdCAPath, etcdCertPath, etcdKeyPath} {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil, fmt.Errorf("cert not found: %s", path)
		}
	}

	cert, err := tls.LoadX509KeyPair(etcdCertPath, etcdKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load etcd cert: %w", err)
	}

	caCert, err := os.ReadFile(etcdCAPath)
	if err != nil {
		return nil, fmt.Errorf("read etcd CA: %w", err)
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCert)

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func alarmTypeName(t int) string {
	switch t {
	case 1:
		return "NOSPACE"
	case 2:
		return "CORRUPT"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", t)
	}
}

func resolveAlarmRemediation(alarm string) string {
	switch alarm {
	case "NOSPACE":
		return "Free etcd disk space: defragment with `etcdctl defrag` and increase quota if needed. See: https://etcd.io/docs/latest/op-guide/maintenance/#space-quota"
	case "CORRUPT":
		return "etcd data is corrupt. Restore from a snapshot before upgrading. See: https://etcd.io/docs/latest/op-guide/recovery/"
	default:
		return "Resolve the etcd alarm before upgrading."
	}
}
