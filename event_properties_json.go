package posthog

import (
	"encoding"
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

func invalidEventJSON(data []byte) error {
	var raw stdjson.RawMessage
	return stdjson.Unmarshal(data, &raw)
}

func marshalEventJSON(value interface{}) ([]byte, error) {
	data, err := json.Marshal(value)
	if err == nil && !stdjson.Valid(data) {
		return nil, invalidEventJSON(data)
	}
	return data, err
}

type jsonPropertyVisit struct {
	typ     reflect.Type
	pointer uintptr
	length  int
}

func normalizeEventProperties(properties Properties) (Properties, error) {
	if properties == nil {
		return nil, nil
	}
	normalized := make(Properties, len(properties))
	seen := make(map[jsonPropertyVisit]bool)
	for key, value := range properties {
		child, err := normalizeEventProperty(reflect.ValueOf(value), seen)
		if err != nil {
			return nil, err
		}
		normalized[key] = child
	}
	return normalized, nil
}

func normalizeEventProperty(v reflect.Value, seen map[jsonPropertyVisit]bool) (interface{}, error) {
	if !v.IsValid() {
		return nil, nil
	}
	if (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) && v.IsNil() {
		return nil, nil
	}
	value := v.Interface()
	if v.CanAddr() {
		value = v.Addr().Interface()
	}
	switch value.(type) {
	case json.Marshaler, json.MarshalerContext, encoding.TextMarshaler:
		return marshalOpaqueEventProperty(value, true)
	}
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Ptr:
		if v.IsNil() {
			return nil, nil
		}
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
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(v.Float()) || math.IsInf(v.Float(), 0) {
			return nil, nil
		}
	case reflect.Ptr, reflect.Interface:
		return normalizeEventProperty(v.Elem(), seen)
	case reflect.Map:
		out := reflect.MakeMapWithSize(reflect.MapOf(v.Type().Key(), reflect.TypeOf((*interface{})(nil)).Elem()), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			child, err := normalizeEventProperty(iter.Value(), seen)
			if err != nil {
				return nil, err
			}
			slot := reflect.New(out.Type().Elem()).Elem()
			if child != nil {
				slot.Set(reflect.ValueOf(child))
			}
			out.SetMapIndex(iter.Key(), slot)
		}
		return out.Interface(), nil
	case reflect.Slice:
		// Byte slices use the encoder's base64 representation, not JSON array slots.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return marshalOpaqueEventProperty(value, false)
		}
		fallthrough
	case reflect.Array:
		if v.Kind() == reflect.Array && !v.CanAddr() {
			// Match go-json's addressable array elements, not the array's own pointer methods.
			copy := reflect.New(v.Type()).Elem()
			copy.Set(v)
			v = copy
		}
		out := make([]interface{}, v.Len())
		for i := range out {
			child, err := normalizeEventProperty(v.Index(i), seen)
			if err != nil {
				return nil, err
			}
			out[i] = child
		}
		return out, nil
	case reflect.Struct:
		return marshalOpaqueEventProperty(value, false)
	}
	return v.Interface(), nil
}

func marshalOpaqueEventProperty(value interface{}, custom bool) (interface{}, error) {
	data, err := json.Marshal(value)
	if !custom && isNonFiniteJSONError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !stdjson.Valid(data) {
		if !custom {
			// go-json silently emits non-finite float32 tokens in ordinary structs.
			// Use the standard encoder only for diagnostics, never its replacement bytes.
			if _, diagnostic := stdjson.Marshal(value); isNonFiniteJSONError(diagnostic) {
				return nil, nil
			}
		}
		return nil, invalidEventJSON(data)
	}
	return json.RawMessage(data), nil
}
