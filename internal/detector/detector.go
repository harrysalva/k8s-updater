package detector

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"upgrade-guardian/internal/checker"
)

// Detect inspects the live cluster and returns its type.
// Order: EKS → Kubespray → Upstream (upstream is the safe default).
func Detect(ctx context.Context, client kubernetes.Interface) (checker.ClusterType, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 3})
	if err != nil {
		return checker.ClusterTypeUnknown, fmt.Errorf("list nodes: %w", err)
	}

	for _, node := range nodes.Items {
		labels := node.Labels
		// EKS managed nodes always carry this label.
		if _, ok := labels["eks.amazonaws.com/nodegroup"]; ok {
			return checker.ClusterTypeEKS, nil
		}
		// EKS self-managed nodes on EC2 carry the instance type label.
		if _, ok := labels["node.kubernetes.io/instance-type"]; ok {
			if isAWSInstanceType(labels["node.kubernetes.io/instance-type"]) {
				if isEKSCluster(ctx, client) {
					return checker.ClusterTypeEKS, nil
				}
			}
		}
		// Kubespray stamps nodes with this label during provisioning.
		if _, ok := labels["kubespray.kubernetes.io/managed"]; ok {
			return checker.ClusterTypeKubespray, nil
		}
	}

	// Secondary EKS signal: the aws-auth ConfigMap in kube-system.
	if _, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "aws-auth", metav1.GetOptions{}); err == nil {
		return checker.ClusterTypeEKS, nil
	}

	// Secondary Kubespray signal: the kubespray DaemonSet.
	if _, err := client.AppsV1().DaemonSets("kube-system").Get(ctx, "node-local-dns", metav1.GetOptions{}); err == nil {
		dsList, _ := client.AppsV1().DaemonSets("kube-system").List(ctx, metav1.ListOptions{
			LabelSelector: "app=kubespray-nodelocaldns",
		})
		if dsList != nil && len(dsList.Items) > 0 {
			return checker.ClusterTypeKubespray, nil
		}
	}

	return checker.ClusterTypeUpstream, nil
}

func isEKSCluster(ctx context.Context, client kubernetes.Interface) bool {
	cm, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, "aws-auth", metav1.GetOptions{})
	return err == nil && cm != nil
}

// isAWSInstanceType checks common EC2 prefixes (m5, c5, r5, t3, etc.).
func isAWSInstanceType(instanceType string) bool {
	prefixes := []string{"m5", "m6", "c5", "c6", "r5", "r6", "t3", "t4", "p3", "p4", "g4", "g5", "inf"}
	for _, p := range prefixes {
		if len(instanceType) >= len(p) && instanceType[:len(p)] == p {
			return true
		}
	}
	return false
}
