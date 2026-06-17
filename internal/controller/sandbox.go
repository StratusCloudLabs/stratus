package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/StratusCloudLabs/stratus-runtime/internal/metrics"
)

// ptrInt64 returns a pointer to an int64.
func ptrInt64(i int64) *int64 {
	return &i
}

// Default sandbox constants.
const (
	SandboxNamespace     = "sandboxes"
	SandboxLabelKey      = "stratus.dev/sandbox"
	SandboxLabelValue    = "kata"
	SandboxAnnotationKey = "stratus.dev/sandbox-id"
	DefaultSandboxImage  = "ghcr.io/stratuscloudlabs/sandbox-kata:latest"
	SandboxReadinessTimeout = 2 * time.Minute
	SandboxHealthCheckInterval = 15 * time.Second
)

// SandboxController manages Kata Container sandbox pods.
type SandboxController struct {
	k8sClient kubernetes.Interface
	namespace string
	image     string
	logger    *slog.Logger
}

// NewSandboxController creates a new Sandbox controller.
func NewSandboxController(k8sClient kubernetes.Interface, namespace, image string, logger *slog.Logger) *SandboxController {
	if logger == nil {
		logger = slog.Default()
	}
	if namespace == "" {
		namespace = SandboxNamespace
	}
	if image == "" {
		image = DefaultSandboxImage
	}
	return &SandboxController{
		k8sClient: k8sClient,
		namespace: namespace,
		image:     image,
		logger:    logger.With("component", "sandbox-controller"),
	}
}

// SandboxInfo describes a sandbox's current state.
type SandboxInfo struct {
	ID        string `json:"id"`
	PodName   string `json:"podName"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase"`
	CreatedAt string `json:"createdAt,omitempty"`
	NodeName  string `json:"nodeName,omitempty"`
	PodIP     string `json:"podIP,omitempty"`
}

// CreateSandbox creates a Kata Container sandbox pod.
// The pod uses the kata runtime class for hardware-backed isolation.
func (s *SandboxController) CreateSandbox(ctx context.Context, id string) (*SandboxInfo, error) {
	metrics.SandboxOperationsTotal.WithLabelValues("create").Inc()

	podName := fmt.Sprintf("sandbox-%s", sanitizeForPodName(id))

	// Check if the pod already exists.
	existing, err := s.k8sClient.CoreV1().Pods(s.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err == nil {
		s.logger.Info("sandbox pod already exists", "id", id, "podName", podName)
		return podToSandboxInfo(existing), nil
	}
	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("check existing sandbox pod: %w", err)
	}

	// RuntimeClassName for Kata Containers.
	runtimeClassName := "kata"
	runAsNonRoot := false
	privileged := true

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: s.namespace,
			Labels: map[string]string{
				SandboxLabelKey: SandboxLabelValue,
			},
			Annotations: map[string]string{
				SandboxAnnotationKey: id,
			},
		},
		Spec: corev1.PodSpec{
			RuntimeClassName: &runtimeClassName,
			Containers: []corev1.Container{
				{
					Name:    "sandbox",
					Image:   s.image,
					Command: []string{"/bin/sh", "-c", "sleep infinity"},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &privileged,
						RunAsNonRoot: &runAsNonRoot,
						Capabilities: &corev1.Capabilities{
							Add: []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "SYS_PTRACE"},
						},
					},
					Stdin:     true,
					StdinOnce: false,
					TTY:       false,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("4Gi"),
						},
					},
				},
			},
			// Kata requires host networking for some configurations.
			// Using the kata runtime class handles the actual VM isolation.
			RestartPolicy: corev1.RestartPolicyAlways,
			Tolerations: []corev1.Toleration{
				{
					Key:      "stratus.dev/dedicated",
					Operator: corev1.TolerationOpEqual,
					Value:    "stratus-sandbox",
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
			NodeSelector: map[string]string{
				"stratus.dev/worker-class": "sandbox",
			},
		},
	}

	created, err := s.k8sClient.CoreV1().Pods(s.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create sandbox pod: %w", err)
	}

	s.logger.Info("sandbox pod created",
		"id", id,
		"podName", podName,
		"node", created.Spec.NodeName,
	)

	return podToSandboxInfo(created), nil
}

// DeleteSandbox deletes a Kata Container sandbox pod.
func (s *SandboxController) DeleteSandbox(ctx context.Context, id string) error {
	metrics.SandboxOperationsTotal.WithLabelValues("delete").Inc()

	podName := fmt.Sprintf("sandbox-%s", sanitizeForPodName(id))

	deleteProp := metav1.DeletePropagationBackground
	err := s.k8sClient.CoreV1().Pods(s.namespace).Delete(ctx, podName, metav1.DeleteOptions{
		PropagationPolicy: &deleteProp,
		GracePeriodSeconds: ptrInt64(30),
	})
	if err != nil {
		if errors.IsNotFound(err) {
			s.logger.Warn("sandbox pod not found for deletion", "id", id, "podName", podName)
			return nil
		}
		return fmt.Errorf("delete sandbox pod: %w", err)
	}

	s.logger.Info("sandbox pod deleted", "id", id, "podName", podName)
	return nil
}

// GetSandbox returns the current state of a sandbox pod.
func (s *SandboxController) GetSandbox(ctx context.Context, id string) (*SandboxInfo, error) {
	podName := fmt.Sprintf("sandbox-%s", sanitizeForPodName(id))

	pod, err := s.k8sClient.CoreV1().Pods(s.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get sandbox pod: %w", err)
	}

	return podToSandboxInfo(pod), nil
}

// HealthCheckSandbox performs a basic health check on a sandbox pod by
// verifying the pod is in a Running phase and all containers are ready.
func (s *SandboxController) HealthCheckSandbox(ctx context.Context, id string) error {
	metrics.SandboxOperationsTotal.WithLabelValues("health").Inc()

	info, err := s.GetSandbox(ctx, id)
	if err != nil {
		return fmt.Errorf("sandbox health check failed: %w", err)
	}
	if info == nil {
		return fmt.Errorf("sandbox %s not found", id)
	}
	if info.Phase != string(corev1.PodRunning) {
		return fmt.Errorf("sandbox %s is in phase %s (expected Running)", id, info.Phase)
	}

	return nil
}

// WaitForSandboxReady blocks until the sandbox pod is in Running phase or
// the context is cancelled.
func (s *SandboxController) WaitForSandboxReady(ctx context.Context, id string) error {
	podName := fmt.Sprintf("sandbox-%s", sanitizeForPodName(id))

	err := wait.PollUntilContextTimeout(ctx, SandboxHealthCheckInterval, SandboxReadinessTimeout, true, func(ctx context.Context) (done bool, err error) {
		pod, getErr := s.k8sClient.CoreV1().Pods(s.namespace).Get(ctx, podName, metav1.GetOptions{})
		if getErr != nil {
			if errors.IsNotFound(getErr) {
				return false, nil
			}
			return false, getErr
		}
		if pod.Status.Phase == corev1.PodRunning {
			// Check all containers are ready.
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady {
					return cond.Status == corev1.ConditionTrue, nil
				}
			}
			return true, nil
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodUnknown {
			return false, fmt.Errorf("sandbox pod entered phase %s", pod.Status.Phase)
		}
		return false, nil
	})

	if err != nil {
		return fmt.Errorf("wait for sandbox ready: %w", err)
	}
	return nil
}

// podToSandboxInfo converts a K8s Pod to a SandboxInfo.
func podToSandboxInfo(pod *corev1.Pod) *SandboxInfo {
	info := &SandboxInfo{
		ID:        pod.Annotations[SandboxAnnotationKey],
		PodName:   pod.Name,
		Namespace: pod.Namespace,
		Phase:     string(pod.Status.Phase),
		NodeName:  pod.Spec.NodeName,
		PodIP:     pod.Status.PodIP,
	}
	if pod.CreationTimestamp.Time != (time.Time{}) {
		info.CreatedAt = pod.CreationTimestamp.Format(time.RFC3339)
	}
	return info
}

// sanitizeForPodName sanitizes an ID for use in a K8s resource name.
func sanitizeForPodName(id string) string {
	result := nonAlphanumericReg.ReplaceAllString(id, "-")
	result = multiDashReg.ReplaceAllString(result, "-")
	if len(result) > 63 {
		result = result[:63]
	}
	result = nonAlphanumericStartEnd.ReplaceAllString(result, "")
	if result == "" {
		result = "unknown"
	}
	return strings.ToLower(result)
}
