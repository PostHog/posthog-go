package evaluations

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const experimentsPath = "ai_observability/offline_experiments/"

type ExperimentReceipt struct {
	ID                  string     `json:"id"`
	Status              string     `json:"status"`
	Created             bool       `json:"created"`
	StartedAt           time.Time  `json:"started_at"`
	CreatedAt           time.Time  `json:"created_at"`
	FinishedAt          *time.Time `json:"finished_at"`
	ExpectedItemCount   *int64     `json:"expected_item_count"`
	ExpectedResultCount *int64     `json:"expected_result_count"`
	AcceptedItemCount   int64      `json:"accepted_item_count"`
	AcceptedResultCount int64      `json:"accepted_result_count"`
}

// Experiment serializes operations on this handle, including multi-request uploads.
type Experiment struct {
	client     *Client
	id         string
	submission []byte
	receipt    atomic.Pointer[ExperimentReceipt]
	gate       chan struct{}
}

func (e *Experiment) ID() string   { return e.id }
func (e *Experiment) path() string { return experimentsPath + e.id + "/" }

// Submission returns a detached copy of the original creation fields for replay.
// A locally resumed handle has no creation submission.
func (e *Experiment) Submission() Fields {
	var f Fields
	if e.submission != nil {
		_ = decode(e.submission, &f)
	}
	return f
}
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Receipt returns a detached copy of the last creation or lifecycle acknowledgment.
func (e *Experiment) Receipt() *ExperimentReceipt {
	r := copyPtr(e.receipt.Load())
	if r != nil {
		r.FinishedAt = copyPtr(r.FinishedAt)
		r.ExpectedItemCount = copyPtr(r.ExpectedItemCount)
		r.ExpectedResultCount = copyPtr(r.ExpectedResultCount)
	}
	return r
}

// ResumeExperiment opens a local handle to an existing run ID. The next
// request checks whether the run exists and the caller can access it.
func (c *Client) ResumeExperiment(id string) (*Experiment, error) {
	id, err := idString(id)
	if err != nil {
		return nil, err
	}
	return &Experiment{client: c, id: id, gate: make(chan struct{}, 1)}, nil
}

// CreateExperiment acknowledges a named run. Optional properties include id,
// started_at, run_source, expected_item_count, expected_result_count, suite_key,
// experiment_scorer_version_id, dataset_source, dataset_identifier,
// dataset_revision_identifier, dataset_revision_id,
// application_version, model_version and prompt_version.
// An exact experiment_scorer_version_id overrides the suite default. Omit it
// or pass nil to use the default chosen by the server at first creation.
// When reloading a saved submission into Fields, use json.Decoder.UseNumber
// to preserve integer counts above the safe float64 range.
func (c *Client) CreateExperiment(ctx context.Context, fields Fields) (*Experiment, error) {
	body := copyFields(fields)
	textKeys := []string{"suite_key", "dataset_source", "dataset_identifier", "dataset_revision_identifier", "application_version", "model_version", "prompt_version"}
	if err := allowed(body, append(textKeys, "id", "name", "started_at", "run_source", "expected_item_count", "expected_result_count", "dataset_revision_id", "experiment_scorer_version_id")...); err != nil {
		return nil, err
	}
	if !identifier(body["name"], 400) {
		return nil, errors.New("evaluations: invalid experiment name")
	}
	if err := identifiers(body, textKeys...); err != nil {
		return nil, err
	}
	if _, ok := body["id"]; !ok {
		body["id"] = uuid.NewString()
	}
	id, err := idString(body["id"])
	if err != nil {
		return nil, err
	}
	body["id"] = id
	if _, ok := body["started_at"]; !ok {
		body["started_at"] = time.Now()
	}
	body["started_at"], err = instant(body["started_at"])
	if err != nil {
		return nil, err
	}
	if v := body["run_source"]; v != nil && !enum(v, "ci", "local", "scheduled") {
		return nil, errors.New("evaluations: invalid run source")
	}
	for _, key := range []string{"dataset_revision_id", "experiment_scorer_version_id"} {
		if v := body[key]; v != nil {
			body[key], err = idString(v)
			if err != nil {
				return nil, err
			}
		}
	}
	for _, k := range []string{"expected_item_count", "expected_result_count"} {
		if v := body[k]; v != nil {
			body[k], err = count(v, 0)
			if err != nil {
				return nil, err
			}
		}
	}
	b, err := encode(body)
	if err != nil {
		return nil, err
	}
	receipt, err := call(ctx, c, request{method: "POST", path: experimentsPath, body: b, retry: true}, experimentParser(id, ""))
	if err != nil {
		var failure *APIError
		if !errors.As(err, &failure) {
			failure = &APIError{Code: "cancelled", Persistence: NotSent, cause: err}
		}
		failure.Submission = body
		return nil, failure
	}
	e, _ := c.ResumeExperiment(id)
	e.submission = b
	e.receipt.Store(&receipt)
	return e, nil
}
func experimentParser(id, status string) func(Fields) (ExperimentReceipt, error) {
	return func(f Fields) (ExperimentReceipt, error) {
		if !validIDs(f, "id") || !utcFields(f, "started_at", "created_at", "finished_at") {
			return ExperimentReceipt{}, errRecord
		}
		r, err := record[ExperimentReceipt](f)
		if err != nil || !required(f, "id", "status", "created", "started_at", "created_at", "accepted_item_count", "accepted_result_count") ||
			!present(f, "finished_at", "expected_item_count", "expected_result_count") || r.ID != id ||
			!enum(r.Status, "uploading", "completed", "failed") || (status != "" && r.Status != status) ||
			r.AcceptedItemCount < 0 || r.AcceptedResultCount < 0 || (r.ExpectedItemCount != nil && *r.ExpectedItemCount < 0) || (r.ExpectedResultCount != nil && *r.ExpectedResultCount < 0) {
			return ExperimentReceipt{}, errRecord
		}
		return r, nil
	}
}
func serialized[T any](ctx context.Context, e *Experiment, fn func() (T, error)) (T, error) {
	var zero T
	select {
	case <-ctx.Done():
	case <-e.client.closed.Done():
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	}
	if e.client.closed.Err() != nil {
		return zero, closedError()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return fn()
}
func (e *Experiment) finish(ctx context.Context, action, status string) (ExperimentReceipt, error) {
	return serialized(ctx, e, func() (ExperimentReceipt, error) {
		r, err := call(ctx, e.client, request{method: "POST", path: e.path() + action + "/", body: []byte("{}"), retry: true}, experimentParser(e.id, status))
		if err != nil {
			return ExperimentReceipt{}, err
		}
		e.receipt.Store(&r)
		return *e.Receipt(), nil
	})
}

// Complete marks the run complete after the service verifies any expected counts.
// Individual cases can have failed scores in a completed run.
func (e *Experiment) Complete(ctx context.Context) (ExperimentReceipt, error) {
	return e.finish(ctx, "complete", "completed")
}

// Fail marks the run failed and retains items and results already accepted.
func (e *Experiment) Fail(ctx context.Context) (ExperimentReceipt, error) {
	return e.finish(ctx, "fail", "failed")
}
func (e *Experiment) tags(ctx context.Context, method string, body []byte) ([]string, error) {
	return serialized(ctx, e, func() ([]string, error) {
		return call(ctx, e.client, request{method: method, path: e.path(), body: body}, func(f Fields) ([]string, error) {
			if !validIDs(f, "id") || f["id"] != e.id {
				return nil, errRecord
			}
			return stringsOf(f["tags"])
		})
	})
}

// GetTags reads the current tags from the service.
func (e *Experiment) GetTags(ctx context.Context) ([]string, error) { return e.tags(ctx, "GET", nil) }

// SetTags replaces all experiment tags. Pass an empty slice to clear them.
func (e *Experiment) SetTags(ctx context.Context, tags []string) ([]string, error) {
	if tags == nil {
		tags = []string{}
	}
	if _, err := checkTags(tags, 100); err != nil {
		return nil, err
	}
	b, err := encode(Fields{"tags": tags})
	if err != nil {
		return nil, err
	}
	return e.tags(ctx, "PATCH", b)
}
