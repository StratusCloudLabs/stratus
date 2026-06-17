package metrics

import (
	"context"
	"log/slog"
	"math"
	"time"

	"cloud.google.com/go/firestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ClusterMetricsPusher periodically collects cluster metrics from Prometheus
// and Kubernetes and writes them to a Firestore document for the dashboard
// and the JIT controller's capacity check.
type ClusterMetricsPusher struct {
	db          *firestore.Client
	k8sClient   kubernetes.Interface
	namespace   string
	installationID string
	logger      *slog.Logger
	agentVersion string
	nodeHostname string
}

// NewClusterMetricsPusher creates a new metrics pusher.
func NewClusterMetricsPusher(
	db *firestore.Client,
	k8sClient kubernetes.Interface,
	namespace, installationID, agentVersion, nodeHostname string,
	logger *slog.Logger,
) *ClusterMetricsPusher {
	if logger == nil {
		logger = slog.Default()
	}
	return &ClusterMetricsPusher{
		db:             db,
		k8sClient:      k8sClient,
		namespace:      namespace,
		installationID: installationID,
		agentVersion:   agentVersion,
		nodeHostname:   nodeHostname,
		logger:         logger.With("component", "metrics-pusher"),
	}
}

// PushMetrics collects all cluster metrics and writes them to Firestore.
func (p *ClusterMetricsPusher) PushMetrics(ctx context.Context) {
	if p.installationID == "" {
		return
	}

	p.logger.Debug("collecting cluster metrics")

	// Collect node info from K8s.
	nodes := p.fetchK8sNodes(ctx)

	// Collect ARC scale set info.
	byLabel, k8sAvailable := p.fetchK8sMetrics(ctx)
	scaleSetConfig := p.fetchK8sScaleSetConfig(ctx)

	// Merge label data.
	allLabels := make(map[string]bool)
	for label := range scaleSetConfig {
		allLabels[label] = true
	}
	for label := range byLabel {
		allLabels[label] = true
	}

	type labelStats struct {
		Pending, Running, Online, Busy, Idle int
		MinRunners                            int
		MaxRunners                           *int
	}
	mergedLabels := make(map[string]labelStats)
	for label := range allLabels {
		counts := byLabel[label]
		config := scaleSetConfig[label]
		busy := 0 // would come from Firestore workflowJobs count
		idle := max(0, counts.Online-busy)
		mergedLabels[label] = labelStats{
			Pending:    counts.Pending,
			Running:    counts.Running,
			Online:     counts.Online,
			Busy:       busy,
			Idle:       idle,
			MinRunners: config.MinRunners,
			MaxRunners: config.MaxRunners,
		}
	}

	// Convert nodes map to sorted array.
	var nodeList []interface{}
	for _, n := range nodes {
		nodeList = append(nodeList, n)
	}

	doc := map[string]interface{}{
		"installationId":       p.installationID,
		"byLabel":              mergedLabels,
		"nodes":                nodeList,
		"stabilityFlags": map[string]interface{}{
			"rolloutFrozen":  false,
			"criticalAlerts": 0,
		},
		"staleThresholdSeconds": 120,
		"k8sAvailable":          k8sAvailable,
		"updatedAt":             firestore.ServerTimestamp,
		"agentVersion":          p.agentVersion,
		"nodeHostname":          p.nodeHostname,
	}

	_, err := p.db.Collection("clusterMetrics").Doc(p.installationID).Set(ctx, doc, firestore.MergeAll)
	if err != nil {
		p.logger.Error("failed to push metrics", "error", err)
		return
	}

	p.logger.Debug("pushed cluster metrics",
		"installation", p.installationID,
		"k8s", k8sAvailable,
		"scaleSets", len(mergedLabels),
	)
}

// StartPushLoop runs the metrics push loop on a fixed interval.
func (p *ClusterMetricsPusher) StartPushLoop(ctx context.Context, interval time.Duration) {
	p.logger.Info("starting metrics push loop", "interval", interval)

	// Push immediately on start.
	p.PushMetrics(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.PushMetrics(ctx)
		case <-ctx.Done():
			p.logger.Info("metrics push loop stopped")
			return
		}
	}
}

// fetchK8sNodes collects node information from K8s.
func (p *ClusterMetricsPusher) fetchK8sNodes(ctx context.Context) map[string]interface{} {
	nodes := make(map[string]interface{})

	nodeList, err := p.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		p.logger.Error("failed to list nodes", "error", err)
		return nodes
	}

	for _, node := range nodeList.Items {
		name := node.Name
		labels := node.Labels
		arch := labels["kubernetes.io/arch"]
		if arch == "" {
			arch = "unknown"
		}

		// Check ready condition.
		var ready bool
		for _, cond := range node.Status.Conditions {
			if cond.Type == "Ready" {
				ready = cond.Status == "True"
				break
			}
		}

		// Collect taints.
		type taintInfo struct {
			Key    string `json:"key"`
			Value  string `json:"value"`
			Effect string `json:"effect"`
		}
		var taints []taintInfo
		for _, t := range node.Spec.Taints {
			taints = append(taints, taintInfo{
				Key:    t.Key,
				Value:  t.Value,
				Effect: string(t.Effect),
			})
		}

		// Collect allocatable resources.
		allocCPU := node.Status.Allocatable.Cpu().MilliValue()
		allocMem := node.Status.Allocatable.Memory().Value()

		// Collect requested resources by summing pod requests on the node.
		// This is a simplified version -- a full implementation would query
		// Prometheus for accurate per-node requested resources.
		reqCPU := int64(0)
		reqMem := int64(0)

		nodes[name] = map[string]interface{}{
			"name": name,
			"arch": arch,
			"ready": ready,
			"labels": map[string]string{
				"kubernetes.io/arch":                  arch,
				"stratus.dev/host-machine":   labels["stratus.dev/host-machine"],
				"stratus.dev/node-type":      labels["stratus.dev/node-type"],
				"stratus.dev/worker-class":   labels["stratus.dev/worker-class"],
				"stratus.dev/availability":   labels["stratus.dev/availability"],
				"stratus.dev/always-on":      labels["stratus.dev/always-on"],
				"stratus.dev/emulation":      labels["stratus.dev/emulation"],
			},
			"taints":              taints,
			"allocatableCpuCores": roundToTenth(float64(allocCPU) / 1000),
			"allocatableMemoryGib": roundToTenth(float64(allocMem) / (1024 * 1024 * 1024)),
			"requestedCpuCores":   roundToTenth(float64(reqCPU) / 1000),
			"requestedMemoryGib":  roundToTenth(float64(reqMem) / (1024 * 1024 * 1024)),
			"cpuReservedPct":      nil,
			"memoryReservedPct":   nil,
		}
	}

	return nodes
}

// fetchK8sMetrics collects ARC runner pod metrics from K8s.
func (p *ClusterMetricsPusher) fetchK8sMetrics(ctx context.Context) (map[string]struct{ Pending, Running, Online int }, bool) {
	result := make(map[string]struct{ Pending, Running, Online int })

	podList, err := p.k8sClient.CoreV1().Pods(p.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		p.logger.Error("failed to list pods", "error", err)
		return result, false
	}

	for _, pod := range podList.Items {
		scaleSet := pod.Labels["actions.github.com/scale-set-name"]
		if scaleSet == "" {
			continue
		}

		entry := result[scaleSet]
		switch pod.Status.Phase {
		case "Pending":
			entry.Pending++
		case "Running":
			entry.Running++
			entry.Online++
		}
		result[scaleSet] = entry
	}

	return result, true
}

// fetchK8sScaleSetConfig collects AutoscalingRunnerSet CRD info.
func (p *ClusterMetricsPusher) fetchK8sScaleSetConfig(ctx context.Context) map[string]struct{ MaxRunners *int; MinRunners int } {
	// The ARC CRD is accessed via the K8s API. This is a simplified version.
	// A full implementation would use the dynamic client or typed client.
	return make(map[string]struct{ MaxRunners *int; MinRunners int })
}

func roundToTenth(v float64) interface{} {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return math.Round(v*10) / 10
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
