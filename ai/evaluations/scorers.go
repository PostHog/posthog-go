package evaluations

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strconv"
	"time"
)

const scorersPath = "llm_analytics/score_definitions/"

type Scorers struct{ client *Client }
type Scoring struct {
	Kind        string       `json:"kind"`
	Scope       string       `json:"scope"`
	SuiteKeys   []string     `json:"suite_keys"`
	Config      ScorerConfig `json:"config"`
	Calculation string       `json:"calculation"`
	Composition *Composition `json:"composition"`
}
type ScorerInput struct {
	Alias           string       `json:"alias"`
	ScorerVersionID string       `json:"scorer_version_id"`
	DefinitionID    string       `json:"definition_id"`
	Name            string       `json:"name"`
	Kind            string       `json:"kind"`
	Config          ScorerConfig `json:"config"`
	Version         int          `json:"version"`
	LatestVersionID *string      `json:"latest_version_id"`
	LatestVersion   *int         `json:"latest_version"`
}
type Scorer struct {
	Scoring
	ID                      string        `json:"id"`
	Name                    string        `json:"name"`
	Description             string        `json:"description"`
	Archived                bool          `json:"archived"`
	Tags                    []string      `json:"tags"`
	CurrentVersion          int           `json:"current_version"`
	CurrentVersionID        string        `json:"current_version_id"`
	Team                    int           `json:"team"`
	CreatedAt               time.Time     `json:"created_at"`
	UpdatedAt               time.Time     `json:"updated_at"`
	CreatedBy               Fields        `json:"created_by"`
	InputScorers            []ScorerInput `json:"input_scorers"`
	DependentCompositeCount int           `json:"dependent_composite_count"`
	AssignedSuiteKeys       []string      `json:"assigned_suite_keys"`
}
type ScorerVersion struct {
	Scoring
	ID           string    `json:"id"`
	DefinitionID string    `json:"definition_id"`
	Version      int       `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    Fields    `json:"created_by"`
}

func validKind(v any) bool  { return enum(v, "boolean", "numeric", "categorical") }
func validScope(v any) bool { return enum(v, "item", "experiment") }
func (s *Scoring) parse(f Fields) error {
	if _, ok := f["scope"]; !ok {
		s.Scope = "item"
	}
	if !validScope(s.Scope) {
		return errRecord
	}
	suites, err := responseSuiteKeys(f, "suite_keys", []string{})
	if err != nil {
		return err
	}
	s.SuiteKeys = suites
	if _, ok := f["calculation"]; !ok {
		s.Calculation = "reported"
	}
	switch s.Calculation {
	case "reported":
		if s.Scope == "experiment" || s.Composition != nil {
			return errRecord
		}
	case "composite":
		if s.Composition == nil {
			return errRecord
		}
		c, err := s.Composition.checked(s.Scope)
		if err != nil {
			return err
		}
		s.Composition = &c
	default:
		return errRecord
	}
	config, _ := f["config"].(Fields)
	c, err := parseConfig(s.Kind, config)
	s.Config = c
	return err
}

// The concrete config type depends on kind. These two fields are parsed after
// the common record has been decoded.
func scorerRecord[T any](f Fields) (T, error) {
	base := copyFields(f)
	delete(base, "config")
	delete(base, "input_scorers")
	return record[T](base)
}
func parseInput(f Fields) (ScorerInput, error) {
	if !validIDs(f, "scorer_version_id", "definition_id") || !required(f, "name") || !present(f, "latest_version_id", "latest_version") {
		return ScorerInput{}, errRecord
	}
	if f["latest_version_id"] != nil && !validIDs(f, "latest_version_id") {
		return ScorerInput{}, errRecord
	}
	r, err := scorerRecord[ScorerInput](f)
	if err != nil || !validAlias(r.Alias) || r.Version < 1 || (r.LatestVersion != nil && *r.LatestVersion < 1) {
		return ScorerInput{}, errRecord
	}
	cfg, _ := f["config"].(Fields)
	r.Config, err = parseConfig(r.Kind, cfg)
	return r, err
}
func (s Scorers) scorer(id string) func(Fields) (Scorer, error) {
	return func(f Fields) (Scorer, error) {
		if !validIDs(f, "id", "current_version_id") || !required(f, "name", "description", "archived", "created_at", "updated_at") || !present(f, "created_by") || responseTags(f) != nil {
			return Scorer{}, errRecord
		}
		if !utcFields(f, "created_at", "updated_at") {
			return Scorer{}, errRecord
		}
		r, err := scorerRecord[Scorer](f)
		if err != nil || (id != "" && r.ID != id) || r.Team != s.client.project || r.CurrentVersion < 1 {
			return Scorer{}, errRecord
		}
		if err := r.Scoring.parse(f); err != nil {
			return Scorer{}, err
		}
		if v, ok := f["dependent_composite_count"]; ok {
			if _, err := count(v, 0); err != nil {
				return Scorer{}, err
			}
		}
		r.AssignedSuiteKeys, err = responseSuiteKeys(f, "assigned_suite_keys", nil)
		if err != nil {
			return Scorer{}, err
		}
		r.InputScorers = []ScorerInput{}
		if v, ok := f["input_scorers"]; ok {
			list, ok := v.([]any)
			if !ok {
				return Scorer{}, errRecord
			}
			for _, entry := range list {
				row, ok := entry.(Fields)
				if !ok {
					return Scorer{}, errRecord
				}
				input, err := parseInput(row)
				if err != nil {
					return Scorer{}, err
				}
				r.InputScorers = append(r.InputScorers, input)
			}
		}
		return r, nil
	}
}
func versionParser(definitionID, versionID string) func(Fields) (ScorerVersion, error) {
	return func(f Fields) (ScorerVersion, error) {
		if !validIDs(f, "id", "definition_id") || !required(f, "created_at") || !present(f, "created_by") {
			return ScorerVersion{}, errRecord
		}
		if !utcFields(f, "created_at") {
			return ScorerVersion{}, errRecord
		}
		r, err := scorerRecord[ScorerVersion](f)
		if err != nil || r.DefinitionID != definitionID || (versionID != "" && r.ID != versionID) || r.Version < 1 {
			return ScorerVersion{}, errRecord
		}
		err = r.Scoring.parse(f)
		return r, err
	}
}

// CreateScorerOptions adds metadata and, for a composite scorer, its formula
// and its inputs. Calculation defaults to "reported" and scope to "item".
// ApplyToSuiteKeys assigns suite defaults on an experiment scorer version.
// A nil slice omits the field, while an empty slice sends an empty selection.
type CreateScorerOptions struct {
	Calculation      string
	Composition      *Composition
	Description      Optional[string]
	Tags             []string
	Scope            string
	ApplyToSuiteKeys []string
}

func (o CreateScorerOptions) MarshalJSON() ([]byte, error) {
	fields := Fields{"Calculation": o.Calculation, "Composition": o.Composition, "Tags": o.Tags}
	if o.Scope != "" {
		fields["Scope"] = o.Scope
	}
	if o.ApplyToSuiteKeys != nil {
		fields["ApplyToSuiteKeys"] = o.ApplyToSuiteKeys
	}
	o.Description.put(fields, "Description")
	return encode(fields)
}

// CreateVersionOptions can update metadata with the version. Set BaseVersion
// to the observed version number to reject a concurrent change.
type CreateVersionOptions struct {
	BaseVersion *int
	Name        *string
	Description Optional[string]
	Composition *Composition
	// Nil preserves existing assignments. An empty selection releases defaults
	// from the previous current version.
	ApplyToSuiteKeys []string
}

func (o CreateVersionOptions) MarshalJSON() ([]byte, error) {
	fields := Fields{"BaseVersion": o.BaseVersion, "Name": o.Name, "Composition": o.Composition}
	if o.ApplyToSuiteKeys != nil {
		fields["ApplyToSuiteKeys"] = o.ApplyToSuiteKeys
	}
	o.Description.put(fields, "Description")
	return encode(fields)
}

// configFields snapshots the exact output configuration for the request.
func configFields(config ScorerConfig, composition *Composition, scope string) (Fields, error) {
	if config == nil {
		return nil, errors.New("evaluations: scorer config required")
	}
	b, err := encode(config)
	if err != nil {
		return nil, err
	}
	var obj Fields
	if decode(b, &obj) != nil || obj == nil || !validKind(config.Kind()) {
		return nil, errors.New("evaluations: invalid scorer config")
	}
	f := Fields{"config": obj}
	if composition != nil {
		c, err := composition.checked(scope)
		if err != nil {
			return nil, err
		}
		if config.Kind() == "numeric" && obj["step"] != nil {
			return nil, errors.New("evaluations: numeric composites cannot set a step")
		}
		f["composition"] = c
	}
	return f, nil
}
func responseSuiteKeys(f Fields, key string, fallback []string) ([]string, error) {
	value, ok := f[key]
	if !ok {
		return fallback, nil
	}
	return stringsOf(value)
}

func scorerMetadata(f Fields) error {
	if v, ok := f["apply_to_suite_keys"]; ok {
		keys, err := stringsOf(v)
		if err != nil {
			return err
		}
		f["apply_to_suite_keys"] = keys
	}
	if v, ok := f["suite_version_id"]; ok {
		id, err := idString(v)
		if err != nil {
			return err
		}
		f["suite_version_id"] = id
	}
	if v, ok := f["name"]; ok && !identifier(v, 400) {
		return errors.New("evaluations: invalid scorer name")
	}
	if v := f["description"]; v != nil {
		if text, ok := v.(string); !ok || !validText(text) {
			return errors.New("evaluations: invalid scorer description")
		}
	}
	if v, ok := f["tags"]; ok {
		tags, err := checkTags(v, 100)
		if err != nil {
			return err
		}
		f["tags"] = tags
	}
	if v, ok := f["archived"]; ok {
		if _, ok := v.(bool); !ok {
			return errors.New("evaluations: archived must be boolean")
		}
	}
	return nil
}
func addDescription(f Fields, description Optional[string]) {
	if description.Present {
		f["description"] = nil
		if description.Value != nil {
			f["description"] = *description.Value
		}
	}
}

// Create creates a scorer definition and its first version. Use the returned
// CurrentVersionID when reporting a result or pinning a composite input.
func (s Scorers) Create(ctx context.Context, name string, config ScorerConfig, options CreateScorerOptions) (Scorer, error) {
	selected := options.Calculation
	if selected == "" {
		selected = "reported"
	}
	if !enum(selected, "reported", "composite") || (selected == "composite") != (options.Composition != nil) {
		return Scorer{}, errors.New("evaluations: only composite scorers require a composition")
	}
	scope := options.Scope
	if scope == "" {
		scope = "item"
	}
	if !validScope(scope) || (scope == "experiment" && selected != "composite") {
		return Scorer{}, errors.New("evaluations: invalid scorer scope or calculation")
	}
	f, err := configFields(config, options.Composition, scope)
	if err != nil {
		return Scorer{}, err
	}
	f["kind"] = config.Kind()
	f["name"] = name
	if scope == "experiment" {
		f["scope"] = scope
	}
	if options.ApplyToSuiteKeys != nil {
		f["apply_to_suite_keys"] = options.ApplyToSuiteKeys
	}
	if selected == "composite" {
		f["calculation"] = selected
	}
	addDescription(f, options.Description)
	if options.Tags != nil {
		f["tags"] = options.Tags
	}
	if err := scorerMetadata(f); err != nil {
		return Scorer{}, err
	}
	return write(ctx, s.client, "POST", scorersPath, f, false, func(f Fields) (Scorer, error) {
		r, err := s.scorer("")(f)
		if err != nil || r.Kind != config.Kind() || r.Calculation != selected || r.Scope != scope {
			return Scorer{}, errRecord
		}
		return r, nil
	})
}

// Get retrieves a definition and its current scorer version.
func (s Scorers) Get(ctx context.Context, id string) (Scorer, error) {
	id, err := idString(id)
	if err != nil {
		return Scorer{}, err
	}
	return call(ctx, s.client, request{method: "GET", path: scorersPath + id + "/"}, s.scorer(id))
}

// Update replaces the supplied metadata or suite assignments. Set
// apply_to_suite_keys to an empty list to clear defaults, and optionally set
// suite_version_id to select a historical version.
func (s Scorers) Update(ctx context.Context, id string, fields Fields) (Scorer, error) {
	id, err := idString(id)
	if err != nil {
		return Scorer{}, err
	}
	if err := allowed(fields, "name", "description", "archived", "tags", "apply_to_suite_keys", "suite_version_id"); err != nil {
		return Scorer{}, err
	}
	f := copyFields(fields)
	if err := scorerMetadata(f); err != nil {
		return Scorer{}, err
	}
	return write(ctx, s.client, "PATCH", scorersPath+id+"/", f, false, s.scorer(id))
}

// ScorerListOptions filters definitions by scope, output kind, calculation, or input definition.
type ScorerListOptions struct {
	ListOptions
	Kind, Calculation, InputDefinitionID, Scope string
}

// List returns one page of matching scorer definitions.
func (s Scorers) List(ctx context.Context, options ScorerListOptions) (Page[Scorer], error) {
	q, err := options.PageOptions.query(100, math.MaxInt)
	if err != nil {
		return Page[Scorer]{}, err
	}
	if err := options.filters(q, "name", "kind", "created_at", "updated_at", "current_version"); err != nil {
		return Page[Scorer]{}, err
	}
	if options.Kind != "" {
		if !validKind(options.Kind) {
			return Page[Scorer]{}, errors.New("evaluations: invalid scorer kind")
		}
		q.Set("kind", options.Kind)
	}
	if options.Scope != "" {
		if !validScope(options.Scope) {
			return Page[Scorer]{}, errors.New("evaluations: invalid scorer scope")
		}
		q.Set("scope", options.Scope)
	}
	if options.Calculation != "" {
		if !enum(options.Calculation, "reported", "composite") {
			return Page[Scorer]{}, errors.New("evaluations: invalid calculation")
		}
		q.Set("calculation", options.Calculation)
	}
	if options.InputDefinitionID != "" {
		id, err := idString(options.InputDefinitionID)
		if err != nil {
			return Page[Scorer]{}, err
		}
		q.Set("input_definition_id", id)
	}
	return call(ctx, s.client, request{method: "GET", path: scorersPath, query: q}, pageParser(options.Offset, s.scorer("")))
}

// CreateVersion appends an immutable configuration version and returns the
// updated definition. Existing results keep their original scorer version.
func (s Scorers) CreateVersion(ctx context.Context, id string, config ScorerConfig, options CreateVersionOptions) (Scorer, error) {
	id, err := idString(id)
	if err != nil {
		return Scorer{}, err
	}
	f, err := configFields(config, options.Composition, "")
	if err != nil {
		return Scorer{}, err
	}
	addDescription(f, options.Description)
	if options.ApplyToSuiteKeys != nil {
		f["apply_to_suite_keys"] = options.ApplyToSuiteKeys
	}
	if options.Name != nil {
		f["name"] = *options.Name
	}
	if options.BaseVersion != nil {
		if *options.BaseVersion < 1 {
			return Scorer{}, errors.New("evaluations: base version must be positive")
		}
		f["base_version"] = *options.BaseVersion
	}
	if err := scorerMetadata(f); err != nil {
		return Scorer{}, err
	}
	return write(ctx, s.client, "POST", scorersPath+id+"/new_version/", f, false, s.scorer(id))
}

// GetVersion retrieves an immutable version by its definition ID and version UUID.
func (s Scorers) GetVersion(ctx context.Context, id, versionID string) (ScorerVersion, error) {
	id, err := idString(id)
	if err != nil {
		return ScorerVersion{}, err
	}
	versionID, err = idString(versionID)
	if err != nil {
		return ScorerVersion{}, err
	}
	return call(ctx, s.client, request{method: "GET", path: scorersPath + id + "/versions/" + versionID + "/"}, versionParser(id, versionID))
}

// VersionListOptions selects a page of scorer versions. Use the previous
// page's NextCursor to continue.
type VersionListOptions struct {
	Limit  int
	Cursor string
}
type ScorerVersionPage struct {
	Count      int64
	Results    []ScorerVersion
	NextCursor *string
}

// ListVersions returns one cursor-paginated page of a definition's versions.
func (s Scorers) ListVersions(ctx context.Context, id string, options VersionListOptions) (ScorerVersionPage, error) {
	id, err := idString(id)
	if err != nil {
		return ScorerVersionPage{}, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || !validText(options.Cursor) {
		return ScorerVersionPage{}, errors.New("evaluations: invalid cursor or limit")
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if options.Cursor != "" {
		q.Set("cursor", options.Cursor)
	}
	return call(ctx, s.client, request{method: "GET", path: scorersPath + id + "/versions/", query: q}, func(f Fields) (ScorerVersionPage, error) {
		var p ScorerVersionPage
		p.Count, err = count(f["count"], 0)
		if err != nil {
			return p, err
		}
		p.Results, err = rows(f, versionParser(id, ""))
		if err != nil {
			return p, err
		}
		if !present(f, "next_cursor") {
			return p, errRecord
		}
		if v := f["next_cursor"]; v != nil {
			cursor, ok := v.(string)
			if !ok || cursor == "" || cursor == options.Cursor || len(p.Results) == 0 {
				return p, errRecord
			}
			p.NextCursor = &cursor
		}
		return p, nil
	})
}

// ValidateComposition checks a formula with read access. Pass nil config to
// validate its syntax and types without an output configuration.
func (s Scorers) ValidateComposition(ctx context.Context, kind string, composition Composition, config ScorerConfig) (bool, error) {
	return s.ValidateCompositionWithScope(ctx, "item", kind, composition, config)
}

// ValidateCompositionWithScope checks an item or experiment formula with read
// access. Pass nil config for syntax and type validation alone.
func (s Scorers) ValidateCompositionWithScope(ctx context.Context, scope, kind string, composition Composition, config ScorerConfig) (bool, error) {
	if !validKind(kind) || !validScope(scope) {
		return false, errors.New("evaluations: invalid scorer scope or kind")
	}
	c, err := composition.checked(scope)
	if err != nil {
		return false, err
	}
	f := Fields{"kind": kind, "composition": c}
	if scope == "experiment" {
		f["scope"] = scope
	}
	if config != nil {
		cfg, err := configFields(config, &c, scope)
		if err != nil {
			return false, err
		}
		if config.Kind() != kind {
			return false, errors.New("evaluations: mismatched output config kind")
		}
		f["config"] = cfg["config"]
	}
	return write(ctx, s.client, "POST", scorersPath+"validate_composition/", f, true, func(row Fields) (bool, error) {
		if row["valid"] != true {
			return false, errRecord
		}
		return true, nil
	})
}
