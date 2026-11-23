package evaluations

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"slices"
	"strings"

	"github.com/google/uuid"
)

var itemPayloadKeys = []string{"input", "output", "expected_output", "metadata"}
var resultPayloadKeys = []string{"reasoning", "error_message", "metadata"}
var editableItemKeys = []string{"case_key", "trial", "model_version", "application_trace_id"}
var itemKeys = []string{"id", "case_key", "trial", "model_version", "application_trace_id", "dataset_item_identifier", "dataset_item_version_identifier", "dataset_item_version_id", "payload"}
var resultKeys = []string{"status", "value", "error_code", "evaluated_at", "evaluator_trace_id", "calculate", "payload"}

// Item is an immutable execution declaration or a reference to an accepted execution.
type Item struct{ id, wire string }

func (i Item) ID() string { return i.id }

// MarshalJSON returns the declaration to save separately from its results.
func (i Item) MarshalJSON() ([]byte, error) {
	if i.wire == "" {
		return nil, errors.New("evaluations: item has no declaration")
	}
	return []byte(i.wire), nil
}

// ItemReference refers to a previously accepted item without redeclaring its content.
func ItemReference(id string) (Item, error) {
	id, err := idString(id)
	return Item{id: id}, err
}

// NewItem snapshots a declaration, generating its UUID if omitted. Supply payload
// fields individually or under "payload". Model and provenance fields use the
// wire names model_version, case_key, trial, application_trace_id,
// dataset_item_identifier, dataset_item_version_identifier, and dataset_item_version_id.
func NewItem(fields Fields) (Item, error) {
	body := copyFields(fields)
	if err := collectPayload(body, itemPayloadKeys, 1<<20); err != nil {
		return Item{}, err
	}
	if err := allowed(body, itemKeys...); err != nil {
		return Item{}, err
	}
	if err := identifiers(body, "case_key", "trial", "model_version", "application_trace_id", "dataset_item_identifier", "dataset_item_version_identifier"); err != nil {
		return Item{}, err
	}
	if _, ok := body["id"]; !ok {
		body["id"] = uuid.NewString()
	}
	id, err := idString(body["id"])
	if err != nil {
		return Item{}, err
	}
	body["id"] = id
	for k, v := range body {
		if v == nil {
			delete(body, k)
		}
	}
	if v := body["dataset_item_version_id"]; v != nil {
		body["dataset_item_version_id"], err = idString(v)
		if err != nil {
			return Item{}, err
		}
	}
	b, err := encode(body)
	return Item{id: id, wire: string(b)}, err
}

func collectPayload(body Fields, keys []string, limit int) error {
	data, has := body["payload"]
	individual := Fields{}
	for _, key := range keys {
		if value, ok := body[key]; ok {
			individual[key] = value
			delete(body, key)
		}
	}
	if len(individual) > 0 {
		if has {
			return errors.New("evaluations: supply payload or individual fields")
		}
		data = individual
	} else if !has {
		return nil
	}
	payload, err := checkedPayload(data, keys, limit)
	if err == nil {
		body["payload"] = payload
	}
	return err
}
func checkedPayload(data any, keys []string, limit int) (Fields, error) {
	p, err := jsonObject(data)
	if err != nil {
		return nil, err
	}
	if err := allowed(p, keys...); err != nil {
		return nil, err
	}
	if p["metadata"] != nil {
		if _, ok := p["metadata"].(Fields); !ok {
			return nil, errors.New("evaluations: metadata must be an object")
		}
	}
	for _, k := range []string{"reasoning", "error_message"} {
		if p[k] != nil {
			if _, ok := p[k].(string); !ok {
				return nil, errors.New("evaluations: expected a text payload field")
			}
		}
	}
	b, err := encode(p)
	if len(b) > limit {
		return nil, errors.New("evaluations: payload exceeds byte limit")
	}
	return p, err
}
func RestoreItem(data []byte) (Item, error) {
	var f Fields
	if err := decode(data, &f); err != nil {
		return Item{}, err
	}
	if err := allowed(f, itemKeys...); err != nil {
		return Item{}, err
	}
	if f["id"] == nil {
		return Item{}, errors.New("evaluations: saved item requires an ID")
	}
	return NewItem(f)
}

// Result is an immutable reported outcome or request for a server-calculated score.
type Result struct {
	item         Item
	scorer, wire string
	calculate    bool
}

func (r Result) ItemID() string          { return r.item.id }
func (r Result) ScorerVersionID() string { return r.scorer }
func (r Result) MarshalJSON() ([]byte, error) {
	if r.wire == "" {
		return nil, errors.New("evaluations: uninitialized result")
	}
	return []byte(r.wire), nil
}
func validStatus(v any) bool { return enum(v, "ok", "error", "skipped", "not_applicable") }

func score(v any) (any, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	if n, err := number(v); err == nil {
		f, err := n.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errors.New("evaluations: score must be finite")
		}
		// Go floats are already binary64 values. Exact integer inputs and saved
		// integer literals must round-trip through binary64 without any change.
		switch v.(type) {
		case float64, float32:
			return floatScore(f), nil
		}
		if !strings.ContainsAny(string(n), ".eE") {
			i, ok := new(big.Int).SetString(string(n), 10)
			asInt, _ := new(big.Float).SetFloat64(f).Int(nil)
			if !ok || i.Cmp(asInt) != 0 {
				return nil, errors.New("evaluations: integer score is not exactly representable as binary64")
			}
			return n, nil
		}
		return floatScore(f), nil
	}
	list, err := stringsOf(v)
	if err != nil || len(list) == 0 {
		return nil, errors.New("evaluations: expected a boolean, finite number, or nonempty category list")
	}
	slices.Sort(list)
	for i, k := range list {
		if !identifier(k, 128) || (i > 0 && list[i-1] == k) {
			return nil, errors.New("evaluations: invalid or duplicate category")
		}
	}
	return list, nil
}
func floatScore(f float64) any {
	if math.Trunc(f) == f {
		i, _ := new(big.Float).SetFloat64(f).Int(nil)
		return json.Number(i.String())
	}
	return f
}

// NewResult accepts status (default "ok"), value, error_code, evaluated_at,
// evaluator_trace_id and payload, or calculate=true for a composite scorer.
func NewResult(item Item, scorerVersionID string, fields Fields) (Result, error) {
	if item.id == "" {
		return Result{}, errors.New("evaluations: expected an item or item reference")
	}
	scorer, err := idString(scorerVersionID)
	if err != nil {
		return Result{}, err
	}
	body := copyFields(fields)
	if err := collectPayload(body, resultPayloadKeys, 256<<10); err != nil {
		return Result{}, err
	}
	if err := allowed(body, resultKeys...); err != nil {
		return Result{}, err
	}
	calculated := false
	if v, ok := body["calculate"]; ok {
		calculated, ok = v.(bool)
		if !ok {
			return Result{}, errors.New("evaluations: calculate must be boolean")
		}
	}
	status, hasStatus := body["status"]
	if !hasStatus {
		status = "ok"
	}
	if !validStatus(status) {
		return Result{}, errors.New("evaluations: invalid result status")
	}
	for k, v := range body {
		if v == nil {
			delete(body, k)
		}
	}
	if calculated {
		delete(body, "status")
		if status != "ok" || len(body) != 1 {
			return Result{}, errors.New("evaluations: calculated results cannot supply outcome fields")
		}
	} else {
		delete(body, "calculate")
		body["status"] = status
		if status == "ok" {
			body["value"], err = score(body["value"])
			if err != nil {
				return Result{}, err
			}
		} else if body["value"] != nil {
			return Result{}, errors.New("evaluations: only ok results accept scores")
		}
		p, _ := body["payload"].(Fields)
		if status != "error" && (body["error_code"] != nil || p["error_message"] != nil) {
			return Result{}, errors.New("evaluations: error fields require error status")
		}
		if v := body["error_code"]; v != nil && !identifier(v, 128) {
			return Result{}, errors.New("evaluations: invalid error code")
		}
		if err := identifiers(body, "evaluator_trace_id"); err != nil {
			return Result{}, err
		}
		if v := body["evaluated_at"]; v != nil {
			body["evaluated_at"], err = instant(v)
			if err != nil {
				return Result{}, err
			}
		}
	}
	body["item_id"] = item.id
	body["scorer_version_id"] = scorer
	b, err := encode(body)
	return Result{item: item, scorer: scorer, wire: string(b), calculate: calculated}, err
}

// RestoreResult optionally reattaches the independently saved item declaration.
func RestoreResult(data []byte, attached *Item) (Result, error) {
	var f Fields
	if err := decode(data, &f); err != nil {
		return Result{}, err
	}
	id, err := idString(f["item_id"])
	if err != nil {
		return Result{}, err
	}
	scorer, err := idString(f["scorer_version_id"])
	if err != nil {
		return Result{}, err
	}
	delete(f, "item_id")
	delete(f, "scorer_version_id")
	if err := allowed(f, resultKeys...); err != nil {
		return Result{}, err
	}
	item := Item{id: id}
	if attached != nil {
		if attached.id != id {
			return Result{}, errors.New("evaluations: mismatched attached item")
		}
		item = *attached
	}
	return NewResult(item, scorer, f)
}
