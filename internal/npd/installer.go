// Package npd manages the node-problem-detector DaemonSet lifecycle.
// Install() is idempotent — safe to call even if NPD is already deployed.
package npd

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const (
	// Image is the official NPD image from the Kubernetes project.
	Image     = "registry.k8s.io/node-problem-detector/node-problem-detector:v0.8.19"
	Name      = "node-problem-detector"
	Namespace = "kube-system"
)

// InstallResult reports what actions were taken.
type InstallResult struct {
	AlreadyInstalled bool   `json:"already_installed"`
	Message          string `json:"message"`
}

// Install creates the ServiceAccount, RBAC, and DaemonSet for NPD.
// All operations are idempotent: existing resources are left unchanged.
func Install(ctx context.Context, client kubernetes.Interface) (*InstallResult, error) {
	if deployed(ctx, client) {
		return &InstallResult{
			AlreadyInstalled: true,
			Message:          "node-problem-detector is already deployed",
		}, nil
	}

	if err := ensureServiceAccount(ctx, client); err != nil {
		return nil, fmt.Errorf("service account: %w", err)
	}
	if err := ensureClusterRole(ctx, client); err != nil {
		return nil, fmt.Errorf("cluster role: %w", err)
	}
	if err := ensureClusterRoleBinding(ctx, client); err != nil {
		return nil, fmt.Errorf("cluster role binding: %w", err)
	}
	if err := ensureDaemonSet(ctx, client); err != nil {
		return nil, fmt.Errorf("daemonset: %w", err)
	}

	return &InstallResult{
		AlreadyInstalled: false,
		Message:          "node-problem-detector installed successfully in kube-system",
	}, nil
}

func deployed(ctx context.Context, client kubernetes.Interface) bool {
	_, err := client.AppsV1().DaemonSets(Namespace).Get(ctx, Name, metav1.GetOptions{})
	return err == nil
}

func ensureServiceAccount(ctx context.Context, client kubernetes.Interface) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: Name, Namespace: Namespace},
	}
	_, err := client.CoreV1().ServiceAccounts(Namespace).Create(ctx, sa, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func ensureClusterRole(ctx context.Context, client kubernetes.Interface) error {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: Name},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"create", "patch", "update"},
			},
		},
	}
	_, err := client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func ensureClusterRoleBinding(ctx context.Context, client kubernetes.Interface) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: Name},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     Name,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      Name,
			Namespace: Namespace,
		}},
	}
	_, err := client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func ensureDaemonSet(ctx context.Context, client kubernetes.Interface) error {
	privileged := true
	hostPathType := corev1.HostPathType("")
	maxUnavailable := intstr.FromInt(1)

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name,
			Namespace: Namespace,
			Labels:    map[string]string{"app": Name},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": Name},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": Name},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: Name,
					HostNetwork:        true,
					HostPID:            true,
					// Run on all nodes including control-plane.
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
						{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
					},
					Containers: []corev1.Container{{
						Name:            Name,
						Image:           Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						SecurityContext: &corev1.SecurityContext{
							Privileged: &privileged,
						},
						Env: []corev1.EnvVar{{
							Name: "NODE_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
							},
						}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "log", MountPath: "/var/log", ReadOnly: true},
							{Name: "kmsg", MountPath: "/dev/kmsg", ReadOnly: true},
							{Name: "localtime", MountPath: "/etc/localtime", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "log", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/var/log", Type: &hostPathType},
						}},
						{Name: "kmsg", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/dev/kmsg", Type: &hostPathType},
						}},
						{Name: "localtime", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/etc/localtime", Type: &hostPathType},
						}},
					},
				},
			},
		},
	}

	_, err := client.AppsV1().DaemonSets(Namespace).Create(ctx, ds, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
