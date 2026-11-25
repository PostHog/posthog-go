package evaluations

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Optional distinguishes an omitted property (zero value), explicit null, and
// a supplied value, including false or zero. A standalone JSON value cannot
// express omission, so an unset Optional marshals as null. Enclosing SDK
// configs and options omit unset properties when marshaling. For a custom
// JSON object, omit the key when Present is false.
type Optional[T any] struct {
	Present bool
	Value   *T
}

func Value[T any](v T) Optional[T] { return Optional[T]{Present: true, Value: &v} }
func Null[T any]() Optional[T]     { return Optional[T]{Present: true} }
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if !o.Present {
		return []byte("null"), nil
	}
	return encode(o.Value)
}
func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	var value *T
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	*o = Optional[T]{Present: true, Value: value}
	return nil
}
func (o Optional[T]) put(f Fields, k string) {
	if o.Present {
		f[k] = o.Value
	}
}

// ScorerConfig pins one of the three supported output kinds.
type ScorerConfig interface {
	Kind() string
	json.Marshaler
}
type BooleanConfig struct {
	TrueLabel     *string        `json:"true_label"`
	FalseLabel    *string        `json:"false_label"`
	TrueIsFailure Optional[bool] `json:"true_is_failure"`
}

func (BooleanConfig) Kind() string { return "boolean" }
func (c BooleanConfig) MarshalJSON() ([]byte, error) {
	if (c.TrueLabel != nil && !validText(*c.TrueLabel)) || (c.FalseLabel != nil && !validText(*c.FalseLabel)) {
		return nil, errors.New("evaluations: invalid boolean scorer label")
	}
	f := Fields{}
	if c.TrueLabel != nil {
		f["true_label"] = *c.TrueLabel
	}
	if c.FalseLabel != nil {
		f["false_label"] = *c.FalseLabel
	}
	c.TrueIsFailure.put(f, "true_is_failure")
	return encode(f)
}

type NumericPassingRule struct {
	Operator  string  `json:"operator"`
	Threshold float64 `json:"threshold"`
}
type NumericConfig struct {
	Min         Optional[float64]            `json:"min"`
	Max         Optional[float64]            `json:"max"`
	Step        Optional[float64]            `json:"step"`
	PassingRule Optional[NumericPassingRule] `json:"passing_rule"`
}

func (NumericConfig) Kind() string { return "numeric" }
func (c NumericConfig) MarshalJSON() ([]byte, error) {
	if rule := c.PassingRule; rule.Present && rule.Value != nil && !validText(rule.Value.Operator) {
		return nil, errors.New("evaluations: invalid numeric passing rule operator")
	}
	f := Fields{}
	c.Min.put(f, "min")
	c.Max.put(f, "max")
	c.Step.put(f, "step")
	c.PassingRule.put(f, "passing_rule")
	return encode(f)
}

type CategoryOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}
type CategoryPassingRule struct {
	Categories []string `json:"categories"`
}
type CategoricalConfig struct {
	Options       []CategoryOption              `json:"options"`
	SelectionMode string                        `json:"selection_mode"`
	MinSelections Optional[int]                 `json:"min_selections"`
	MaxSelections Optional[int]                 `json:"max_selections"`
	PassingRule   Optional[CategoryPassingRule] `json:"passing_rule"`
}

func (CategoricalConfig) Kind() string { return "categorical" }
func (c CategoricalConfig) MarshalJSON() ([]byte, error) {
	if !validText(c.SelectionMode) {
		return nil, errors.New("evaluations: invalid categorical selection mode")
	}
	for _, option := range c.Options {
		if !validText(option.Key) || !validText(option.Label) {
			return nil, errors.New("evaluations: invalid categorical option key or label")
		}
	}
	if rule := c.PassingRule; rule.Present && rule.Value != nil {
		for _, category := range rule.Value.Categories {
			if !validText(category) {
				return nil, errors.New("evaluations: invalid passing category")
			}
		}
	}
	f := Fields{"options": c.Options}
	if c.SelectionMode != "" {
		f["selection_mode"] = c.SelectionMode
	}
	c.MinSelections.put(f, "min_selections")
	c.MaxSelections.put(f, "max_selections")
	c.PassingRule.put(f, "passing_rule")
	return encode(f)
}
func parseConfig(kind string, f Fields) (ScorerConfig, error) {
	if f == nil {
		return nil, errRecord
	}
	switch kind {
	case "boolean":
		for _, k := range []string{"true_label", "false_label"} {
			if v, ok := f[k]; ok {
				if _, ok := v.(string); !ok {
					return nil, errRecord
				}
			}
		}
		return record[BooleanConfig](f)
	case "numeric":
		if v := f["passing_rule"]; v != nil {
			r, ok := v.(Fields)
			if !ok || !enum(r["operator"], "gte", "lte") || !required(r, "threshold") {
				return nil, errRecord
			}
		}
		return record[NumericConfig](f)
	case "categorical":
		options, ok := f["options"].([]any)
		if !ok {
			return nil, errRecord
		}
		for _, v := range options {
			o, ok := v.(Fields)
			if !ok || !required(o, "key", "label") {
				return nil, errRecord
			}
		}
		if mode, ok := f["selection_mode"]; ok && !enum(mode, "single", "multiple") {
			return nil, errRecord
		}
		for _, key := range []string{"min_selections", "max_selections"} {
			if v := f[key]; v != nil {
				if _, err := count(v, 1); err != nil {
					return nil, err
				}
			}
		}
		if v := f["passing_rule"]; v != nil {
			r, ok := v.(Fields)
			if !ok {
				return nil, errRecord
			}
			if _, err := stringsOf(r["categories"]); err != nil {
				return nil, err
			}
		}
		return record[CategoricalConfig](f)
	default:
		return nil, errors.New("evaluations: invalid scorer kind")
	}
}

type CompositionInput struct {
	Alias           string `json:"alias"`
	ScorerVersionID string `json:"scorer_version_id"`
}
type Composition struct {
	Expression string             `json:"expression"`
	Inputs     []CompositionInput `json:"inputs"`
}

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func validAlias(s string) bool {
	return aliasPattern.MatchString(s) && !enum(s, "number", "min", "max", "abs", "and", "or", "not", "if", "else", "in", "is", "lambda", "for", "await")
}
func (c Composition) checked() (Composition, error) {
	if !identifier(c.Expression, 1024) || strings.TrimSpace(c.Expression) == "" || len(c.Inputs) < 1 || len(c.Inputs) > 16 {
		return Composition{}, errors.New("evaluations: invalid composite expression or input count")
	}
	c.Inputs = append([]CompositionInput(nil), c.Inputs...)
	seen := make(map[string]bool)
	for i, input := range c.Inputs {
		if !validAlias(input.Alias) || seen[input.Alias] {
			return Composition{}, errors.New("evaluations: invalid or duplicate input alias")
		}
		id, err := idString(input.ScorerVersionID)
		if err != nil {
			return Composition{}, err
		}
		c.Inputs[i].ScorerVersionID = id
		seen[input.Alias] = true
	}
	return c, nil
}
