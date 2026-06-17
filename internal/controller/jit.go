// Package controller implements the JIT (Just-In-Time) runner controller
// and sandbox lifecycle management.
//
// The JIT controller watches the Firestore `pendingRunners` collection for
// documents with status "pending", atomically claims them via Firestore
// transaction, and creates the corresponding K8s BatchV1 Job.
//
// Concurrency safety is achieved through a Firestore transaction that flips
// the document status from "pending" to "scheduling" before the K8s API
// call, preventing duplicate Job creation across controller replicas.
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/StratusCloudLabs/stratus-runtime/internal/metrics"
)

// JITController manages the lifecycle of JIT runner requests.
type JITController struct {
	db        *firestore.Client
	k8sClient kubernetes.Interface
	namespace string
	ghcrBase  string
	ghcrSecret string
	logger    *slog.Logger

	// Capacity check guard.
	mu          sync.Mutex
	clusterDoc  string // Firestore doc path for cluster metrics

	installationID string

	// Retry settings.
	retryBaseInterval time.Duration
	retryMaxInterval  time.Duration
}

// NewJITController creates a new JIT controller.
func NewJITController(
	db *firestore.Client,
	k8sClient kubernetes.Interface,
	namespace, ghcrBase, ghcrSecret, installationID string,
	retryBaseInterval, retryMaxInterval time.Duration,
	logger *slog.Logger,
) *JITController {
	if logger == nil {
		logger = slog.Default()
	}
	return &JITController{
		db:                db,
		k8sClient:         k8sClient,
		namespace:         namespace,
		ghcrBase:          ghcrBase,
		ghcrSecret:        ghcrSecret,
		installationID:    installationID,
		retryBaseInterval: retryBaseInterval,
		retryMaxInterval:  retryMaxInterval,
		logger:            logger.With("component", "jit-controller"),
	}
}

// ClaimAndSchedule atomically claims a pending runner document and creates
// the corresponding K8s Job. This is the core scheduling function.
func (c *JITController) ClaimAndSchedule(ctx context.Context, docSnap *firestore.DocumentSnapshot) error {
	start := time.Now()
	defer func() {
		metrics.ControllerJobClaimLatency.Observe(time.Since(start).Seconds())
	}()

	doc := docSnap.Data()
	docRef := docSnap.Ref

	// Atomic claim: pending -> scheduling.
	var claimed bool
	err := c.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		fresh, err := tx.Get(docRef)
		if err != nil {
			return fmt.Errorf("get doc in tx: %w", err)
		}
		if status, _ := fresh.Data()["status"].(string); status != "pending" {
			return nil // skip, not ours
		}
		err = tx.Update(docRef, []firestore.Update{
			{Path: "status", Value: "scheduling"},
			{Path: "claimedAt", Value: firestore.ServerTimestamp},
			{Path: "updatedAt", Value: firestore.ServerTimestamp},
		})
		if err != nil {
			return fmt.Errorf("update in tx: %w", err)
		}
		claimed = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("claim transaction: %w", err)
	}
	if !claimed {
		c.logger.Debug("skip already claimed doc", "id", docRef.ID)
		return nil
	}

	metrics.ControllerClaimsTotal.Inc()

	// Build the K8s Job manifest from the doc.
	runnerDoc := docToRunnerDocument(doc)
	job := BuildJobManifest(runnerDoc, c.ghcrBase, c.namespace, c.ghcrSecret)

	// Capacity check.
	if c.installationID != "" {
		hasCapacity, err := c.checkNodeCapacity(ctx, doc, runnerDoc)
		if err != nil {
			c.logger.Warn("capacity check failed (failing open)", "id", docRef.ID, "error", err)
		} else if !hasCapacity {
			return c.deferWithBackoff(ctx, doc, docRef, runnerDoc)
		}
	}

	c.logger.Info("creating K8s Job",
		"jobName", job.Name,
		"org", runnerDoc.OrgName,
		"jobId", runnerDoc.JobID,
		"namespace", c.namespace,
	)

	createStart := time.Now()
	_, err = c.k8sClient.BatchV1().Jobs(c.namespace).Create(ctx, job, metav1.CreateOptions{})
	metrics.JobCreationDuration.Observe(time.Since(createStart).Seconds())

	if err != nil {
		if errors.IsAlreadyExists(err) {
			// The Job already exists (controller crashed after creating the Job
			// but before updating Firestore). Treat as success.
			c.logger.Info("Job already exists -- marking scheduled (idempotent recovery)",
				"jobName", job.Name,
			)
			_, updateErr := docRef.Update(ctx, []firestore.Update{
				{Path: "status", Value: "scheduled"},
				{Path: "jobName", Value: job.Name},
				{Path: "scheduledAt", Value: firestore.ServerTimestamp},
				{Path: "recoveredAt", Value: firestore.ServerTimestamp},
				{Path: "updatedAt", Value: firestore.ServerTimestamp},
			})
			if updateErr != nil {
				return fmt.Errorf("update doc after recovery: %w", updateErr)
			}
			return nil
		}

		metrics.JobsFailedTotal.Inc()
		c.logger.Error("failed to create K8s Job",
			"jobName", job.Name,
			"error", err,
		)
		_, updateErr := docRef.Update(ctx, []firestore.Update{
			{Path: "status", Value: "failed"},
			{Path: "error", Value: err.Error()},
			{Path: "updatedAt", Value: firestore.ServerTimestamp},
		})
		if updateErr != nil {
			return fmt.Errorf("update doc after failure: %w", updateErr)
		}
		return fmt.Errorf("create job %s: %w", job.Name, err)
	}

	metrics.JobsCreatedTotal.Inc()

	_, err = docRef.Update(ctx, []firestore.Update{
		{Path: "status", Value: "scheduled"},
		{Path: "jobName", Value: job.Name},
		{Path: "scheduledAt", Value: firestore.ServerTimestamp},
		{Path: "updatedAt", Value: firestore.ServerTimestamp},
	})
	if err != nil {
		return fmt.Errorf("update doc after schedule: %w", err)
	}

	c.logger.Info("scheduled runner Job",
		"jobName", job.Name,
		"org", runnerDoc.OrgName,
		"jobId", runnerDoc.JobID,
	)
	return nil
}

// deferWithBackoff resets a pending runner to "pending" with an exponential
// backoff retry time, indicating the cluster currently lacks capacity.
func (c *JITController) deferWithBackoff(ctx context.Context, doc map[string]interface{}, docRef *firestore.DocumentRef, runnerDoc *RunnerDocument) error {
	// Extract current retry count.
	retryCount := 0
	if rc, ok := doc["retryCount"].(int); ok {
		retryCount = rc
	}
	if rc, ok := doc["retryCount"].(int64); ok {
		retryCount = int(rc)
	}
	retryCount++

	backoffMs := math.Min(
		float64(c.retryBaseInterval.Milliseconds())*math.Pow(2, float64(retryCount-1)),
		float64(c.retryMaxInterval.Milliseconds()),
	)
	nextRetryAt := time.Now().Add(time.Duration(backoffMs) * time.Millisecond)

	// Determine label and arch for metrics.
	label := "unknown"
	if len(runnerDoc.Labels) > 0 {
		label = runnerDoc.Labels[0]
	}
	arch := runnerDoc.Arch
	if arch == "" {
		arch = "amd64"
	}
	metrics.DispatchCapacityCheckFailures.WithLabelValues(label, arch).Inc()

	c.logger.Info("no capacity for runner; deferring with backoff",
		"id", docRef.ID,
		"label", label,
		"arch", arch,
		"retryCount", retryCount,
		"backoff", time.Duration(backoffMs)*time.Millisecond,
	)

	queuedAt := doc["queuedAt"]
	if queuedAt == nil {
		queuedAt = firestore.ServerTimestamp
	}

	_, err := docRef.Update(ctx, []firestore.Update{
		{Path: "status", Value: "pending"},
		{Path: "queuedAt", Value: queuedAt},
		{Path: "nextRetryAt", Value: nextRetryAt},
		{Path: "retryCount", Value: retryCount},
		{Path: "updatedAt", Value: firestore.ServerTimestamp},
	})
	if err != nil {
		return fmt.Errorf("defer update: %w", err)
	}
	return nil
}

// RecoverStuckSchedulingDocs resets any pendingRunners stuck in "scheduling"
// state for longer than the threshold back to "pending" so the watcher can
// re-process them. This handles the case where the controller crashed after
// claiming a doc but before (or during) K8s Job creation.
func (c *JITController) RecoverStuckSchedulingDocs(ctx context.Context, threshold time.Duration) (int, error) {
	cutoff := time.Now().Add(-threshold)

	iter := c.db.Collection("pendingRunners").
		Where("status", "==", "scheduling").
		Documents(ctx)

	var stuckRefs []*firestore.DocumentRef
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("iterate scheduling docs: %w", err)
		}

		data := snap.Data()
		ts := getPendingRunnerScheduledAt(data)
		if ts != nil && ts.After(cutoff) {
			continue
		}
		// If missing timestamp or older than threshold, it is stuck.
		stuckRefs = append(stuckRefs, snap.Ref)
	}

	if len(stuckRefs) == 0 {
		return 0, nil
	}

	c.logger.Info("startup recovery: resetting stuck scheduling docs",
		"count", len(stuckRefs),
	)

	// Use a batched write for efficiency.
	batch := c.db.Batch()
	for _, ref := range stuckRefs {
		batch.Update(ref, []firestore.Update{
			{Path: "status", Value: "pending"},
			{Path: "startupRecoveredAt", Value: firestore.ServerTimestamp},
			{Path: "updatedAt", Value: firestore.ServerTimestamp},
		})
	}
	_, err := batch.Commit(ctx)
	if err != nil {
		return 0, fmt.Errorf("batch recover: %w", err)
	}

	c.logger.Info("startup recovery complete", "recovered", len(stuckRefs))
	return len(stuckRefs), nil
}

// ProcessPendingRunners queries for all pending runners and processes them.
// This is used by the retry loop and the initial sweep.
func (c *JITController) ProcessPendingRunners(ctx context.Context) error {
	iter := c.db.Collection("pendingRunners").
		Where("status", "==", "pending").
		Documents(ctx)

	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("iterate pending runners: %w", err)
		}

		if err := c.ClaimAndSchedule(ctx, snap); err != nil {
			c.logger.Error("claim and schedule failed", "id", snap.Ref.ID, "error", err)
		}
	}
	return nil
}

// ProcessRetryableRunners re-processes queued runners whose nextRetryAt
// timestamp has passed.
func (c *JITController) ProcessRetryableRunners(ctx context.Context) error {
	if c.installationID == "" {
		return nil
	}

	iter := c.db.Collection("pendingRunners").
		Where("installationId", "==", c.installationID).
		Where("status", "==", "pending").
		Where("nextRetryAt", "<=", time.Now()).
		Limit(10).
		Documents(ctx)

	count := 0
	for {
		snap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("iterate retryable runners: %w", err)
		}
		count++
		if err := c.ClaimAndSchedule(ctx, snap); err != nil {
			c.logger.Error("retry claim failed", "id", snap.Ref.ID, "error", err)
		}
	}

	if count > 0 {
		c.logger.Info("re-tried queued runners", "count", count)
	}
	return nil
}

// StartController runs the JIT controller loop in a background goroutine.
// It performs startup recovery, then runs a periodic retry loop.
func (c *JITController) StartController(ctx context.Context, startupThreshold time.Duration, retryInterval time.Duration) {
	c.logger.Info("starting JIT controller")

	// Startup recovery: reset stuck scheduling docs.
	recovered, err := c.RecoverStuckSchedulingDocs(ctx, startupThreshold)
	if err != nil {
		c.logger.Warn("startup recovery failed (non-fatal)", "error", err)
	} else if recovered > 0 {
		c.logger.Info("startup recovery reset docs", "count", recovered)
	}

	// Process any pending docs that are already in the collection.
	if err := c.ProcessPendingRunners(ctx); err != nil {
		c.logger.Warn("initial pending sweep failed (non-fatal)", "error", err)
	}

	// Retry loop for queued (retryable) runners.
	go func() {
		ticker := time.NewTicker(retryInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := c.ProcessRetryableRunners(ctx); err != nil {
					c.logger.Error("retry loop error", "error", err)
				}
			case <-ctx.Done():
				c.logger.Info("controller retry loop stopped")
				return
			}
		}
	}()

	// Firestore snapshot listener runs in the main goroutine context.
	// Start a goroutine for the real-time watcher.
	go func() {
		c.logger.Info("starting Firestore snapshot watcher for pendingRunners")
		// Use a polling approach since Firestore Go doesn't have onSnapshot
		// like the Node.js SDK. We poll every 15 seconds.
		pollTicker := time.NewTicker(15 * time.Second)
		defer pollTicker.Stop()

		for {
			select {
			case <-pollTicker.C:
				if err := c.ProcessPendingRunners(ctx); err != nil {
					c.logger.Error("polling sweep error", "error", err)
				}
			case <-ctx.Done():
				c.logger.Info("controller poll loop stopped")
				return
			}
		}
	}()
}

// checkNodeCapacity checks whether any ready node matching the doc's arch
// has enough free capacity for the resource request.
func (c *JITController) checkNodeCapacity(ctx context.Context, doc map[string]interface{}, runnerDoc *RunnerDocument) (bool, error) {
	metricsSnap, err := c.db.Collection("clusterMetrics").Doc(c.installationID).Get(ctx)
	if err != nil {
		// If the metrics doc doesn't exist, fail open.
		if isFirestoreNotFound(err) {
			return true, nil
		}
		return true, fmt.Errorf("fetch clusterMetrics: %w", err)
	}

	metricsData := metricsSnap.Data()
	nodesRaw, ok := metricsData["nodes"].([]interface{})
	if !ok || len(nodesRaw) == 0 {
		return true, nil
	}

	profile := ResolveRunnerProfile(runnerDoc.Labels)
	arch := profile.Arch
	if arch == "multi" {
		arch = "multi"
	} else if runnerDoc.Arch == "arm64" {
		arch = "arm64"
	} else {
		arch = "amd64"
	}

	resourceProfile := ResolveJobResources(runnerDoc.Labels)
	reqCPU := parseCPUCores(resourceProfile.CPU)
	reqMemGiB := parseMemoryGiB(resourceProfile.Memory)

	for _, n := range nodesRaw {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}

		// Check ready.
		ready, _ := node["ready"].(bool)
		if !ready {
			// Could be nil (unknown); treat as not-ready if explicitly false.
			if v, exists := node["ready"]; exists {
				if b, ok := v.(bool); ok && !b {
					continue
				}
			}
		}

		// Check arch.
		nodeArch, _ := node["arch"].(string)
		if arch == "multi" {
			if nodeArch != "amd64" && nodeArch != "arm64" && nodeArch != "" {
				continue
			}
		} else if nodeArch != arch && !(nodeArch == "" && arch == "amd64") {
			continue
		}

		// Check capacity.
		allocCPU := toFloat64(node["allocatableCpuCores"])
		allocMem := toFloat64(node["allocatableMemoryGib"])
		reqdCPU := toFloat64(node["requestedCpuCores"])
		reqdMem := toFloat64(node["requestedMemoryGib"])

		// If allocatable values are nil, fail open for this node.
		if allocCPU == nil || allocMem == nil {
			return true, nil
		}

		freeCPU := *allocCPU - *reqdCPU
		freeMemGiB := *allocMem - *reqdMem

		if freeCPU >= reqCPU && freeMemGiB >= reqMemGiB {
			return true, nil
		}
	}

	return false, nil
}

// docToRunnerDocument converts a Firestore document map to a RunnerDocument.
func docToRunnerDocument(doc map[string]interface{}) *RunnerDocument {
	rd := &RunnerDocument{}
	if v, ok := doc["orgName"].(string); ok {
		rd.OrgName = v
	}
	if v, ok := doc["repoFullName"].(string); ok {
		rd.RepoFullName = v
	}
	if v, ok := doc["jobId"].(string); ok {
		rd.JobID = v
	}
	if v, ok := doc["runId"].(string); ok {
		rd.RunID = v
	}
	if v, ok := doc["runnerName"].(string); ok {
		rd.RunnerName = v
	}
	if v, ok := doc["encodedJitConfig"].(string); ok {
		rd.EncodedJitConfig = v
	}
	if v, ok := doc["arch"].(string); ok {
		rd.Arch = v
	}
	if v, ok := doc["workflowName"].(string); ok {
		rd.WorkflowName = v
	}
	if v, ok := doc["jobName"].(string); ok {
		rd.WorkflowJobName = v
	}
	if labels, ok := doc["labels"].([]interface{}); ok {
		for _, l := range labels {
			if s, ok := l.(string); ok {
				rd.Labels = append(rd.Labels, s)
			}
		}
	}
	if v, ok := doc["installationId"].(string); ok {
		rd.InstallationID = v
	}
	if v, ok := doc["status"].(string); ok {
		rd.Status = v
	}
	// activeDeadlineSeconds may be int or int64.
	if v, ok := doc["activeDeadlineSeconds"]; ok {
		switch n := v.(type) {
		case int64:
			rd.ActiveDeadlineSeconds = &n
		case float64:
			i := int64(n)
			rd.ActiveDeadlineSeconds = &i
		}
	}
	return rd
}

// Resource parsing helpers.
func parseCPUCores(cpu string) float64 {
	if cpu == "" {
		return 0
	}
	if len(cpu) > 0 && cpu[len(cpu)-1] == 'm' {
		f := 0.0
		fmt.Sscanf(cpu[:len(cpu)-1], "%f", &f)
		return f / 1000
	}
	f := 0.0
	fmt.Sscanf(cpu, "%f", &f)
	return f
}

func parseMemoryGiB(mem string) float64 {
	if mem == "" {
		return 0
	}
	if strings.HasSuffix(mem, "Gi") {
		f := 0.0
		fmt.Sscanf(mem[:len(mem)-2], "%f", &f)
		return f
	}
	if strings.HasSuffix(mem, "Mi") {
		f := 0.0
		fmt.Sscanf(mem[:len(mem)-2], "%f", &f)
		return f / 1024
	}
	if strings.HasSuffix(mem, "Ki") {
		f := 0.0
		fmt.Sscanf(mem[:len(mem)-2], "%f", &f)
		return f / (1024 * 1024)
	}
	f := 0.0
	fmt.Sscanf(mem, "%f", &f)
	return f / (1024 * 1024 * 1024)
}

func toFloat64(v interface{}) *float64 {
	if v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		return &n
	case int:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	default:
		return nil
	}
}
