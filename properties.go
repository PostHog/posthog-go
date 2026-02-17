package posthog

// Properties is used to represent properties in messages that support it.
// It is a free-form object so the application can set any value it sees fit but
// a few helper method are defined to make it easier to instantiate properties with
// common fields.
// Here's a quick example of how this type is meant to be used:
//
//	posthog.Page{
//		DistinctId: "0123456789",
//		Properties: posthog.NewProperties()
//			.Set("revenue", 10.0)
//			.Set("currency", "USD"),
//	}
type Properties map[string]interface{}

func NewProperties() Properties {
	return make(Properties, 10)
}

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

// WithCurrentURL sets the $current_url property.
// This is commonly used for pageview events.
func (p Properties) WithCurrentURL(url string) Properties {
	return p.Set("$current_url", url)
}

// WithReferrer sets the $referrer property.
func (p Properties) WithReferrer(referrer string) Properties {
	return p.Set("$referrer", referrer)
}

// WithTitle sets the $title property (page title).
func (p Properties) WithTitle(title string) Properties {
	return p.Set("$title", title)
}

// WithPath sets the $pathname property (URL path without query/hash).
func (p Properties) WithPath(path string) Properties {
	return p.Set("$pathname", path)
}

// WithScreen sets screen/viewport dimensions.
func (p Properties) WithScreen(width, height int) Properties {
	return p.Set("$screen_width", width).Set("$screen_height", height)
}
