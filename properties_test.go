package posthog

import (
	"reflect"
	"testing"
)

func TestPropertiesSimple(t *testing.T) {
	text := "ABC"
	number := 0.5

	tests := map[string]struct {
		ref Properties
		run func(Properties)
	}{
		"revenue":  {Properties{"revenue": number}, func(p Properties) { p.Set("revenue", number) }},
		"currency": {Properties{"currency": text}, func(p Properties) { p.Set("currency", text) }},
	}

	for name, test := range tests {
		prop := NewProperties()
		test.run(prop)

		if !reflect.DeepEqual(prop, test.ref) {
			t.Errorf("%s: invalid properties produced: %#v\n", name, prop)
		}
	}
}

func TestPropertiesMulti(t *testing.T) {
	p0 := Properties{"title": "A", "value": 0.5}
	p1 := NewProperties().Set("title", "A").Set("value", 0.5)

	if !reflect.DeepEqual(p0, p1) {
		t.Errorf("invalid properties produced by chained setters:\n- expected %#v\n- found: %#v", p0, p1)
	}
}

func TestPropertiesMerge(t *testing.T) {
	defaultProps := Properties{"currency": "USD", "service": "api"}

	props := NewProperties().Set("title", "A").Set("value", 0.5).Set("currency", "BRL")
	props.Merge(defaultProps)

	expected := Properties{"title": "A", "value": 0.5, "currency": "USD", "service": "api"}

	if !reflect.DeepEqual(props, expected) {
		t.Errorf("invalid properties produced by merge:\n- expected %#v\n- found: %#v", expected, props)
	}
}

func TestPropertiesMergeNil(t *testing.T) {
	props := NewProperties().Set("title", "A")
	props.Merge(nil)

	expected := Properties{"title": "A"}

	if !reflect.DeepEqual(props, expected) {
		t.Errorf("invalid properties produced by merge:\n- expected %#v\n- found: %#v", expected, props)
	}
}
