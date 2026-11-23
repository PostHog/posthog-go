package posthog

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	gojson "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
)

type arrayRedactedProperty struct{ Value string }

func (*arrayRedactedProperty) MarshalJSON() ([]byte, error) { return []byte(`"redacted"`), nil }

type arrayRedactedContainer [1]arrayRedactedProperty

func (*arrayRedactedContainer) MarshalJSON() ([]byte, error) { return []byte(`"array-custom"`), nil }

func arrayMarshalerProperties() (Properties, map[string]interface{}) {
	row := arrayRedactedProperty{Value: "private fixture marker"}
	named := arrayRedactedContainer{row}
	props := Properties{
		"direct":       [1]arrayRedactedProperty{row},
		"nested":       [1][1]arrayRedactedProperty{{row}},
		"nested_field": [1]struct{ Field arrayRedactedProperty }{{row}},
		"map_array":    map[string]interface{}{"rows": [1]arrayRedactedProperty{row}},
		"named_value":  named, "named_pointer": &named, "named_element": [1]arrayRedactedContainer{named},
		"slice": []arrayRedactedProperty{row}, "bytes": []byte("ab"), "finite": 42.5, "literal": "NaN",
	}
	want := map[string]interface{}{
		"direct": []interface{}{"redacted"}, "nested": []interface{}{[]interface{}{"redacted"}},
		"nested_field": []interface{}{map[string]interface{}{"Field": "redacted"}},
		"map_array":    map[string]interface{}{"rows": []interface{}{"redacted"}},
		"named_value":  []interface{}{"redacted"}, "named_pointer": "array-custom", "named_element": []interface{}{"array-custom"},
		"slice": []interface{}{"redacted"}, "bytes": "YWI=", "finite": 42.5, "literal": "NaN",
	}
	return props, want
}

func TestNativeArrayMarshalerSemantics(t *testing.T) {
	props, want := arrayMarshalerProperties()
	data, err := gojson.Marshal(props)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private fixture marker")
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, want, got)
}

func TestCaptureArrayMarshalerPrivacy(t *testing.T) {
	for _, number := range []float64{42.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			props, want := arrayMarshalerProperties()
			before, _ := arrayMarshalerProperties()
			props["number"] = number
			events, bodies, cb := captureWire(t,
				Capture{Event: "array", DistinctId: "user", Properties: props},
				Capture{Event: "healthy", DistinctId: "user", Properties: Properties{"finite": 7}})
			successes, failures := cb.counts()
			require.Zero(t, failures)
			require.Equal(t, 2, successes)
			require.NotEmpty(t, bodies)
			for _, body := range bodies {
				require.NotContains(t, body, "private fixture marker")
			}
			require.Len(t, events, 2)
			require.Equal(t, "array", events[0]["event"])
			require.Equal(t, "healthy", events[1]["event"])
			got := events[0]["properties"].(map[string]interface{})
			for key, value := range want {
				require.Contains(t, got, key)
				require.Equal(t, value, got[key], key)
				require.Equal(t, before[key], props[key], "caller property %s", key)
			}
			require.Contains(t, got, "number")
			if math.IsNaN(number) || math.IsInf(number, 0) {
				require.Nil(t, got["number"])
			} else {
				require.Equal(t, number, got["number"])
			}
			if math.IsNaN(number) {
				require.True(t, math.IsNaN(props["number"].(float64)))
			} else {
				require.Equal(t, number, props["number"])
			}
			require.Equal(t, float64(7), events[1]["properties"].(map[string]interface{})["finite"])
		})
	}
}
