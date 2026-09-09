package posthog

import (
	"bytes"

	json "github.com/goccy/go-json"
)

// marshalProperty applies the event-property policy to the JSON representation,
// not the Go value: typed nils, structs and custom marshalers follow the same
// rules. Token traversal preserves ordered duplicate members; UseNumber keeps
// numeric tokens intact. Only the private output buffer is changed.
func marshalProperty(value interface{}) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return appendPropertyJSON(make([]byte, 0, len(data)), decoder)
}

// The input has already been validated by json.Marshal, including custom JSON.
// Append each occurrence independently, rolling back only null object members.
func appendPropertyJSON(data []byte, decoder *json.Decoder) ([]byte, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, container := token.(json.Delim)
	if !container {
		scalar, err := json.Marshal(token)
		return append(data, scalar...), err
	}
	data = append(data, byte(delim))
	start := len(data)
	for decoder.More() {
		memberStart := len(data)
		if memberStart > start {
			data = append(data, ',')
		}
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			data = append(data, encodedKey...)
			data = append(data, ':')
		}
		valueStart := len(data)
		data, err = appendPropertyJSON(data, decoder)
		if err != nil {
			return nil, err
		}
		if delim == '{' && string(data[valueStart:]) == "null" {
			data = data[:memberStart]
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	return append(data, byte(end.(json.Delim))), nil
}

// eventProperties is a wire-only wrapper, deliberately not a MarshalJSON method
// on public Properties: feature flag requests, caches and other JSON are outside
// the event-property contract. preserve names typed metadata already installed
// by event producers; custom $set/$group_set are never exempt.
type eventProperties struct {
	values   Properties
	preserve []string
}

func (p eventProperties) MarshalJSON() ([]byte, error) {
	if len(p.preserve) == 0 {
		return marshalProperty(p.values)
	}
	custom := make(Properties, len(p.values))
	for key, value := range p.values {
		custom[key] = value
	}
	for _, key := range p.preserve {
		delete(custom, key)
	}
	data, err := marshalProperty(custom)
	if err != nil {
		return nil, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, err
	}
	for _, key := range p.preserve {
		if value, ok := p.values[key]; ok {
			data, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			merged[key] = data
		}
	}
	return json.Marshal(merged)
}

// Flag producers deliberately emit nil responses for missing/error evaluations.
// Preserve only those root nulls on their event, and only the exact evaluated
// feature key when present. Never restore fields removed by the privacy allowlist.
func flagEventProperties(event string, values Properties) eventProperties {
	props := eventProperties{values: values}
	if event == "$feature_flag_called" {
		keys := []string{"$feature_flag_response"}
		if key, ok := values["$feature_flag"].(string); ok {
			keys = append(keys, "$feature/"+key)
		}
		for _, key := range keys {
			if value, ok := values[key]; ok && value == nil {
				props.preserve = append(props.preserve, key)
			}
		}
	}
	return props
}

func marshalAPIEvent(apiMsg APIMessage) ([]byte, error) {
	switch msg := apiMsg.(type) {
	case CaptureInApi:
		return json.Marshal(struct {
			CaptureInApi
			Properties eventProperties `json:"properties"`
		}{msg, flagEventProperties(msg.Event, msg.Properties)})
	case IdentifyInApi:
		return json.Marshal(struct {
			IdentifyInApi
			Set eventProperties `json:"$set"`
		}{msg, eventProperties{values: msg.Set}})
	case GroupIdentifyInApi:
		return json.Marshal(struct {
			GroupIdentifyInApi
			Properties eventProperties `json:"properties"`
		}{msg, eventProperties{values: msg.Properties}})
	default:
		// ExceptionInApiProperties flattens and normalizes only Custom, leaving its
		// typed metadata alone. Alias has no caller-supplied property subtree.
		return json.Marshal(apiMsg)
	}
}
