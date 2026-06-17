package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// ActiveWorkflowJobStatuses is the set of workflow job statuses that indicate
// the job is still active and should not be reaped.
var ActiveWorkflowJobStatuses = map[string]bool{
	"queued":      true,
	"in_progress": true,
}

// DefaultMinScheduledAge is the minimum age a scheduled runner must be before
// the reaper considers it for reaping.
const DefaultMinScheduledAge = 2 * time.Minute

// Reaper handles cleanup of completed runner jobs.
type Reaper struct {
	db        *firestore.Client
	k8sClient kubernetes.Interface
	namespace string
	logger    *slog.Logger
}

// NewReaper creates a new Reaper.
func NewReaper(db *firestore.Client, k8sClient kubernetes.Interface, namespace string, logger *slog.Logger) *Reaper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reaper{
		db:        db,
		k8sClient: k8sClient,
		namespace: namespace,
		logger:    logger.With("component", "reaper"),
	}
}

// ReconcileCompletedPendingRunner checks a single pendingRunner document and
// reaps its K8s Job if the corresponding workflow job is in a terminal state.
func (r *Reaper) ReconcileCompletedPendingRunner(ctx context.Context, docSnap *firestore.DocumentSnapshot, now time.Time, minScheduledAge time.Duration) (*ReconcileResult, error) {
	pendingRunner := docSnap.Data()
	if pendingRunner == nil {
		return &ReconcileResult{Action: "skipped", Reason: "no-data"}, nil
	}
	if status, _ := pendingRunner["status"].(string); status != "scheduled" {
		return &ReconcileResult{Action: "skipped", Reason: "not-scheduled"}, nil
	}

	scheduledAt := getPendingRunnerScheduledAt(pendingRunner)
	if scheduledAt == nil {
		return &ReconcileResult{Action: "skipped", Reason: "missing-scheduled-at"}, nil
	}

	if now.Sub(*scheduledAt) < minScheduledAge {
		return &ReconcileResult{Action: "skipped", Reason: "grace-period"}, nil
	}

	workflowJobDocID, _ := pendingRunner["workflowJobDocId"].(string)
	if workflowJobDocID == "" {
		workflowJobDocID = docSnap.Ref.ID
	}

	workflowJobSnap, err := r.db.Collection("workflowJobs").Doc(workflowJobDocID).Get(ctx)
	if err != nil {
		if isFirestoreNotFound(err) {
			return &ReconcileResult{Action: "skipped", Reason: "workflow-job-missing"}, nil
		}
		return nil, fmt.Errorf("fetch workflow job %s: %w", workflowJobDocID, err)
	}

	workflowJob := workflowJobSnap.Data()
	if !isWorkflowJobTerminal(workflowJob) {
		return &ReconcileResult{Action: "skipped", Reason: "workflow-job-active"}, nil
	}

	jobName := getPendingRunnerJobName(pendingRunner)
	if jobName == "" {
		return &ReconcileResult{Action: "skipped", Reason: "missing-job-name"}, nil
	}

	// Delete the K8s Job (ignore NotFound).
	deleteProp := metav1.DeletePropagationBackground
	err = r.k8sClient.BatchV1().Jobs(r.namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &deleteProp,
	})
	if err != nil && !isK8sNotFound(err) {
		return nil, fmt.Errorf("delete job %s: %w", jobName, err)
	}

	// Mark the pendingRunner as completed.
	completedAt := toTime(workflowJob["completedAt"])
	if completedAt == nil {
		completedAt = &now
	}
	workflowStatus, _ := workflowJob["status"].(string)
	workflowConclusion, _ := workflowJob["conclusion"].(string)
	if workflowConclusion == "" {
		workflowConclusion = workflowStatus
	}

	_, err = docSnap.Ref.Update(ctx, []firestore.Update{
		{Path: "status", Value: "completed"},
		{Path: "completedAt", Value: completedAt},
		{Path: "workflowStatus", Value: workflowStatus},
		{Path: "workflowConclusion", Value: workflowConclusion},
		{Path: "reapedAt", Value: now},
		{Path: "updatedAt", Value: now},
	})
	if err != nil {
		return nil, fmt.Errorf("update pending runner %s: %w", docSnap.Ref.ID, err)
	}

	r.logger.Info("reaped completed runner job",
		"jobName", jobName,
		"pendingRunnerID", docSnap.Ref.ID,
		"workflowStatus", workflowStatus,
	)

	return &ReconcileResult{
		Action:          "reaped",
		JobName:         jobName,
		WorkflowJobDocID: workflowJobDocID,
	}, nil
}

// ReconcileResult describes the outcome of a reaper reconciliation.
type ReconcileResult struct {
	Action          string `json:"action"`
	Reason          string `json:"reason,omitempty"`
	JobName         string `json:"jobName,omitempty"`
	WorkflowJobDocID string `json:"workflowJobDocId,omitempty"`
}

// ReapCompletedPendingRunners sweeps all 'scheduled' pendingRunners and reaps
// those whose corresponding workflow job has completed.
func (r *Reaper) ReapCompletedPendingRunners(ctx context.Context, now time.Time, minScheduledAge time.Duration, limit int) (checked, reaped int, reapErr error) {
	if limit <= 0 {
		limit = 100
	}

	iter := r.db.Collection("pendingRunners").
		Where("status", "==", "scheduled").
		Limit(limit).
		Documents(ctx)

	for {
		docSnap, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return checked, reaped, fmt.Errorf("iterate pendingRunners: %w", err)
		}

		checked++
		result, err := r.ReconcileCompletedPendingRunner(ctx, docSnap, now, minScheduledAge)
		if err != nil {
			r.logger.Error("reconcile failed", "id", docSnap.Ref.ID, "error", err)
			continue
		}
		if result.Action == "reaped" {
			reaped++
		}
	}

	return checked, reaped, nil
}

// StartReaperLoop runs the reaper on a fixed interval in a background goroutine.
func (r *Reaper) StartReaperLoop(ctx context.Context, interval, minScheduledAge time.Duration) {
	r.logger.Info("starting reaper loop", "interval", interval, "grace_period", minScheduledAge)

	// Run immediately on start.
	r.runOnce(ctx, minScheduledAge)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.runOnce(ctx, minScheduledAge)
		case <-ctx.Done():
			r.logger.Info("reaper loop stopped")
			return
		}
	}
}

func (r *Reaper) runOnce(ctx context.Context, minScheduledAge time.Duration) {
	checked, reaped, err := r.ReapCompletedPendingRunners(ctx, time.Now(), minScheduledAge, 100)
	if err != nil {
		r.logger.Error("reaper sweep failed", "error", err)
		return
	}
	if reaped > 0 {
		r.logger.Info("reaper sweep", "checked", checked, "reaped", reaped)
	}
}

// Helpers.
func getPendingRunnerScheduledAt(data map[string]interface{}) *time.Time {
	if t := toTime(data["scheduledAt"]); t != nil {
		return t
	}
	if t := toTime(data["claimedAt"]); t != nil {
		return t
	}
	return toTime(data["createdAt"])
}

func getPendingRunnerJobName(data map[string]interface{}) string {
	if name, ok := data["jobName"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	jobID, ok := data["jobId"].(string)
	if !ok || jobID == "" {
		return ""
	}
	return SanitizeJobName(fmt.Sprintf("stratus-jit-%s", jobID), "")
}

func isWorkflowJobTerminal(data map[string]interface{}) bool {
	if data == nil {
		return false
	}
	if t := toTime(data["completedAt"]); t != nil {
		return true
	}
	status, _ := data["status"].(string)
	status = strings.TrimSpace(strings.ToLower(status))
	if status == "" {
		return false
	}
	return !ActiveWorkflowJobStatuses[status]
}

func toTime(v interface{}) *time.Time {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case time.Time:
		return &t
	default:
		// Try to handle it as a map (Firestore Timestamp map representation).
		if ts, ok := v.(map[string]interface{}); ok {
			if sec, ok := ts["seconds"].(int64); ok {
				if nsec, ok := ts["nanos"].(int64); ok {
					t := time.Unix(sec, nsec)
					return &t
				}
			}
		}
		return nil
	}
}

func isFirestoreNotFound(err error) bool {
	return strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "not found")
}

func isK8sNotFound(err error) bool {
	return strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "not found")
}
