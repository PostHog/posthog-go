package evaluations

import (
	"context"
	"errors"
	"net/url"
	"strconv"
)

func (e *Experiment) revision(ctx context.Context, method, resource, id, suffix string, revision int64, changes Fields) (int64, error) {
	id, err := idString(id)
	if err != nil {
		return 0, err
	}
	if revision < 0 {
		return 0, errors.New("evaluations: revision must be nonnegative")
	}
	r := request{method: method, path: e.path() + resource + "/" + id + "/" + suffix}
	switch method {
	case "PATCH":
		if len(changes) == 0 {
			return 0, errors.New("evaluations: supply at least one changed field")
		}
		changes["revision"] = revision
		r.body, err = encode(changes)
		if err != nil {
			return 0, err
		}
	case "DELETE":
		r.query = url.Values{"revision": {strconv.FormatInt(revision, 10)}}
	}
	return serialized(ctx, e, func() (int64, error) {
		return call(ctx, e.client, r, func(f Fields) (int64, error) {
			if method == "DELETE" {
				return 0, nil
			}
			if method == "GET" && (!validIDs(f, "id") || f["id"] != id) {
				return 0, errRecord
			}
			return count(f["revision"], revision)
		})
	})
}

// GetItemRevision reads the concurrency token for the item's next edit or deletion.
func (e *Experiment) GetItemRevision(ctx context.Context, id string) (int64, error) {
	return e.revision(ctx, "GET", "items", id, "", 0, nil)
}

// GetResultRevision reads the concurrency token for the result's next edit or deletion.
func (e *Experiment) GetResultRevision(ctx context.Context, id string) (int64, error) {
	return e.revision(ctx, "GET", "results", id, "payload/", 0, nil)
}

// UpdateItem corrects case_key, trial, model_version or application_trace_id.
// An explicit nil clears a field.
func (e *Experiment) UpdateItem(ctx context.Context, id string, revision int64, changes Fields) (int64, error) {
	if err := allowed(changes, editableItemKeys...); err != nil {
		return 0, err
	}
	if err := identifiers(changes, editableItemKeys...); err != nil {
		return 0, err
	}
	return e.revision(ctx, "PATCH", "items", id, "", revision, copyFields(changes))
}

// UpdateResult corrects the supplied status, value and/or error_code.
func (e *Experiment) UpdateResult(ctx context.Context, id string, revision int64, changes Fields) (int64, error) {
	if err := allowed(changes, "status", "value", "error_code"); err != nil {
		return 0, err
	}
	body := copyFields(changes)
	value, hasValue := body["value"]
	code := body["error_code"]
	if s, ok := body["status"]; ok {
		if !validStatus(s) || (s != "ok" && value != nil) || (s == "ok" && hasValue && value == nil) || (s != "error" && code != nil) {
			return 0, errors.New("evaluations: inconsistent status and score fields")
		}
	}
	if code != nil && (!identifier(code, 128) || value != nil) {
		return 0, errors.New("evaluations: invalid score/error code combination")
	}
	if value != nil {
		v, err := score(value)
		if err != nil {
			return 0, err
		}
		body["value"] = v
	}
	return e.revision(ctx, "PATCH", "results", id, "", revision, body)
}

// UpdateItemPayload replaces the supplied input, output, expected_output, or
// metadata fields and returns the new revision. Omitted fields keep their values.
// A nil field value writes JSON null. Pass Fields{} to keep existing fields or
// create an empty payload within the retention window. A nil map is invalid.
// The patch and merged payload must each fit in 1 MiB of encoded JSON.
func (e *Experiment) UpdateItemPayload(ctx context.Context, id string, revision int64, payload Fields) (int64, error) {
	p, err := checkedPayload(payload, itemPayloadKeys, 1<<20)
	if err != nil {
		return 0, err
	}
	return e.revision(ctx, "PATCH", "items", id, "payload/", revision, Fields{"payload": p})
}

// UpdateResultPayload replaces the supplied reasoning, error_message, or metadata
// fields and returns the new revision. Omitted fields keep their values. A nil
// field value writes JSON null. Pass Fields{} to keep existing fields or create
// an empty payload within the retention window. A nil map is invalid.
// The patch and merged payload must each fit in 256 KiB of encoded JSON.
func (e *Experiment) UpdateResultPayload(ctx context.Context, id string, revision int64, payload Fields) (int64, error) {
	p, err := checkedPayload(payload, resultPayloadKeys, 256<<10)
	if err != nil {
		return 0, err
	}
	return e.revision(ctx, "PATCH", "results", id, "payload/", revision, Fields{"payload": p})
}

// DeleteItem removes the item, its scores, and their payloads at the observed revision.
func (e *Experiment) DeleteItem(ctx context.Context, id string, revision int64) error {
	_, err := e.revision(ctx, "DELETE", "items", id, "", revision, nil)
	return err
}

// DeleteResult removes the reported result, its payload, and dependent composite results.
func (e *Experiment) DeleteResult(ctx context.Context, id string, revision int64) error {
	_, err := e.revision(ctx, "DELETE", "results", id, "", revision, nil)
	return err
}

// DeleteItemPayload removes stored item content and retains the item and its scores.
func (e *Experiment) DeleteItemPayload(ctx context.Context, id string, revision int64) error {
	_, err := e.revision(ctx, "DELETE", "items", id, "payload/", revision, nil)
	return err
}

// DeleteResultPayload removes stored result content and retains the score.
func (e *Experiment) DeleteResultPayload(ctx context.Context, id string, revision int64) error {
	_, err := e.revision(ctx, "DELETE", "results", id, "payload/", revision, nil)
	return err
}
