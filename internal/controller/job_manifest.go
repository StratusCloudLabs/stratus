package controller

import (
	"fmt"
	"regexp"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Default constants mirroring the Node.js implementation.
const (
	DefaultRunnerNamespace                = "arc-runners"
	DefaultGHCRSecret                     = "ghcr-secret"
	DefaultActiveDeadlineSeconds          = 3600
	DefaultLongRunningActiveDeadlineSecs  = 7200
)

// RunnerProfile maps a label prefix to a runner image and optional arch override.
type RunnerProfile struct {
	Image string
	Arch  string // empty means inherit from the doc's arch
}

// ResourceProfile holds CPU and memory requests/limits for a runner container.
// When DinD is true, the profile also contains dind sub-resources.
type ResourceProfile struct {
	CPU    string
	Memory string
	DinD   *DinDResourceProfile `json:"dind,omitempty"`
}

// DinDResourceProfile holds resource requests for the DinD sidecar.
type DinDResourceProfile struct {
	CPU    string
	Memory string
}

// RunnerProfiles maps label prefixes to runner images.
var RunnerProfiles = map[string]RunnerProfile{
	"ci-nano":         {Image: "arc-runner-ci", Arch: "multi"},
	"ci-system":       {Image: "arc-runner-ci", Arch: "multi"},
	"ci-standard":     {Image: "arc-runner-ci"},
	"ci-medium":       {Image: "arc-runner-ci"},
	"ci-high":         {Image: "arc-runner-ci-high"},
	"agents-high":     {Image: "arc-runner-agents"},
	"agents-medium":   {Image: "arc-runner-agents"},
	"agents-standard": {Image: "arc-runner-agents"},
	"docker":          {Image: "arc-runner-base"},
	"security":        {Image: "arc-runner-security"},
}

// JITResourceProfiles maps label prefixes to resource allocations.
var JITResourceProfiles = map[string]ResourceProfile{
	"ci-nano":         {CPU: "250m", Memory: "256Mi"},
	"ci-system":       {CPU: "500m", Memory: "512Mi"},
	"ci-standard":     {CPU: "2", Memory: "3Gi"},
	"ci-medium":       {CPU: "4", Memory: "8Gi"},
	"ci-high":         {CPU: "8", Memory: "16Gi"},
	"agents-standard": {CPU: "2", Memory: "3Gi"},
	"agents-medium":   {CPU: "4", Memory: "8Gi"},
	"agents-high":     {CPU: "8", Memory: "16Gi"},
	"docker-standard": {CPU: "500m", Memory: "1Gi", DinD: &DinDResourceProfile{CPU: "3500m", Memory: "4608Mi"}},
	"docker-medium":   {CPU: "500m", Memory: "1Gi", DinD: &DinDResourceProfile{CPU: "3500m", Memory: "4608Mi"}},
	"docker-high":     {CPU: "500m", Memory: "1Gi", DinD: &DinDResourceProfile{CPU: "3500m", Memory: "4608Mi"}},
	"security":        {CPU: "2", Memory: "4Gi"},
}

// ActiveDeadlineProfiles maps label prefixes to extended active deadline values.
var ActiveDeadlineProfiles = map[string]int64{
	"docker":     DefaultLongRunningActiveDeadlineSecs,
	"ci-high":    DefaultLongRunningActiveDeadlineSecs,
	"agents-high": DefaultLongRunningActiveDeadlineSecs,
}

// GHCRBase is the container registry base URL.
var GHCRBase = "ghcr.io/stratuscloudlabs"

// nonAlphanumeric is used for label sanitization.
var nonAlphanumericStartEnd = regexp.MustCompile(`(^[^A-Za-z0-9]+)|([^A-Za-z0-9]+$)`)

// ResolveRunnerProfile resolves the runner profile from a set of labels.
// Returns the matching profile or a default ci-standard profile.
func ResolveRunnerProfile(labels []string) RunnerProfile {
	var arch string
	for _, label := range labels {
		if strings.Contains(label, "arm64") {
			arch = "arm64"
			break
		}
	}
	if arch == "" {
		arch = "amd64"
	}

	for _, label := range labels {
		for prefix, profile := range RunnerProfiles {
			if strings.HasPrefix(label, prefix) {
				resultArch := profile.Arch
				if resultArch == "" {
					resultArch = arch
				}
				return RunnerProfile{Image: profile.Image, Arch: resultArch}
			}
		}
	}
	return RunnerProfile{Image: "arc-runner-ci", Arch: arch}
}

// ResolveJobResources resolves the resource profile from labels.
func ResolveJobResources(labels []string) ResourceProfile {
	for _, label := range labels {
		for prefix, resources := range JITResourceProfiles {
			if strings.HasPrefix(label, prefix) {
				return resources
			}
		}
	}
	return JITResourceProfiles["ci-standard"]
}

// IsDinDLabel returns true if any label starts with "docker-" and contains "dind".
func IsDinDLabel(labels []string) bool {
	for _, label := range labels {
		if strings.HasPrefix(label, "docker-") && strings.Contains(label, "dind") {
			return true
		}
	}
	return false
}

// ResolveActiveDeadlineSeconds resolves the active deadline for the job.
func ResolveActiveDeadlineSeconds(labels []string, requestedDeadlineSeconds *int64) int64 {
	if requestedDeadlineSeconds != nil && *requestedDeadlineSeconds > 0 {
		if *requestedDeadlineSeconds < 900 {
			return 900
		}
		return *requestedDeadlineSeconds
	}

	for _, label := range labels {
		for prefix, deadline := range ActiveDeadlineProfiles {
			if strings.HasPrefix(label, prefix) {
				return deadline
			}
		}
	}
	return DefaultActiveDeadlineSeconds
}

// BuildTolerations returns the standard tolerations for runner pods.
func BuildTolerations() []corev1.Toleration {
	return []corev1.Toleration{
		{Key: "node.kubernetes.io/disk-pressure", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: "stratus.dev/dedicated", Operator: corev1.TolerationOpEqual, Value: "stratus-sandbox", Effect: corev1.TaintEffectNoSchedule},
		{Key: "stratus.dev/emulation", Operator: corev1.TolerationOpEqual, Value: "rosetta", Effect: corev1.TaintEffectNoSchedule},
	}
}

// SanitizeLabelValue sanitizes a string for use as a K8s label value.
func SanitizeLabelValue(raw string) string {
	if raw == "" {
		return ""
	}
	// Replace non-allowed chars with '-'
	result := nonAlphanumericReg.ReplaceAllString(raw, "-")
	// Collapse multiple dashes
	result = multiDashReg.ReplaceAllString(result, "-")
	// Truncate to 63 chars
	if len(result) > 63 {
		result = result[:63]
	}
	// Strip leading/trailing non-alphanumeric
	result = nonAlphanumericStartEnd.ReplaceAllString(result, "")
	return result
}

// SanitizeJobName sanitizes a string for use as a K8s Job name.
func SanitizeJobName(raw, fallback string) string {
	source := raw
	if source == "" {
		source = fallback
	}
	if source == "" {
		source = "stratus-jit"
	}
	result := strings.ToLower(source)
	result = nonAlphanumericReg.ReplaceAllString(result, "-")
	result = multiDashReg.ReplaceAllString(result, "-")
	if len(result) > 63 {
		result = result[:63]
	}
	result = nonAlphanumericStartEnd.ReplaceAllString(result, "")
	if result == "" {
		result = "stratus-jit"
	}
	return result
}

// RunnerDocument represents a pending runner document from Firestore.
type RunnerDocument struct {
	OrgName              string   `firestore:"orgName"`
	RepoFullName         string   `firestore:"repoFullName"`
	JobID                string   `firestore:"jobId"`
	RunID                string   `firestore:"runId"`
	RunnerName           string   `firestore:"runnerName"`
	EncodedJitConfig     string   `firestore:"encodedJitConfig"`
	Arch                 string   `firestore:"arch"`
	Labels               []string `firestore:"labels"`
	WorkflowName         string   `firestore:"workflowName"`
	WorkflowJobName      string   `firestore:"jobName"`
	ActiveDeadlineSeconds *int64  `firestore:"activeDeadlineSeconds"`
	InstallationID       string   `firestore:"installationId"`
	Status               string   `firestore:"status"`
}

// BuildJobManifest constructs a K8s BatchV1 Job object from a runner document.
func BuildJobManifest(doc *RunnerDocument, ghcrBase, namespace, ghcrSecret string) *batchv1.Job {
	profile := ResolveRunnerProfile(doc.Labels)
	resourceProfile := ResolveJobResources(doc.Labels)

	archLabel := profile.Arch
	if archLabel == "multi" {
		archLabel = "multi"
	} else if doc.Arch == "arm64" {
		archLabel = "arm64"
	} else {
		archLabel = "amd64"
	}

	var imageTag string
	if archLabel == "multi" {
		imageTag = fmt.Sprintf("%s/%s:latest", ghcrBase, profile.Image)
	} else {
		imageTag = fmt.Sprintf("%s/%s:latest-%s", ghcrBase, profile.Image, archLabel)
	}

	jobName := SanitizeJobName(doc.RunnerName, fmt.Sprintf("stratus-jit-%s", doc.JobID))

	// Metadata labels
	metaLabels := map[string]string{
		"stratus.dev/jit":     "true",
		"stratus.dev/org":     sanitizeLabelValue(doc.OrgName),
		"stratus.dev/job-id":  doc.JobID,
	}
	if wf := SanitizeLabelValue(doc.WorkflowName); wf != "" {
		metaLabels["stratus.dev/workflow"] = wf
	}
	if jn := SanitizeLabelValue(doc.WorkflowJobName); jn != "" {
		metaLabels["stratus.dev/job-name"] = jn
	}

	activeDeadline := ResolveActiveDeadlineSeconds(doc.Labels, doc.ActiveDeadlineSeconds)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels:    metaLabels,
			Annotations: map[string]string{
				"stratus.dev/repo":        doc.RepoFullName,
				"stratus.dev/run-id":      doc.RunID,
				"stratus.dev/runner-name": doc.RunnerName,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: ptrToInt32(300),
			ActiveDeadlineSeconds:   &activeDeadline,
			BackoffLimit:            ptrToInt32(0),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: metaLabels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:    corev1.RestartPolicyNever,
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: ghcrSecret}},
					Affinity:         buildAffinity(archLabel),
					Tolerations:      BuildTolerations(),
					DNSPolicy:        buildDNSPolicy(doc.Labels),
					Containers:       buildContainers(doc.Labels, imageTag, doc.EncodedJitConfig, &resourceProfile),
					Volumes:          buildVolumes(doc.Labels),
				},
			},
		},
	}

	return job
}

func buildAffinity(archLabel string) *corev1.Affinity {
	if archLabel == "multi" {
		return nil
	}
	return &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/arch",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{archLabel},
							},
						},
					},
				},
			},
		},
	}
}

func buildDNSPolicy(labels []string) corev1.DNSPolicy {
	if IsDinDLabel(labels) {
		return corev1.DNSDefault
	}
	return corev1.DNSClusterFirst
}

func buildContainers(labels []string, imageTag, encodedJitConfig string, resourceProfile *ResourceProfile) []corev1.Container {
	isDinD := IsDinDLabel(labels)
	scaleSetName := "stratus-jit"
	if len(labels) > 0 && labels[0] != "" {
		scaleSetName = labels[0]
	}

	runnerContainer := corev1.Container{
		Name:            "runner",
		Image:           imageTag,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/home/runner/run.sh", "--jitconfig", encodedJitConfig},
		Env: []corev1.EnvVar{
			{Name: "RUNNER_FEATURE_FLAG_EPHEMERAL", Value: "true"},
			{Name: "RUNNER_SCALE_SET_NAME", Value: scaleSetName},
			{
				Name: "NODE_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
				},
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits:   buildResourceList(resourceProfile.CPU, resourceProfile.Memory),
			Requests: buildResourceList(resourceProfile.CPU, resourceProfile.Memory),
		},
	}

	if isDinD {
		runnerContainer.Env = append(runnerContainer.Env,
			corev1.EnvVar{Name: "DOCKER_HOST", Value: "tcp://localhost:2376"},
			corev1.EnvVar{Name: "DOCKER_TLS_VERIFY", Value: "1"},
			corev1.EnvVar{Name: "DOCKER_CERT_PATH", Value: "/certs/client"},
		)
		runnerContainer.VolumeMounts = []corev1.VolumeMount{
			{Name: "docker-certs", MountPath: "/certs"},
			{Name: "host-cgroup", MountPath: "/host-cgroup", ReadOnly: true},
		}

		dindCPU := "3500m"
		dindMem := "4608Mi"
		if resourceProfile.DinD != nil {
			dindCPU = resourceProfile.DinD.CPU
			dindMem = resourceProfile.DinD.Memory
		}

		dindContainer := corev1.Container{
			Name:  "dind",
			Image: fmt.Sprintf("%s/docker:27.5-dind", GHCRBase),
			Args:  []string{"--storage-driver=overlay2", "--log-level=warn", "--dns=8.8.8.8"},
			Env: []corev1.EnvVar{
				{Name: "DOCKER_TLS_CERTDIR", Value: "/certs"},
				{Name: "DOCKER_BUILDKIT", Value: "1"},
			},
			Resources: corev1.ResourceRequirements{
				Limits:   buildResourceList(dindCPU, dindMem),
				Requests: buildResourceList(dindCPU, dindMem),
			},
			SecurityContext: &corev1.SecurityContext{Privileged: ptrBool(true)},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "docker-certs", MountPath: "/certs"},
				{Name: "docker-storage", MountPath: "/var/lib/docker"},
			},
		}

		return []corev1.Container{runnerContainer, dindContainer}
	}

	return []corev1.Container{runnerContainer}
}

func buildVolumes(labels []string) []corev1.Volume {
	if !IsDinDLabel(labels) {
		return nil
	}
	sizeLimit := resource.MustParse("50Gi")
	return []corev1.Volume{
		{Name: "docker-certs", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "docker-storage", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &sizeLimit}}},
		{Name: "host-cgroup", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/sys/fs/cgroup", Type: hostPathType(corev1.HostPathDirectory)}}},
	}
}

// Helper functions.

var nonAlphanumericReg = regexp.MustCompile(`[^A-Za-z0-9._-]`)
var multiDashReg = regexp.MustCompile(`-+`)

func sanitizeLabelValue(raw string) string {
	if raw == "" {
		return ""
	}
	result := nonAlphanumericReg.ReplaceAllString(raw, "-")
	result = multiDashReg.ReplaceAllString(result, "-")
	if len(result) > 63 {
		result = result[:63]
	}
	result = nonAlphanumericStartEnd.ReplaceAllString(result, "")
	return result
}

func buildResourceList(cpu, memory string) corev1.ResourceList {
	rl := make(corev1.ResourceList)
	if cpu != "" {
		rl[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		rl[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	return rl
}

func hostPathType(t corev1.HostPathType) *corev1.HostPathType {
	return &t
}

func ptrToInt32(i int32) *int32 {
	return &i
}

func ptrBool(b bool) *bool {
	return &b
}
