package posthog

import (
	"encoding/json"
	"time"
)

var _ Message = (*Exception)(nil)

// Exception represents an error tracking exception event.
// Enqueue validates DistinctId and ExceptionList (or obtains DistinctId from request context),
// fills Type, Uuid, Timestamp, and DisableGeoIP, then sends the message as a $exception event.
type Exception struct {
	// Type is reserved for SDK serialization and is overwritten by Enqueue.
	Type string
	// Uuid is an optional event UUID. If empty, Enqueue generates a random UUID.
	Uuid string

	// DistinctId identifies the user or entity associated with the exception.
	// When using EnqueueWithContext, this can be inherited from RequestContext.
	DistinctId string
	// Timestamp is the event timestamp. If zero, Enqueue uses the current time.
	Timestamp time.Time
	// Properties are custom event properties flattened into the exception event.
	Properties Properties
	// DisableGeoIP controls whether this exception event disables GeoIP lookup.
	// Enqueue overwrites it from Config.GetDisableGeoIP.
	DisableGeoIP bool
	// IsServer controls whether the event includes the $is_server property.
	// Enqueue overwrites it from Config.GetIsServer.
	IsServer bool

	// ExceptionList is the list of exception items sent as $exception_list. It must be non-empty.
	ExceptionList []ExceptionItem
	// ExceptionFingerprint optionally overrides PostHog's default exception grouping fingerprint.
	ExceptionFingerprint *string
	// DebugImages lists the binary images referenced by "native" stack
	// frames, sent as $debug_images for server-side symbolication.
	DebugImages []DebugImage
}

// ExceptionItem describes one exception in an Exception event.
type ExceptionItem struct {
	// Type is rendered as the exception title in the PostHog UI.
	Type string `json:"type"`
	// Value is rendered as the exception description in the PostHog UI.
	Value string `json:"value"`
	// Mechanism describes how the exception was produced or handled.
	Mechanism *ExceptionMechanism `json:"mechanism,omitempty"`
	// Stacktrace contains stack frames for the exception. Use StackTraceExtractor to generate it.
	Stacktrace *ExceptionStacktrace `json:"stacktrace,omitempty"`
}

// ExceptionMechanism describes whether an exception was handled or synthesized.
type ExceptionMechanism struct {
	// Handled reports whether the exception was handled by application code.
	Handled *bool `json:"handled,omitempty"`
	// Synthetic reports whether the exception was synthesized by instrumentation.
	Synthetic *bool `json:"synthetic,omitempty"`
}

// ExceptionStacktrace is a PostHog-compatible stack trace.
type ExceptionStacktrace struct {
	// Type is the stack trace representation type. The default extractor uses "raw".
	Type string `json:"type"`
	// Frames is the ordered list of stack frames.
	Frames []StackFrame `json:"frames"`
}

// StackFrame represents a single "Frame" within a stack trace.
// Documentation about the available fields can be found here:
// https://github.com/PostHog/posthog/blob/39b9326320c23acbdc6e96a8beb41b30d3c99099/rust/cymbal/src/langs/go.rs#L7
type StackFrame struct {
	// Filename is the source file path for the frame.
	Filename string `json:"filename"`
	// LineNo is the one-based source line number.
	LineNo int `json:"lineno"`
	// Function is the fully qualified function name.
	Function string `json:"function"`
	// InApp reports whether the frame is considered application code.
	InApp bool `json:"in_app"`
	// Synthetic reports whether the frame was synthesized by instrumentation.
	Synthetic bool `json:"synthetic"`
	// Platform identifies the runtime platform; Go frames use "go", frames
	// with raw addresses for server-side symbolication use "native".
	Platform string `json:"platform"`
	// Lang is a display-language hint for "native" frames; Go frames use "go".
	Lang string `json:"lang,omitempty"`
	// InstructionAddr is the absolute address of the instruction, as hex.
	InstructionAddr string `json:"instruction_addr,omitempty"`
	// SymbolAddr is the start address of the enclosing function, as hex.
	SymbolAddr string `json:"symbol_addr,omitempty"`
	// ImageAddr is the load address of the image containing the instruction.
	ImageAddr string `json:"image_addr,omitempty"`
}

// ExceptionInApi is the wire-format payload produced from an Exception message.
type ExceptionInApi struct {
	// Type is the message type sent to the batch API.
	Type string `json:"type"`
	// Uuid is the event UUID sent to the batch API.
	Uuid string `json:"uuid"`
	// Library is the SDK name sent to the batch API.
	Library string `json:"library"`
	// LibraryVersion is the SDK version sent to the batch API.
	LibraryVersion string `json:"library_version"`
	// Timestamp is the event timestamp sent to the batch API.
	Timestamp time.Time `json:"timestamp"`
	// Event is always $exception for Exception messages.
	Event string `json:"event"`
	// Properties contains built-in and custom exception properties.
	Properties ExceptionInApiProperties `json:"properties"`
}

// ExceptionInApiProperties is the wire-format properties object for an Exception message.
type ExceptionInApiProperties struct {
	sysContext
	// Lib is the SDK name sent as $lib.
	Lib string `json:"$lib"`
	// LibVersion is the SDK version sent as $lib_version.
	LibVersion string `json:"$lib_version"`
	// IsServer marks the event as originating from a server-side SDK.
	// Omitted entirely when nil (Config.IsServer resolved to false).
	IsServer *bool `json:"$is_server,omitempty"`
	// DistinctId is the user distinct ID associated with the exception.
	DistinctId string `json:"distinct_id"`
	// DisableGeoIP is sent as $geoip_disable when GeoIP lookup is disabled.
	DisableGeoIP bool `json:"$geoip_disable,omitempty"`
	// ExceptionList is sent as $exception_list.
	ExceptionList []ExceptionItem `json:"$exception_list"`
	// ExceptionFingerprint is sent as $exception_fingerprint when provided.
	ExceptionFingerprint *string `json:"$exception_fingerprint,omitempty"`
	// DebugImages is sent as $debug_images when "native" frames are present.
	DebugImages []DebugImage `json:"$debug_images,omitempty"`

	// Custom is flattened into the wire "properties" on marshal.
	// Typed fields win on collision.
	Custom Properties `json:"-"`
}

// MarshalJSON lets users add extra keys (via Custom) to the event's
// JSON output next to the built-in ones like $lib and distinct_id.
// If a custom key clashes with a built-in, we keep ours and drop theirs.
func (p ExceptionInApiProperties) MarshalJSON() ([]byte, error) {
	type alias ExceptionInApiProperties
	typedBytes, err := json.Marshal(alias(p))
	if err != nil {
		return nil, err
	}

	if len(p.Custom) == 0 {
		return typedBytes, nil
	}

	var merged map[string]json.RawMessage
	if err := json.Unmarshal(typedBytes, &merged); err != nil {
		return nil, err
	}

	for k, v := range p.Custom {
		if _, exists := merged[k]; exists {
			continue
		}
		vb, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		merged[k] = vb
	}

	return json.Marshal(merged)
}

func (msg Exception) internal() { panic(unimplementedError) }

// Validate checks that the exception has a DistinctId and at least one valid ExceptionItem.
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

// Validate checks that the exception item has Type and Value set.
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

// APIfy converts an Exception message into the PostHog batch API representation.
func (msg Exception) APIfy() APIMessage {
	libVersion := getVersion()

	var isServer *bool
	if msg.IsServer {
		isServer = Ptr(true)
	}

	return ExceptionInApi{
		Type:           msg.Type, // set to "exception" by Enqueue switch
		Uuid:           msg.Uuid,
		Event:          "$exception",
		Library:        SDKName,
		LibraryVersion: libVersion,
		Timestamp:      msg.Timestamp,
		Properties: ExceptionInApiProperties{
			sysContext:           getSystemContext(),
			Lib:                  SDKName,
			LibVersion:           libVersion,
			IsServer:             isServer,
			DistinctId:           msg.DistinctId,
			DisableGeoIP:         msg.DisableGeoIP,
			ExceptionList:        msg.ExceptionList,
			ExceptionFingerprint: msg.ExceptionFingerprint,
			DebugImages:          msg.DebugImages,
			Custom:               msg.Properties,
		},
	}
}

// NewDefaultException builds an Exception with a default stack trace.
//
// The timestamp parameter becomes Exception.Timestamp, distinctID becomes
// Exception.DistinctId, title becomes the first ExceptionItem.Type, and
// description becomes the first ExceptionItem.Value. The returned Exception is
// ready to pass to Client.Enqueue. Build Exception manually if you need custom
// properties, fingerprints, mechanisms, or stack frames.
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
	}
}
