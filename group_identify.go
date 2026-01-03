package posthog

import (
	"fmt"
	"time"

	json "github.com/goccy/go-json"
)

type GroupIdentify struct {
	Type string
	Key  string

	DistinctId   string
	Timestamp    time.Time
	Properties   Properties
	DisableGeoIP bool
}

func (msg GroupIdentify) internal() {
	panic(unimplementedError)
}

func (msg GroupIdentify) Validate() error {
	if len(msg.Type) == 0 {
		return FieldError{
			Type:  "posthog.GroupIdentify",
			Name:  "Type",
			Value: msg.Type,
		}
	}

	if len(msg.Key) == 0 {
		return FieldError{
			Type:  "posthog.GroupIdentify",
			Name:  "Key",
			Value: msg.Key,
		}
	}

	return nil
}

type GroupIdentifyInApi struct {
	Library        string    `json:"library"`
	LibraryVersion string    `json:"library_version"`
	Timestamp      time.Time `json:"timestamp"`

	Event      string     `json:"event"`
	DistinctId string     `json:"distinct_id"`
	Properties Properties `json:"properties"`
}

func (msg GroupIdentify) APIfy() APIMessage {
	myProperties := Properties{}.
		Set("$lib", SDKName).
		Set("$lib_version", getVersion()).
		Set("$group_type", msg.Type).
		Set("$group_key", msg.Key).
		Set("$group_set", msg.Properties)

	if msg.DisableGeoIP {
		myProperties.Set(propertyGeoipDisable, true)
	}

	distinctId := fmt.Sprintf("$%s_%s", msg.Type, msg.Key)

	apified := GroupIdentifyInApi{
		Event:          "$groupidentify",
		Properties:     myProperties,
		DistinctId:     distinctId,
		Timestamp:      msg.Timestamp,
		Library:        SDKName,
		LibraryVersion: getVersion(),
	}

	return apified
}

// prepareForSend creates the API message and serializes it to JSON.
// Returns pre-serialized JSON for efficient batch building, the original
// APIMessage for callbacks, and any serialization error.
func (msg GroupIdentify) prepareForSend() (json.RawMessage, APIMessage, error) {
	apiMsg := msg.APIfy()
	data, err := json.Marshal(apiMsg)
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(data), apiMsg, nil
}
