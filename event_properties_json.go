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

// normalizeLegacyMessage nulls NaN and Inf in the property bags the legacy
// batch encoder sends. Capture already did this. Identify, group identify,
// and exception custom properties go through the same rules so one bad
// number does not drop the rest of the event.
func normalizeLegacyMessage(apiMsg APIMessage) (APIMessage, error) {
	switch msg := apiMsg.(type) {
	case CaptureInApi:
		properties, err := normalizeEventProperties(msg.Properties)
		if err != nil {
			return nil, err
		}
		msg.Properties = properties
		return msg, nil
	case IdentifyInApi:
		set, err := normalizeEventProperties(msg.Set)
		if err != nil {
			return nil, err
		}
		msg.Set = set
		return msg, nil
	case GroupIdentifyInApi:
		properties, err := normalizeEventProperties(msg.Properties)
		if err != nil {
			return nil, err
		}
		msg.Properties = properties
		return msg, nil
	case ExceptionInApi:
		// Typed exception fields win on collision and are never sent from Custom.
		// Normalize only the keys MarshalJSON will keep. A failing or cyclic value
		// under a shadowed key, such as $lib, must not drop the exception.
		// Exception custom values are encoded with encoding/json, which honors
		// omitzero. Pre-encoding them with go-json would send zero fields the
		// standard encoder omits.
		custom, err := normalizeEventPropertiesMode(exceptionCustomWithoutShadowedKeys(msg.Properties), true)
		if err != nil {
			return nil, err
		}
		msg.Properties.Custom = custom
		return msg, nil
	default:
		return apiMsg, nil
	}
}

func normalizeEventProperties(properties Properties) (Properties, error) {
	return normalizeEventPropertiesMode(properties, false)
}

func normalizeEventPropertiesMode(properties Properties, standardJSON bool) (Properties, error) {
	if properties == nil {
		return nil, nil
	}
	normalized := make(Properties, len(properties))
	seen := make(map[jsonPropertyVisit]bool)
	for key, value := range properties {
		child, err := normalizeEventProperty(reflect.ValueOf(value), seen, standardJSON)
		if err != nil {
			return nil, err
		}
		normalized[key] = child
	}
	return normalized, nil
}

func normalizeEventProperty(v reflect.Value, seen map[jsonPropertyVisit]bool, standardJSON bool) (interface{}, error) {
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
		return marshalOpaqueEventProperty(value, true, standardJSON)
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
		return normalizeEventProperty(v.Elem(), seen, standardJSON)
	case reflect.Map:
		out := reflect.MakeMapWithSize(reflect.MapOf(v.Type().Key(), reflect.TypeOf((*interface{})(nil)).Elem()), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			child, err := normalizeEventProperty(iter.Value(), seen, standardJSON)
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
			return marshalOpaqueEventProperty(value, false, standardJSON)
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
			child, err := normalizeEventProperty(v.Index(i), seen, standardJSON)
			if err != nil {
				return nil, err
			}
			out[i] = child
		}
		return out, nil
	case reflect.Struct:
		return marshalOpaqueEventProperty(value, false, standardJSON)
	}
	return v.Interface(), nil
}

func marshalOpaqueEventProperty(value interface{}, custom bool, standardJSON bool) (interface{}, error) {
	var (
		data []byte
		err  error
	)
	if standardJSON {
		// Match ExceptionInApiProperties, which merges custom values with encoding/json.
		data, err = stdjson.Marshal(value)
	} else {
		data, err = json.Marshal(value)
	}
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
	if standardJSON {
		return stdjson.RawMessage(data), nil
	}
	return json.RawMessage(data), nil
}

// exceptionCustomWithoutShadowedKeys drops custom keys that ExceptionInApiProperties
// already emits. MarshalJSON skips those keys, so they must be ignored before
// normalization too.
func exceptionCustomWithoutShadowedKeys(properties ExceptionInApiProperties) Properties {
	custom := properties.Custom
	if len(custom) == 0 {
		return custom
	}
	typed := properties
	typed.Custom = nil
	typedBytes, err := json.Marshal(typed)
	if err != nil {
		return custom
	}
	var reserved map[string]json.RawMessage
	if err := json.Unmarshal(typedBytes, &reserved); err != nil {
		return custom
	}
	filtered := make(Properties, len(custom))
	for key, value := range custom {
		if _, shadowed := reserved[key]; shadowed {
			continue
		}
		filtered[key] = value
	}
	return filtered
}
