package posthog

import "context"

// Options holds per-event capture options, such as person profile processing.
// They are sent in the event's options object, next to its properties, on both
// Enqueue and EnqueueAI.
//
// Keys are strings and values are any JSON-serializable value. The SDK sends
// them unchanged, so an option that PostHog adds later needs no SDK upgrade.
// PostHog validates them: it ignores unknown keys, reads common forms of a known
// key's value (for a boolean, "yes", "off" or 0), and drops the event when a
// known key has a value it cannot read. That drop reaches Callback.Failure as a
// *CaptureEventError whose Details is "invalid_options".
//
// The options PostHog reads:
//
//	process_person_profile   boolean
//	cookieless_mode          boolean
//	disable_skew_correction  boolean
//	product_tour_id          string
//
// The legacy properties $process_person_profile, $cookieless_mode,
// $ignore_sent_at and $product_tour_id still work, but an option overrides its
// legacy property. Use options.
//
// An identify-style event ($identify, $create_alias, $merge_dangerously,
// $groupidentify, $set or $set_once, sent by Identify, Alias, GroupIdentify or
// Capture) takes process_person_profile from the call alone: the option, else
// the legacy property, else true. No default or request-context value reaches
// it, because with person processing off PostHog drops these events or ignores
// their person update. A Capture whose distinct ID the SDK generates keeps the
// personless false instead of true. BeforeSend sees the value and can change it.
//
//	posthog.Capture{
//		DistinctId: "0123456789",
//		Event:      "order completed",
//		Options:    posthog.NewOptions().Set("process_person_profile", false),
//	}
type Options map[string]interface{}

// NewOptions creates an empty Options map for fluent construction.
func NewOptions() Options {
	return newStringInterfaceMap[Options]()
}

// Set assigns an option value and returns the receiver.
func (o Options) Set(name string, value interface{}) Options {
	return setStringInterfaceMapValue(o, name, value)
}

// fillOptions returns a copy of options with each option from layers added
// when options and the earlier layers leave it missing or nil. A nil option
// counts as unset here, as it does for the legacy properties in mergeOptions.
// options is returned unchanged when no layer has an option.
func fillOptions(options Options, layers ...Options) Options {
	size := len(options)
	for _, layer := range layers {
		size += len(layer)
	}
	if size == len(options) {
		return options
	}
	filled := make(Options, size)
	for key, value := range options {
		filled[key] = value
	}
	for _, layer := range layers {
		for key, value := range layer {
			if current, set := filled[key]; set && (!isNilValue(current) || isNilValue(value)) {
				continue
			}
			filled[key] = value
		}
	}
	return filled
}

// identifyStyleEvents are the events that exist to create, link or change a
// person or group, so they process the person profile unless the call itself
// says otherwise.
var identifyStyleEvents = map[string]struct{}{
	"$identify":          {},
	"$create_alias":      {},
	"$merge_dangerously": {},
	"$groupidentify":     {},
	"$set":               {},
	"$set_once":          {},
}

// withIdentifyStylePersonProfile resolves process_person_profile for an
// identify-style event from the call alone, before any layer fills and before
// BeforeSend, so the call's value beats every layer and BeforeSend sees it.
// It reads the event name at submission, so a BeforeSend rename does not count.
func withIdentifyStylePersonProfile(ctx context.Context, msg Message) Message {
	switch m := msg.(type) {
	case Identify:
		m.Options, m.EventProperties = callerPersonProfile(m.Options, m.EventProperties, true)
		return m
	case Alias:
		m.Options, m.EventProperties = callerPersonProfile(m.Options, m.EventProperties, true)
		return m
	case GroupIdentify:
		m.Options, m.EventProperties = callerPersonProfile(m.Options, m.EventProperties, true)
		return m
	case Capture:
		if _, ok := identifyStyleEvents[m.Event]; !ok {
			return m
		}
		// Capture generates a personless distinct ID exactly when neither the event
		// nor the request context has one, and that event keeps the personless default.
		requestContext, _ := RequestContextFromContext(ctx)
		hasDistinctID := eventDistinctID(m.DistinctId, requestContext) != ""
		m.Options, m.Properties = callerPersonProfile(m.Options, m.Properties, hasDistinctID)
		return m
	}
	return msg
}

// callerPersonProfile returns copies of options and properties with
// process_person_profile set from the call: the option if set, else the legacy
// property moved into the option, else true when defaultTrue. A nil value
// counts as unset. The caller's maps are never written.
func callerPersonProfile(options Options, properties Properties, defaultTrue bool) (Options, Properties) {
	if !isNilValue(options[optionProcessPersonProfile]) {
		return options, properties
	}
	if legacy := properties[propertyProcessPersonProfile]; !isNilValue(legacy) {
		properties = Properties{}.Merge(properties)
		delete(properties, propertyProcessPersonProfile)
		return fillOptions(options, Options{optionProcessPersonProfile: legacy}), properties
	}
	if !defaultTrue {
		return options, properties
	}
	return fillOptions(options, Options{optionProcessPersonProfile: true}), properties
}
