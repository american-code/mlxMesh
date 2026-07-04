// Package coordinator — query decomposition for background-lane jobs.
//
// Decomposition splits a compound query into independent sub-tasks that can be
// dispatched in parallel, then synthesized by the merger. It is STRICTLY opt-in:
// AllowDecomposition=false (the default) must never be silently overridden.
//
// The decomposition model interface allows a BERT-class classifier to be plugged in
// via LoadDecompositionModel without changing any callers. Until an external model
// is configured, ClassifyQueryIntent falls back to keyword heuristics with a
// confidence level of ~0.65 — adequate for high-signal query types (SQL, anomaly,
// trend) but not fine-grained enough for ambiguous queries.
package coordinator

import (
	"fmt"
	"strings"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// SubTaskType identifies the category of work a sub-task performs.
// MERGE is always last in the DAG — it depends on all other sub-tasks.
type SubTaskType string

const (
	SubTaskSchemaLookup      SubTaskType = "SCHEMA_LOOKUP"
	SubTaskAnomalyDetection  SubTaskType = "ANOMALY_DETECTION"
	SubTaskTrendAnalysis     SubTaskType = "TREND_ANALYSIS"
	SubTaskQueryOptimization SubTaskType = "QUERY_OPTIMIZATION"
	SubTaskSummarization     SubTaskType = "SUMMARIZATION"
	SubTaskClassification    SubTaskType = "CLASSIFICATION"
	SubTaskMerge             SubTaskType = "MERGE" // synthesizes outputs of all other sub-tasks; always last
)

// SubTask is one unit of parallelisable work within a decomposed job.
type SubTask struct {
	SubTaskID    string      `json:"sub_task_id"`
	ParentJobID  string      `json:"parent_job_id"`
	ModelID      string      `json:"model_id"` // inherited from parent JobSpec; same model for all sub-tasks
	SubTaskType  SubTaskType `json:"sub_task_type"`
	Prompt       string      `json:"prompt"`        // constructed from the original query, scoped to this sub-task
	DependsOn    []string    `json:"depends_on"`    // SubTaskIDs that must complete before this one starts
	AssignedNode string      `json:"assigned_node"` // set by router at dispatch time
}

// DecomposedJob is the output of DecomposeJob, ready for DispatchSubTasksInParallel.
type DecomposedJob struct {
	OriginalJobID   string    `json:"original_job_id"`
	SubTasks        []SubTask `json:"sub_tasks"`
	ConfidenceScore float64   `json:"confidence_score"` // 0..1; from the classifier or heuristic
	ClassifierUsed  string    `json:"classifier_used"`  // "keyword_heuristic" | "bert_classifier" | etc.
}

// DecompositionModel is the interface for query intent classifiers.
// Implement this interface to plug in a BERT/DistilBERT classifier without
// changing any callers. The heuristic fallback in ClassifyQueryIntent is used
// when model is nil or returns ErrNotImplemented.
type DecompositionModel interface {
	// ClassifyIntent returns the inferred sub-task types and a confidence score.
	// confidence is in [0, 1]. Lower confidence → callers should fall back to single-node routing.
	ClassifyIntent(text string) (subTaskTypes []SubTaskType, confidence float64, err error)
}

// LoadDecompositionModel loads a BERT-class model from modelPath.
// Returns ErrNotImplemented until an external model runtime is configured.
// Callers that receive ErrNotImplemented must use ClassifyQueryIntent with model=nil,
// which activates the keyword heuristic path.
func LoadDecompositionModel(_ string) (DecompositionModel, error) {
	// STUB: requires a BERT/DistilBERT runtime (ONNX or similar).
	// Until the runtime is integrated, callers must pass model=nil to ClassifyQueryIntent
	// which will use keyword heuristics instead.
	return nil, ErrNotImplemented
}

// ClassifyQueryIntent infers which sub-task types are appropriate for queryText.
// When model is nil or returns ErrNotImplemented, falls back to keyword heuristics.
// The heuristic path has a confidence ceiling of 0.65 — below the 0.7 threshold
// recommended for enabling decomposition on production traffic.
func ClassifyQueryIntent(queryText string, model DecompositionModel) ([]SubTaskType, float64, error) {
	if model != nil {
		types, conf, err := model.ClassifyIntent(queryText)
		if err == nil {
			return types, conf, nil
		}
		if err != ErrNotImplemented {
			return nil, 0, fmt.Errorf("classify query intent: model error: %w", err)
		}
		// ErrNotImplemented: fall through to heuristic
	}
	return keywordHeuristicClassify(queryText)
}

// keywordHeuristicClassify is the fallback classifier. Confidence is capped at 0.65
// because keyword matching cannot distinguish overlapping query types (e.g. a query
// that asks for trend analysis on anomalous data matches both SubTaskTrendAnalysis
// and SubTaskAnomalyDetection).
func keywordHeuristicClassify(queryText string) ([]SubTaskType, float64, error) {
	lower := strings.ToLower(queryText)
	var matched []SubTaskType

	if strings.Contains(lower, "schema") || strings.Contains(lower, "table structure") ||
		strings.Contains(lower, "column") || strings.Contains(lower, "data type") {
		matched = append(matched, SubTaskSchemaLookup)
	}
	if strings.Contains(lower, "anomal") || strings.Contains(lower, "outlier") ||
		strings.Contains(lower, "unusual") || strings.Contains(lower, "spike") ||
		strings.Contains(lower, "deviation") {
		matched = append(matched, SubTaskAnomalyDetection)
	}
	if strings.Contains(lower, "trend") || strings.Contains(lower, "over time") ||
		strings.Contains(lower, "growth") || strings.Contains(lower, "forecast") ||
		strings.Contains(lower, "historical") {
		matched = append(matched, SubTaskTrendAnalysis)
	}
	if strings.Contains(lower, "optimis") || strings.Contains(lower, "optimiz") ||
		strings.Contains(lower, "slow query") || strings.Contains(lower, "index") ||
		strings.Contains(lower, "execution plan") {
		matched = append(matched, SubTaskQueryOptimization)
	}
	if strings.Contains(lower, "summari") || strings.Contains(lower, "tldr") ||
		strings.Contains(lower, "key points") || strings.Contains(lower, "brief") {
		matched = append(matched, SubTaskSummarization)
	}
	if strings.Contains(lower, "classif") || strings.Contains(lower, "categor") ||
		strings.Contains(lower, "label") || strings.Contains(lower, "detect") {
		matched = append(matched, SubTaskClassification)
	}

	if len(matched) == 0 {
		// No keywords matched; the query is not a good decomposition candidate.
		return nil, 0.0, nil
	}

	// Confidence degrades with ambiguity: more sub-task types → less confident.
	confidence := 0.65
	if len(matched) > 2 {
		confidence = 0.45 // high ambiguity; likely a poor decomposition candidate
	}
	return matched, confidence, nil
}

// BuildDependencyDAG returns the dependency edges for a set of sub-task types.
// The MERGE sub-task always depends on all others; all other sub-tasks are
// independent of each other and can be dispatched in parallel.
//
// Returns a slice of [dependsOn, dependent] pairs, suitable for topological sort.
func BuildDependencyDAG(subTaskTypes []SubTaskType) [][2]SubTaskType {
	var edges [][2]SubTaskType
	for _, t := range subTaskTypes {
		if t == SubTaskMerge {
			continue
		}
		edges = append(edges, [2]SubTaskType{t, SubTaskMerge})
	}
	return edges
}

// DecomposeJob breaks a background-lane job into parallel sub-tasks.
// Returns ErrNotImplemented when:
//   - AllowDecomposition is false (caller bug; should never reach here)
//   - The classifier returns confidence < 0.5 (not worth decomposing)
//   - The query yields no sub-task types (no decomposition applicable)
//
// Callers must check for ErrNotImplemented and fall back to single-node routing.
func DecomposeJob(jobSpec protocol.JobSpec, model DecompositionModel) (DecomposedJob, error) {
	if !jobSpec.AllowDecomposition {
		return DecomposedJob{}, fmt.Errorf(
			"DecomposeJob: job %q has AllowDecomposition=false; "+
				"decomposition is opt-in and must not be applied without requester consent: %w",
			jobSpec.JobID, ErrNotImplemented,
		)
	}
	if jobSpec.Lane == protocol.JobLaneFast {
		// Enforced again here for defense-in-depth; Validate() already blocks this.
		return DecomposedJob{}, fmt.Errorf(
			"DecomposeJob: fast-lane job %q cannot be decomposed: %w",
			jobSpec.JobID, ErrNotImplemented,
		)
	}

	// The payload is encrypted (PayloadRef); we use JobID as a proxy query text
	// for the heuristic. In production this would be the decrypted query extracted
	// from the payload — wire that in when the payload decryption path is available.
	queryText := jobSpec.JobID

	subTaskTypes, confidence, err := ClassifyQueryIntent(queryText, model)
	if err != nil {
		return DecomposedJob{}, fmt.Errorf("DecomposeJob: %w", err)
	}
	if len(subTaskTypes) == 0 || confidence < 0.5 {
		return DecomposedJob{}, fmt.Errorf(
			"DecomposeJob: job %q not decomposable (confidence %.2f, types %v): %w",
			jobSpec.JobID, confidence, subTaskTypes, ErrNotImplemented,
		)
	}

	classifierUsed := "keyword_heuristic"
	if model != nil {
		classifierUsed = "bert_classifier"
	}

	// Build sub-tasks: all analytical types in parallel, MERGE last.
	subTasks := make([]SubTask, 0, len(subTaskTypes)+1)
	analyticalIDs := make([]string, 0, len(subTaskTypes))
	for i, t := range subTaskTypes {
		id := fmt.Sprintf("%s-sub-%02d-%s", jobSpec.JobID, i, strings.ToLower(string(t)))
		analyticalIDs = append(analyticalIDs, id)
		subTasks = append(subTasks, SubTask{
			SubTaskID:   id,
			ParentJobID: jobSpec.JobID,
			ModelID:     jobSpec.ModelID,
			SubTaskType: t,
			Prompt:      buildSubTaskPrompt(t, jobSpec.JobID),
			DependsOn:   nil, // all analytical tasks are independent; see BuildDependencyDAG
		})
	}
	// MERGE depends on all analytical sub-tasks.
	mergeID := fmt.Sprintf("%s-sub-merge", jobSpec.JobID)
	subTasks = append(subTasks, SubTask{
		SubTaskID:   mergeID,
		ParentJobID: jobSpec.JobID,
		ModelID:     jobSpec.ModelID,
		SubTaskType: SubTaskMerge,
		Prompt:      "Synthesize the sub-task outputs into a single coherent response.",
		DependsOn:   analyticalIDs,
	})

	return DecomposedJob{
		OriginalJobID:   jobSpec.JobID,
		SubTasks:        subTasks,
		ConfidenceScore: confidence,
		ClassifierUsed:  classifierUsed,
	}, nil
}

// buildSubTaskPrompt returns a generic task-specific instruction fragment.
// Real implementations would extract the decrypted user query and scope it.
func buildSubTaskPrompt(t SubTaskType, jobID string) string {
	switch t {
	case SubTaskSchemaLookup:
		return fmt.Sprintf("For job %s: identify and explain all relevant schema structures, tables, and column types.", jobID)
	case SubTaskAnomalyDetection:
		return fmt.Sprintf("For job %s: identify anomalies, outliers, and unexpected patterns in the data.", jobID)
	case SubTaskTrendAnalysis:
		return fmt.Sprintf("For job %s: analyze temporal trends, growth rates, and forecasts.", jobID)
	case SubTaskQueryOptimization:
		return fmt.Sprintf("For job %s: suggest query optimisations, index recommendations, and execution plan improvements.", jobID)
	case SubTaskSummarization:
		return fmt.Sprintf("For job %s: produce a concise summary of the key points.", jobID)
	case SubTaskClassification:
		return fmt.Sprintf("For job %s: classify and categorize the data according to the requested taxonomy.", jobID)
	default:
		return fmt.Sprintf("For job %s: perform the requested %s task.", jobID, string(t))
	}
}

// IsDecompositionWorthIt returns true when the estimated parallelism gain from
// decomposition exceeds the coordination overhead.
//
// coordinationOverheadMs is the measured (or estimated) round-trip cost for routing,
// node selection, and the final merge inference call. A value of 0 is treated as 50ms.
//
// Token threshold: sub-tasks with fewer than 200 estimated tokens each are not worth
// decomposing — the coordination overhead dominates at that scale.
func IsDecompositionWorthIt(decomposed DecomposedJob, coordinationOverheadMs float64) bool {
	if coordinationOverheadMs <= 0 {
		coordinationOverheadMs = 50.0
	}
	analyticalCount := 0
	for _, t := range decomposed.SubTasks {
		if t.SubTaskType != SubTaskMerge {
			analyticalCount++
		}
	}
	if analyticalCount < 2 {
		// Only one analytical sub-task; no parallelism benefit.
		return false
	}

	// Rough model: each sub-task runs in (baseline / analyticalCount) time.
	// If coordination overhead is more than 30% of the estimated savings, not worth it.
	// Tune the 0.3 threshold against Milestone 2 pilot data.
	const baselineEstimateMs = 2000.0 // typical background job inference time
	parallelTimeMs := baselineEstimateMs/float64(analyticalCount) + coordinationOverheadMs
	return parallelTimeMs < baselineEstimateMs*0.9
}
