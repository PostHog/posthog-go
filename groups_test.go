package posthog

import (
	"reflect"
	"testing"
)

func TestGroups(t *testing.T) {
	number := 5

	tests := map[string]struct {
		ref Groups
		run func(Groups)
	}{
		"company": {Groups{"company": number}, func(g Groups) { g.Set("company", number) }},
	}

	for name, test := range tests {
		group := NewGroups()
		test.run(group)

		if !reflect.DeepEqual(group, test.ref) {
			t.Errorf("%s: invalid groups produced: %#v\n", name, group)
		}
	}
}
