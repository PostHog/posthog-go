package posthog

import "time"

var _ Message = (*Exception)(nil)

type Exception struct {
	// This field is exported for serialization purposes and shouldn't be set by
	// the application, its value is always overwritten by the library.
	Type string

	DistinctId   string
	Timestamp    time.Time
	DisableGeoIP bool
	Properties   Properties

	// Typed properties that end up in the API "properties" object:
	ExceptionList        []ExceptionItem
	ExceptionFingerprint *string
}

type ExceptionItem struct {
	// Type will be rendered as title in the UI
	Type string `json:"type"`
	// Value will be rendered as description in the UI
	Value     string              `json:"value"`
	Mechanism *ExceptionMechanism `json:"mechanism,omitempty"`
	// Stacktrace can conveniently be generated through the use of StackTraceExtractor
	Stacktrace *ExceptionStacktrace `json:"stacktrace,omitempty"`
}

type ExceptionMechanism struct {
	Handled   *bool `json:"handled,omitempty"`
	Synthetic *bool `json:"synthetic,omitempty"`
}

type ExceptionStacktrace struct {
	Type   string       `json:"type"`
	Frames []StackFrame `json:"frames"`
}

// StackFrame represents a single "Frame" within a stack trace.
// Documentation about the available fields can be found here:
// https://github.com/PostHog/posthog/blob/39b9326320c23acbdc6e96a8beb41b30d3c99099/rust/cymbal/src/langs/go.rs#L7
type StackFrame struct {
	Filename  string `json:"filename"`
	LineNo    int    `json:"lineno"`
	Function  string `json:"function"`
	InApp     bool   `json:"in_app"`
	Synthetic bool   `json:"synthetic"`
	Platform  string `json:"platform"`
}

type ExceptionInApi struct {
	Type           string     `json:"type"`
	Library        string     `json:"library"`
	LibraryVersion string     `json:"library_version"`
	Timestamp      time.Time  `json:"timestamp"`
	Event          string     `json:"event"`
	Properties     Properties `json:"properties"`
}

func (msg Exception) internal() { panic(unimplementedError) }

func (msg Exception) Validate() error {
	if len(msg.DistinctId) == 0 {
		return FieldError{
			Type:  "posthog.Exception",
			Name:  "DistinctId",
			Value: msg.DistinctId,
		}
	}
	if len(msg.ExceptionList) == 0 {
		return FieldError{
			Type:  "posthog.Exception",
			Name:  "ExceptionList",
			Value: []ExceptionItem{},
		}
	}
	for _, item := range msg.ExceptionList {
		if err := item.Validate(); err != nil {
			return err
		}
	}

	return nil
}

func (msg ExceptionItem) Validate() error {
	if msg.Type == "" {
		return FieldError{
			Type:  "posthog.Exception",
			Name:  "Type",
			Value: msg.Type,
		}
	}
	if msg.Value == "" {
		return FieldError{
			Type:  "posthog.Exception",
			Name:  "Value",
			Value: msg.Value,
		}
	}

	return nil
}

func (msg Exception) APIfy() APIMessage {
	libVersion := getVersion()

	msg.Properties.
		Set("$lib", SDKName).
		Set("$lib_version", libVersion).
		Set("distinct_id", msg.DistinctId).
		Set("$exception_list", msg.ExceptionList)
	if msg.DisableGeoIP {
		msg.Properties.Set("$geoip_disable", msg.DisableGeoIP)
	}
	if msg.ExceptionFingerprint != nil {
		msg.Properties.Set("$exception_fingerprint", msg.ExceptionFingerprint)
	}

	return ExceptionInApi{
		Type:           msg.Type, // set to "exception" by Enqueue switch
		Event:          "$exception",
		Library:        SDKName,
		LibraryVersion: libVersion,
		Timestamp:      msg.Timestamp,
		Properties:     msg.Properties,
	}
}

// NewDefaultException is a convenience function to build an Exception object (usable for `client.Enqueue`)
// with sane defaults. If you want more control, please manually build the Exception object.
func NewDefaultException(
	timestamp time.Time,
	distinctID, title, description string,
) Exception {
	defaultStackTrace := DefaultStackTraceExtractor{InAppDecider: SimpleInAppDecider}

	return Exception{
		DistinctId: distinctID,
		Timestamp:  timestamp,
		ExceptionList: []ExceptionItem{
			{
				Type:       title,
				Value:      description,
				Stacktrace: defaultStackTrace.GetStackTrace(3),
			},
		},
		Properties: NewProperties(),
	}
}
