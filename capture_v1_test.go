package posthog

import (
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// marshalV1 builds the v1 wire event for a message and decodes it back into a
// generic map so tests can assert the on-the-wire shape.
func marshalV1(t *testing.T, msg Message) map[string]interface{} {
	t.Helper()
	data, _, uuid, err := prepareForSendV1(msg)
	if err != nil {
		t.Fatalf("prepareForSendV1: %v", err)
	}
	if uuid == "" {
		t.Fatalf("expected non-empty event uuid")
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal wire event: %v", err)
	}
	return out
}

func wireProps(t *testing.T, ev map[string]interface{}) map[string]interface{} {
	t.Helper()
	props, ok := ev["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties missing or wrong type: %T", ev["properties"])
	}
	return props
}

func wireOptions(t *testing.T, ev map[string]interface{}) map[string]interface{} {
	t.Helper()
	opts, ok := ev["options"].(map[string]interface{})
	if !ok {
		t.Fatalf("options missing or wrong type: %T", ev["options"])
	}
	return opts
}

func TestV1EventNamesAndDistinctId(t *testing.T) {
	cases := []struct {
		name           string
		msg            Message
		wantEvent      string
		wantDistinctId string
	}{
		{"capture", Capture{Uuid: "u", Event: "clicked", DistinctId: "user-1"}, "clicked", "user-1"},
		{"identify", Identify{Uuid: "u", DistinctId: "user-2"}, "$identify", "user-2"},
		{"groupidentify", GroupIdentify{Uuid: "u", Type: "company", Key: "acme"}, "$groupidentify", "$company_acme"},
		{"alias", Alias{Uuid: "u", DistinctId: "user-3", Alias: "anon-9"}, "$create_alias", "user-3"},
		{"exception", Exception{Uuid: "u", DistinctId: "user-4", ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}}}, "$exception", "user-4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := marshalV1(t, tc.msg)
			if ev["event"] != tc.wantEvent {
				t.Errorf("event = %v, want %v", ev["event"], tc.wantEvent)
			}
			if ev["distinct_id"] != tc.wantDistinctId {
				t.Errorf("distinct_id = %v, want %v", ev["distinct_id"], tc.wantDistinctId)
			}
		})
	}
}

func TestV1DropsLibFromProperties(t *testing.T) {
	// Decision B: SDK identity rides the PostHog-Sdk-Info header, never properties.
	msgs := []Message{
		Capture{Uuid: "u", Event: "e", DistinctId: "d"},
		Identify{Uuid: "u", DistinctId: "d"},
		GroupIdentify{Uuid: "u", Type: "company", Key: "acme"},
		Alias{Uuid: "u", DistinctId: "d", Alias: "a"},
		Exception{Uuid: "u", DistinctId: "d", ExceptionList: []ExceptionItem{{Type: "E", Value: "v"}}},
	}
	for _, m := range msgs {
		ev := marshalV1(t, m)
		props := wireProps(t, ev)
		if _, ok := props["$lib"]; ok {
			t.Errorf("%T: $lib must not be in v1 properties", m)
		}
		if _, ok := props["$lib_version"]; ok {
			t.Errorf("%T: $lib_version must not be in v1 properties", m)
		}
	}
}

func TestV1OptionsExtractedOnlyWhenPresent(t *testing.T) {
	// No magic props -> empty options object.
	ev := marshalV1(t, Capture{Uuid: "u", Event: "e", DistinctId: "d"})
	if opts := wireOptions(t, ev); len(opts) != 0 {
		t.Errorf("expected empty options, got %v", opts)
	}

	// Overridden magic props -> lifted into options with renamed keys and
	// removed from properties.
	ev = marshalV1(t, Capture{
		Uuid: "u", Event: "e", DistinctId: "d",
		Properties: Properties{
			propertyCookielessMode:       true,
			propertyIgnoreSentAt:         true,
			propertyProductTourId:        "tour-7",
			propertyProcessPersonProfile: false,
			"plan":                       "pro",
		},
	})
	opts := wireOptions(t, ev)
	if opts["cookieless_mode"] != true {
		t.Errorf("cookieless_mode = %v", opts["cookieless_mode"])
	}
	if opts["disable_skew_correction"] != true {
		t.Errorf("disable_skew_correction (from $ignore_sent_at) = %v", opts["disable_skew_correction"])
	}
	if opts["product_tour_id"] != "tour-7" {
		t.Errorf("product_tour_id = %v", opts["product_tour_id"])
	}
	if opts["process_person_profile"] != false {
		t.Errorf("process_person_profile = %v", opts["process_person_profile"])
	}
	props := wireProps(t, ev)
	for _, k := range []string{propertyCookielessMode, propertyIgnoreSentAt, propertyProductTourId, propertyProcessPersonProfile} {
		if _, ok := props[k]; ok {
			t.Errorf("%s must be removed from properties after lifting", k)
		}
	}
	// Unknown $-prefixed and plain props stay in properties.
	if props["plan"] != "pro" {
		t.Errorf("custom prop should remain in properties, got %v", props["plan"])
	}
}

func TestV1SessionAndWindowLifted(t *testing.T) {
	ev := marshalV1(t, Capture{
		Uuid: "u", Event: "e", DistinctId: "d",
		Properties: Properties{
			propertySessionID: "sess-1",
			propertyWindowID:  "win-1",
			"$custom":         "keep",
		},
	})
	if ev["session_id"] != "sess-1" {
		t.Errorf("session_id = %v, want sess-1", ev["session_id"])
	}
	if ev["window_id"] != "win-1" {
		t.Errorf("window_id = %v, want win-1", ev["window_id"])
	}
	props := wireProps(t, ev)
	if _, ok := props[propertySessionID]; ok {
		t.Error("$session_id must be removed from properties after lifting")
	}
	if _, ok := props[propertyWindowID]; ok {
		t.Error("$window_id must be removed from properties after lifting")
	}
	// Unknown $-prop stays put.
	if props["$custom"] != "keep" {
		t.Errorf("unknown $-prop should remain, got %v", props["$custom"])
	}
}

func TestV1IdentifySetInProperties(t *testing.T) {
	ev := marshalV1(t, Identify{Uuid: "u", DistinctId: "d", Properties: Properties{"email": "a@b.co"}})
	props := wireProps(t, ev)
	set, ok := props["$set"].(map[string]interface{})
	if !ok {
		t.Fatalf("$set missing from properties: %T", props["$set"])
	}
	if set["email"] != "a@b.co" {
		t.Errorf("$set.email = %v", set["email"])
	}
}

func TestV1GroupIdentifyKeepsGroupFieldsInProperties(t *testing.T) {
	ev := marshalV1(t, GroupIdentify{Uuid: "u", Type: "company", Key: "acme", Properties: Properties{"name": "Acme"}})
	props := wireProps(t, ev)
	if props["$group_type"] != "company" {
		t.Errorf("$group_type = %v", props["$group_type"])
	}
	if props["$group_key"] != "acme" {
		t.Errorf("$group_key = %v", props["$group_key"])
	}
	set, ok := props["$group_set"].(map[string]interface{})
	if !ok {
		t.Fatalf("$group_set missing: %T", props["$group_set"])
	}
	if set["name"] != "Acme" {
		t.Errorf("$group_set.name = %v", set["name"])
	}
}

func TestV1AliasIdentityPlacement(t *testing.T) {
	// C: alias merge reads "alias" from properties and the top-level distinct_id;
	// distinct_id must NOT be duplicated into properties.
	ev := marshalV1(t, Alias{Uuid: "u", DistinctId: "user-3", Alias: "anon-9"})
	if ev["distinct_id"] != "user-3" {
		t.Errorf("top-level distinct_id = %v", ev["distinct_id"])
	}
	props := wireProps(t, ev)
	if props["alias"] != "anon-9" {
		t.Errorf("properties.alias = %v", props["alias"])
	}
	if _, ok := props["distinct_id"]; ok {
		t.Error("distinct_id must not be duplicated into properties")
	}
}

func TestV1OptionsRendersEmptyObjectNotNull(t *testing.T) {
	data, _, _, err := prepareForSendV1(Capture{Uuid: "u", Event: "e", DistinctId: "d"})
	if err != nil {
		t.Fatalf("prepareForSendV1: %v", err)
	}
	if got := string(data); !containsSubstring(got, `"options":{}`) {
		t.Errorf("expected options to render as {}, got %s", got)
	}
}

func TestV1EnvelopeShape(t *testing.T) {
	data, _, _, err := prepareForSendV1(Capture{Uuid: "u", Event: "e", DistinctId: "d"})
	if err != nil {
		t.Fatalf("prepareForSendV1: %v", err)
	}
	batch := eventBatch{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Batch:     []json.RawMessage{data},
	}
	out, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var generic map[string]interface{}
	if err := json.Unmarshal(out, &generic); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if _, ok := generic["created_at"]; !ok {
		t.Error("envelope missing created_at")
	}
	if _, ok := generic["api_key"]; ok {
		t.Error("v1 envelope must not carry api_key")
	}
	// historical_migration omitted when false.
	if _, ok := generic["historical_migration"]; ok {
		t.Error("historical_migration should be omitted when false")
	}
	if _, ok := generic["batch"]; !ok {
		t.Error("envelope missing batch")
	}
}

func TestV1ResponseUnmarshal(t *testing.T) {
	body := `{"results":{
		"a":{"result":"ok"},
		"b":{"result":"warning","details":"person_processing_disabled"},
		"c":{"result":"drop","details":"billing_limit_exceeded"},
		"d":{"result":"retry","details":"not_persisted"},
		"e":{"result":"some_future_status"}
	}}`
	var resp captureV1Response
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Results["a"].Result != resultOk || resp.Results["a"].Details != nil {
		t.Errorf("a = %+v", resp.Results["a"])
	}
	if resp.Results["b"].Result != resultWarning || resp.Results["b"].Details == nil {
		t.Errorf("b = %+v", resp.Results["b"])
	}
	if resp.Results["c"].Result != resultDrop {
		t.Errorf("c = %+v", resp.Results["c"])
	}
	if resp.Results["d"].Result != resultRetry {
		t.Errorf("d = %+v", resp.Results["d"])
	}
	// Forward-compat: an unrecognized result string parses without error.
	if resp.Results["e"].Result != "some_future_status" {
		t.Errorf("e = %+v", resp.Results["e"])
	}
}

func TestV1ErrorResponseUnmarshal(t *testing.T) {
	var e v1ErrorResponse
	if err := json.Unmarshal([]byte(`{"error":"billing_limit_exceeded","error_description":"over quota"}`), &e); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if e.Error != "billing_limit_exceeded" || e.ErrorDescription != "over quota" {
		t.Errorf("error response = %+v", e)
	}
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
