package posthog

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
// The options PostHog reads today:
//
//	process_person_profile   boolean
//	cookieless_mode          boolean
//	disable_skew_correction  boolean
//	product_tour_id          string
//
// The legacy properties $process_person_profile, $cookieless_mode,
// $ignore_sent_at and $product_tour_id still work. The SDK removes each one from
// properties and uses its value only when the matching option is missing or nil,
// so an option always wins.
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
