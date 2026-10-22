package posthog

import (
	"io"
	"log"
	"strings"
	"testing"
)

func TestRegexCompatibilityInconclusive(t *testing.T) {
	cases := []struct{ name, pattern, input string }{
		{"Unicode digits", `^\d+$`, "١"},
		{"astral digits", `^\d+$`, "𝟙"},
		{"Unicode word", `^\w+$`, "é"},
		{"Unicode whitespace", `^\s$`, "\u00a0"},
		{"ASCII vertical tab", `^\s$`, "\v"},
		{"complement digit", `^\D$`, "١"},
		{"complement word", `^\W$`, "é"},
		{"complement whitespace", `^\S$`, "\v"},
		{"shorthand in class", `^[\d]+$`, "١"},
		{"lookahead", `a(?=b)`, "ab"},
		{"lookbehind", `(?<=a+)b`, "aab"},
		{"backreference", `(a)\1`, "aa"},
		{"class backspace", `[\b]`, "\b"},
		{"intersection", `^[a-z&&[^aeiou]]+$`, "bcdf"},
		{"flat intersection", `^[a&&b]$`, "a"},
		{"difference", `^[a-z--b]$`, "b"},
		{"symmetric difference", `^[a~~b]$`, "~"},
		{"boundary budget", `.*\bbot\b.*`, strings.Repeat("a", 140)},
		{"nonboundary budget", `.*\Bbot\B.*`, strings.Repeat("a", 140)},
		{"quantified anchor", `^?https?://`, "https://a"},
		{"quantified end anchor", `$?`, "x"},
		{"empty group repetition", `(?:)*`, "x"},
		{"empty capture repetition", `()+`, "x"},
		{"oversized repetition", `a{1001}`, strings.Repeat("a", 1001)},
		{"stacked repetition", `a*{2}`, "aa"},
		{"leading zero repetition", `a{0002}`, "aa"},
		{"shorthand range start", `[\s-9]`, "-"},
		{"shorthand range end", `[a-\d]`, "-"},
		{"leading repetition", `{2}`, "x"},
		{"leading alternative repetition", `a|{2}`, "x"},
		{"leading capture repetition", `({2})`, "x"},
		{"leading group repetition", `(?:{2})`, "x"},
		{"Rust word start/end", `\<foo\>`, "foo"},
		{"Rust word start independent", `^\<$`, "<"},
		{"Rust word end independent", `^\>$`, ">"},
		{"leading literal bracket intersection", `[]&&a]`, "]"},
		{"negated leading literal bracket intersection", `[^]&&a]`, "a"},
		{"Unicode property range", `[\p{L}-9]`, "a"},
	}
	for _, tc := range cases {
		for _, operator := range []string{"regex", "not_regex"} {
			t.Run(tc.name+"/"+operator, func(t *testing.T) {
				got, err := matchProperty(FlagProperty{Key: "k", Operator: operator, Value: tc.pattern}, Properties{"k": tc.input})
				if got || !isInconclusiveError(err) {
					t.Fatalf("got %v, %v; want false, inconclusive", got, err)
				}
			})
		}
	}
}

func TestRegexCompatibilityLocal(t *testing.T) {
	cases := []struct {
		name, pattern string
		input         interface{}
		match         bool
	}{
		{"search", `pro`, "a pro plan", true},
		{"case sensitive", `^pro$`, "PRO", false},
		{"ASCII digits", `^\d+$`, "123", true},
		{"ASCII word", `^\w+$`, "abc_123", true},
		{"ASCII whitespace", `^\s+$`, " \t\r\n\f", true},
		{"email", `^[\w.+-]+@example\.com$`, "a.b+tag@example.com", true},
		{"group repetition", `^(?:ab|cd){2,3}$`, "abcdab", true},
		{"inline flags", `(?i)^null$`, "NULL", true},
		{"scoped flags", `(?i:^null$)`, "NULL", true},
		{"absolute anchors", `\Afoo\z`, "foo", true},
		{"strict end anchor", `^foo$`, "foo\n", false},
		{"internal newline", `^foo$`, "foo\nbar", false},
		{"multiline", `(?m)^foo$`, "bar\nfoo\nbaz", true},
		{"Unicode property", `^\p{L}+$`, "é", true},
		{"Greek property", `^\p{Greek}+$`, "α", true},
		{"Unicode literal", `^😀?$`, "", true},
		{"scalar dot", `^.$`, "😀", true},
		{"carriage return dot", `^.$`, "\r", true},
		{"POSIX ASCII", `^[[:alpha:]]+$`, "é", false},
		{"POSIX letters", `^[[:alpha:]]+$`, "foo", true},
		{"leading literal bracket", `^[]a]+$`, "]a", true},
		{"negated leading literal bracket", `^[^]a]+$`, "b", true},
		{"hex scalar", `^\x{1F600}$`, "😀", true},
		{"escaped marker", `^\\p\{L\}$`, `\p{L}`, true},
		{"escaped flags", `^\(\?i\)$`, "(?i)", true},
		{"escaped punctuation", `^a\@\:\_\=\ b$`, "a@:_= b", true},
		{"escaped class punctuation", `[\@]`, "@", true},
		{"escaped caret repetition", `^\^?$`, "", true},
		{"escaped alternation repetition", `^\|{2}$`, "||", true},
		{"class trailing hyphen", `^[\w-]+$`, "a-b", true},
		{"class escaped hyphen", `^[a\-\d]+$`, "a-1", true},
		{"literal set operators", `^a&&b--c~~d$`, "a&&b--c~~d", true},
		{"empty Unicode", ``, "é", true},
		{"empty carriage return", ``, "\r", true},
		{"explicit null", `^null$`, nil, true},
	}
	for _, tc := range cases {
		for _, operator := range []string{"regex", "not_regex"} {
			t.Run(tc.name+"/"+operator, func(t *testing.T) {
				want := tc.match
				if operator == "not_regex" {
					want = !want
				}
				got, err := matchProperty(FlagProperty{Key: "k", Operator: operator, Value: tc.pattern}, Properties{"k": tc.input})
				if err != nil || got != want {
					t.Fatalf("got %v, %v; want %v, nil", got, err, want)
				}
			})
		}
	}
	for _, pattern := range []string{"[", "(", "*", "?*", `\q`} {
		for _, operator := range []string{"regex", "not_regex"} {
			got, err := matchProperty(FlagProperty{Key: "k", Operator: operator, Value: pattern}, Properties{"k": "é"})
			if got || err != nil {
				t.Errorf("invalid %q %s: got %v, %v", pattern, operator, got, err)
			}
		}
	}
}

// Only the remote request is stubbed; property matching and the public fallback gate are real.
type regexCompatibilityDecider struct{ calls int }

func (d *regexCompatibilityDecider) makeFlagsRequest(string, *string, Groups, Properties, map[string]Properties, bool, []string) (*FlagsResponse, error) {
	d.calls++
	return &FlagsResponse{FeatureFlags: map[string]interface{}{"regex-flag": "remote-sentinel"}}, nil
}

func TestRegexCompatibilityFallback(t *testing.T) {
	for _, operator := range []string{"regex", "not_regex"} {
		for _, mode := range []string{"direct", "cohort", "negated cohort"} {
			for _, tc := range []struct {
				name, pattern string
				properties    Properties
				fallback      bool
			}{
				{"Unicode mismatch", `^\d+$`, Properties{"k": "١"}, true},
				{"unsupported compile", `a(?=b)`, Properties{"k": "ab"}, true},
				{"backreference", `(a)\1`, Properties{"k": "aa"}, true},
				{"class backspace", `[\b]`, Properties{"k": "\b"}, true},
				{"set interpretation", `^[a&&b]$`, Properties{"k": "a"}, true},
				{"boundary budget", `.*\bbot\b.*`, Properties{"k": strings.Repeat("a", 140)}, true},
				{"missing property control", `a`, Properties{}, true},
				{"local match", `^a$`, Properties{"k": "a"}, false},
				{"local mismatch", `^a$`, Properties{"k": "b"}, false},
			} {
				for _, onlyLocal := range []bool{false, true} {
					t.Run(operator+"/"+mode+"/"+tc.name+"/local="+valueToString(onlyLocal), func(t *testing.T) {
						d := &regexCompatibilityDecider{}
						ready := make(chan bool)
						close(ready)
						p := &FeatureFlagsPoller{firstFeatureFlagRequestFinished: ready, decider: d, Logger: StdLogger(log.New(io.Discard, "", 0), false)}
						prop := FlagProperty{Key: "k", Type: "person", Operator: operator, Value: tc.pattern}
						cohorts := map[string]PropertyGroup{}
						if mode != "direct" {
							cohorts["1"] = preParsePG(PropertyGroup{Type: "AND", Values: []any{map[string]interface{}{"key": "k", "type": "person", "operator": operator, "value": tc.pattern, "negation": mode == "negated cohort"}}})
							prop = FlagProperty{Key: "id", Type: "cohort", Value: "1"}
						}
						flag := FeatureFlag{Key: "regex-flag", Active: true, Filters: Filter{Groups: []FeatureFlagCondition{{Properties: []FlagProperty{prop}}}}}
						p.state.Store(&flagsState{featureFlags: []FeatureFlag{flag}, flagsByKey: map[string]FeatureFlag{flag.Key: flag}, cohorts: cohorts})
						got, local, err := p.GetFeatureFlag(FeatureFlagPayload{Key: flag.Key, DistinctId: "person", PersonProperties: tc.properties, OnlyEvaluateLocally: onlyLocal})
						if tc.fallback {
							if onlyLocal {
								if local || !isInconclusiveError(err) || d.calls != 0 {
									t.Fatalf("local-only: got %v local=%v err=%v calls=%d", got, local, err, d.calls)
								}
							} else if got != "remote-sentinel" || local || err != nil || d.calls != 1 {
								t.Fatalf("fallback: got %v local=%v err=%v calls=%d", got, local, err, d.calls)
							}
						} else {
							want := tc.name == "local match"
							if operator == "not_regex" {
								want = !want
							}
							if mode == "negated cohort" {
								want = !want
							}
							if got != want || !local || err != nil || d.calls != 0 {
								t.Fatalf("local: got %v local=%v err=%v calls=%d; want %v", got, local, err, d.calls, want)
							}
						}
					})
				}
			}
		}
	}
}
