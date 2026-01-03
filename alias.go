package posthog

import (
	"time"

	json "github.com/goccy/go-json"
)

var _ Message = (*Alias)(nil)

// This type represents object sent in a alias call
type Alias struct {
	// This field is exported for serialization purposes and shouldn't be set by
	// the application, its value is always overwritten by the library.
	Type string

	Alias        string
	DistinctId   string
	Timestamp    time.Time
	DisableGeoIP bool
}

func (msg Alias) internal() {
	panic(unimplementedError)
}

func (msg Alias) Validate() error {
	if len(msg.DistinctId) == 0 {
		return FieldError{
			Type:  "posthog.Alias",
			Name:  "DistinctId",
			Value: msg.DistinctId,
		}
	}

	if len(msg.Alias) == 0 {
		return FieldError{
			Type:  "posthog.Alias",
			Name:  "Alias",
			Value: msg.Alias,
		}
	}

	return nil
}

type AliasInApiProperties struct {
	DistinctId   string `json:"distinct_id"`
	Alias        string `json:"alias"`
	Lib          string `json:"$lib"`
	LibVersion   string `json:"$lib_version"`
	DisableGeoIP bool   `json:"$geoip_disable,omitempty"`
}

type AliasInApi struct {
	Type           string    `json:"type"`
	Library        string    `json:"library"`
	LibraryVersion string    `json:"library_version"`
	Timestamp      time.Time `json:"timestamp"`

	Properties AliasInApiProperties `json:"properties"`

	Event string `json:"event"`
}

func (msg Alias) APIfy() APIMessage {
	libraryVersion := getVersion()

	apified := AliasInApi{
		Type:           msg.Type,
		Event:          "$create_alias",
		Library:        SDKName,
		LibraryVersion: libraryVersion,
		Timestamp:      msg.Timestamp,
		Properties: AliasInApiProperties{
			DistinctId:   msg.DistinctId,
			Alias:        msg.Alias,
			Lib:          SDKName,
			LibVersion:   libraryVersion,
			DisableGeoIP: msg.DisableGeoIP,
		},
	}

	return apified
}

// prepareForSend creates the API message and serializes it to JSON.
// Returns pre-serialized JSON for efficient batch building, the original
// APIMessage for callbacks, and any serialization error.
func (msg Alias) prepareForSend() (json.RawMessage, APIMessage, error) {
	apiMsg := msg.APIfy()
	data, err := json.Marshal(apiMsg)
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(data), apiMsg, nil
}
