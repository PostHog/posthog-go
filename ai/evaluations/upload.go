package evaluations

import (
	"context"
	"errors"
	"strings"
	"time"
)

type ItemReceipt struct {
	ID         string    `json:"id"`
	Created    bool      `json:"created"`
	AcceptedAt time.Time `json:"accepted_at"`
}
type ResultReceipt struct {
	ItemReceipt
	ItemID          string  `json:"item_id"`
	ScorerVersionID string  `json:"scorer_version_id"`
	Status          *string `json:"status"`
	ErrorCode       *string `json:"error_code"`
}
type UploadReceipt struct {
	Items   []ItemReceipt
	Results []ResultReceipt
}
type BulkReceipt struct{ Chunks []UploadReceipt }

// BulkError includes the failed chunk in PendingResults even if the server may
// already have committed it. Unwrap exposes the underlying API or context error.
type BulkError struct {
	Completed      BulkReceipt
	FailedIndex    int
	PendingResults []Result
	FailedChunk    []byte
	Err            error
}

func (e *BulkError) Error() string    { return "evaluations: bulk upload interrupted: " + e.Err.Error() }
func (e *BulkError) Unwrap() error    { return e.Err }
func (e *BulkError) GoString() string { return e.Error() }

func parseUpload(f Fields, submitted []Result) (UploadReceipt, error) {
	var out UploadReceipt
	expected := make(map[string]bool)
	for _, r := range submitted {
		expected[r.ItemID()] = true
	}
	items, ok := f["items"].([]any)
	if !ok || len(items) != len(expected) {
		return out, errRecord
	}
	for _, entry := range items {
		row, ok := entry.(Fields)
		if !ok || !required(row, "created", "accepted_at") || !validIDs(row, "id") || !utcFields(row, "accepted_at") {
			return out, errRecord
		}
		r, err := record[ItemReceipt](row)
		if err != nil || !expected[r.ID] {
			return out, errRecord
		}
		delete(expected, r.ID)
		out.Items = append(out.Items, r)
	}
	results, ok := f["results"].([]any)
	if !ok || len(results) != len(submitted) {
		return out, errRecord
	}
	seen := make(map[string]bool)
	for i, entry := range results {
		row, ok := entry.(Fields)
		if !ok || !required(row, "created", "accepted_at") || !validIDs(row, "id", "item_id", "scorer_version_id") || !utcFields(row, "accepted_at") {
			return out, errRecord
		}
		r, err := record[ResultReceipt](row)
		if err != nil || r.ItemID != submitted[i].ItemID() || r.ScorerVersionID != submitted[i].scorer {
			return out, errRecord
		}
		if seen[r.ID] {
			return out, errRecord
		}
		seen[r.ID] = true
		_, hasStatus := row["status"]
		_, hasCode := row["error_code"]
		if hasStatus || hasCode || submitted[i].calculate {
			if !hasCode || !validStatus(row["status"]) || (r.ErrorCode != nil && (*r.Status != "error" || !identifier(*r.ErrorCode, 128))) {
				return out, errRecord
			}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

const maxRows = 1000
const maxUploadBytes = 5 << 20
const emptyUpload = `{"items":[],"results":[]}`

type uploadChunk struct {
	start, end int
	body       string
}
type chunkBuilder struct {
	items, results []string
	included       map[string]bool
	size           int
}

func newChunkBuilder() chunkBuilder {
	return chunkBuilder{included: make(map[string]bool), size: len(emptyUpload)}
}
func (b *chunkBuilder) appendGroup(group []Result) bool {
	// Measure first so an indivisible composite group can move whole to a fresh chunk.
	if len(b.results)+len(group) > maxRows {
		return false
	}
	ids := make(map[string]bool)
	var items []Item
	size := b.size
	for i, r := range group {
		size += len(r.wire)
		if i+len(b.results) > 0 {
			size++
		}
		item := r.item
		if item.wire != "" && !b.included[item.id] && !ids[item.id] {
			size += len(item.wire)
			if len(items)+len(b.items) > 0 {
				size++
			}
			items = append(items, item)
			ids[item.id] = true
		}
	}
	if size > maxUploadBytes || len(items)+len(b.items) > maxRows {
		return false
	}
	for _, i := range items {
		b.items = append(b.items, i.wire)
		b.included[i.id] = true
	}
	for _, r := range group {
		b.results = append(b.results, r.wire)
	}
	b.size = size
	return true
}
func (b *chunkBuilder) body() string {
	return `{"items":[` + strings.Join(b.items, ",") + `],"results":[` + strings.Join(b.results, ",") + "]}"
}
func prepareChunks(input []Result) ([]Result, []uploadChunk, error) {
	results := append([]Result(nil), input...)
	items := make(map[string]Item)
	pairs := make(map[[2]string]bool)
	first, last := make(map[string]int), make(map[string]int)
	composites := make(map[string]bool)
	for i, r := range results {
		if r.wire == "" {
			return nil, nil, errors.New("evaluations: uninitialized result")
		}
		id := r.item.id
		pair := [2]string{id, r.scorer}
		if pairs[pair] {
			return nil, nil, errors.New("evaluations: duplicate item/scorer pair")
		}
		pairs[pair] = true
		if r.item.wire != "" {
			if prev, ok := items[id]; ok && prev.wire != r.item.wire {
				return nil, nil, errors.New("evaluations: conflicting declarations for one item")
			}
			items[id] = r.item
		}
		if _, ok := first[id]; !ok {
			first[id] = i
		}
		last[id] = i + 1
		if r.calculate {
			composites[id] = true
		}
	}
	spans := make(map[int]int)
	for id := range composites {
		spans[first[id]] = last[id]
	}
	for i, r := range results {
		if item, ok := items[r.item.id]; ok {
			results[i].item = item
		}
	}
	var chunks []uploadChunk
	b := newChunkBuilder()
	start := 0
	for group := 0; group < len(results); {
		end := group + 1
		// Merge overlapping spans, retaining the order of interleaved executions.
		for i := group; i < end; i++ {
			end = max(end, spans[i])
		}
		if !b.appendGroup(results[group:end]) {
			if group > start {
				chunks = append(chunks, uploadChunk{start, group, b.body()})
				start = group
				b = newChunkBuilder()
			}
			if !b.appendGroup(results[group:end]) {
				return nil, nil, errors.New("evaluations: indivisible result group exceeds upload limits")
			}
		}
		group = end
	}
	if start < len(results) {
		chunks = append(chunks, uploadChunk{start, len(results), b.body()})
	}
	return results, chunks, nil
}
func (e *Experiment) upload(ctx context.Context, chunk uploadChunk, results []Result) (UploadReceipt, error) {
	return call(ctx, e.client, request{method: "POST", path: e.path() + "upload/", body: []byte(chunk.body), retry: true}, func(f Fields) (UploadReceipt, error) {
		return parseUpload(f, results[chunk.start:chunk.end])
	})
}
func (e *Experiment) UploadResult(ctx context.Context, result Result) (UploadReceipt, error) {
	results, chunks, err := prepareChunks([]Result{result})
	if err != nil {
		return UploadReceipt{}, err
	}
	return serialized(ctx, e, func() (UploadReceipt, error) { return e.upload(ctx, chunks[0], results) })
}

// UploadResults validates the complete sequence before sending the first chunk.
// A composite and its supplied inputs stay together. Supply all pinned inputs
// when using an ingestion key. A BulkError contains independently replayable work.
func (e *Experiment) UploadResults(ctx context.Context, input []Result) (BulkReceipt, error) {
	results, chunks, err := prepareChunks(input)
	if err != nil {
		return BulkReceipt{}, err
	}
	return serialized(ctx, e, func() (BulkReceipt, error) {
		out := BulkReceipt{Chunks: make([]UploadReceipt, 0, len(chunks))}
		for _, chunk := range chunks {
			r, err := e.upload(ctx, chunk, results)
			if err != nil {
				return BulkReceipt{}, &BulkError{out, chunk.start, results[chunk.start:], []byte(chunk.body), err}
			}
			out.Chunks = append(out.Chunks, r)
		}
		return out, nil
	})
}
