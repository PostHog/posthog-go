//go:build integration

package evaluations_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	ev "github.com/posthog/posthog-go/v2/ai/evaluations"
	"github.com/stretchr/testify/require"
)

type toolCall struct {
	Tool      string            `json:"tool"`
	Arguments map[string]string `json:"arguments"`
}

type testCase struct {
	key, input string
	expected   toolCall
}

var cases = []testCase{
	{"refund_eligibility", "Can I get a refund for order #1001?", toolCall{"check_refund_policy", map[string]string{"order_id": "1001"}}},
	{"delivery_status", "Where is order #1002?", toolCall{"look_up_order", map[string]string{"order_id": "1002"}}},
	{"cancel_order", "Please cancel order #1003.", toolCall{"cancel_order", map[string]string{"order_id": "1003"}}},
	{"refund_other_order", "Can I get a refund for order #1004?", toolCall{"check_refund_policy", map[string]string{"order_id": "1004"}}},
	{"account_access", "I can't sign in to my account.", toolCall{"handoff_to_support", map[string]string{"queue": "account"}}},
	{"billing_question", "Why was I charged twice?", toolCall{"handoff_to_support", map[string]string{"queue": "billing"}}},
}

var orderNumber = regexp.MustCompile(`#([0-9]+)`)

func runAgent(input string) toolCall {
	text := strings.ToLower(input)
	switch {
	case strings.Contains(text, "cancel"):
		return toolCall{"cancel_order", map[string]string{}}
	case strings.Contains(text, "refund"):
		return toolCall{"check_refund_policy", map[string]string{"order_id": "1001"}}
	case strings.Contains(text, "order"):
		return toolCall{"check_refund_policy", map[string]string{"order_id": strings.TrimPrefix(orderNumber.FindString(text), "#")}}
	case strings.Contains(text, "charged"):
		return toolCall{"handoff_to_support", map[string]string{"queue": "billing"}}
	default:
		return toolCall{"handoff_to_support", map[string]string{"queue": "account"}}
	}
}

func (c toolCall) jsonFields() ev.Fields { return ev.Fields{"tool": c.Tool, "arguments": c.Arguments} }

func results(t *testing.T, item ev.Item, versions [2]string, out, want toolCall) []ev.Result {
	t.Helper()
	checks := []ev.Fields{
		{"value": out.Tool == want.Tool, "reasoning": fmt.Sprintf("Expected %s; selected %s.", want.Tool, out.Tool)},
		{"value": maps.Equal(out.Arguments, want.Arguments), "reasoning": fmt.Sprintf("Expected %v; received %v.", want.Arguments, out.Arguments)},
	}
	var rows []ev.Result
	for i, check := range checks {
		r, err := ev.NewResult(item, versions[i], check)
		require.NoError(t, err)
		rows = append(rows, r)
	}
	return rows
}

func localResults(t *testing.T, c testCase, versions [2]string, extra ev.Fields) (ev.Item, []ev.Result) {
	t.Helper()
	out := runAgent(c.input)
	f := ev.Fields{"case_key": c.key, "input": c.input, "output": out.jsonFields(), "expected_output": c.expected.jsonFields()}
	for k, v := range extra {
		f[k] = v
	}
	item, err := ev.NewItem(f)
	require.NoError(t, err)
	return item, results(t, item, versions, out, c.expected)
}

func uploadGroup(t *testing.T, ctx context.Context, run *ev.Experiment, c testCase, inputs [2]string, composite string, extra ev.Fields) (ev.Item, ev.UploadReceipt) {
	t.Helper()
	item, rows := localResults(t, c, inputs, extra)
	calc, err := ev.NewResult(item, composite, ev.Fields{"calculate": true})
	require.NoError(t, err)
	bulk, err := run.UploadResults(ctx, append(rows, calc))
	require.NoError(t, err)
	require.Len(t, bulk.Chunks, 1)
	chunk := bulk.Chunks[0]
	require.Len(t, chunk.Results, 3)
	for _, r := range chunk.Results {
		require.NotNil(t, r.Status)
		require.Equal(t, "ok", *r.Status)
		require.Nil(t, r.ErrorCode)
	}
	return item, chunk
}

func finish(t *testing.T, ctx context.Context, run *ev.Experiment, items, scores int64) {
	t.Helper()
	r, err := run.Complete(ctx)
	require.NoError(t, err)
	require.Equal(t, "completed", r.Status)
	require.Equal(t, items, r.AcceptedItemCount)
	require.Equal(t, scores, r.AcceptedResultCount)
	t.Logf("completed %s with %d items and %d results", run.ID(), items, scores)
}

// TestDocumentedFlows runs the support-agent examples from posthog.com/docs/ai-evals.
// Use a disposable project with dataset access. It leaves its scorers, dataset,
// and experiments in that project. Set POSTHOG_MANAGEMENT_API_KEY to a project
// secret or personal key with dataset:write, llm_analytics:write, evaluation:write,
// and offline_evaluation_ingestion:write. POSTHOG_PERSONAL_API_KEY is also accepted.
// Set POSTHOG_PROJECT_SECRET_API_KEY to a second key with only offline_evaluation_ingestion:write.
// Run with go test -tags=integration -run '^TestDocumentedFlows$' ./ai/evaluations.
func TestDocumentedFlows(t *testing.T) {
	host := os.Getenv("POSTHOG_HOST")
	require.NotEmpty(t, host, "set POSTHOG_HOST to the host of your disposable PostHog project")
	id, err := strconv.Atoi(os.Getenv("POSTHOG_PROJECT_ID"))
	require.NoError(t, err)
	managementKey := os.Getenv("POSTHOG_MANAGEMENT_API_KEY")
	if managementKey == "" {
		managementKey = os.Getenv("POSTHOG_PERSONAL_API_KEY")
	}
	management, err := ev.NewClient(ev.Config{ProjectID: id, Host: host, SecretKey: managementKey})
	require.NoError(t, err)
	defer management.Close()
	ci, err := ev.NewClient(ev.Config{ProjectID: id, Host: host, SecretKey: os.Getenv("POSTHOG_PROJECT_SECRET_API_KEY")})
	require.NoError(t, err)
	defer ci.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pass, fail := "Pass", "Fail"
	config := ev.BooleanConfig{TrueIsFailure: ev.Value(false), TrueLabel: &pass, FalseLabel: &fail}
	var versions [2]string
	var scorerIDs [2]string
	for i, name := range []string{"Correct tool", "Correct arguments"} {
		scorer, err := management.Scorers.Create(ctx, name, config, ev.CreateScorerOptions{})
		require.NoError(t, err)
		scorer, err = management.Scorers.Get(ctx, scorer.ID)
		require.NoError(t, err)
		versions[i] = scorer.CurrentVersionID
		scorerIDs[i] = scorer.ID
	}
	baseline, err := management.CreateExperiment(ctx, ev.Fields{
		"name": "Support agent baseline", "suite_key": "support-agent", "run_source": "local",
		"expected_item_count": 6, "expected_result_count": 12,
	})
	require.NoError(t, err)
	for _, c := range cases {
		_, rows := localResults(t, c, versions, nil)
		for _, row := range rows {
			_, err := baseline.UploadResult(ctx, row)
			require.NoError(t, err)
		}
	}
	finish(t, ctx, baseline, 6, 12)

	dataset, err := management.Datasets.Create(ctx, ev.Fields{"name": "Go docs cases " + baseline.ID()})
	require.NoError(t, err)
	t.Run("pinned dataset", func(t *testing.T) {
		runDataset(t, ctx, management, dataset.ID, versions)
	})
	composite, inputs := createComposite(t, ctx, management, versions)
	t.Run("composite scorer", func(t *testing.T) {
		grouped, err := management.CreateExperiment(ctx, ev.Fields{
			"name": "Support agent with composite", "suite_key": "support-agent", "run_source": "local",
			"expected_item_count": 6, "expected_result_count": 18,
		})
		require.NoError(t, err)
		for _, c := range cases {
			uploadGroup(t, ctx, grouped, c, inputs, composite, nil)
		}
		finish(t, ctx, grouped, 6, 18)
	})
	t.Run("ingestion key and corrections", func(t *testing.T) {
		runCI(t, ctx, ci, management, scorerIDs[0], dataset.ID, inputs, composite)
	})
	t.Run("score types and evaluator errors", func(t *testing.T) {
		runScoreTypes(t, ctx, management, inputs)
	})
}

func runDataset(t *testing.T, ctx context.Context, management *ev.Client, datasetID string, versions [2]string) {
	t.Helper()
	for _, c := range cases {
		_, err := management.Datasets.CreateItem(ctx, datasetID, ev.Fields{
			"client_item_id": c.key, "input": c.input, "expected_output": c.expected.jsonFields(),
		})
		require.NoError(t, err)
	}
	snapshot, err := management.Datasets.Snapshot(ctx, datasetID, nil)
	require.NoError(t, err)
	require.EqualValues(t, len(cases), snapshot.Revision())
	t.Logf("dataset %s pinned at revision %d (%s)", datasetID, snapshot.Revision(), snapshot.RevisionID())
	var items []ev.DatasetItem
	err = snapshot.ForEach(ctx, 2, func(row ev.DatasetItem) error {
		items = append(items, row)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, items, 6)
	pinned, err := management.CreateExperiment(ctx, ev.Fields{
		"name": "Support agent from a dataset", "suite_key": "support-agent", "run_source": "local",
		"dataset_revision_id": snapshot.RevisionID(),
	})
	require.NoError(t, err)
	for _, row := range items {
		input, ok := row.Input.(string)
		require.True(t, ok)
		raw, err := json.Marshal(row.ExpectedOutput)
		require.NoError(t, err)
		var want toolCall
		require.NoError(t, json.Unmarshal(raw, &want))
		out := runAgent(input)
		item, err := ev.ItemFromDataset(row, ev.Fields{"output": out.jsonFields()})
		require.NoError(t, err)
		for _, score := range results(t, item, versions, out, want) {
			_, err := pinned.UploadResult(ctx, score)
			require.NoError(t, err)
		}
	}
	finish(t, ctx, pinned, 6, 12)
}

func createComposite(t *testing.T, ctx context.Context, management *ev.Client, versions [2]string) (string, [2]string) {
	t.Helper()
	comp := ev.Composition{
		Expression: "tool_correct and arguments_correct",
		Inputs: []ev.CompositionInput{
			{Alias: "tool_correct", ScorerVersionID: versions[0]},
			{Alias: "arguments_correct", ScorerVersionID: versions[1]},
		},
	}
	valid, err := management.Scorers.ValidateComposition(ctx, "boolean", comp, nil)
	require.NoError(t, err)
	require.True(t, valid)
	composite, err := management.Scorers.Create(ctx, "Support task passed", ev.BooleanConfig{TrueIsFailure: ev.Value(false)}, ev.CreateScorerOptions{Calculation: "composite", Composition: &comp})
	require.NoError(t, err)
	composite, err = management.Scorers.Get(ctx, composite.ID)
	require.NoError(t, err)
	aliases := map[string]string{}
	for _, input := range composite.InputScorers {
		aliases[input.Alias] = input.ScorerVersionID
	}
	compositeVersions := [2]string{aliases["tool_correct"], aliases["arguments_correct"]}
	require.Equal(t, versions, compositeVersions)
	return composite.CurrentVersionID, compositeVersions
}

func runCI(t *testing.T, ctx context.Context, ci, management *ev.Client, scorerID, datasetID string, inputs [2]string, composite string) {
	t.Helper()
	// An ingestion-only CI job receives the pinned versions from the setup job.
	requireRejectedRead := func(err error) {
		t.Helper()
		var denied *ev.APIError
		require.ErrorAs(t, err, &denied)
		require.Contains(t, []int{401, 403}, denied.Status)
		require.Equal(t, ev.Rejected, denied.Persistence)
	}
	_, err := ci.Scorers.Get(ctx, scorerID)
	requireRejectedRead(err)
	_, err = ci.Datasets.Get(ctx, datasetID)
	requireRejectedRead(err)
	ciRun, err := ci.CreateExperiment(ctx, ev.Fields{
		"name": "Support agent CI", "suite_key": "support-agent", "run_source": "ci",
		"application_version": "docs-commit", "model_version": "default-model", "prompt_version": "p1",
		"expected_item_count": 2, "expected_result_count": 6,
	})
	require.NoError(t, err)
	first, ack := uploadGroup(t, ctx, ciRun, cases[0], inputs, composite, ev.Fields{"model_version": "item-model", "application_trace_id": "docs-trace"})
	second, _ := uploadGroup(t, ctx, ciRun, cases[1], inputs, composite, nil)
	finish(t, ctx, ciRun, 2, 6)

	// Corrections use the stored IDs and the current revision from the management client.
	edit, err := management.ResumeExperiment(ciRun.ID())
	require.NoError(t, err)
	rev, err := edit.GetItemRevision(ctx, first.ID())
	require.NoError(t, err)
	rev, err = edit.UpdateItem(ctx, first.ID(), rev, ev.Fields{"trial": "2", "case_key": "corrected_case", "model_version": "corrected-model", "application_trace_id": "corrected-trace"})
	require.NoError(t, err)
	rev, err = edit.UpdateItemPayload(ctx, first.ID(), rev, ev.Fields{"output": nil})
	require.NoError(t, err)
	require.NoError(t, edit.DeleteItemPayload(ctx, first.ID(), rev))
	resultID := ack.Results[0].ID
	rr, err := edit.GetResultRevision(ctx, resultID)
	require.NoError(t, err)
	rr, err = edit.UpdateResult(ctx, resultID, rr, ev.Fields{"status": "ok", "value": false})
	require.NoError(t, err)
	rr, err = edit.UpdateResultPayload(ctx, resultID, rr, ev.Fields{"reasoning": "Corrected after review"})
	require.NoError(t, err)
	require.NoError(t, edit.DeleteResultPayload(ctx, resultID, rr))
	rr, err = edit.GetResultRevision(ctx, resultID)
	require.NoError(t, err)
	require.NoError(t, edit.DeleteResult(ctx, resultID, rr))
	rev, err = edit.GetItemRevision(ctx, second.ID())
	require.NoError(t, err)
	require.NoError(t, edit.DeleteItem(ctx, second.ID(), rev))
	t.Log("completed all eight revision-checked edits and deletes on a closed run")
	early, err := ci.CreateExperiment(ctx, ev.Fields{"name": "Support agent interrupted", "run_source": "ci", "expected_item_count": 6})
	require.NoError(t, err)
	end, err := early.Fail(ctx)
	require.NoError(t, err)
	require.Equal(t, "failed", end.Status)
	t.Logf("verified explicit failure for %s", early.ID())
}

func runScoreTypes(t *testing.T, ctx context.Context, client *ev.Client, inputs [2]string) {
	t.Helper()
	numeric := ev.NumericConfig{Min: ev.Value(0.0), Max: ev.Value(5.0)}
	category := ev.CategoricalConfig{
		SelectionMode: "single",
		Options:       []ev.CategoryOption{{Key: "pass", Label: "Pass"}, {Key: "fail", Label: "Fail"}},
	}
	reportedNumber, err := client.Scorers.Create(ctx, "Relevance", numeric, ev.CreateScorerOptions{})
	require.NoError(t, err)
	reportedCategory, err := client.Scorers.Create(ctx, "Reported outcome", category, ev.CreateScorerOptions{})
	require.NoError(t, err)
	var calculatedVersions []string
	for _, formula := range []struct {
		name, expression string
		config           ev.ScorerConfig
	}{
		{"Fraction correct", "(number(tool_correct) + number(arguments_correct)) / 2", numeric},
		{"Overall outcome", `"pass" if tool_correct and arguments_correct else "fail"`, category},
	} {
		composition := ev.Composition{
			Expression: formula.expression,
			Inputs: []ev.CompositionInput{
				{Alias: "tool_correct", ScorerVersionID: inputs[0]},
				{Alias: "arguments_correct", ScorerVersionID: inputs[1]},
			},
		}
		scorer, err := client.Scorers.Create(ctx, formula.name, formula.config, ev.CreateScorerOptions{
			Calculation: "composite", Composition: &composition,
		})
		require.NoError(t, err)
		calculatedVersions = append(calculatedVersions, scorer.CurrentVersionID)
	}

	run, err := client.CreateExperiment(ctx, ev.Fields{"name": "Additional scores and coverage"})
	require.NoError(t, err)
	for _, status := range []string{"ok", "error", "skipped", "not_applicable"} {
		item, err := ev.NewItem(ev.Fields{"case_key": status})
		require.NoError(t, err)
		toolCheck := ev.Fields{"status": status}
		switch status {
		case "ok":
			toolCheck["value"] = false
		case "error":
			toolCheck["error_message"] = "Evaluator timed out"
		}
		var rows []ev.Result
		addResult := func(version string, fields ev.Fields) {
			t.Helper()
			r, err := ev.NewResult(item, version, fields)
			require.NoError(t, err)
			rows = append(rows, r)
		}
		addResult(inputs[0], toolCheck)
		addResult(inputs[1], ev.Fields{"value": true})
		addResult(reportedNumber.CurrentVersionID, ev.Fields{"value": 4})
		addResult(reportedCategory.CurrentVersionID, ev.Fields{"value": []string{"fail"}})
		for _, version := range calculatedVersions {
			addResult(version, ev.Fields{"calculate": true})
		}
		receipt, err := run.UploadResults(ctx, rows)
		require.NoError(t, err)
		require.Len(t, receipt.Chunks, 1)
		require.Len(t, receipt.Chunks[0].Results, len(rows))
		byVersion := make(map[string]ev.ResultReceipt)
		for _, r := range receipt.Chunks[0].Results {
			byVersion[r.ScorerVersionID] = r
		}
		for _, version := range calculatedVersions {
			r := byVersion[version]
			require.Equal(t, &status, r.Status)
			if status == "error" {
				require.NotNil(t, r.ErrorCode)
				require.Equal(t, "composite_input_error", *r.ErrorCode)
			} else {
				require.Nil(t, r.ErrorCode)
			}
		}
	}
	finish(t, ctx, run, 4, 24)
}
