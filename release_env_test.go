package posthog

import "testing"

func TestNormalizeReleaseID(t *testing.T) {
	if got := normalizeReleaseID(""); got != nil {
		t.Errorf("unset: want nil, got %q", *got)
	}
	if got := normalizeReleaseID("   "); got != nil {
		t.Errorf("blank: want nil, got %q", *got)
	}
	const id = "01a04245-8c54-0000-7530-28eed93002b0"
	if got := normalizeReleaseID("  " + id + "  "); got == nil || *got != id {
		t.Errorf("value: want %q trimmed, got %v", id, got)
	}
}

// forceReleaseID pins the cached POSTHOG_RELEASE_ID value for a test and restores it afterwards,
// bypassing the process-global env read (which is behind a sync.Once).
func forceReleaseID(t *testing.T, value *string) {
	t.Helper()
	releaseIDOnce.Do(func() {}) // spend the Once so releaseIDFromEnv returns releaseIDValue
	old := releaseIDValue
	releaseIDValue = value
	t.Cleanup(func() { releaseIDValue = old })
}

func TestReleaseIDIsAddedOnlyToExceptionEvents(t *testing.T) {
	const id = "01a04245-8c54-0000-7530-28eed93002b0"
	forceReleaseID(t, Ptr(id))

	exc := Exception{
		DistinctId:    "user-1",
		ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}},
	}

	// v0 / callback path.
	api, ok := exc.APIfy().(ExceptionInApi)
	if !ok {
		t.Fatalf("APIfy did not return ExceptionInApi")
	}
	if api.Properties.ReleaseId == nil || *api.Properties.ReleaseId != id {
		t.Errorf("APIfy: want $release_id %q, got %v", id, api.Properties.ReleaseId)
	}

	// v1 path.
	if got, _ := exc.apifyEvent().properties["$release_id"].(string); got != id {
		t.Errorf("apifyEvent: want $release_id %q, got %q", id, got)
	}

	// A non-exception event must not carry it — the release is only resolved on exceptions.
	capEv := Capture{Event: "custom_event", DistinctId: "user-1"}.apifyEvent()
	if _, present := capEv.properties["$release_id"]; present {
		t.Errorf("capture event must not carry $release_id")
	}
}

func TestReleaseIDAbsentWhenUnset(t *testing.T) {
	forceReleaseID(t, nil)

	exc := Exception{
		DistinctId:    "user-1",
		ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}},
	}
	if api := exc.APIfy().(ExceptionInApi); api.Properties.ReleaseId != nil {
		t.Errorf("unset: want nil $release_id, got %q", *api.Properties.ReleaseId)
	}
	if _, present := exc.apifyEvent().properties["$release_id"]; present {
		t.Errorf("unset: exception must not carry $release_id")
	}
}
