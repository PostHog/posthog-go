package posthog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// PropertyCardinality defines the number of properties to generate
type PropertyCardinality int

const (
	CardinalityLow    PropertyCardinality = iota // 10-100 properties
	CardinalityMedium                            // 500-2000 properties
	CardinalityHigh                              // 2000-10000 properties
)

// Pool sizes per cardinality - balance setup time vs. cycling frequency
const (
	LowCardinalityPoolSize    = 50000 // Fast to create, ~5s
	MediumCardinalityPoolSize = 10000 // Moderate setup time
	HighCardinalityPoolSize   = 2000  // Expensive, keep small
	NumTemplates              = 50    // Number of unique property templates per pool
)

// PropertyCardinalityDistribution defines realistic distribution of property counts
// Based on typical PostHog usage patterns
var PropertyCardinalityDistribution = []struct {
	Cardinality PropertyCardinality
	Weight      int // percentage of events with this cardinality
}{
	{CardinalityLow, 60},    // 60% of events have 10-100 props
	{CardinalityMedium, 30}, // 30% have 500-2000 props
	{CardinalityHigh, 10},   // 10% have 2000-10000 props (power users)
}

// EventPool holds pre-generated events to avoid generation time during benchmarks
type EventPool struct {
	events []Capture
	index  atomic.Int64
}

// NewEventPool pre-generates n varied events for benchmark use
// Events are varied enough to avoid caching effects
func NewEventPool(n int) *EventPool {
	pool := &EventPool{events: make([]Capture, n)}
	for i := 0; i < n; i++ {
		pool.events[i] = generateVariedCapture(i)
	}
	return pool
}

// NewEventPoolWithCardinalityDistribution creates events with realistic cardinality distribution
// Uses template-based generation for efficiency
func NewEventPoolWithCardinalityDistribution(n int) *EventPool {
	// Create templates for each cardinality level
	templatesPerCardinality := make(map[PropertyCardinality][]templateData)
	for _, cardinality := range []PropertyCardinality{CardinalityLow, CardinalityMedium, CardinalityHigh} {
		templates := make([]templateData, NumTemplates)
		for i := 0; i < NumTemplates; i++ {
			templates[i] = templateData{
				properties: generatePropertiesWithCardinality(i, cardinality),
				groups:     generateGroupsWithCardinality(i, cardinality),
			}
		}
		templatesPerCardinality[cardinality] = templates
	}

	// Clone templates to fill pool - each slot gets unique maps
	pool := &EventPool{events: make([]Capture, n)}
	for i := 0; i < n; i++ {
		cardinality := selectCardinality(i)
		templates := templatesPerCardinality[cardinality]
		tmpl := templates[i%len(templates)]
		pool.events[i] = Capture{
			DistinctId: generateDistinctId(i),
			Event:      realisticEvents[i%len(realisticEvents)],
			Timestamp:  time.Now().Add(time.Duration(-i) * time.Second),
			Properties: cloneProperties(tmpl.properties),
			Groups:     cloneGroups(tmpl.groups),
		}
	}
	return pool
}

// NewEventPoolWithCardinality creates all events with a specific cardinality
// Uses template-based generation: creates NumTemplates unique property sets,
// then clones them to fill the pool. Each slot gets unique maps to prevent races.
func NewEventPoolWithCardinality(n int, cardinality PropertyCardinality) *EventPool {
	// Create templates with varied properties
	templates := make([]templateData, NumTemplates)
	for i := 0; i < NumTemplates; i++ {
		templates[i] = templateData{
			properties: generatePropertiesWithCardinality(i, cardinality),
			groups:     generateGroupsWithCardinality(i, cardinality),
		}
	}

	// Clone templates to fill pool - each slot gets unique maps
	pool := &EventPool{events: make([]Capture, n)}
	for i := 0; i < n; i++ {
		tmpl := templates[i%NumTemplates]
		pool.events[i] = Capture{
			DistinctId: generateDistinctId(i),
			Event:      realisticEvents[i%len(realisticEvents)],
			Timestamp:  time.Now().Add(time.Duration(-i) * time.Second),
			Properties: cloneProperties(tmpl.properties),
			Groups:     cloneGroups(tmpl.groups),
		}
	}
	return pool
}

// NewEventPoolWithDefaultSize creates a pool with cardinality-appropriate default size
// Use this when you don't need a specific pool size
func NewEventPoolWithDefaultSize(cardinality PropertyCardinality) *EventPool {
	size := defaultPoolSize(cardinality)
	return NewEventPoolWithCardinality(size, cardinality)
}

// defaultPoolSize returns the recommended pool size for a cardinality level
func defaultPoolSize(cardinality PropertyCardinality) int {
	switch cardinality {
	case CardinalityLow:
		return LowCardinalityPoolSize
	case CardinalityMedium:
		return MediumCardinalityPoolSize
	case CardinalityHigh:
		return HighCardinalityPoolSize
	default:
		return LowCardinalityPoolSize
	}
}

// templateData holds pre-generated properties/groups for cloning
type templateData struct {
	properties Properties
	groups     Groups
}

// cloneProperties creates a shallow copy of a Properties map
func cloneProperties(src Properties) Properties {
	if src == nil {
		return nil
	}
	dst := make(Properties, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// cloneGroups creates a shallow copy of a Groups map
func cloneGroups(src Groups) Groups {
	if src == nil {
		return nil
	}
	dst := make(Groups, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// Next returns the next event (cycling through pool) - thread-safe
// Returns a copy with cloned Properties/Groups to prevent races when the SDK
// modifies the maps (via Merge, prepareForSend) in the Enqueue caller thread
func (p *EventPool) Next() Capture {
	idx := p.index.Add(1) - 1
	original := p.events[idx%int64(len(p.events))]
	return Capture{
		DistinctId: original.DistinctId,
		Event:      original.Event,
		Timestamp:  original.Timestamp,
		Properties: cloneProperties(original.Properties),
		Groups:     cloneGroups(original.Groups),
	}
}

// Get returns the event at index i (cycling through pool)
func (p *EventPool) Get(i int) Capture {
	return p.events[i%len(p.events)]
}

// Len returns the number of events in the pool
func (p *EventPool) Len() int {
	return len(p.events)
}

// Events returns the underlying slice of events
func (p *EventPool) Events() []Capture {
	return p.events
}

// selectCardinality returns cardinality based on weighted distribution
func selectCardinality(seed int) PropertyCardinality {
	r := seed % 100
	cumulative := 0
	for _, dist := range PropertyCardinalityDistribution {
		cumulative += dist.Weight
		if r < cumulative {
			return dist.Cardinality
		}
	}
	return CardinalityLow
}

// generateVariedCapture creates a unique, realistic event
func generateVariedCapture(seed int) Capture {
	return Capture{
		DistinctId: generateDistinctId(seed),
		Event:      realisticEvents[seed%len(realisticEvents)],
		Timestamp:  time.Now().Add(time.Duration(-seed) * time.Second),
		Properties: generateVariedProperties(seed),
	}
}

// generateDistinctId creates varied distinct IDs to avoid caching
func generateDistinctId(seed int) string {
	formats := []func(int) string{
		func(s int) string { return fmt.Sprintf("user_%d@example.com", s) },
		func(s int) string { return fmt.Sprintf("auth0|%012d", s) },
		func(s int) string { return generateUUID(s) },
		func(s int) string { return fmt.Sprintf("anon_%s", randomHex(8)) },
	}
	return formats[seed%len(formats)](seed)
}

// generateUUID creates a deterministic UUID-like string based on seed
func generateUUID(seed int) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		seed,
		(seed>>8)&0xffff,
		(seed>>16)&0xffff,
		(seed>>24)&0xffff,
		seed*1000000)
}

// randomHex generates a random hex string of specified length
func randomHex(length int) string {
	bytes := make([]byte, length/2+1)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)[:length]
}

// Realistic event names matching PostHog patterns
var realisticEvents = []string{
	"$pageview", "$autocapture", "$identify", "$pageleave",
	"user_signed_up", "purchase_completed", "item_added_to_cart",
	"feature_flag_called", "$exception", "form_submitted",
	"button_clicked", "search_performed", "video_played",
}

// generateVariedProperties creates realistic property payloads with moderate cardinality
func generateVariedProperties(seed int) Properties {
	props := Properties{
		"$lib":         "posthog-go",
		"$lib_version": "1.0.0",
		"session_id":   fmt.Sprintf("sess_%d", seed/100),
		"page_url":     fmt.Sprintf("https://app.example.com/page/%d", seed%50),
	}
	// Add varied properties based on seed
	if seed%3 == 0 {
		props["user_agent"] = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)"
		props["screen_width"] = 1920
		props["screen_height"] = 1080
	}
	if seed%5 == 0 {
		props["utm_source"] = "google"
		props["utm_campaign"] = fmt.Sprintf("campaign_%d", seed%10)
	}
	if seed%7 == 0 {
		props["$referrer"] = "https://google.com/search?q=example"
	}
	return props
}

// generatePropertiesWithCardinality creates properties with specified cardinality
func generatePropertiesWithCardinality(seed int, cardinality PropertyCardinality) Properties {
	var propCount int
	switch cardinality {
	case CardinalityLow:
		propCount = 10 + (seed % 91) // 10-100
	case CardinalityMedium:
		propCount = 500 + (seed % 1501) // 500-2000
	case CardinalityHigh:
		propCount = 2000 + (seed % 8001) // 2000-10000
	}

	props := Properties{
		"$lib":         "posthog-go",
		"$lib_version": "1.0.0",
	}

	// Realistic string patterns to simulate real payloads
	stringPatterns := []func(seed, i int) string{
		func(s, i int) string { return fmt.Sprintf("string_value_%d_%d", s, i) },                                          // short
		func(s, i int) string { return fmt.Sprintf("https://app.example.com/dashboard/project/%d/page/%d", s%100, i%50) }, // URL
		func(s, i int) string { return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36" },             // user agent
		func(s, i int) string { return fmt.Sprintf("user_%d@company%d.example.com", s, i%10) },                            // email
		func(s, i int) string { return strings.Repeat("x", 50+i%200) },                                                    // varied length
	}

	for i := 0; i < propCount; i++ {
		key := fmt.Sprintf("prop_%d", i)
		// Vary value types based on index - 8 types for more realism
		switch i % 8 {
		case 0, 1, 2: // 3/8 strings with varied patterns
			props[key] = stringPatterns[i%len(stringPatterns)](seed, i)
		case 3:
			props[key] = seed*1000 + i // integer
		case 4:
			props[key] = float64(seed) + float64(i)/100.0 // float
		case 5:
			props[key] = i%2 == 0 // bool
		case 6:
			props[key] = []string{fmt.Sprintf("item_%d", i), fmt.Sprintf("item_%d", i+1)} // array
		case 7:
			props[key] = map[string]interface{}{"nested_key": i, "nested_val": seed} // nested map
		}
	}
	return props
}

// generateGroupsWithCardinality creates groups with specified cardinality
// Group cardinality is scaled proportionally but capped at realistic limits
func generateGroupsWithCardinality(seed int, cardinality PropertyCardinality) Groups {
	var groupCount int
	switch cardinality {
	case CardinalityLow:
		groupCount = 1 + (seed % 3) // 1-3
	case CardinalityMedium:
		groupCount = 3 + (seed % 5) // 3-7
	case CardinalityHigh:
		groupCount = 5 + (seed % 6) // 5-10
	}

	groups := Groups{}
	groupTypes := []string{"company", "project", "team", "workspace", "organization", "department", "region", "account", "tenant", "division"}
	for i := 0; i < groupCount && i < len(groupTypes); i++ {
		groups[groupTypes[i]] = fmt.Sprintf("%s_%d", groupTypes[i], seed+i)
	}
	return groups
}

// GenerateCapturesBatch generates a batch of captures for stress tests
func GenerateCapturesBatch(count int) []Capture {
	pool := NewEventPool(count)
	return pool.events
}

// GenerateCapturesBatchWithCardinality generates a batch of captures with specific cardinality
func GenerateCapturesBatchWithCardinality(count int, cardinality PropertyCardinality) []Capture {
	pool := NewEventPoolWithCardinality(count, cardinality)
	return pool.events
}

// Property edge case generators for testing edge cases
var edgeCaseProperties = map[string]Properties{
	"unicode":       {"名前": "テスト", "emoji": "🚀", "arabic": "مرحبا"},
	"nested":        {"user": map[string]interface{}{"id": 1, "meta": map[string]interface{}{"created": "2024-01-01"}}},
	"large_string":  {"data": strings.Repeat("x", 10000)},
	"special_chars": {"path": "/api/v1?foo=bar&baz=qux", "json_in_string": `{"nested": "json"}`},
	"numbers":       {"int": 42, "float": 3.14159, "negative": -100, "zero": 0},
}

// GetEdgeCaseProperties returns edge case properties for testing
func GetEdgeCaseProperties(name string) Properties {
	if props, ok := edgeCaseProperties[name]; ok {
		return props
	}
	return Properties{}
}

// CardinalityName returns a human-readable name for a cardinality level
func CardinalityName(c PropertyCardinality) string {
	switch c {
	case CardinalityLow:
		return "low_10-100_props"
	case CardinalityMedium:
		return "medium_500-2000_props"
	case CardinalityHigh:
		return "high_2000-10000_props"
	default:
		return "unknown"
	}
}

