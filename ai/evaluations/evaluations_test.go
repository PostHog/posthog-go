package evaluations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	runID     = "00000000-0000-4000-8000-000000000001"
	itemID    = "00000000-0000-4000-8000-000000000002"
	versionID = "00000000-0000-4000-8000-000000000003"
	resultID  = "00000000-0000-4000-8000-000000000004"
	stamp     = "2026-10-01T00:00:00Z"
	prefix    = "/proxy/api/projects/42/"
)

const projectSecretKey = "phs_example_only"

var ctx = context.Background()

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type exchange struct {
	method, path string
	check        func(Fields, url.Values)
	status       int
	response     any
}

// The script fails on unexpected requests, including retries or implicit reads.
func scripted(t *testing.T, steps ...exchange) *Client {
	t.Helper()
	index := 0
	fn := func(r *http.Request) (*http.Response, error) {
		t.Helper()
		require.Less(t, index, len(steps), "unexpected %s %s", r.Method, r.URL)
		step := steps[index]
		index++
		require.Equal(t, "api.example", r.URL.Host)
		require.Equal(t, prefix+step.path, r.URL.Path)
		require.Equal(t, step.method, r.Method)
		require.Equal(t, "Bearer "+projectSecretKey, r.Header.Get("Authorization"))
		var body Fields
		if r.Body != nil && r.Body != http.NoBody {
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, decode(raw, &body))
		}
		if step.check != nil {
			step.check(body, r.URL.Query())
		}
		if step.status == 0 {
			return nil, errors.New("lost response")
		}
		raw, err := encode(step.response)
		require.NoError(t, err)
		return &http.Response{StatusCode: step.status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
	}
	c, err := NewClient(Config{ProjectID: 42, SecretKey: projectSecretKey, Host: "https://api.example/proxy/", Transport: transportFunc(fn)})
	require.NoError(t, err)
	t.Cleanup(func() { c.Close(); require.Equal(t, len(steps), index) })
	return c
}
func reqBody(t *testing.T, want string) func(Fields, url.Values) {
	return func(got Fields, _ url.Values) {
		b, err := encode(got)
		require.NoError(t, err)
		require.JSONEq(t, want, string(b))
	}
}
func apiFailure(t *testing.T, err error, code string, p Persistence) *APIError {
	t.Helper()
	var e *APIError
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code)
	require.Equal(t, p, e.Persistence)
	return e
}
func receipt(id, status string) Fields {
	return Fields{"id": id, "status": status, "created": true, "started_at": stamp, "created_at": stamp, "finished_at": nil, "expected_item_count": nil, "expected_result_count": nil, "accepted_item_count": 0, "accepted_result_count": 0}
}
func TestExperimentLifecycle(t *testing.T) {
	path := experimentsPath + runID + "/"
	c := scripted(t,
		exchange{"POST", experimentsPath, nil, 201, receipt(runID, "uploading")},
		exchange{"POST", path + "complete/", reqBody(t, "{}"), 200, receipt(runID, "completed")},
		exchange{"PATCH", path, reqBody(t, `{"tags":[" Regression ","regression"]}`), 200, Fields{"id": runID, "tags": []string{"regression"}}},
		exchange{"GET", path, nil, 200, Fields{"id": runID, "tags": []string{"regression"}}},
		exchange{"POST", path + "fail/", reqBody(t, "{}"), 409, Fields{"code": "experiment_closed"}},
	)
	e, err := c.CreateExperiment(ctx, Fields{"id": runID, "name": "suite", "model_version": nil})
	require.NoError(t, err)
	f := e.Submission()
	require.Contains(t, f, "model_version")
	require.NotEmpty(t, f["started_at"])
	delete(f, "id")
	require.Equal(t, runID, e.Submission()["id"])
	r, err := e.Complete(ctx)
	require.NoError(t, err)
	require.Equal(t, "completed", r.Status)
	tags, err := e.SetTags(ctx, []string{" Regression ", "regression"})
	require.NoError(t, err)
	require.Equal(t, []string{"regression"}, tags)
	tags, err = e.GetTags(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"regression"}, tags)
	resumed, err := c.ResumeExperiment(runID)
	require.NoError(t, err)
	require.Nil(t, resumed.Receipt())
	require.Nil(t, resumed.Submission())
	_, err = resumed.Fail(ctx)
	apiFailure(t, err, "experiment_closed", Rejected)
	require.Equal(t, "completed", e.Receipt().Status)
}
func TestUncertainCreationAndRecovery(t *testing.T) {
	var original Fields
	c := scripted(t,
		exchange{"POST", experimentsPath, func(f Fields, _ url.Values) { original = f }, 0, nil},
		exchange{"POST", experimentsPath, func(f Fields, _ url.Values) { require.Equal(t, original, f) }, 403, Fields{"code": "permission_denied", "attr": "key", "detail": "sensitive payload", "errors": []string{"sensitive payload"}}},
	)
	_, err := c.CreateExperiment(ctx, Fields{"name": "private name", "experiment_scorer_version_id": versionID, "expected_result_count": int64(1<<60) + 1, "started_at": "2026-10-01T02:00:00+02:00"})
	failure := apiFailure(t, err, "permission_denied", Unknown)
	require.Equal(t, original["id"], failure.Submission["id"])
	require.Equal(t, versionID, original["experiment_scorer_version_id"])
	require.Equal(t, versionID, failure.Submission["experiment_scorer_version_id"])
	require.Equal(t, stamp, failure.Submission["started_at"])
	require.Equal(t, "key", failure.Attr)
	require.Equal(t, "sensitive payload", failure.Response["detail"])
	require.NotContains(t, err.Error(), "sensitive")
	require.NotContains(t, err.Error(), "private")
	require.NotContains(t, err.Error(), projectSecretKey)
}

func TestExperimentScorerSuiteDefault(t *testing.T) {
	c := scripted(t,
		exchange{"POST", experimentsPath, func(f Fields, _ url.Values) {
			require.Equal(t, "Agent-CI", f["suite_key"])
			require.NotContains(t, f, "experiment_scorer_version_id")
		}, 201, receipt(runID, "uploading")},
		exchange{"POST", experimentsPath, func(f Fields, _ url.Values) {
			require.Contains(t, f, "experiment_scorer_version_id")
			require.Nil(t, f["experiment_scorer_version_id"])
		}, 201, receipt(runID, "uploading")},
	)
	for _, fields := range []Fields{
		{"id": runID, "name": "Run", "suite_key": "Agent-CI"},
		{"id": runID, "name": "Run", "suite_key": "Agent-CI", "experiment_scorer_version_id": nil},
	} {
		run, err := c.CreateExperiment(ctx, fields)
		require.NoError(t, err)
		_, explicit := fields["experiment_scorer_version_id"]
		_, sent := run.Submission()["experiment_scorer_version_id"]
		require.Equal(t, explicit, sent)
	}
}

func TestSubmissionReplayAcrossJSON(t *testing.T) {
	for _, tc := range []struct {
		count      int64
		useNumbers bool
	}{
		{999_999, false}, {1_000_000, false}, {1<<53 - 1, false},
		{1<<53 + 1, true}, {math.MaxInt64, true},
	} {
		var submitted Fields
		c := scripted(t,
			exchange{"POST", experimentsPath, func(f Fields, _ url.Values) { submitted = f }, 503, nil},
			exchange{"POST", experimentsPath, func(f Fields, _ url.Values) { require.Equal(t, submitted, f) }, 201, receipt(runID, "uploading")},
		)
		c.retries = 0
		_, err := c.CreateExperiment(ctx, Fields{"id": runID, "name": "replay", "expected_result_count": tc.count})
		failure := apiFailure(t, err, "", Unknown)
		saved, err := json.Marshal(failure.Submission)
		require.NoError(t, err)
		var restored Fields
		decoder := json.NewDecoder(bytes.NewReader(saved))
		if tc.useNumbers {
			decoder.UseNumber()
		}
		require.NoError(t, decoder.Decode(&restored))
		_, err = c.CreateExperiment(ctx, restored)
		require.NoError(t, err)
	}
	c := scripted(t)
	for _, invalid := range []float64{-1, 1.5, 1 << 53, 1<<53 + 2, math.Inf(1), math.NaN()} {
		_, err := c.CreateExperiment(ctx, Fields{"name": "invalid count", "expected_item_count": invalid})
		require.Error(t, err)
	}
}

func TestCanceledEmptyUpload(t *testing.T) {
	c := scripted(t)
	run, err := c.ResumeExperiment(runID)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for i := 0; i < 100; i++ {
		_, err := run.UploadResults(canceled, nil)
		require.ErrorIs(t, err, context.Canceled)
	}
	c.Close()
	_, err = run.UploadResults(canceled, nil)
	apiFailure(t, err, "client_closed", NotSent)
	_, err = c.CreateExperiment(canceled, Fields{"name": "closed"})
	apiFailure(t, err, "client_closed", NotSent)
}

// closeDuringSetup closes the client as the transport derives the request
// context, before the first attempt can reach RoundTrip.
type closeDuringSetup struct {
	context.Context
	close func()
}

func (c closeDuringSetup) Done() <-chan struct{} {
	c.close()
	return c.Context.Done()
}

func TestClientClosedBeforeFirstAttempt(t *testing.T) {
	c := scripted(t)
	parent, cancel := context.WithCancel(ctx)
	defer cancel()
	requestCtx := closeDuringSetup{parent, func() {
		c.Close()
		cancel()
	}}
	_, err := c.CreateExperiment(requestCtx, Fields{"name": "closed during setup"})
	failure := apiFailure(t, err, "client_closed", NotSent)
	require.NotEmpty(t, failure.Submission["id"])
}

func TestRetryJitter(t *testing.T) {
	require.Equal(t, 400*time.Millisecond, retryDelay(0, nil, 0))
	require.Equal(t, 600*time.Millisecond, retryDelay(0, nil, 1))
	require.Equal(t, time.Second, retryDelay(1, nil, 0.5))
	require.Equal(t, 30*time.Second, retryDelay(100, nil, 1))
	minimum := 550 * time.Millisecond
	require.Equal(t, minimum, retryDelay(0, &minimum, 0))
	require.Equal(t, 600*time.Millisecond, retryDelay(0, &minimum, 1))
	long := time.Minute
	require.Equal(t, long, retryDelay(0, &long, 0.5))
}
func TestCanceledRetryPreservesUncertainCreation(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	c, err := NewClient(Config{
		ProjectID: 42,
		SecretKey: "test-key",
		Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("lost creation response")
			}
			cancel()
			return &http.Response{
				StatusCode: http.StatusConflict,
				Body:       io.NopCloser(strings.NewReader(`{"code":"conflict"}`)),
			}, nil
		}),
	})
	require.NoError(t, err)
	defer c.Close()

	_, err = c.CreateExperiment(requestCtx, Fields{"name": "canceled retry"})
	require.ErrorIs(t, err, context.Canceled)
	failure := apiFailure(t, err, "conflict", Unknown)
	require.Equal(t, 2, attempts)
	require.NotEmpty(t, failure.Submission["id"])
}

func TestTransportBoundaries(t *testing.T) {
	for _, bad := range []Config{{}, {ProjectID: 1, SecretKey: "a b"}, {ProjectID: 1, SecretKey: "s", Host: "https://a@api"}, {ProjectID: 1, SecretKey: "s", Host: "https://api/#"}, {ProjectID: 1, SecretKey: "s", Timeout: -1}} {
		_, err := NewClient(bad)
		require.Error(t, err)
	}
	for host, want := range map[string]string{"app.posthog.com": "us.posthog.com", "us.i.posthog.com": "us.posthog.com", "eu.i.posthog.com": "eu.posthog.com"} {
		c, err := NewClient(Config{ProjectID: 1, SecretKey: "key", Host: "https://" + host + "/prefix"})
		require.NoError(t, err)
		require.Equal(t, "https://"+want+"/prefix/api/projects/1/", c.base)
		c.Close()
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		case 2:
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	c, err := NewClient(Config{ProjectID: 42, SecretKey: "secret", Host: server.URL})
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Datasets.Get(ctx, itemID)
	require.Equal(t, 307, apiFailure(t, err, "", Unknown).Status)
	require.Zero(t, redirected.Load())
	_, err = c.Datasets.Get(ctx, itemID)
	failure := apiFailure(t, err, "", Rejected)
	require.Equal(t, 60*time.Second, *failure.RetryAfter)
	c.Close()
	_, err = c.Datasets.Get(ctx, itemID)
	apiFailure(t, err, "client_closed", NotSent)
	require.Equal(t, int32(2), calls.Load())
}

func TestIncompleteErrorResponse(t *testing.T) {
	for _, tc := range []struct {
		status      int
		retryAfter  string
		persistence Persistence
	}{
		{http.StatusTooManyRequests, "60", Rejected},
		{http.StatusForbidden, "", Rejected},
		{http.StatusServiceUnavailable, "60", Unknown},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			calls := 0
			client, err := NewClient(Config{ProjectID: 42, SecretKey: "test-key", Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, 1, calls, "unexpected retry")
				body := io.MultiReader(strings.NewReader(`{"code":"incomplete"}`), iotest.ErrReader(io.ErrUnexpectedEOF))
				return &http.Response{
					StatusCode: tc.status, Header: http.Header{"Retry-After": {tc.retryAfter}}, Body: io.NopCloser(body),
				}, nil
			})})
			require.NoError(t, err)
			defer client.Close()

			_, err = client.CreateExperiment(ctx, Fields{"name": "test"})
			failure := apiFailure(t, err, "", tc.persistence)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			require.Equal(t, tc.status, failure.Status)
			require.NotEmpty(t, failure.Submission["id"])
			require.Nil(t, failure.Response)
			if tc.retryAfter != "" {
				require.NotNil(t, failure.RetryAfter)
				require.Equal(t, 60*time.Second, *failure.RetryAfter)
			}
		})
	}
}

func TestIncompleteSuccessRetriesReplayableRequest(t *testing.T) {
	calls := 0
	raw, err := encode(receipt(runID, "uploading"))
	require.NoError(t, err)
	client, err := NewClient(Config{ProjectID: 42, SecretKey: "test-key", Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		var body io.Reader = iotest.ErrReader(io.ErrUnexpectedEOF)
		if calls == 2 {
			body = bytes.NewReader(raw)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(body)}, nil
	})})
	require.NoError(t, err)
	defer client.Close()
	_, err = client.CreateExperiment(ctx, Fields{"id": runID, "name": "test"})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestResponseBodyLimit(t *testing.T) {
	raw, err := readResponseBody(io.NopCloser(strings.NewReader("{}")), 2)
	require.NoError(t, err)
	require.Equal(t, []byte("{}"), raw)

	body := strings.NewReader("{} extra bytes")
	raw, err = readResponseBody(io.NopCloser(body), 2)
	require.ErrorIs(t, err, errResponseTooLarge)
	require.Nil(t, raw)
	require.Equal(t, len("extra bytes"), body.Len(), "stop reading after the overflow byte")
}

func TestOversizedResponses(t *testing.T) {
	// Padding leaves a valid JSON document at the limit. Parsing that prefix
	// would incorrectly acknowledge a write or trust incomplete error details.
	padding := strings.Repeat(" ", maxResponseBytes)
	calls := 0
	client, err := NewClient(Config{ProjectID: 42, SecretKey: "test-key", Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		var data Fields
		status := http.StatusOK
		switch calls {
		case 1:
			data = receipt(runID, "uploading")
		case 2:
			status = http.StatusForbidden
			data = Fields{"code": "partial"}
		case 3:
			data = page(nil)
		default:
			t.Fatalf("unexpected request %d", calls)
		}
		raw, err := encode(data)
		require.NoError(t, err)
		body := io.MultiReader(bytes.NewReader(raw), strings.NewReader(padding))
		return &http.Response{StatusCode: status, Body: io.NopCloser(body)}, nil
	})})
	require.NoError(t, err)
	defer client.Close()

	_, err = client.CreateExperiment(ctx, Fields{"id": runID, "name": "test"})
	failure := apiFailure(t, err, "invalid_response", Unknown)
	require.ErrorIs(t, err, errResponseTooLarge)
	require.Equal(t, http.StatusOK, failure.Status)
	require.Equal(t, runID, failure.Submission["id"])
	_, err = client.Datasets.List(ctx, ListOptions{})
	failure = apiFailure(t, err, "", Rejected)
	require.ErrorIs(t, err, errResponseTooLarge)
	require.Equal(t, http.StatusForbidden, failure.Status)
	require.Nil(t, failure.Response)
	page, err := client.Datasets.List(ctx, ListOptions{})
	require.NoError(t, err)
	require.Empty(t, page.Results)
	require.Equal(t, 3, calls)
}

func TestRevisionOperations(t *testing.T) {
	base := experimentsPath + runID + "/"
	item := base + "items/" + itemID + "/"
	result := base + "results/" + resultID + "/"
	rev := func(n int) Fields { return Fields{"revision": n} }
	deletion := func(f Fields, q url.Values) { require.Nil(t, f); require.Equal(t, url.Values{"revision": {"0"}}, q) }
	c := scripted(t,
		exchange{"GET", item, nil, 200, Fields{"id": itemID, "revision": 0}},
		exchange{"GET", result + "payload/", nil, 200, Fields{"id": resultID, "revision": 1}},
		exchange{"PATCH", item, reqBody(t, `{"revision":0,"model_version":null}`), 200, rev(0)},
		exchange{"PATCH", result, reqBody(t, `{"revision":1,"status":"ok","value":false}`), 200, rev(2)},
		exchange{"PATCH", item + "payload/", reqBody(t, `{"revision":0,"payload":{}}`), 200, rev(1)},
		exchange{"PATCH", result + "payload/", reqBody(t, `{"revision":2,"payload":{"reasoning":null}}`), 409, Fields{"code": "payload_unavailable"}},
		exchange{"DELETE", result + "payload/", deletion, 204, nil},
		exchange{"DELETE", item + "payload/", deletion, 204, nil},
		exchange{"DELETE", result, deletion, 200, nil},
		exchange{"DELETE", item, deletion, 409, Fields{"code": "revision_conflict"}},
		exchange{"GET", item, nil, 200, Fields{"id": itemID}},
		exchange{"PATCH", item, nil, 200, rev(1)},
		exchange{"PATCH", result, nil, 0, nil},
	)
	e, err := c.ResumeExperiment(runID)
	require.NoError(t, err)
	n, err := e.GetItemRevision(ctx, itemID)
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = e.GetResultRevision(ctx, resultID)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	_, err = e.UpdateItem(ctx, itemID, 0, Fields{"model_version": nil})
	require.NoError(t, err)
	_, err = e.UpdateResult(ctx, resultID, 1, Fields{"value": false, "status": "ok"})
	require.NoError(t, err)
	_, err = e.UpdateItemPayload(ctx, itemID, 0, Fields{})
	require.NoError(t, err)
	_, err = e.UpdateResultPayload(ctx, resultID, 2, Fields{"reasoning": nil})
	apiFailure(t, err, "payload_unavailable", Rejected)
	require.NoError(t, e.DeleteResultPayload(ctx, resultID, 0))
	require.NoError(t, e.DeleteItemPayload(ctx, itemID, 0))
	apiFailure(t, e.DeleteResult(ctx, resultID, 0), "invalid_response", Unknown)
	apiFailure(t, e.DeleteItem(ctx, itemID, 0), "revision_conflict", Rejected)
	_, err = e.GetItemRevision(ctx, itemID)
	apiFailure(t, err, "invalid_response", Unknown)
	_, err = e.UpdateItem(ctx, itemID, 2, Fields{"trial": "2"})
	apiFailure(t, err, "invalid_response", Unknown)
	_, err = e.UpdateResult(ctx, resultID, 2, Fields{"error_code": nil})
	apiFailure(t, err, "transport_error", Unknown)
	for _, f := range []Fields{{}, {"value": 1, "status": "error"}, {"status": "ok", "value": nil}, {"unknown": true}} {
		_, err = e.UpdateResult(ctx, resultID, 0, f)
		require.Error(t, err)
	}
}
func TestDeclarations(t *testing.T) {
	meta := Fields{"n": json.Number("9007199254740993")}
	i, err := NewItem(Fields{"metadata": meta, "model_version": " m ", "output": nil})
	require.NoError(t, err)
	meta["n"] = 1
	raw, err := i.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(raw), "9007199254740993")
	i2, err := RestoreItem(raw)
	require.NoError(t, err)
	require.Equal(t, i, i2)
	r, err := NewResult(i, versionID, Fields{"value": []string{"z", "a"}})
	require.NoError(t, err)
	b, err := r.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(b), `"value":["a","z"]`)
	saved, err := RestoreResult(b, &i2)
	require.NoError(t, err)
	require.Equal(t, r, saved)
	ref, err := ItemReference(uuid.NewString())
	require.NoError(t, err)
	_, err = RestoreResult(b, &ref)
	require.Error(t, err)
	for _, v := range []any{int64(9007199254740993), []string{"a", "a"}, []string{}, nil} {
		_, err = NewResult(i, versionID, Fields{"value": v})
		require.Error(t, err)
	}
	for _, v := range []any{int64(1 << 60), float64(1000000000000000128), false} {
		r, err := NewResult(i, versionID, Fields{"value": v})
		require.NoError(t, err)
		b, err := r.MarshalJSON()
		require.NoError(t, err)
		saved, err := RestoreResult(b, &i)
		require.NoError(t, err)
		require.Equal(t, r, saved)
	}
	for _, f := range []Fields{{"input": make(chan int)}, {"payload": Fields{}, "input": nil}, {"payload": nil}, {"output": "\x00"}, {"input": string([]byte{0xff})}, {"metadata": []int{1}}, {"payload": Fields{"bad": true}}, {"input": strings.Repeat("x", 1<<20)}} {
		_, err = NewItem(f)
		require.Error(t, err)
	}
	_, err = RestoreItem([]byte(`{"id":"` + itemID + `","payload":{"output":"\ud800"}}`))
	require.Error(t, err)
	_, err = RestoreItem([]byte(`{}`))
	require.Error(t, err)
	for _, f := range []Fields{{"calculate": true, "value": 0}, {"calculate": true, "payload": Fields{}}, {"status": "skipped", "error_message": "x"}, {"status": "error", "value": false}, {"value": true, "error_code": "x"}} {
		_, err = NewResult(i, versionID, f)
		require.Error(t, err)
	}
}
