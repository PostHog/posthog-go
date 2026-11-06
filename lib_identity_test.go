package posthog

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type apiMessageRecorder struct {
	mu   sync.Mutex
	msgs []APIMessage
}

func (r *apiMessageRecorder) Success(m APIMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
}

func (r *apiMessageRecorder) Failure(m APIMessage, err error) {}

func (r *apiMessageRecorder) last(t *testing.T) APIMessage {
	t.Helper()
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.msgs) > 0
	}, 5e9, 1e7)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msgs[len(r.msgs)-1]
}

// callbackIdentity returns the top-level library fields and the $lib pair in
// the properties of a callback message.
func callbackIdentity(t *testing.T, m APIMessage) (library, libraryVersion string, props map[string]interface{}) {
	t.Helper()
	switch v := m.(type) {
	case CaptureInApi:
		return v.Library, v.LibraryVersion, v.Properties
	case IdentifyInApi:
		return v.Library, v.LibraryVersion, v.Properties
	case GroupIdentifyInApi:
		return v.Library, v.LibraryVersion, v.Properties
	case AliasInApi:
		return v.Library, v.LibraryVersion, map[string]interface{}{"$lib": v.Properties.Lib, "$lib_version": v.Properties.LibVersion}
	case ExceptionInApi:
		return v.Library, v.LibraryVersion, map[string]interface{}{"$lib": v.Properties.Lib, "$lib_version": v.Properties.LibVersion}
	}
	t.Fatalf("unexpected callback message %T", m)
	return "", "", nil
}

func withLibEventProperties(msg Message, props Properties) Message {
	switch m := msg.(type) {
	case Capture:
		m.Properties = props
		return m
	case Exception:
		m.Properties = props
		return m
	case Identify:
		m.EventProperties = props
		return m
	case Alias:
		m.EventProperties = props
		return m
	case GroupIdentify:
		m.EventProperties = props
		return m
	}
	return msg
}

func libEventProperties(msg Message) Properties {
	switch m := msg.(type) {
	case Capture:
		return m.Properties
	case Exception:
		return m.Properties
	case Identify:
		return m.EventProperties
	case Alias:
		return m.EventProperties
	case GroupIdentify:
		return m.EventProperties
	}
	return nil
}

// absentProperty marks a key that must not be in the properties.
type absentProperty struct{}

// libPair is an expected $lib and $lib_version, each a value or absentProperty.
type libPair struct{ lib, version interface{} }

func requireLibPair(t *testing.T, want libPair, props map[string]interface{}, msgAndArgs string) {
	t.Helper()
	for key, value := range map[string]interface{}{propertyLib: want.lib, propertyLibVersion: want.version} {
		if _, absent := value.(absentProperty); absent {
			require.NotContains(t, props, key, "%s: %s", msgAndArgs, key)
			continue
		}
		require.Contains(t, props, key, "%s: %s", msgAndArgs, key)
		require.Equal(t, value, props[key], "%s: %s", msgAndArgs, key)
	}
}

type namedLib string

func TestSDKIdentityLayers(t *testing.T) {
	exceptionList := []ExceptionItem{{Type: "t", Value: "v"}}
	messages := []Message{
		Capture{DistinctId: "d", Event: "e"},
		Exception{DistinctId: "d", ExceptionList: exceptionList},
		Identify{DistinctId: "d"},
		Alias{DistinctId: "d", Alias: "a"},
		GroupIdentify{Type: "company", Key: "k"},
	}
	lanes := []struct {
		name    string
		enqueue func(c Client, ctx context.Context, m Message) error
	}{
		{"analytics", func(c Client, ctx context.Context, m Message) error { return EnqueueWithContext(ctx, c, m) }},
		{"ai", func(c Client, ctx context.Context, m Message) error { return c.(*client).EnqueueAIWithContext(ctx, m) }},
	}
	pair := func(lib, version interface{}) Properties {
		return NewProperties().Set(propertyLib, lib).Set(propertyLibVersion, version)
	}
	absent := absentProperty{}
	sdk := libPair{SDKName, getVersion()}
	cases := []struct {
		name     string
		event    Properties
		context  Properties
		defaults Properties
		hook     func(Properties)
		// seen is what BeforeSend saw; it defaults to wire.
		seen libPair
		// wire is the pair on the wire, after BeforeSend.
		wire libPair
		// callback is the pair the callback reports, which is the one PostHog
		// stores: the server fills an unset $lib from the PostHog-Sdk-Info header.
		callback [2]string
	}{
		{name: "no layer sets $lib", wire: sdk},
		{name: "event pair wins", event: pair("event-lib", "event-v"), context: pair("ctx-lib", "ctx-v"), defaults: pair("default-lib", "default-v"), wire: libPair{"event-lib", "event-v"}},
		{name: "request context beats defaults", context: pair("ctx-lib", "ctx-v"), defaults: pair("default-lib", "default-v"), wire: libPair{"ctx-lib", "ctx-v"}},
		{name: "defaults beat the sdk", defaults: pair("default-lib", "default-v"), wire: libPair{"default-lib", "default-v"}},
		{name: "caller $lib alone gets no version", event: NewProperties().Set(propertyLib, "event-lib"), wire: libPair{"event-lib", absent}},
		{name: "caller $lib alone takes no lower layer's version", event: NewProperties().Set(propertyLib, "event-lib"), defaults: pair("default-lib", "default-v"), wire: libPair{"event-lib", absent}},
		{name: "sdk $lib alone gets the sdk version", event: NewProperties().Set(propertyLib, SDKName), wire: sdk},
		{name: "sdk $lib with an empty version gets the sdk version", event: pair(SDKName, ""), wire: sdk},
		{name: "sdk $lib keeps a set version", event: pair(SDKName, "event-v"), wire: libPair{SDKName, "event-v"}},
		{name: "lone $lib_version is replaced", event: NewProperties().Set(propertyLibVersion, "event-v"), wire: sdk},
		{name: "lone $lib_version yields to a lower layer's pair", event: NewProperties().Set(propertyLibVersion, "event-v"), defaults: pair("default-lib", "default-v"), wire: libPair{"default-lib", "default-v"}},
		{name: "nil $lib is replaced", event: pair(nil, "event-v"), wire: sdk},
		{name: "empty $lib is replaced", event: pair("", "event-v"), wire: sdk},
		{name: "non-string $lib is replaced", event: pair(5, "event-v"), wire: sdk},
		{name: "nil $lib yields to a lower layer's pair", event: NewProperties().Set(propertyLib, nil), context: pair("ctx-lib", "ctx-v"), wire: libPair{"ctx-lib", "ctx-v"}},
		{name: "named string $lib is set", event: NewProperties().Set(propertyLib, namedLib("named-lib")), seen: libPair{namedLib("named-lib"), absent}, wire: libPair{"named-lib", absent}, callback: [2]string{"named-lib", ""}},
		{name: "hook changes both", hook: func(p Properties) { p.Set(propertyLib, "hook-lib").Set(propertyLibVersion, "hook-v") }, seen: sdk, wire: libPair{"hook-lib", "hook-v"}},
		{name: "hook sets $lib alone", hook: func(p Properties) { p.Set(propertyLib, "hook-lib"); delete(p, propertyLibVersion) }, seen: sdk, wire: libPair{"hook-lib", absent}},
		{name: "hook removes both", hook: func(p Properties) { delete(p, propertyLib); delete(p, propertyLibVersion) }, seen: sdk, wire: libPair{absent, absent}, callback: [2]string{SDKName, getVersion()}},
		{name: "hook sets nil", hook: func(p Properties) { p.Set(propertyLib, nil) }, seen: sdk, wire: libPair{nil, getVersion()}, callback: [2]string{SDKName, getVersion()}},
		{name: "hook sets a non-string", hook: func(p Properties) { p.Set(propertyLib, 5) }, seen: sdk, wire: libPair{float64(5), getVersion()}, callback: [2]string{SDKName, getVersion()}},
	}
	for _, lane := range lanes {
		for _, tc := range cases {
			for _, msg := range messages {
				lane, tc, msg := lane, tc, msg
				t.Run(fmt.Sprintf("%s/%s/%T", lane.name, tc.name, msg), func(t *testing.T) {
					t.Parallel()
					body, server := mockServer()
					defer server.Close()
					recorder := &apiMessageRecorder{}
					var seen Properties
					c, err := NewWithConfig("test-api-key", Config{
						Endpoint:               server.URL,
						BatchSize:              1,
						now:                    mockTime,
						Callback:               recorder,
						DefaultEventProperties: tc.defaults,
						BeforeSend: func(m Message) Message {
							props := libEventProperties(m)
							seen = NewProperties().Merge(props)
							if tc.hook != nil {
								props = NewProperties().Merge(props)
								tc.hook(props)
								return withLibEventProperties(m, props)
							}
							return m
						},
					})
					require.NoError(t, err)
					defer c.Close()

					ctx := context.Background()
					if tc.context != nil {
						ctx = WithFreshRequestContext(ctx, RequestContext{Properties: tc.context})
					}
					m := msg
					if tc.event != nil {
						m = withLibEventProperties(m, NewProperties().Merge(tc.event))
					}
					require.NoError(t, lane.enqueue(c, ctx, m))

					props := requireProperties(t, readSingleBatchEvent(t, body))
					require.NotNil(t, seen, "BeforeSend ran")
					wantSeen := tc.seen
					if wantSeen == (libPair{}) {
						wantSeen = tc.wire
					}
					requireLibPair(t, wantSeen, seen, "BeforeSend saw")
					requireLibPair(t, tc.wire, props, "wire")

					wantLib, wantVersion := tc.callback[0], tc.callback[1]
					if tc.callback == [2]string{} {
						wantLib, _ = tc.wire.lib.(string)
						wantVersion, _ = tc.wire.version.(string)
					}
					library, libraryVersion, cbProps := callbackIdentity(t, recorder.last(t))
					require.Equal(t, wantLib, library, "callback Library")
					require.Equal(t, wantVersion, libraryVersion, "callback LibraryVersion")
					gotLib, _ := libString(cbProps[propertyLib])
					gotVersion, _ := libString(cbProps[propertyLibVersion])
					require.Equal(t, wantLib, gotLib, "callback properties $lib")
					require.Equal(t, wantVersion, gotVersion, "callback properties $lib_version")
				})
			}
		}
	}
}

func TestMinimalFlagCalledEventKeepsLibOnWire(t *testing.T) {
	props := minimalFlagCalledEventProperties(fillSDKProperties(NewProperties().Set("$feature_flag", "f").Set("custom", "x"), false, false))
	ev := buildEvent(Capture{DistinctId: "d", Event: "$feature_flag_called", Properties: props}.apifyEvent(), nil)
	require.Equal(t, SDKName, ev.Properties[propertyLib])
	require.Equal(t, getVersion(), ev.Properties[propertyLibVersion])
	require.NotContains(t, ev.Properties, "custom")
}

func TestSDKIdentityPropertiesIsNotWritten(t *testing.T) {
	before := NewProperties().Merge(sdkIdentityProperties())
	for _, props := range []Properties{{"a": 1}, {propertyLib: SDKName}, {propertyLib: nil, propertyLibVersion: "v"}} {
		caller := NewProperties().Merge(props)
		filled := fillSDKProperties(caller, true, true)
		filled[propertyLib] = "changed"
		filled[propertyLibVersion] = "changed"
		require.Equal(t, before, sdkIdentityProperties())
		require.Equal(t, props, caller, "the caller's map is not written")
	}
}
