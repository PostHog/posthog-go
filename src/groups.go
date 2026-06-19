package posthog

// Groups is used to represent groups in messages that support it.
// It is a free-form object so the application can set any value it sees fit but
// a few helper methods are defined to make it easier to instantiate groups with
// common fields.
type Groups map[string]interface{}

// NewGroups creates an empty Groups map for fluent construction.
func NewGroups() Groups {
	return newStringInterfaceMap[Groups]()
}

// Set assigns a group type to a group key or ID and returns the receiver.
func (p Groups) Set(name string, value interface{}) Groups {
	return setStringInterfaceMapValue(p, name, value)
}
