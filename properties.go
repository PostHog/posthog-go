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
	return newStringInterfaceMap[Properties]()
}

func newStringInterfaceMap[M ~map[string]interface{}]() M {
	return make(M, 10)
}

// Set assigns a property value and returns the receiver.
func (p Properties) Set(name string, value interface{}) Properties {
	return setStringInterfaceMapValue(p, name, value)
}

func setStringInterfaceMapValue[M ~map[string]interface{}](m M, name string, value interface{}) M {
	m[name] = value
	return m
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

// mergeDefaults adds properties from defaults only when the receiver does not
// already contain the key.
func (p Properties) mergeDefaults(defaults Properties) Properties {
	if p == nil {
		p = Properties{}
	}
	if defaults == nil {
		return p
	}

	for k, v := range defaults {
		if _, exists := p[k]; !exists {
			p[k] = v
		}
	}

	return p
}

// nestedFillProperties fill one level deep when the event's value and the
// default are both maps.
var nestedFillProperties = map[string]struct{}{
	"$set":       {},
	"$set_once":  {},
	"$groups":    {},
	"$group_set": {},
}

// fillProperties returns a copy of props with each key from layers added when
// props and the earlier layers do not contain it. A key with a nil value counts
// as set. For the nestedFillProperties keys, when both values are maps, a layer
// adds only the inner keys that are still missing. props is returned unchanged
// when no layer has a key.
func fillProperties(props Properties, layers ...Properties) Properties {
	size := len(props)
	for _, layer := range layers {
		size += len(layer)
	}
	if size == len(props) {
		return props
	}
	filled := make(Properties, size)
	for k, v := range props {
		filled[k] = v
	}
	for _, layer := range layers {
		for k, v := range layer {
			current, exists := filled[k]
			if !exists {
				filled[k] = v
				continue
			}
			if _, nested := nestedFillProperties[k]; !nested {
				continue
			}
			if merged, ok := mergeNestedMaps(v, current); ok {
				filled[k] = merged
			}
		}
	}
	return filled
}

// mergeNestedMaps returns a new map with the keys of base and then the keys of
// top, so top wins. ok is false when either value is not a non-nil map with
// string keys.
func mergeNestedMaps(base, top interface{}) (Properties, bool) {
	baseMap, ok := stringKeyedMap(base)
	if !ok {
		return nil, false
	}
	topMap, ok := stringKeyedMap(top)
	if !ok {
		return nil, false
	}
	merged := make(Properties, len(baseMap)+len(topMap))
	for k, v := range baseMap {
		merged[k] = v
	}
	for k, v := range topMap {
		merged[k] = v
	}
	return merged, true
}

func stringKeyedMap(v interface{}) (map[string]interface{}, bool) {
	switch m := v.(type) {
	case Properties:
		return m, m != nil
	case map[string]interface{}:
		return m, m != nil
	case Groups:
		if m == nil {
			return nil, false
		}
		converted := make(map[string]interface{}, len(m))
		for k, value := range m {
			converted[k] = value
		}
		return converted, true
	}
	return nil, false
}

// mergeOverNested returns top merged over base when both are maps, so top wins
// key by key, and top alone otherwise.
func mergeOverNested(base interface{}, top interface{}) interface{} {
	if merged, ok := mergeNestedMaps(base, top); ok {
		return merged
	}
	return top
}
