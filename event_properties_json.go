package posthog

import (
	stdjson "encoding/json"
	"math"
	"reflect"

	json "github.com/goccy/go-json"
)

func isNonFiniteJSONError(err error) bool {
	// Do not unwrap MarshalerError: errors from caller-supplied marshalers must propagate.
	var value reflect.Value
	switch unsupported := err.(type) {
	case *json.UnsupportedValueError:
		value = unsupported.Value
	case *stdjson.UnsupportedValueError:
		value = unsupported.Value
	default:
		return false
	}
	if value.Kind() != reflect.Float32 && value.Kind() != reflect.Float64 {
		return false
	}
	return math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0)
}

func marshalEventJSON(value interface{}) ([]byte, error) {
	data, err := json.Marshal(value)
	// go-json can emit bare non-finite float32 tokens without reporting an error.
	if err == nil && !stdjson.Valid(data) {
		return stdjson.Marshal(value)
	}
	return data, err
}

type jsonPropertyVisit struct {
	typ     reflect.Type
	pointer uintptr
	length  int
}

func marshalEventProperties(properties Properties) (Properties, error) {
	normalized := make(Properties, len(properties))
	for key, value := range properties {
		data, err := marshalEventProperty(value, make(map[jsonPropertyVisit]bool))
		if err != nil {
			return nil, err
		}
		normalized[key] = json.RawMessage(data)
	}
	return normalized, nil
}

func marshalEventProperty(value interface{}, seen map[jsonPropertyVisit]bool) ([]byte, error) {
	data, err := marshalEventJSON(value)
	if !isNonFiniteJSONError(err) {
		return data, err
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Ptr:
		visit := jsonPropertyVisit{typ: v.Type(), pointer: v.Pointer()}
		if v.Kind() != reflect.Ptr {
			visit.length = v.Len()
		}
		if seen[visit] {
			return nil, &json.UnsupportedValueError{Value: v, Str: "encountered a cycle via " + v.Type().String()}
		}
		seen[visit] = true
		defer delete(seen, visit)
	}
	switch v.Kind() {
	case reflect.Ptr:
		return marshalEventProperty(v.Elem().Interface(), seen)
	case reflect.Map:
		out := reflect.MakeMapWithSize(reflect.MapOf(v.Type().Key(), reflect.TypeOf(json.RawMessage{})), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			child, err := marshalEventProperty(iter.Value().Interface(), seen)
			if err != nil {
				return nil, err
			}
			out.SetMapIndex(iter.Key(), reflect.ValueOf(json.RawMessage(child)))
		}
		return json.Marshal(out.Interface())
	case reflect.Slice, reflect.Array:
		out := make([]json.RawMessage, v.Len())
		for i := range out {
			child, err := marshalEventProperty(v.Index(i).Interface(), seen)
			if err != nil {
				return nil, err
			}
			out[i] = child
		}
		return json.Marshal(out)
	default:
		// An arbitrary struct with a non-finite field is discarded as one property.
		// Keeping its field/tag/custom-marshaler semantics belongs to the JSON encoder.
		return []byte("null"), nil
	}
}
