package posthog

import "time"

var _ Message = (*Alias)(nil)

// This type represents object sent in a alias call
type Alias struct {
	// This field is exported for serialization purposes and shouldn't be set by
	// the application, its value is always overwritten by the library.
	Type string

	Alias      string
	DistinctId string
	Timestamp  time.Time
	properties Properties
}

func (msg Alias) SetProperty(name string, value interface{}) Properties {
	if msg.properties == nil {
		msg.properties = Properties{}
	}

	msg.properties.Set(name, value)

	return msg.properties
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
	GeoIPDisable bool   `json:"$geoip_disable,omitempty"`
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
	library := "posthog-go"
	libraryVersion := getVersion()

	properties := AliasInApiProperties{
		DistinctId: msg.DistinctId,
		Alias:      msg.Alias,
		Lib:        library,
		LibVersion: libraryVersion,
	}

	if msg.properties != nil {
		geoIPDisable, exist := msg.properties.Get(GeoIPDisableKey)
		if exist {
			properties.GeoIPDisable = geoIPDisable.(bool)
		}
	}

	apified := AliasInApi{
		Type:           msg.Type,
		Event:          "$create_alias",
		Library:        library,
		LibraryVersion: libraryVersion,
		Timestamp:      msg.Timestamp,
		Properties:     properties,
	}

	return apified
}
