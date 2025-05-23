package posthog

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGroups(t *testing.T) {
	number := 5

	tests := []struct {
		name string
		want Groups
		run  func(Groups)
	}{
		{
			"company",
			Groups{"company": number},
			func(g Groups) { g.Set("company", number) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			group := NewGroups()
			test.run(group)
			require.EqualValues(t, test.want, group)
		})
	}
}
