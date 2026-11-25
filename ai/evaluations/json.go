package evaluations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Fields contains wire-format (snake_case) properties. A missing key omits the
// property. A present nil value sends JSON null. Returned JSON numbers use
// json.Number so integers in dataset content and saved declarations stay exact.
type Fields = map[string]any

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

func validText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func identifier(v any, limit int) bool {
	s, ok := v.(string)
	return ok && s != "" && validText(s) && utf8.RuneCountInString(s) <= limit
}
func idString(v any) (string, error) {
	s, ok := v.(string)
	if ok && uuid.Validate(s) == nil {
		if id, err := uuid.Parse(s); err == nil {
			return id.String(), nil
		}
	}
	return "", errors.New("evaluations: expected a UUID")
}
func validIDs(f Fields, keys ...string) bool {
	for _, k := range keys {
		id, err := idString(f[k])
		if err != nil {
			return false
		}
		f[k] = id
	}
	return true
}
func instant(v any) (string, error) {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case string:
		var err error
		t, err = time.Parse(time.RFC3339Nano, strings.Replace(x, " ", "T", 1))
		if err != nil {
			return "", errors.New("evaluations: expected timestamp with timezone")
		}
	default:
		return "", errors.New("evaluations: expected timestamp with timezone")
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}
func allowed(fields Fields, keys ...string) error {
	for k := range fields {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("evaluations: unsupported field %q", k)
		}
	}
	return nil
}
func copyFields(f Fields) Fields {
	out := make(Fields, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out
}
func required(f Fields, keys ...string) bool {
	for _, k := range keys {
		if f[k] == nil {
			return false
		}
	}
	return true
}
func present(f Fields, keys ...string) bool {
	for _, k := range keys {
		if _, ok := f[k]; !ok {
			return false
		}
	}
	return true
}
func enum(v any, options ...string) bool {
	s, ok := v.(string)
	return ok && slices.Contains(options, s)
}
func identifiers(f Fields, keys ...string) error {
	for _, k := range keys {
		if v := f[k]; v != nil && !identifier(v, 255) {
			return fmt.Errorf("evaluations: invalid %s", k)
		}
	}
	return nil
}

// jsonValue accepts only JSON-native values and walks caller-owned collections
// before encoding. The depth bound also terminates cyclic maps and slices.
func jsonValue(v any, depth int) (any, error) {
	if v == nil {
		return nil, nil
	}
	if n, ok := v.(json.Number); ok {
		if _, err := json.Marshal(n); err == nil && n != "" {
			if strings.ContainsAny(string(n), ".eE") {
				if _, err := n.Float64(); err != nil {
					return nil, errors.New("evaluations: invalid JSON number")
				}
			}
			return n, nil
		}
		return nil, errors.New("evaluations: invalid JSON number")
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Bool:
		return r.Bool(), nil
	case reflect.String:
		if validText(r.String()) {
			return r.String(), nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return r.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return r.Uint(), nil
	case reflect.Float32, reflect.Float64:
		if !math.IsNaN(r.Float()) && !math.IsInf(r.Float(), 0) {
			return r.Float(), nil
		}
	case reflect.Map:
		if r.Type().Key().Kind() != reflect.String || depth > 32 {
			break
		}
		if r.IsNil() {
			return nil, nil
		}
		data := make(Fields, r.Len())
		iter := r.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			if !validText(key) {
				return nil, errors.New("evaluations: invalid JSON key")
			}
			value, err := jsonValue(iter.Value().Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			data[key] = value
		}
		return data, nil
	case reflect.Array, reflect.Slice:
		if depth > 32 || r.Type().Elem().Kind() == reflect.Uint8 {
			break
		}
		if r.Kind() == reflect.Slice && r.IsNil() {
			return nil, nil
		}
		values := make([]any, r.Len())
		for i := range values {
			value, err := jsonValue(r.Index(i).Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		return values, nil
	}
	return nil, errors.New("evaluations: expected valid JSON with at most 32 container levels")
}
func jsonObject(v any) (Fields, error) {
	value, err := jsonValue(v, 1)
	if err != nil {
		return nil, err
	}
	obj, ok := value.(Fields)
	if !ok {
		return nil, errors.New("evaluations: expected JSON object")
	}
	return obj, nil
}
func number(v any) (json.Number, error) {
	value, err := jsonValue(v, 1)
	if err == nil {
		switch x := value.(type) {
		case json.Number:
			return x, nil
		case int64:
			return json.Number(strconv.FormatInt(x, 10)), nil
		case uint64:
			return json.Number(strconv.FormatUint(x, 10)), nil
		case float64:
			return json.Number(strconv.FormatFloat(x, 'g', -1, 64)), nil
		}
	}
	return "", errors.New("evaluations: expected a number")
}
func count(v any, minValue int64) (int64, error) {
	// Standard JSON decoding into Fields produces float64. Beyond the safe
	// integer range adjacent counts can decode to the same float.
	r := reflect.ValueOf(v)
	if r.Kind() == reflect.Float64 || r.Kind() == reflect.Float32 {
		x := r.Float()
		if math.Abs(x) < 1<<53 && math.Trunc(x) == x && int64(x) >= minValue {
			return int64(x), nil
		}
	} else if n, err := number(v); err == nil {
		i, err := n.Int64()
		if err == nil && i >= minValue {
			return i, nil
		}
	}
	return 0, errors.New("evaluations: invalid integer")
}
func stringsOf(v any) ([]string, error) {
	values, err := jsonValue(v, 1)
	if err != nil {
		return nil, err
	}
	list, ok := values.([]any)
	if !ok {
		return nil, errors.New("evaluations: expected a list of strings")
	}
	out := make([]string, len(list))
	for i, entry := range list {
		s, ok := entry.(string)
		if !ok {
			return nil, errors.New("evaluations: expected a string")
		}
		out[i] = s
	}
	return out, nil
}
func checkTags(v any, maxTags int) ([]string, error) {
	list, err := stringsOf(v)
	if err != nil {
		return nil, err
	}
	if len(list) > maxTags {
		return nil, errors.New("evaluations: too many tags")
	}
	for _, tag := range list {
		if !identifier(strings.TrimSpace(tag), 255) {
			return nil, errors.New("evaluations: invalid tag")
		}
	}
	return list, nil
}
func record[T any](data Fields) (T, error) {
	var out T
	b, err := encode(data)
	if err == nil {
		err = decode(b, &out)
	}
	return out, err
}

var errRecord = errors.New("evaluations: malformed record")

func utcFields(f Fields, keys ...string) bool {
	for _, key := range keys {
		if v := f[key]; v != nil {
			s, err := instant(v)
			if err != nil {
				return false
			}
			f[key] = s
		}
	}
	return true
}
