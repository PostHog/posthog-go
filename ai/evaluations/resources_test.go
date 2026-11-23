package evaluations

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func datasetRow() Fields {
	return Fields{"id": runID, "name": "cases", "description": "", "metadata": Fields{}, "team_id": 42, "created_by": nil, "created_at": stamp, "updated_at": nil, "archived": false, "current_revision": 3, "current_revision_id": resultID}
}
func itemRow() Fields {
	return Fields{"id": itemID, "dataset": runID, "client_item_id": "case-A", "version": 1, "version_id": versionID, "dataset_revision": 1, "dataset_revision_id": resultID, "archived": false, "input": json.Number("9007199254740993"), "expected_output": false, "source_output": "old output", "metadata": Fields{"model_version": "source model"}, "source_trace_id": "old trace", "source_event_id": nil, "source_timestamp": stamp, "created_by": nil, "team_id": 42, "updated_at": nil, "created_at": stamp, "version_created_at": stamp, "version_created_by": nil}
}
func revisionRow(n int, id string) Fields {
	return Fields{"id": id, "dataset_id": runID, "revision": n, "created_at": stamp, "created_by": nil, "team_id": 42}
}
func page(next any, rows ...Fields) Fields {
	if rows == nil {
		rows = []Fields{}
	}
	return Fields{"count": len(rows), "next": next, "previous": nil, "results": rows}
}
func params(t *testing.T, want url.Values) func(Fields, url.Values) {
	return func(_ Fields, got url.Values) { require.Equal(t, want, got) }
}
func TestPinnedSnapshotAndConversion(t *testing.T) {
	id := uuid.NewString()
	two := 2
	path := "datasets/" + runID + "/"
	query := url.Values{"dataset": {runID}, "limit": {"1"}, "archived": {"false"}, "revision": {"2"}, "offset": {"0"}}
	next := func(_ Fields, got url.Values) {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("offset", "1")
		require.Equal(t, q, got)
	}
	future := itemRow()
	future["dataset_revision"] = 3
	c := scripted(t,
		exchange{"GET", path, nil, 200, datasetRow()},
		exchange{"GET", path + "revisions/", params(t, url.Values{"limit": {"1"}, "offset": {"1"}}), 200, page(nil, revisionRow(3, resultID))},
		exchange{"GET", path + "revisions/", params(t, url.Values{"limit": {"100"}, "offset": {"0"}}), 200, page(nil, revisionRow(3, resultID), revisionRow(2, id))},
		exchange{"GET", "dataset_items/", params(t, query), 200, page("https://untrusted.invalid/stolen?offset=1", itemRow())},
		exchange{"GET", "dataset_items/", next, 200, page(nil, future)},
		exchange{"GET", "dataset_items/", params(t, query), 200, page(nil, itemRow())},
	)
	s, err := c.Datasets.Snapshot(ctx, runID, &two)
	require.NoError(t, err)
	require.Equal(t, id, s.RevisionID())
	require.Equal(t, 2, s.Revision())
	s.Dataset.ID = "modified metadata"
	var cases []DatasetItem
	visit := func(item DatasetItem) error { cases = append(cases, item); return nil }
	err = s.ForEach(ctx, 1, visit)
	apiFailure(t, err, "invalid_response", Unknown)
	require.Len(t, cases, 1)
	require.NoError(t, s.ForEach(ctx, 1, visit))
	require.Len(t, cases, 2)
	row := cases[0]
	i, err := ItemFromDataset(row, Fields{"output": "new", "model_version": "new model"})
	require.NoError(t, err)
	var f Fields
	require.NoError(t, decode([]byte(i.wire), &f))
	require.Equal(t, versionID, f["dataset_item_version_id"])
	require.Equal(t, itemID, f["dataset_item_identifier"])
	require.Equal(t, "case-A", f["case_key"])
	require.NotContains(t, f, "application_trace_id")
	require.Equal(t, "new model", f["model_version"])
	p := f["payload"].(Fields)
	require.Equal(t, "new", p["output"])
	require.Equal(t, false, p["expected_output"])
	require.Equal(t, json.Number("9007199254740993"), p["input"])
	i2, err := ItemFromDataset(row, nil)
	require.NoError(t, err)
	require.NotEqual(t, i.ID(), i2.ID())
	var other Fields
	require.NoError(t, decode([]byte(i2.wire), &other))
	require.NotContains(t, other, "model_version")
	require.NotContains(t, other["payload"], "output")
	row.Archived = true
	_, err = ItemFromDataset(row, nil)
	require.Error(t, err)
}
func TestDatasetWritesAndExplicitPagination(t *testing.T) {
	ds := "datasets/" + runID + "/"
	item := "dataset_items/" + itemID + "/"
	archived := itemRow()
	archived["archived"] = true
	archivedDataset := datasetRow()
	archivedDataset["archived"] = true
	q := url.Values{"limit": {"50"}, "offset": {"8"}, "archived": {"false"}, "order_by": {"-created_at"}, "tags": {`[" Raw "]`}, "tag_match": {"all"}}
	c := scripted(t,
		exchange{"POST", "datasets/", reqBody(t, `{"name":"cases","tags":[]}`), 201, datasetRow()},
		exchange{"PATCH", ds, reqBody(t, `{"description":"","metadata":{}}`), 200, datasetRow()},
		exchange{"GET", "datasets/", params(t, q), 200, page(nil, datasetRow())},
		exchange{"POST", ds + "archive/", reqBody(t, "{}"), 200, archivedDataset},
		exchange{"POST", ds + "restore/", reqBody(t, "{}"), 200, datasetRow()},
		exchange{"POST", "dataset_items/", nil, 0, nil},
		exchange{"POST", "dataset_items/", nil, 200, itemRow()},
		exchange{"POST", "dataset_items/", nil, 0, nil},
		exchange{"GET", item, params(t, url.Values{"revision": {"1"}}), 200, itemRow()},
		exchange{"PATCH", item, reqBody(t, `{"base_version":1,"expected_output":null,"metadata":{}}`), 409, Fields{"code": "version_conflict"}},
		exchange{"POST", item + "archive/", reqBody(t, `{"base_version":1}`), 200, archived},
		exchange{"POST", item + "restore/", reqBody(t, `{"base_version":2,"source_version":1}`), 200, itemRow()},
		exchange{"GET", item + "versions/", params(t, url.Values{"limit": {"25"}, "offset": {"0"}}), 200, page(nil, itemRow())},
	)
	d := c.Datasets
	_, err := d.Create(ctx, Fields{"name": "cases", "tags": []string{}})
	require.NoError(t, err)
	_, err = d.Update(ctx, runID, Fields{"description": "", "metadata": Fields{}})
	require.NoError(t, err)
	list, err := d.List(ctx, ListOptions{PageOptions: PageOptions{Offset: 8}, Tags: []string{" Raw "}, TagMatch: "all"})
	require.NoError(t, err)
	require.Empty(t, list.Results[0].Tags)
	_, err = d.Archive(ctx, runID)
	require.NoError(t, err)
	_, err = d.Restore(ctx, runID)
	require.NoError(t, err)
	_, err = d.CreateItem(ctx, runID, Fields{"input": false, "source_trace_id": "trace"})
	require.Error(t, err)
	_, err = d.CreateItem(ctx, runID, Fields{"input": false, "client_item_id": "key"})
	require.NoError(t, err)
	_, err = d.CreateItem(ctx, runID, Fields{"input": false})
	apiFailure(t, err, "transport_error", Unknown)
	one := 1
	_, err = d.GetItem(ctx, itemID, &one)
	require.NoError(t, err)
	_, err = d.UpdateItem(ctx, itemID, 1, Fields{"expected_output": nil, "metadata": Fields{}})
	apiFailure(t, err, "version_conflict", Rejected)
	_, err = d.ArchiveItem(ctx, itemID, 1)
	require.NoError(t, err)
	_, err = d.RestoreItem(ctx, itemID, 2, &one)
	require.NoError(t, err)
	history, err := d.ListItemVersions(ctx, itemID, PageOptions{})
	require.NoError(t, err)
	require.Equal(t, versionID, history.Results[0].VersionID)
}
func scorerRow() Fields {
	return Fields{"id": itemID, "name": "quality", "description": "", "kind": "boolean", "config": Fields{"true_is_failure": false}, "team": 42, "current_version": 1, "current_version_id": versionID, "created_at": stamp, "updated_at": stamp, "created_by": nil, "archived": false}
}
func TestReportedScorerAndVersions(t *testing.T) {
	base := scorersPath + itemID + "/"
	version := Fields{"id": versionID, "definition_id": itemID, "version": 1, "kind": "boolean", "config": Fields{"true_is_failure": nil}, "created_at": stamp, "created_by": nil}
	history := Fields{"count": 2, "next_cursor": "cursor/+", "results": []Fields{version}}
	c := scripted(t,
		exchange{"POST", scorersPath, reqBody(t, `{"name":"quality","kind":"boolean","config":{"true_is_failure":false}}`), 201, scorerRow()},
		exchange{"GET", base, nil, 200, scorerRow()},
		exchange{"PATCH", base, reqBody(t, `{"description":null,"tags":[],"archived":false}`), 200, scorerRow()},
		exchange{"POST", base + "new_version/", reqBody(t, `{"base_version":1,"config":{"true_is_failure":null}}`), 409, Fields{"code": "version_conflict"}},
		exchange{"POST", base + "new_version/", nil, 0, nil},
		exchange{"GET", base + "versions/", params(t, url.Values{"limit": {"50"}}), 200, history},
		exchange{"GET", base + "versions/", params(t, url.Values{"limit": {"50"}, "cursor": {"cursor/+"}}), 200, Fields{"count": 2, "next_cursor": nil, "results": []Fields{version}}},
		exchange{"GET", base + "versions/" + versionID + "/", nil, 200, version},
	)
	s := c.Scorers
	r, err := s.Create(ctx, "quality", BooleanConfig{TrueIsFailure: Value(false)}, CreateScorerOptions{})
	require.NoError(t, err)
	require.Equal(t, "reported", r.Calculation)
	require.Empty(t, r.InputScorers)
	require.Nil(t, r.Composition)
	cfg := r.Config.(BooleanConfig)
	require.True(t, cfg.TrueIsFailure.Present)
	require.False(t, *cfg.TrueIsFailure.Value)
	_, err = s.Get(ctx, itemID)
	require.NoError(t, err)
	_, err = s.Update(ctx, itemID, Fields{"description": nil, "tags": []string{}, "archived": false})
	require.NoError(t, err)
	one := 1
	_, err = s.CreateVersion(ctx, itemID, BooleanConfig{TrueIsFailure: Null[bool]()}, CreateVersionOptions{BaseVersion: &one})
	apiFailure(t, err, "version_conflict", Rejected)
	_, err = s.CreateVersion(ctx, itemID, BooleanConfig{}, CreateVersionOptions{})
	apiFailure(t, err, "transport_error", Unknown)
	p, err := s.ListVersions(ctx, itemID, VersionListOptions{})
	require.NoError(t, err)
	p, err = s.ListVersions(ctx, itemID, VersionListOptions{Cursor: *p.NextCursor})
	require.NoError(t, err)
	require.Nil(t, p.NextCursor)
	v, err := s.GetVersion(ctx, itemID, versionID)
	require.NoError(t, err)
	require.Equal(t, Null[bool](), v.Config.(BooleanConfig).TrueIsFailure)
}
func TestCompositeManagement(t *testing.T) {
	comp := Composition{Expression: "number(flag)", Inputs: []CompositionInput{{Alias: "flag", ScorerVersionID: versionID}}}
	row := scorerRow()
	row["kind"] = "numeric"
	row["config"] = Fields{"min": 0, "max": nil, "passing_rule": Fields{"operator": "gte", "threshold": 0}}
	row["calculation"] = "composite"
	row["composition"] = comp
	row["input_scorers"] = []Fields{{"alias": "flag", "scorer_version_id": versionID, "definition_id": runID, "name": "source", "kind": "boolean", "version": 1, "config": Fields{}, "latest_version_id": resultID, "latest_version": 2}}
	cfg := NumericConfig{Min: Value(0.0), Max: Null[float64](), PassingRule: Value(NumericPassingRule{Operator: "gte", Threshold: 0})}
	query := url.Values{"limit": {"100"}, "offset": {"20"}, "archived": {"false"}, "tags": {`["prod"]`}, "tag_match": {"any"}, "kind": {"numeric"}, "calculation": {"composite"}, "input_definition_id": {runID}}
	c := scripted(t,
		exchange{"POST", scorersPath, func(f Fields, _ url.Values) {
			require.Equal(t, row["calculation"], f["calculation"])
			require.Equal(t, json.Number("0"), f["config"].(Fields)["min"])
			require.Contains(t, f["config"], "max")
		}, 201, row},
		exchange{"POST", scorersPath + itemID + "/new_version/", nil, 200, row},
		exchange{"GET", scorersPath, params(t, query), 200, page("https://other/?offset=30", row)},
		exchange{"POST", scorersPath + "validate_composition/", func(f Fields, _ url.Values) { require.NotContains(t, f, "config") }, 200, Fields{"valid": true}},
		exchange{"POST", scorersPath + "validate_composition/", nil, 200, Fields{"valid": false}},
	)
	s := c.Scorers
	r, err := s.Create(ctx, "quality", cfg, CreateScorerOptions{Calculation: "composite", Composition: &comp})
	require.NoError(t, err)
	require.Equal(t, comp, *r.Composition)
	require.Equal(t, versionID, r.InputScorers[0].ScorerVersionID)
	require.EqualValues(t, 2, *r.InputScorers[0].LatestVersion)
	_, err = s.CreateVersion(ctx, itemID, cfg, CreateVersionOptions{Composition: &comp})
	require.NoError(t, err)
	p, err := s.List(ctx, ScorerListOptions{ListOptions: ListOptions{PageOptions: PageOptions{Offset: 20}, Tags: []string{"prod"}}, Kind: "numeric", Calculation: "composite", InputDefinitionID: runID})
	require.NoError(t, err)
	require.Equal(t, 30, *p.NextOffset)
	ok, err := s.ValidateComposition(ctx, "numeric", comp, nil)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.ValidateComposition(ctx, "numeric", comp, cfg)
	apiFailure(t, err, "invalid_response", Unknown)
	_, err = s.ValidateComposition(ctx, "numeric", comp, NumericConfig{Step: Value(1.0)})
	require.Error(t, err)
	comp.Inputs[0].Alias = "if"
	_, err = s.ValidateComposition(ctx, "numeric", comp, nil)
	require.Error(t, err)
}
func TestCategoricalConfig(t *testing.T) {
	cfg := CategoricalConfig{Options: []CategoryOption{{"a", "A"}, {"b", "B"}}, SelectionMode: "multiple", PassingRule: Value(CategoryPassingRule{Categories: []string{}})}
	b, err := encode(cfg)
	require.NoError(t, err)
	require.JSONEq(t, `{"options":[{"key":"a","label":"A"},{"key":"b","label":"B"}],"selection_mode":"multiple","passing_rule":{"categories":[]}}`, string(b))
	var f Fields
	require.NoError(t, decode(b, &f))
	got, err := parseConfig("categorical", f)
	require.NoError(t, err)
	require.Equal(t, cfg, got)
}

func TestInvalidResourceAcknowledgments(t *testing.T) {
	for _, override := range []Fields{
		{"team_id": 43}, {"version_id": "x" + versionID + "y"}, {"version": true}, {"input": nil},
		{"dataset_revision": 4}, {"archived": true}, {"metadata": []string{}}, {"created_at": "2026-10-01"},
	} {
		row := itemRow()
		for k, v := range override {
			row[k] = v
		}
		c := scripted(t, exchange{"GET", "dataset_items/", nil, 200, page(nil, row)})
		revision := 3
		_, err := c.Datasets.ListItems(ctx, runID, ItemListOptions{Revision: &revision})
		apiFailure(t, err, "invalid_response", Unknown)
	}
	for _, override := range []Fields{
		{"team": 43}, {"current_version_id": "invalid"}, {"tags": []any{nil}}, {"calculation": "automatic"},
		{"input_scorers": nil}, {"calculation": "composite"}, {"dependent_composite_count": -1},
		{"kind": "numeric", "config": Fields{"passing_rule": Fields{"operator": "gte"}}},
	} {
		row := scorerRow()
		for k, v := range override {
			row[k] = v
		}
		c := scripted(t, exchange{"GET", scorersPath + itemID + "/", nil, 200, row})
		_, err := c.Scorers.Get(ctx, itemID)
		failure := apiFailure(t, err, "invalid_response", Unknown)
		require.Equal(t, row["current_version_id"], failure.Response["current_version_id"])
	}
	for _, link := range []string{"?offset=0", "?offset=-1", "?offset=1&offset=2", "?cursor=next"} {
		c := scripted(t, exchange{"GET", "datasets/", nil, 200, page(link, datasetRow())})
		_, err := c.Datasets.List(ctx, ListOptions{})
		apiFailure(t, err, "invalid_response", Unknown)
	}
}
