package posthog

import (
	"io"
	"log"
	"strings"
	"testing"
)

func TestRegexCompatibilityRequiresServer(t *testing.T) {
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
		{"nested oversized repetition", `(?:a{100}){11}`, strings.Repeat("a", 1100)},
		{"large valid bounds", `a{1001,1002}`, "a"},
		{"overflowing bounds", `a{4294967296,2}`, "aa"},
		{"leading-zero reversed bounds", `a{02,1}`, "aa"},
		{"overflowing maximum", `a{2,4294967296}`, "aa"},
		{"unsupported verbose comment with reversed text", `(?x)#a{2,1}`, "aa"},
		{"stacked repetition", `a*{2}`, "aa"},
		{"leading zero repetition", `a{0002}`, "aa"},
		{"leading zero maximum repetition", `a{1,02}`, "aa"},
		{"open lower bound repetition", `a{,2}`, "aa"},
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
				if got || !isServerEvalError(err) {
					t.Fatalf("got %v, %v; want false, server-required", got, err)
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
		{"exact repetition", `^a{2}$`, "aa", true},
		{"zero minimum repetition", `^a{0,2}$`, "", true},
		{"unbounded repetition", `^a{2,}$`, "aaa", true},
		{"escaped repetition candidate", `^a\{02\}$`, "a{02}", true},
		{"class repetition candidate", `^[{02,}]+$`, "{02,}", true},
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
		{"closed hex escape", `^\x{41}+$`, "AA", true},
		{"closed complement property", `^\P{L}+$`, "123", true},
		{"escaped incomplete escape", `^\\p\{$`, `\p{`, true},
		{"escaped reversed bounds", `^a\{2,1\}$`, "a{2,1}", true},
		{"class-local reversed bounds", `^[a{2,1}]+$`, "a{2,1}", true},
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
								wantError := isServerEvalError(err)
								if tc.name == "missing property control" {
									wantError = isInconclusiveError(err)
								}
								if local || !wantError || d.calls != 0 {
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

func TestRegexCompatibilityOrderedVariants(t *testing.T) {
	for _, operator := range []string{"regex", "not_regex"} {
		for _, mode := range []string{"direct", "cohort", "negated cohort", "nested cohort", "dependency"} {
			for _, tc := range []struct {
				name, pattern           string
				properties              Properties
				requiresServer, matches bool
			}{
				{"boundary", `^a\b$`, Properties{"k": "a"}, true, false},
				{"unsupported compilation", `a(?=b)`, Properties{"k": "ab"}, true, false},
				{"Unicode input", `^\d+$`, Properties{"k": "١"}, true, false},
				{"RE2 size cap", `a{1001}`, Properties{"k": "a"}, true, false},
				{"nested RE2 size cap", `(?:a{100}){11}`, Properties{"k": "a"}, true, false},
				{"missing context", `^a$`, Properties{}, false, false},
				{"safe match", `^a$`, Properties{"k": "a"}, false, true},
				{"safe mismatch", `^a$`, Properties{"k": "b"}, false, false},
				{"invalid", `[`, Properties{"k": "a"}, false, false},
			} {
				for _, onlyLocal := range []bool{false, true} {
					t.Run(operator+"/"+mode+"/"+tc.name+"/local="+valueToString(onlyLocal), func(t *testing.T) {
						d := &regexCompatibilityDecider{}
						ready := make(chan bool)
						close(ready)
						p := &FeatureFlagsPoller{firstFeatureFlagRequestFinished: ready, decider: d, Logger: StdLogger(log.New(io.Discard, "", 0), false)}
						prop := FlagProperty{Key: "k", Type: "person", Operator: operator, Value: tc.pattern}
						cohorts := map[string]PropertyGroup{}
						flags := map[string]FeatureFlag{}
						if mode == "dependency" {
							flags["dependency"] = FeatureFlag{Key: "dependency", Active: true, Filters: Filter{Groups: []FeatureFlagCondition{{Properties: []FlagProperty{prop}}}}}
							prop = FlagProperty{Key: "dependency", Type: "flag", Operator: "flag_evaluates_to", Value: true, DependencyChain: []string{"dependency"}}
						} else if mode != "direct" {
							leaf := map[string]interface{}{"key": "k", "type": "person", "operator": operator, "value": tc.pattern, "negation": mode == "negated cohort"}
							group := PropertyGroup{Type: "AND", Values: []any{leaf}}
							if mode == "nested cohort" {
								group = PropertyGroup{Type: "OR", Values: []any{map[string]interface{}{"type": "AND", "values": []any{leaf}}}}
							}
							cohorts["1"] = preParsePG(group)
							prop = FlagProperty{Key: "id", Type: "cohort", Value: "1"}
						}
						first, second := "first", "second"
						flag := FeatureFlag{Key: "regex-flag", Active: true, Filters: Filter{
							Multivariate: &Variants{Variants: []FlagVariant{{Key: first}, {Key: second}}},
							Groups:       []FeatureFlagCondition{{Properties: []FlagProperty{prop}, Variant: &first}, {Variant: &second}},
						}}
						flags[flag.Key] = flag
						p.state.Store(&flagsState{featureFlags: []FeatureFlag{flag}, flagsByKey: flags, cohorts: cohorts})
						got, local, err := p.GetFeatureFlag(FeatureFlagPayload{Key: flag.Key, DistinctId: "person", PersonProperties: tc.properties, OnlyEvaluateLocally: onlyLocal})
						if tc.requiresServer {
							if onlyLocal {
								if got != nil || local || !isServerEvalError(err) || d.calls != 0 {
									t.Fatalf("got %v local=%v err=%v calls=%d; want unresolved server-required", got, local, err, d.calls)
								}
							} else if got != "remote-sentinel" || local || err != nil || d.calls != 1 {
								t.Fatalf("got %v local=%v err=%v calls=%d; want remote fallback, not second", got, local, err, d.calls)
							}
						} else {
							matches := tc.matches
							if operator == "not_regex" && tc.name != "invalid" {
								matches = !matches
							}
							if mode == "negated cohort" {
								matches = !matches
							}
							if tc.name == "missing context" {
								matches = false
							}
							want := second
							if matches {
								want = first
							}
							if got != want || !local || err != nil || d.calls != 0 {
								t.Fatalf("got %v local=%v err=%v calls=%d; want local %s", got, local, err, d.calls, want)
							}
						}
					})
				}
			}
		}
	}
}

func BenchmarkClassifyFlagRegexRepetitionCandidates(b *testing.B) {
	for _, size := range []int{1000, 2000, 4000} {
		pattern := strings.Repeat("{", size) + strings.Repeat(",", size) + "}"
		b.Run(valueToString(size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(pattern)))
			for i := 0; i < b.N; i++ {
				if _, err := classifyFlagRegex(pattern); err == nil {
					b.Fatal("want deferred repetition candidate")
				}
			}
		})
	}
}

func TestRegexCompatibilityReversedBounds(t *testing.T) {
	for _, pattern := range []string{`a{2,1}`, `(?:a{2,1})`, `a{1001,1000}`, `a{4294967295,2}`} {
		for _, operator := range []string{"regex", "not_regex"} {
			t.Run(pattern+"/"+operator, func(t *testing.T) {
				prop := FlagProperty{Key: "k", Type: "person", Operator: operator, Value: pattern}
				matched, err := matchProperty(prop, Properties{"k": "aa"})
				if matched || err != nil {
					t.Errorf("got %v, %v; invalid bounds must fail both operators", matched, err)
				}
				for _, ordered := range []bool{false, true} {
					for _, onlyLocal := range []bool{false, true} {
						d := &regexCompatibilityDecider{}
						ready := make(chan bool)
						close(ready)
						p := &FeatureFlagsPoller{firstFeatureFlagRequestFinished: ready, decider: d, Logger: StdLogger(log.New(io.Discard, "", 0), false)}
						flag := FeatureFlag{Key: "regex-flag", Active: true, Filters: Filter{Groups: []FeatureFlagCondition{{Properties: []FlagProperty{prop}}}}}
						var want interface{} = false
						if ordered {
							first, second := "first", "second"
							flag.Filters.Multivariate = &Variants{Variants: []FlagVariant{{Key: first}, {Key: second}}}
							flag.Filters.Groups[0].Variant = &first
							flag.Filters.Groups = append(flag.Filters.Groups, FeatureFlagCondition{Variant: &second})
							want = second
						}
						p.state.Store(&flagsState{featureFlags: []FeatureFlag{flag}, flagsByKey: map[string]FeatureFlag{flag.Key: flag}})
						got, local, err := p.GetFeatureFlag(FeatureFlagPayload{Key: flag.Key, DistinctId: "person", PersonProperties: Properties{"k": "aa"}, OnlyEvaluateLocally: onlyLocal})
						if got != want || !local || err != nil || d.calls != 0 {
							t.Errorf("ordered=%v local-only=%v: got %v local=%v err=%v calls=%d; want local %v", ordered, onlyLocal, got, local, err, d.calls, want)
						}
					}
				}
			})
		}
	}
}

func TestRegexCompatibilityUnterminatedBracedEscapes(t *testing.T) {
	for _, tc := range []struct {
		pattern        string
		requiresServer bool
	}{
		{`\x{`, true},
		{`\p{`, false},
		{`\P{`, false},
		{`[\x{`, true},
		{`[\p{`, false},
		{`[\P{`, false},
		{`(?x)#a{2,1}\x{`, true},
		{`(?x)#a{2,1}\p{`, true},
		{`(?x)#a{2,1}\P{`, true},
	} {
		for _, operator := range []string{"regex", "not_regex"} {
			matched, err := matchProperty(FlagProperty{Key: "k", Operator: operator, Value: tc.pattern}, Properties{"k": "a"})
			if matched || (tc.requiresServer && !isServerEvalError(err)) || (!tc.requiresServer && err != nil) {
				t.Errorf("%s %q: got %v, %v; requires-server=%v", operator, tc.pattern, matched, err, tc.requiresServer)
			}
		}
	}
}

func BenchmarkRegexCompatibilityUnterminatedBracedEscapes(b *testing.B) {
	for _, escape := range []string{`\x{`, `\p{`, `\P{`, `(?x)#\x{`} {
		for _, size := range []int{12 << 10, 48 << 10, 192 << 10} {
			fragment, prefix := escape, ""
			if strings.HasPrefix(escape, "(?x)#") {
				fragment, prefix = `\x{`, "(?x)#"
			}
			pattern := prefix + strings.Repeat(fragment, size/len(fragment))
			for _, match := range []bool{false, true} {
				b.Run(escape+"/"+valueToString(size)+"/match="+valueToString(match), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(pattern)))
					for i := 0; i < b.N; i++ {
						if match {
							matchProperty(FlagProperty{Key: "k", Operator: "regex", Value: pattern}, Properties{"k": "a"})
						} else {
							classifyFlagRegex(pattern)
						}
					}
				})
			}
		}
	}
}
