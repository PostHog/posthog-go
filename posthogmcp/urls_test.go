package posthogmcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Cases ported from posthog-python's test_sanitize_url_credentials, with its
// explanations. Each expected value must also come back unchanged.
func TestSanitizeStringRedactsURLCredentials(t *testing.T) {
	for _, test := range []struct {
		value, want string
	}{
		{"https://example.com/guide?token=fakesecret&token=fakeaccess&empty=", "https://example.com/guide?token=%5Bredacted%5D&token=%5Bredacted%5D&empty="},
		{"https://example.com/guide?X-Goog-Credential=fakecredential&X-Goog-Signature=fakesignature", "https://example.com/guide?X-Goog-Credential=%5Bredacted%5D&X-Goog-Signature=%5Bredacted%5D"},
		{"https://example.com/guide?sig=fakesignature&Signature=fakesignature&X-Amz-Security-Token=fakesecret", "https://example.com/guide?sig=%5Bredacted%5D&Signature=%5Bredacted%5D&X-Amz-Security-Token=%5Bredacted%5D"},
		{"https://fakeuser@example.com/guide", "https://%5Bredacted%5D@example.com/guide"},
		{"https://example.com/guide?%61=hello%20world&empty=#part", "https://example.com/guide?%61=hello%20world&empty=#part"},
		{"Cannot read https://fakeuser:fakepass@example.com/guide or https://example.com/guide?token=fakesecret", "Cannot read https://%5Bredacted%5D@example.com/guide or https://example.com/guide?token=%5Bredacted%5D"},
		{"https://fakeuser:fakepass@[invalid/guide?token=fakesecret", "[redacted]"},
		{"https://app.example.com/cb#access_token=fakeaccess&token_type=bearer", "https://app.example.com/cb#access_token=%5Bredacted%5D&token_type=%5Bredacted%5D"},
		{"https://example.com/doc#section-2", "https://example.com/doc#section-2"},
		// A plain fragment is text, and text can carry an address: a match ends at
		// the first `#`, so this pass is the only one that sees that address.
		{"[a](https://public.test/#intro)[b](https://fakeuser:fakepass@private.test/doc)", "[a](https://public.test/#intro)[b](https://%5Bredacted%5D@private.test/doc)"},
		{"[a](https://public.test/#intro)[b](https://private.test/doc)", "[a](https://public.test/#intro)[b](https://private.test/doc)"},
		// A route prefix is that same plain text: it is kept verbatim, so it has to
		// be sanitized too.
		{"https://public.test/#https://fakeuser:fakepass@private.test/doc?page=1", "https://public.test/#https://%5Bredacted%5D@private.test/doc?page=1"},
		{"[a](https://public.test/#intro)[b](https://fakeuser:fakepass@private.test/doc?page=1)", "[a](https://public.test/#intro)[b](https://%5Bredacted%5D@private.test/doc?page=1)"},
		// A hash-routed URL puts the route in the fragment: it stays verbatim, and
		// only what follows the first `?` is a field list.
		{"https://example.com/#/callback?token=fakesecret", "https://example.com/#/callback?token=%5Bredacted%5D"},
		{"https://example.com/#/docs?page=2", "https://example.com/#/docs?page=2"},
		{"https://example.com/#/callback", "https://example.com/#/callback"},
		// Shape says nothing: this half of the fragment holds a `=`, so it is read
		// as fields even though it looks like a path.
		{"https://example.com/#/docs/id=1?token=fakesecret", "https://example.com/#/docs/id=1?token=%5Bredacted%5D"},
		// ... but when the half before the `?` ends in a value we just redacted,
		// that `?` may be a character of the credential rather than a boundary, so
		// what follows it goes too.
		{"https://example.com/#password=prefix?fakesecret", "https://example.com/#password=%5Bredacted%5D?[redacted]"},
		{"https://example.com/#password=prefix?token=x&page=1", "https://example.com/#password=%5Bredacted%5D?[redacted]"},
		// The head is byte-identical here — the PostHog-token pass had already
		// redacted that value — so what marks the tail as suspect is the key.
		{"https://example.com/#password=phx_EXAMPLEONLYFAKEVALUE00000000000?private-suffix", "https://example.com/#password=[redacted]?[redacted]"},
		{"https://example.com/#/docs/id=1", "https://example.com/#/docs/id=1"},
		// A field list whose key happens to start with a `/`; re-serializing the
		// redacted field percent-encodes that `/`.
		{"https://example.com/#/token=fakesecret", "https://example.com/#%2Ftoken=%5Bredacted%5D"},
		// A `?` splits a fragment in two, and each half is a field list or plain
		// text on its own terms — the half before the `?` here is fields, and the
		// half after keeps its own encoding.
		{"https://example.com/#access_token=fakesecret&next=https://other.test/?page=1", "https://example.com/#access_token=%5Bredacted%5D&next=https%3A%2F%2Fother.test%2F?page=1"},
		{"https://example.com/#/token=fakesecret&next=https://other.test/?page=1", "https://example.com/#%2Ftoken=%5Bredacted%5D&next=https%3A%2F%2Fother.test%2F?page=1"},
		// A `;` is a field separator to some servers and a value character to
		// others, so a value holding one goes whole rather than being split: the
		// legacy field is still redacted, and `password=pre;fix` keeps its tail
		// out of the payload.
		{"https://example.com/x?a=1;token=fakesecret", "https://example.com/x?a=%5Bredacted%5D"},
		{"https://example.com/guide?password=prefix;remainingsecret", "https://example.com/guide?password=%5Bredacted%5D"},
		{"https://example.com/x?a=1;b=2", "https://example.com/x?a=1;b=2"},
		// A `;` inside a KEY names the credential just as a `-` or `_` would.
		{"https://example.com/?download;token=fakesecret", "https://example.com/?download%3Btoken=%5Bredacted%5D"},
		// Neither a `;` nor the fragment's own `?` ends a value that is already
		// open, so the address behind one stays with its field and goes with it.
		{"https://example.com/?password=prefix;https://private.example/remainingsecret", "https://example.com/?password=%5Bredacted%5D"},
		{"https://example.com/#password=prefix?https://private.example/remainingsecret", "https://example.com/#password=%5Bredacted%5D?[redacted]"},
		// Attached the same way, but this head holds no credential, so the tail is
		// sanitized as the text it is.
		{"https://example.com/#/docs/id=1?https://fakeuser:fakepass@x.test/doc", "https://example.com/#/docs/id=1?https://%5Bredacted%5D@x.test/doc"},
		// ... while a `?` that opens no value still divides: the address after it
		// is its own.
		{"https://example.com/?a=1#b?https://fakeuser:fakepass@x.test/doc", "https://example.com/?a=1#b?https://%5Bredacted%5D@x.test/doc"},
		{"https://example.com/x?jwt=fakejwt&sessionid=fakesession&code=fakecode&country_code=BR", "https://example.com/x?jwt=%5Bredacted%5D&sessionid=%5Bredacted%5D&code=%5Bredacted%5D&country_code=BR"},
		// A redacted LAST field takes the split-off punctuation with it: the
		// punctuation may be the credential's own tail (`?password=hunter2!!!`).
		{"See https://example.com/x?sig=fakesignature, then retry.", "See https://example.com/x?sig=%5Bredacted%5D then retry."},
		{"Failed (https://example.com/x?sig=fakesignature).", "Failed (https://example.com/x?sig=%5Bredacted%5D"},
		{"See https://example.com/x?password=fakepass!, then retry.", "See https://example.com/x?password=%5Bredacted%5D then retry."},
		// ... but only the last field: anything after it proves where the URL ended.
		{"See https://example.com/x?sig=fakesignature&page=2, then retry.", "See https://example.com/x?sig=%5Bredacted%5D&page=2, then retry."},
		{"See https://example.com/x?sig=fakesignature#intro, then retry.", "See https://example.com/x?sig=%5Bredacted%5D#intro, then retry."},
		{"Failed (https://example.com/x?a=b).", "Failed (https://example.com/x?a=b)."},
		{"resource_https://fakeuser:fakepass@example.com/doc", "resource_https://%5Bredacted%5D@example.com/doc"},
		{"https://gitlab.example.com/api?private_token=fakesecret&oauth_signature=fakesignature&id_token=fakeaccess&subscription-key=fakekey&sort_key=name", "https://gitlab.example.com/api?private_token=%5Bredacted%5D&oauth_signature=%5Bredacted%5D&id_token=%5Bredacted%5D&subscription-key=%5Bredacted%5D&sort_key=%5Bredacted%5D"},
		{"https://gateway.example.com/fetch?url=https://svc:fakepass@internal.example.com/doc%3Ftoken%3Dfakesecret", "https://gateway.example.com/fetch?url=https%3A%2F%2F%255Bredacted%255D%40internal.example.com%2Fdoc%3Ftoken%3D%255Bredacted%255D"},
		{"https://en.wikipedia.org/wiki/Foo_(bar)", "https://en.wikipedia.org/wiki/Foo_(bar)"},
		{"https://fakeuser:fakepass@en.wikipedia.org/wiki/Foo_(bar).", "https://%5Bredacted%5D@en.wikipedia.org/wiki/Foo_(bar)."},
		{"file:///guide.md", "file:///guide.md"},
		// An MCP resource uri need not have an authority, so the `//` is optional.
		// `urlunsplit` re-serializes an authority-less `file:` uri as `file:///`,
		// which is also what JS's `new URL()` produces.
		{"file:/guide.md?token=fakesecret", "file:///guide.md?token=%5Bredacted%5D"},
		{"resource:guide?token=fakesecret", "resource:guide?token=%5Bredacted%5D"},
		{"see:resource:guide?token=fakesecret", "see:resource:guide?token=%5Bredacted%5D"},
		// An optional authority over-matches prose, which costs nothing: a match
		// with nothing to redact is returned byte-for-byte.
		{"Error: see resource:guide.", "Error: see resource:guide."},
		{"Meet at12:30 today", "Meet at12:30 today"},
		{"resource:guide", "resource:guide"},
		// A match that holds a prose word in front of the address, or two addresses
		// run together, is split at the second address and each part sanitized on
		// its own — otherwise the second one hides in the first one's path.
		{"Failed URL:https://fakeuser:fakepass@example.com/doc", "Failed URL:https://%5Bredacted%5D@example.com/doc"},
		{"URL:https://example.com/x?token=fakesecret", "URL:https://example.com/x?token=%5Bredacted%5D"},
		{"Note:https://example.com/doc", "Note:https://example.com/doc"},
		{"a:b:https://fakeuser:fakepass@example.com/doc", "a:b:https://%5Bredacted%5D@example.com/doc"},
		{"https://example.com/doc,https://fakeuser:fakepass@other.example.com/doc", "https://example.com/doc,https://%5Bredacted%5D@other.example.com/doc"},
		// An adjacent address is adjacent wherever it sits: a query key holds one
		// as readily as a path, and a key is never redacted on its own.
		{"[a](https://public.test/?download)[b](https://fakeuser:fakepass@private.test/doc)", "[a](https://public.test/?download)[b](https://%5Bredacted%5D@private.test/doc)"},
		// ... unless it sits in a field's value, where splitting it off would cut
		// that value in two and publish the tail. The nearest structural character
		// before the authority decides: `=` means value, `/` and the field
		// separators mean a new address.
		{"https://host/x?token=foo%20https://secret.test/private", "https://host/x?token=%5Bredacted%5D"},
		// A second `?` inside a value is a character of that value, not a
		// delimiter, so the address after it belongs to the token.
		{"https://example.com/?token=prefix?https://secret.example/private", "https://example.com/?token=%5Bredacted%5D"},
		{"https://example.com/?q=see,https://fakeuser:fakepass@x.test/doc", "https://example.com/?q=see%2Chttps%3A%2F%2F%255Bredacted%255D%40x.test%2Fdoc"},
		{"https://host/a=b/c,https://fakeuser:fakepass@x.test/doc", "https://host/a=b/c,https://%5Bredacted%5D@x.test/doc"},
		// Only the fields region holds values, so a `=` in the path never makes
		// the address that follows it part of one.
		{"https://example.com/redirect=https://fakeuser:fakepass@private.example.com/doc", "https://example.com/redirect=https://%5Bredacted%5D@private.example.com/doc"},
		{"https://example.com/?https://fakeuser:fakepass@x.test/doc", "https://example.com/?https://%5Bredacted%5D@x.test/doc"},
		// The closing `)` goes with the redacted trailing field, by the rule above:
		// punctuation after a rewritten last field may be the credential's own.
		{"[a](https://example.com/a)[b](https://example.com/b?token=fakesecret)", "[a](https://example.com/a)[b](https://example.com/b?token=%5Bredacted%5D"},
		// An authority after the first `?` is a query value, not a second address:
		// the outer URI is parsed whole, which is what redacts its own password.
		{"file:/guide?password=fakepass&url=https://example.com", "file:///guide?password=%5Bredacted%5D&url=https%3A%2F%2Fexample.com"},
		// The `+` decodes to a space, so the inner address is part of the token's
		// value and goes with it.
		{"resource:g?token=fakesecret+https://fakeuser:fakepass@b", "resource:g?token=%5Bredacted%5D"},
		// The credential detectors run before the URL pass: rewriting a URL can
		// push a word past the window the detector scans, and a known token format
		// in a long URL would survive.
		{"https://example.com/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?ref=ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&token=x", "[redacted]"},
		{"https://example.com/o'reilly?token=fakesecret", "https://example.com/o'reilly?token=%5Bredacted%5D"},
		{"https://fakeuser:fake'pass@example.com/doc", "https://%5Bredacted%5D@example.com/doc"},
		// A string that is nothing but a URL has no prose, so its tail belongs to
		// the URL: `!!!` is part of the password, `.` is part of the path.
		{"https://example.com/login?password=fakepass!!!", "https://example.com/login?password=%5Bredacted%5D"},
		{"https://example.com/x?a=b.", "https://example.com/x?a=b."},
		// One level of nesting is sanitized; a value still carrying a URL past that
		// is dropped rather than trusted.
		{"https://gateway.example.com/fetch?url=https%3A%2F%2Fgateway2.example.com%2Ffetch%3Furl%3Dhttps%253A%252F%252Finternal.test%252Fdoc%253Ftoken%253Dfakesecret", "https://gateway.example.com/fetch?url=https%3A%2F%2Fgateway2.example.com%2Ffetch%3Furl%3D%255Bredacted%255D"},
		// PostHog tokens are redacted before the URL is rewritten: percent-encoding
		// the `/` would put `%2F` where the token pattern's `\bph` boundary needs a
		// word boundary.
		{"https://example.com/?ref=/phx_EXAMPLEONLYFAKEVALUE00000000000&token=fakesecret", "https://example.com/?ref=%2F%5Bredacted%5D&token=%5Bredacted%5D"},
		{"Read 'https://example.com/x?sig=fakesignature' first.", "Read 'https://example.com/x?sig=%5Bredacted%5D first."},
		// Neither the fragment text pass nor the address split may recurse per URL.
		{strings.Repeat("resource:x#", 10_000) + "intro", "resource:x#resource:x#[redacted]"},
		{
			strings.Repeat("https://a.test/x,", 10_000) + "https://fakeuser:fakepass@b.test/doc",
			strings.Repeat("https://a.test/x,", 10_000) + "https://%5Bredacted%5D@b.test/doc",
		},
		// Characters NFKC folds into a delimiter make the netloc invalid, so the
		// userinfo they would hide goes with the URL.
		{"https://fakeuser：fakepass＠example.com/doc", "[redacted]"},
		{"HTTPS://fakeuser@example.com/doc", "https://%5Bredacted%5D@example.com/doc"},
		{"https://[::1]/x?token=fakesecret", "https://[::1]/x?token=%5Bredacted%5D"},
		{"https://[127.0.0.1]/x?token=fakesecret", "[redacted]"},
		{"https://example.com/x?token%0A=fakesecret", "https://example.com/x?token%0A=%5Bredacted%5D"},
		{"https://example.com/x?q=%E2%82&token=fakesecret", "https://example.com/x?q=%EF%BF%BD&token=%5Bredacted%5D"},
	} {
		assert.Equal(t, test.want, sanitizeString(test.value), test.value)
		assert.Equal(t, test.want, sanitizeString(test.want), "sanitizing twice: "+test.value)
	}
}

func TestSanitizeStringURLBounds(t *testing.T) {
	for _, test := range []struct {
		name      string
		uri       string
		oversized bool
	}{
		{"length limit", "https://example.com/" + strings.Repeat("a/", 4086), false},
		{"length over limit", "https://example.com/" + strings.Repeat("a/", 4086) + "a", true},
		{"field limit", "https://example.com/?" + strings.Repeat("page=1&", 127) + "page=1", false},
		{"fields over limit", "https://example.com/?" + strings.Repeat("page=1&", 128) + "page=1", true},
		{"empty fields", "https://example.com/?" + strings.Repeat("&", 128) + "token=fakesecret", true},
		// The length bound guards authority parsing, so it must not swallow a
		// long data uri, and invalid base64 keeps the binary gate from taking it.
		{"authority-less over limit", "data:application/octet-stream;base64,AAAA%ZZ" + strings.Repeat("A", 10_000), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := test.uri
			if test.oversized {
				want = redactedValue
			}
			assert.Equal(t, want, sanitizeString(test.uri))
			assert.Equal(t, "Cannot read "+want, sanitizeString("Cannot read "+test.uri))
		})
	}
}

func TestSanitizeResourceNameKeepsIdentifiersAndRedactsURIs(t *testing.T) {
	for _, test := range []struct {
		value, want string
	}{
		// A tool name is an identifier: the entropy detector would read this one
		// as a credential and cost every per-tool metric.
		{"Get_Organization_Memberships", "Get_Organization_Memberships"},
		{"https://fakeuser:fakepass@example.com/doc", "https://%5Bredacted%5D@example.com/doc"},
		{"https://example.com/guide?token=fakesecret", "https://example.com/guide?token=%5Bredacted%5D"},
		{"resource:guide?token=fakesecret", "resource:guide?token=%5Bredacted%5D"},
		{"see:resource:guide?token=fakesecret", "see:resource:guide?token=%5Bredacted%5D"},
		{"query_phx_EXAMPLEONLYFAKEVALUE00000000000", "query_phx_EXAMPLEONLYFAKEVALUE00000000000"},
		{"query phx_EXAMPLEONLYFAKEVALUE00000000000", "query [redacted]"},
	} {
		assert.Equal(t, test.want, sanitizeResourceName(test.value), test.value)
	}
	assert.Equal(t, redactedValue, sanitizeString("Get_Organization_Memberships"))
}
