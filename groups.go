package posthog

// Groups maps a group type, such as "company", to the key of the group an event
// or flag evaluation belongs to, such as "acme-inc". Convert a numeric group ID
// to its decimal string, for example with strconv.FormatInt, which buckets it
// for feature flags exactly as the number did.
type Groups map[string]string

// NewGroups creates an empty Groups map for fluent construction.
func NewGroups() Groups {
	return make(Groups, 10)
}

// Set assigns a group type to a group key and returns the receiver.
func (p Groups) Set(name string, value string) Groups {
	p[name] = value
	return p
}
