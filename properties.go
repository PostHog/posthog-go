package posthog

// Properties is used to represent properties in messages that support it.
// It is a free-form object so the application can set any value it sees fit but
// a few helper method are defined to make it easier to instantiate properties with
// common fields.
// Here's a quick example of how this type is meant to be used:
//
//	posthog.Capture{
//		DistinctId: "0123456789",
//		Event:      "order completed",
//		Properties: posthog.NewProperties()
//			.Set("revenue", 10.0)
//			.Set("currency", "USD"),
//	}
type Properties map[string]interface{}

// NewProperties creates an empty Properties map for fluent construction.
func NewProperties() Properties {
	return make(Properties, 10)
}

// Set assigns a property value and returns the receiver.
func (p Properties) Set(name string, value interface{}) Properties {
	p[name] = value
	return p
}

// Merge adds the properties from the provided `props` into the receiver `p`.
// If a property in `props` already exists in `p`, its value will be overwritten.
func (p Properties) Merge(props Properties) Properties {
	if props == nil {
		return p
	}

	for k, v := range props {
		p[k] = v
	}

	return p
}
