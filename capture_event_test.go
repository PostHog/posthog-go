package posthog

import (
	"fmt"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

// marshalEvent builds the wire event for a message and decodes it back into a
// generic map so tests can assert the on-the-wire shape.
func marshalEvent(t *testing.T, msg Message) map[string]interface{} {
	t.Helper()
	data, _, uuid, err := prepareForSend(msg)
	if err != nil {
		t.Fatalf("prepareForSend: %v", err)
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

func TestEventNamesAndDistinctId(t *testing.T) {
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
			ev := marshalEvent(t, tc.msg)
			if ev["event"] != tc.wantEvent {
				t.Errorf("event = %v, want %v", ev["event"], tc.wantEvent)
			}
			if ev["distinct_id"] != tc.wantDistinctId {
				t.Errorf("distinct_id = %v, want %v", ev["distinct_id"], tc.wantDistinctId)
			}
		})
	}
}

func TestDropsLibFromProperties(t *testing.T) {
	// Decision B: SDK identity rides the PostHog-Sdk-Info header, never properties.
	msgs := []Message{
		Capture{Uuid: "u", Event: "e", DistinctId: "d"},
		Identify{Uuid: "u", DistinctId: "d"},
		GroupIdentify{Uuid: "u", Type: "company", Key: "acme"},
		Alias{Uuid: "u", DistinctId: "d", Alias: "a"},
		Exception{Uuid: "u", DistinctId: "d", ExceptionList: []ExceptionItem{{Type: "E", Value: "v"}}},
	}
	for _, m := range msgs {
		t.Run(fmt.Sprintf("%T", m), func(t *testing.T) {
			ev := marshalEvent(t, m)
			props := wireProps(t, ev)
			if _, ok := props["$lib"]; ok {
				t.Errorf("$lib must not be in properties")
			}
			if _, ok := props["$lib_version"]; ok {
				t.Errorf("$lib_version must not be in properties")
			}
		})
	}
}

func TestSystemContextAppliedAsDefaults(t *testing.T) {
	sysCtx := getSystemContext().ToProperties()
	msgs := []struct {
		name string
		msg  Message
	}{
		{"capture", Capture{Uuid: "u", Event: "e", DistinctId: "d"}},
		{"identify", Identify{Uuid: "u", DistinctId: "d"}},
		{"groupidentify", GroupIdentify{Uuid: "u", Type: "company", Key: "acme"}},
		{"alias", Alias{Uuid: "u", DistinctId: "d", Alias: "a"}},
		{"exception", Exception{Uuid: "u", DistinctId: "d", ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}}}},
	}

	for _, tc := range msgs {
		t.Run(tc.name, func(t *testing.T) {
			ev := marshalEvent(t, tc.msg)
			props := wireProps(t, ev)

			for _, key := range []string{"$os", "$go_version"} {
				if props[key] != sysCtx[key] {
					t.Errorf("%s = %v, want system default %v", key, props[key], sysCtx[key])
				}
			}
		})
	}
}

func TestSystemContextDoesNotOverwriteCallerProperties(t *testing.T) {
	callerContext := Properties{
		"$os":         "caller-os",
		"$os_version": "caller-os-version",
		"$os_distro":  "caller-os-distro",
		"$go_version": "caller-go-version",
	}
	msgs := []struct {
		name string
		msg  Message
	}{
		{
			name: "capture",
			msg: Capture{
				Uuid:       "u",
				Event:      "e",
				DistinctId: "d",
				Properties: callerContext,
			},
		},
		{
			name: "exception",
			msg: Exception{
				Uuid:          "u",
				DistinctId:    "d",
				Properties:    callerContext,
				ExceptionList: []ExceptionItem{{Type: "Error", Value: "boom"}},
			},
		},
	}

	for _, tc := range msgs {
		t.Run(tc.name, func(t *testing.T) {
			ev := marshalEvent(t, tc.msg)
			props := wireProps(t, ev)
			for key, want := range callerContext {
				if props[key] != want {
					t.Errorf("%s = %v, want caller value %v", key, props[key], want)
				}
			}
		})
	}
}

// wireOptionsRaw returns the wire options of msg as raw JSON, so tests can
// prove a value is sent exactly as the caller set it.
func wireOptionsRaw(t *testing.T, msg Message) map[string]json.RawMessage {
	t.Helper()
	data, _, _, err := prepareForSend(msg)
	if err != nil {
		t.Fatalf("prepareForSend: %v", err)
	}
	var ev struct {
		Options map[string]json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal wire event: %v", err)
	}
	return ev.Options
}

func mustMarshalJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return string(data)
}

func TestOptionsSentUnchanged(t *testing.T) {
	// PostHog validates option values, so the SDK must not convert or drop
	// any of them: not the forms PostHog reads leniently, not the ones it
	// rejects, and not unknown keys.
	values := []struct {
		name  string
		value interface{}
	}{
		{"bool", true},
		{"string_no", "no"},
		{"string_YES", "YES"},
		{"string_off", "off"},
		{"unreadable_string", "maybe"},
		{"empty_string", ""},
		{"int", 5},
		{"int64", int64(-3)},
		{"float", 0.5},
		{"json_number", json.Number("0.0")},
		{"array", []interface{}{1, "two"}},
		{"object", map[string]interface{}{"nested": []interface{}{1, "two"}}},
	}
	keys := []string{"process_person_profile", "cookieless_mode", "disable_skew_correction", "product_tour_id", "future_option"}
	for _, key := range keys {
		for _, tc := range values {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				opts := wireOptionsRaw(t, Capture{
					Uuid: "u", Event: "e", DistinctId: "d",
					Options: NewOptions().Set(key, tc.value),
				})
				if len(opts) != 1 {
					t.Fatalf("options = %v, want only %s", opts, key)
				}
				if got, want := string(opts[key]), mustMarshalJSON(t, tc.value); got != want {
					t.Errorf("%s = %s, want %s unchanged", key, got, want)
				}
			})
		}
	}
	for _, pair := range legacyOptionProperties {
		for _, tc := range values {
			t.Run(pair.propKey+"/"+tc.name, func(t *testing.T) {
				opts := wireOptionsRaw(t, Capture{
					Uuid: "u", Event: "e", DistinctId: "d",
					Properties: Properties{pair.propKey: tc.value},
				})
				if got, want := string(opts[pair.optionKey]), mustMarshalJSON(t, tc.value); got != want {
					t.Errorf("%s from %s = %s, want %s unchanged", pair.optionKey, pair.propKey, got, want)
				}
			})
		}
	}
}

func TestLegacyPropertyFillsOnlyUnsetOption(t *testing.T) {
	var nilBool *bool
	optionFalse := false
	absent := struct{}{}
	cases := []struct {
		name   string
		option interface{} // absent: option not set
		legacy interface{} // absent: legacy property not set
		want   string      // "": option key absent from the wire
	}{
		{"neither_set_leaves_key_out", absent, absent, ""},
		{"option_only", false, absent, "false"},
		{"legacy_only", absent, "maybe", `"maybe"`},
		{"option_wins_over_legacy", false, true, "false"},
		{"pointer_option_wins_over_legacy", &optionFalse, true, "false"},
		{"nil_option_falls_back_to_legacy", nil, "tour-1", `"tour-1"`},
		{"typed_nil_option_falls_back_to_legacy", nilBool, "tour-1", `"tour-1"`},
		{"nil_option_without_legacy_is_sent_as_null", nil, absent, "null"},
		{"nil_legacy_fills_unset_option_as_null", absent, nil, "null"},
	}
	for _, pair := range legacyOptionProperties {
		for _, tc := range cases {
			t.Run(pair.optionKey+"/"+tc.name, func(t *testing.T) {
				msg := Capture{Uuid: "u", Event: "e", DistinctId: "d", Properties: Properties{"keep": "yes"}}
				if tc.option != absent {
					msg.Options = Options{pair.optionKey: tc.option}
				}
				if tc.legacy != absent {
					msg.Properties[pair.propKey] = tc.legacy
				}
				ev := marshalEvent(t, msg)
				props := wireProps(t, ev)
				if _, ok := props[pair.propKey]; ok {
					t.Errorf("%s must always be removed from properties", pair.propKey)
				}
				if props["keep"] != "yes" {
					t.Errorf("unrelated property must stay in properties, got %v", props["keep"])
				}

				opts := wireOptionsRaw(t, msg)
				got, present := opts[pair.optionKey]
				if tc.want == "" {
					if present {
						t.Errorf("%s = %s, want the key left out", pair.optionKey, got)
					}
					return
				}
				if string(got) != tc.want {
					t.Errorf("%s = %s, want %s", pair.optionKey, got, tc.want)
				}
			})
		}
	}
}

func TestOptionsMergeDoesNotMutateCallerOptions(t *testing.T) {
	options := Options{"process_person_profile": nil}
	msg := Capture{
		Uuid: "u", Event: "e", DistinctId: "d",
		Options:    options,
		Properties: Properties{propertyProcessPersonProfile: false, propertyCookielessMode: true},
	}
	opts := wireOptions(t, marshalEvent(t, msg))
	if opts["process_person_profile"] != false || opts["cookieless_mode"] != true {
		t.Fatalf("legacy properties must fill the wire options, got %v", opts)
	}
	if len(options) != 1 || options["process_person_profile"] != nil {
		t.Errorf("caller Options must not change, got %v", options)
	}
}

func TestOptionsOnEveryMessageType(t *testing.T) {
	options := func() Options { return NewOptions().Set("disable_skew_correction", "yes").Set("future_option", 1) }
	messages := []Message{
		Capture{Uuid: "u", Event: "e", DistinctId: "d", Options: options()},
		Identify{Uuid: "u", DistinctId: "d", Options: options()},
		Alias{Uuid: "u", DistinctId: "d", Alias: "a", Options: options()},
		GroupIdentify{Uuid: "u", Type: "company", Key: "k", Options: options()},
		Exception{Uuid: "u", DistinctId: "d", ExceptionList: []ExceptionItem{{Type: "t", Value: "v"}}, Options: options()},
	}
	for _, msg := range messages {
		t.Run(fmt.Sprintf("%T", msg), func(t *testing.T) {
			opts := wireOptionsRaw(t, msg)
			if len(opts) != 2 || string(opts["disable_skew_correction"]) != `"yes"` || string(opts["future_option"]) != "1" {
				t.Errorf("options = %v, want both caller options unchanged", opts)
			}
		})
	}
}

func TestExceptionLegacyPropertiesMoveIntoOptions(t *testing.T) {
	ev := marshalEvent(t, Exception{
		Uuid: "u", DistinctId: "d",
		ExceptionList: []ExceptionItem{{Type: "t", Value: "v"}},
		Options:       Options{"cookieless_mode": "off"},
		Properties:    Properties{propertyCookielessMode: true, propertyProcessPersonProfile: false},
	})
	opts := wireOptions(t, ev)
	if opts["cookieless_mode"] != "off" {
		t.Errorf("cookieless_mode = %v, want the option to win", opts["cookieless_mode"])
	}
	if opts["process_person_profile"] != false {
		t.Errorf("process_person_profile = %v, want the legacy value", opts["process_person_profile"])
	}
	props := wireProps(t, ev)
	for _, key := range []string{propertyCookielessMode, propertyProcessPersonProfile} {
		if _, ok := props[key]; ok {
			t.Errorf("%s must be removed from exception properties", key)
		}
	}
}

func TestSessionAndWindowLifted(t *testing.T) {
	ev := marshalEvent(t, Capture{
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

func TestIdentifySetInProperties(t *testing.T) {
	ev := marshalEvent(t, Identify{Uuid: "u", DistinctId: "d", Properties: Properties{"email": "a@b.co"}})
	props := wireProps(t, ev)
	set, ok := props["$set"].(map[string]interface{})
	if !ok {
		t.Fatalf("$set missing from properties: %T", props["$set"])
	}
	if set["email"] != "a@b.co" {
		t.Errorf("$set.email = %v", set["email"])
	}
}

func TestNilPropertiesOmitsSetKeys(t *testing.T) {
	t.Run("identify", func(t *testing.T) {
		ev := marshalEvent(t, Identify{Uuid: "u", DistinctId: "d", Properties: nil})
		props := wireProps(t, ev)
		if _, ok := props["$set"]; ok {
			t.Errorf("$set should be omitted when Properties is nil, got %v", props["$set"])
		}
	})
	t.Run("group_identify", func(t *testing.T) {
		ev := marshalEvent(t, GroupIdentify{Uuid: "u", Type: "company", Key: "acme", Properties: nil})
		props := wireProps(t, ev)
		if _, ok := props["$group_set"]; ok {
			t.Errorf("$group_set should be omitted when Properties is nil, got %v", props["$group_set"])
		}
	})
}

func TestGroupIdentifyKeepsGroupFieldsInProperties(t *testing.T) {
	ev := marshalEvent(t, GroupIdentify{Uuid: "u", Type: "company", Key: "acme", Properties: Properties{"name": "Acme"}})
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

func TestAliasIdentityPlacement(t *testing.T) {
	// C: alias merge reads "alias" from properties and the top-level distinct_id;
	// distinct_id must NOT be duplicated into properties.
	ev := marshalEvent(t, Alias{Uuid: "u", DistinctId: "user-3", Alias: "anon-9"})
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

func TestOptionsRendersEmptyObjectNotNull(t *testing.T) {
	data, _, _, err := prepareForSend(Capture{Uuid: "u", Event: "e", DistinctId: "d"})
	if err != nil {
		t.Fatalf("prepareForSend: %v", err)
	}
	if got := string(data); !strings.Contains(got, `"options":{}`) {
		t.Errorf("expected options to render as {}, got %s", got)
	}
}

func TestEnvelopeShape(t *testing.T) {
	data, _, _, err := prepareForSend(Capture{Uuid: "u", Event: "e", DistinctId: "d"})
	if err != nil {
		t.Fatalf("prepareForSend: %v", err)
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
		t.Error("envelope must not carry api_key")
	}
	// historical_migration omitted when false.
	if _, ok := generic["historical_migration"]; ok {
		t.Error("historical_migration should be omitted when false")
	}
	if _, ok := generic["batch"]; !ok {
		t.Error("envelope missing batch")
	}
}

func TestResponseUnmarshal(t *testing.T) {
	body := `{"results":{
		"a":{"result":"ok"},
		"b":{"result":"warning","details":"person_processing_disabled"},
		"c":{"result":"drop","details":"billing_limit_exceeded"},
		"d":{"result":"retry","details":"not_persisted"},
		"e":{"result":"some_future_status"}
	}}`
	var resp captureResponse
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

func TestErrorResponseUnmarshal(t *testing.T) {
	var e captureErrorResponse
	if err := json.Unmarshal([]byte(`{"error":"billing_limit_exceeded","error_description":"over quota"}`), &e); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if e.Error != "billing_limit_exceeded" || e.ErrorDescription != "over quota" {
		t.Errorf("error response = %+v", e)
	}
}
