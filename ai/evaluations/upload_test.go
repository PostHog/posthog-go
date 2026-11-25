package evaluations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func resultFor(t *testing.T, i Item, f Fields) Result {
	t.Helper()
	r, err := NewResult(i, uuid.NewString(), f)
	require.NoError(t, err)
	return r
}
func acknowledgments(submitted []Result) Fields {
	var items, results []Fields
	seen := make(map[string]bool)
	for _, r := range submitted {
		if !seen[r.ItemID()] {
			items = append(items, Fields{"id": r.ItemID(), "created": false, "accepted_at": stamp})
			seen[r.ItemID()] = true
		}
		row := Fields{"id": uuid.NewString(), "item_id": r.ItemID(), "scorer_version_id": r.ScorerVersionID(), "accepted_at": stamp, "created": false}
		if r.calculate {
			row["status"] = "error"
			row["error_code"] = "calculation_error"
		}
		results = append(results, row)
	}
	return Fields{"items": items, "results": results}
}
func TestBulkRecoveryKeepsCompositeInputsAndDeclarations(t *testing.T) {
	a, err := NewItem(Fields{"model_version": "A"})
	require.NoError(t, err)
	b, err := NewItem(Fields{"model_version": "B"})
	require.NoError(t, err)
	aRef, err := ItemReference(a.ID())
	require.NoError(t, err)
	bRef, err := ItemReference(b.ID())
	require.NoError(t, err)
	var results []Result
	for i := 0; i < 998; i++ {
		results = append(results, resultFor(t, a, Fields{"value": i}))
	}
	results = append(results, resultFor(t, bRef, Fields{"value": false}), resultFor(t, aRef, Fields{"value": 0}), resultFor(t, b, Fields{"calculate": true}))
	path := experimentsPath + runID + "/upload/"
	c := scripted(t,
		exchange{"POST", path, nil, 200, acknowledgments(results[:998])},
		exchange{"POST", path, nil, 409, Fields{"code": "upload_conflict"}},
		exchange{"POST", path, nil, 200, acknowledgments(results[998:])},
	)
	e, err := c.ResumeExperiment(runID)
	require.NoError(t, err)
	_, err = e.UploadResults(ctx, results)
	var failure *BulkError
	require.ErrorAs(t, err, &failure)
	apiFailure(t, err, "upload_conflict", Rejected)
	require.Len(t, failure.Completed.Chunks, 1)
	require.Equal(t, 998, failure.FailedIndex)
	require.Len(t, failure.PendingResults, 3)
	_, chunks, err := prepareChunks(failure.PendingResults)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, string(failure.FailedChunk), chunks[0].body)
	var failed struct {
		Items   []Fields
		Results []Fields
	}
	require.NoError(t, json.Unmarshal(failure.FailedChunk, &failed))
	require.Len(t, failed.Items, 2)
	require.Equal(t, "B", failed.Items[0]["model_version"])
	require.Equal(t, "A", failed.Items[1]["model_version"])
	require.Equal(t, Fields{"calculate": true, "item_id": b.ID(), "scorer_version_id": results[1000].scorer}, failed.Results[2])
	r, err := e.UploadResults(ctx, failure.PendingResults)
	require.NoError(t, err)
	require.Equal(t, "error", *r.Chunks[0].Results[2].Status)
	require.Equal(t, "calculation_error", *r.Chunks[0].Results[2].ErrorCode)
}
func TestUploadLimitsAndValidationBeforeIO(t *testing.T) {
	item, err := NewItem(Fields{"id": itemID})
	require.NoError(t, err)
	conflict, err := NewItem(Fields{"id": itemID, "model_version": "different"})
	require.NoError(t, err)
	c := scripted(t)
	e, err := c.ResumeExperiment(runID)
	require.NoError(t, err)
	first := resultFor(t, item, Fields{"value": true})
	_, err = e.UploadResults(ctx, []Result{first, first})
	require.Error(t, err)
	_, err = e.UploadResults(ctx, []Result{first, resultFor(t, conflict, Fields{"value": 0})})
	require.Error(t, err)
	var large []Result
	for i := 0; i < 1000; i++ {
		large = append(large, resultFor(t, item, Fields{"value": i}))
	}
	large = append(large, resultFor(t, item, Fields{"calculate": true}))
	_, err = e.UploadResults(ctx, large)
	require.Error(t, err)
	empty, err := e.UploadResults(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty.Chunks)
	// A UTF-8 payload just under 256 KiB fits individually; the envelope and
	// identity fields push the twentieth result into its own request.
	var bySize []Result
	for i := 0; i < 20; i++ {
		bySize = append(bySize, resultFor(t, item, Fields{"value": 0, "reasoning": strings.Repeat("é", 131060)}))
	}
	_, chunks, err := prepareChunks(bySize)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	for _, chunk := range chunks {
		require.LessOrEqual(t, len(chunk.body), 5<<20)
		require.Contains(t, chunk.body, item.wire)
	}
}
func TestUploadRejectsMalformedSuccess(t *testing.T) {
	item, err := NewItem(nil)
	require.NoError(t, err)
	r := resultFor(t, item, Fields{"calculate": true})
	for _, damage := range []func(Fields){
		func(f Fields) { f["items"] = []Fields{} },
		func(f Fields) { f["results"] = []Fields{} },
		func(f Fields) { delete(f["results"].([]Fields)[0], "status") },
		func(f Fields) { f["results"].([]Fields)[0]["status"] = "ok" },
		func(f Fields) { f["results"].([]Fields)[0]["item_id"] = uuid.NewString() },
	} {
		ack := acknowledgments([]Result{r})
		damage(ack)
		c := scripted(t, exchange{"POST", experimentsPath + runID + "/upload/", nil, 200, ack})
		e, err := c.ResumeExperiment(runID)
		require.NoError(t, err)
		_, err = e.UploadResult(ctx, r)
		apiFailure(t, err, "invalid_response", Unknown)
	}
}
func TestCancellationWhileAnotherHandleOperationIsPending(t *testing.T) {
	start := make(chan struct{})
	c, err := NewClient(Config{SecretKey: "key", ProjectID: 1, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		close(start)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	require.NoError(t, err)
	defer c.Close()
	e, err := c.ResumeExperiment(runID)
	require.NoError(t, err)
	pending, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.Complete(pending); done <- err }()
	select {
	case <-start:
	case <-time.After(5 * time.Second):
		t.Fatal("request never started")
	}
	wait, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	_, err = e.Fail(wait)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	cancel()
	err = <-done
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, e.Receipt())
}
func TestIdenticalUploadRetry(t *testing.T) {
	item, err := NewItem(Fields{"output": false})
	require.NoError(t, err)
	r := resultFor(t, item, Fields{"value": false})
	var first []byte
	c, err := NewClient(Config{SecretKey: "key", ProjectID: 1, Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		if first == nil {
			first = b
			return nil, errors.New("lost response")
		}
		require.Equal(t, first, b)
		data, err := encode(acknowledgments([]Result{r}))
		require.NoError(t, err)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})})
	require.NoError(t, err)
	defer c.Close()
	e, err := c.ResumeExperiment(runID)
	require.NoError(t, err)
	ack, err := e.UploadResult(ctx, r)
	require.NoError(t, err)
	require.False(t, ack.Results[0].Created)
	require.Nil(t, ack.Results[0].Status)
}
