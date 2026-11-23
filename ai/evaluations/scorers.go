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
}
type ScorerVersion struct {
	Scoring
	ID           string    `json:"id"`
	DefinitionID string    `json:"definition_id"`
	Version      int       `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    Fields    `json:"created_by"`
}

func validKind(v any) bool { return enum(v, "boolean", "numeric", "categorical") }
func (s *Scoring) parse(f Fields) error {
	if _, ok := f["calculation"]; !ok {
		s.Calculation = "reported"
	}
	switch s.Calculation {
	case "reported":
		if s.Composition != nil {
			return errRecord
		}
	case "composite":
		if s.Composition == nil {
			return errRecord
		}
		c, err := s.Composition.checked()
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

type CreateScorerOptions struct {
	Calculation string
	Composition *Composition
	Description Optional[string]
	Tags        []string
}
type CreateVersionOptions struct {
	BaseVersion *int
	Name        *string
	Description Optional[string]
	Composition *Composition
}

// configFields snapshots the exact output configuration for the request.
func configFields(config ScorerConfig, composition *Composition) (Fields, error) {
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
		c, err := composition.checked()
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
func scorerMetadata(f Fields) error {
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
func (s Scorers) Create(ctx context.Context, name string, config ScorerConfig, options CreateScorerOptions) (Scorer, error) {
	selected := options.Calculation
	if selected == "" {
		selected = "reported"
	}
	if !enum(selected, "reported", "composite") || (selected == "composite") != (options.Composition != nil) {
		return Scorer{}, errors.New("evaluations: only composite scorers require a composition")
	}
	f, err := configFields(config, options.Composition)
	if err != nil {
		return Scorer{}, err
	}
	f["kind"] = config.Kind()
	f["name"] = name
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
		if err != nil || r.Kind != config.Kind() || r.Calculation != selected {
			return Scorer{}, errRecord
		}
		return r, nil
	})
}
func (s Scorers) Get(ctx context.Context, id string) (Scorer, error) {
	id, err := idString(id)
	if err != nil {
		return Scorer{}, err
	}
	return call(ctx, s.client, request{method: "GET", path: scorersPath + id + "/"}, s.scorer(id))
}

// Update replaces the supplied metadata fields name, description, archived and tags.
func (s Scorers) Update(ctx context.Context, id string, fields Fields) (Scorer, error) {
	id, err := idString(id)
	if err != nil {
		return Scorer{}, err
	}
	if err := allowed(fields, "name", "description", "archived", "tags"); err != nil {
		return Scorer{}, err
	}
	f := copyFields(fields)
	if err := scorerMetadata(f); err != nil {
		return Scorer{}, err
	}
	return write(ctx, s.client, "PATCH", scorersPath+id+"/", f, false, s.scorer(id))
}

type ScorerListOptions struct {
	ListOptions
	Kind, Calculation, InputDefinitionID string
}

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
func (s Scorers) CreateVersion(ctx context.Context, id string, config ScorerConfig, options CreateVersionOptions) (Scorer, error) {
	id, err := idString(id)
	if err != nil {
		return Scorer{}, err
	}
	f, err := configFields(config, options.Composition)
	if err != nil {
		return Scorer{}, err
	}
	addDescription(f, options.Description)
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

type VersionListOptions struct {
	Limit  int
	Cursor string
}
type ScorerVersionPage struct {
	Count      int64
	Results    []ScorerVersion
	NextCursor *string
}

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
	if !validKind(kind) {
		return false, errors.New("evaluations: invalid scorer kind")
	}
	c, err := composition.checked()
	if err != nil {
		return false, err
	}
	f := Fields{"kind": kind, "composition": c}
	if config != nil {
		cfg, err := configFields(config, &c)
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
