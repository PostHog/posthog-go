package posthog

import (
	"testing"
)

func TestSizeLimitedMap(t *testing.T) {
	newMap := newSizeLimitedMap(1)

	newMap.add("some-key", "some-element")

	if newMap.count() != 1 {
		t.Error("Map should have 1 element")
	}

	newMap.add("some-key", "new-element")

	if newMap.count() != 1 {
		t.Error("Map should still have 1 element")
	}

	if !newMap.contains("some-key", "new-element") {
		t.Error("Should contain element")
	}
}
