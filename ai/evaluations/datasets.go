package evaluations

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Datasets manages reusable cases and their immutable versions.
type Datasets struct{ client *Client }
type DatasetAudit struct {
	TeamID    int       `json:"team_id"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy Fields    `json:"created_by"`
}
type Dataset struct {
	DatasetAudit
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Metadata          Fields     `json:"metadata"`
	Tags              []string   `json:"tags"`
	Archived          bool       `json:"archived"`
	CurrentRevision   *int       `json:"current_revision"`
	CurrentRevisionID *string    `json:"current_revision_id"`
	UpdatedAt         *time.Time `json:"updated_at"`
}
type DatasetRevision struct {
	DatasetAudit
	ID        string `json:"id"`
	DatasetID string `json:"dataset_id"`
	Revision  int    `json:"revision"`
}
type DatasetItem struct {
	DatasetAudit
	ID                string     `json:"id"`
	DatasetID         string     `json:"dataset"`
	ClientItemID      *string    `json:"client_item_id"`
	Version           int        `json:"version"`
	VersionID         string     `json:"version_id"`
	DatasetRevision   int        `json:"dataset_revision"`
	DatasetRevisionID string     `json:"dataset_revision_id"`
	Archived          bool       `json:"archived"`
	Input             any        `json:"input"`
	ExpectedOutput    any        `json:"expected_output"`
	SourceOutput      any        `json:"source_output"`
	Metadata          Fields     `json:"metadata"`
	SourceTraceID     *string    `json:"source_trace_id"`
	SourceEventID     *string    `json:"source_event_id"`
	SourceTimestamp   *time.Time `json:"source_timestamp"`
	UpdatedAt         *time.Time `json:"updated_at"`
	VersionCreatedAt  time.Time  `json:"version_created_at"`
	VersionCreatedBy  Fields     `json:"version_created_by"`
}

func validAudit(f Fields) bool {
	return required(f, "created_at", "team_id") && present(f, "created_by") && utcFields(f, "created_at", "updated_at", "source_timestamp", "version_created_at")
}
func responseTags(f Fields) error {
	if _, ok := f["tags"]; !ok {
		f["tags"] = []string{}
		return nil
	}
	_, err := stringsOf(f["tags"])
	return err
}
func (d Datasets) dataset(id string) func(Fields) (Dataset, error) {
	return func(f Fields) (Dataset, error) {
		if !validIDs(f, "id") || !validAudit(f) || !required(f, "name", "description", "metadata", "archived") ||
			!present(f, "updated_at", "current_revision", "current_revision_id") || responseTags(f) != nil {
			return Dataset{}, errRecord
		}
		if f["current_revision_id"] != nil && !validIDs(f, "current_revision_id") {
			return Dataset{}, errRecord
		}
		r, err := record[Dataset](f)
		if err != nil || (id != "" && r.ID != id) || r.TeamID != d.client.project || (r.CurrentRevision == nil) != (r.CurrentRevisionID == nil) || (r.CurrentRevision != nil && *r.CurrentRevision < 1) {
			return Dataset{}, errRecord
		}
		return r, nil
	}
}

type itemScope struct {
	id, dataset string
	revision    *int
	archived    *bool
}

func (d Datasets) item(s itemScope) func(Fields) (DatasetItem, error) {
	return func(f Fields) (DatasetItem, error) {
		if !validIDs(f, "id", "dataset", "version_id", "dataset_revision_id") || !validAudit(f) ||
			!required(f, "input", "metadata", "archived", "version", "dataset_revision", "version_created_at") ||
			!present(f, "client_item_id", "source_trace_id", "source_event_id", "source_timestamp", "updated_at", "version_created_by", "expected_output", "source_output") {
			return DatasetItem{}, errRecord
		}
		if err := identifiers(f, "client_item_id", "source_trace_id", "source_event_id"); err != nil {
			return DatasetItem{}, errRecord
		}
		for _, key := range []string{"input", "expected_output", "source_output", "metadata"} {
			if _, err := jsonValue(f[key], 1); err != nil {
				return DatasetItem{}, err
			}
		}
		r, err := record[DatasetItem](f)
		if err != nil || r.TeamID != d.client.project || r.Version < 1 || r.DatasetRevision < 1 ||
			(s.id != "" && r.ID != s.id) || (s.dataset != "" && r.DatasetID != s.dataset) ||
			(s.revision != nil && r.DatasetRevision > *s.revision) || (s.archived != nil && r.Archived != *s.archived) {
			return DatasetItem{}, errRecord
		}
		return r, nil
	}
}
func write[T any](ctx context.Context, c *Client, method, path string, body Fields, retry bool, parse func(Fields) (T, error)) (T, error) {
	b, err := encode(body)
	if err != nil {
		var zero T
		return zero, err
	}
	return call(ctx, c, request{method: method, path: path, body: b, retry: retry}, parse)
}
func datasetFields(fields Fields) (Fields, error) {
	if err := allowed(fields, "name", "description", "metadata", "tags"); err != nil {
		return nil, err
	}
	f := copyFields(fields)
	if name, ok := f["name"]; ok {
		if !identifier(name, 400) || strings.TrimSpace(name.(string)) == "" {
			return nil, errors.New("evaluations: invalid dataset name")
		}
	}
	if desc, ok := f["description"]; ok {
		s, ok := desc.(string)
		if !ok || !validText(s) || utf8.RuneCountInString(s) > 10000 {
			return nil, errors.New("evaluations: invalid dataset description")
		}
	}
	var err error
	if v, ok := f["metadata"]; ok {
		f["metadata"], err = jsonObject(v)
		if err != nil {
			return nil, err
		}
	}
	if v, ok := f["tags"]; ok {
		f["tags"], err = checkTags(v, 100)
	}
	return f, err
}

// Create requires name and accepts description, metadata and tags.
func (d Datasets) Create(ctx context.Context, fields Fields) (Dataset, error) {
	f, err := datasetFields(fields)
	if err != nil {
		return Dataset{}, err
	}
	if f["name"] == nil {
		return Dataset{}, errors.New("evaluations: dataset name required")
	}
	return write(ctx, d.client, "POST", "datasets/", f, false, d.dataset(""))
}

// Get retrieves dataset metadata, including the current revision when one exists.
func (d Datasets) Get(ctx context.Context, id string) (Dataset, error) {
	id, err := idString(id)
	if err != nil {
		return Dataset{}, err
	}
	return call(ctx, d.client, request{method: "GET", path: "datasets/" + id + "/"}, d.dataset(id))
}

// Update replaces supplied metadata fields without advancing the dataset revision.
func (d Datasets) Update(ctx context.Context, id string, fields Fields) (Dataset, error) {
	id, err := idString(id)
	if err != nil {
		return Dataset{}, err
	}
	f, err := datasetFields(fields)
	if err != nil {
		return Dataset{}, err
	}
	return write(ctx, d.client, "PATCH", "datasets/"+id+"/", f, false, d.dataset(id))
}
func (d Datasets) state(ctx context.Context, id, action string) (Dataset, error) {
	id, err := idString(id)
	if err != nil {
		return Dataset{}, err
	}
	return write(ctx, d.client, "POST", "datasets/"+id+"/"+action+"/", Fields{}, false, func(f Fields) (Dataset, error) {
		row, err := d.dataset(id)(f)
		if err != nil || row.Archived != (action == "archive") {
			return Dataset{}, errRecord
		}
		return row, nil
	})
}

// Archive moves a dataset out of active listings and retains its history.
func (d Datasets) Archive(ctx context.Context, id string) (Dataset, error) {
	return d.state(ctx, id, "archive")
}

// Restore makes an archived dataset active again.
func (d Datasets) Restore(ctx context.Context, id string) (Dataset, error) {
	return d.state(ctx, id, "restore")
}

// List returns one page of datasets, ordered by creation time by default.
func (d Datasets) List(ctx context.Context, options ListOptions) (Page[Dataset], error) {
	q, err := options.PageOptions.query(50, 100)
	if err != nil {
		return Page[Dataset]{}, err
	}
	if options.OrderBy == "" {
		options.OrderBy = "-created_at"
	}
	if options.Search != "" && strings.TrimSpace(options.Search) == "" {
		return Page[Dataset]{}, errors.New("evaluations: search cannot be blank")
	}
	if err := options.filters(q, "created_at", "updated_at"); err != nil {
		return Page[Dataset]{}, err
	}
	return call(ctx, d.client, request{method: "GET", path: "datasets/", query: q}, pageParser(options.Offset, d.dataset("")))
}

// ListRevisions returns one page of the dataset's revision history.
func (d Datasets) ListRevisions(ctx context.Context, id string, options PageOptions) (Page[DatasetRevision], error) {
	id, err := idString(id)
	if err != nil {
		return Page[DatasetRevision]{}, err
	}
	q, err := options.query(50, 100)
	if err != nil {
		return Page[DatasetRevision]{}, err
	}
	parse := func(f Fields) (DatasetRevision, error) {
		if !validAudit(f) || !validIDs(f, "id", "dataset_id") {
			return DatasetRevision{}, errRecord
		}
		r, err := record[DatasetRevision](f)
		if err != nil || r.Revision < 1 || r.DatasetID != id || r.TeamID != d.client.project {
			return DatasetRevision{}, errRecord
		}
		return r, nil
	}
	return call(ctx, d.client, request{method: "GET", path: "datasets/" + id + "/revisions/", query: q}, pageParser(options.Offset, parse))
}

// ItemListOptions selects active or archived items at a dataset revision.
// A nil Revision reads the latest state.
type ItemListOptions struct {
	PageOptions
	Revision *int
	Archived bool
}

func selectRevision(q url.Values, revision *int) error {
	if revision != nil {
		if *revision < 1 {
			return errors.New("evaluations: revision must be positive")
		}
		q.Set("revision", strconv.Itoa(*revision))
	}
	return nil
}

// ListItems returns one page of up to 25 cases from the selected dataset revision.
func (d Datasets) ListItems(ctx context.Context, id string, options ItemListOptions) (Page[DatasetItem], error) {
	id, err := idString(id)
	if err != nil {
		return Page[DatasetItem]{}, err
	}
	q, err := options.PageOptions.query(25, 25)
	if err != nil {
		return Page[DatasetItem]{}, err
	}
	if err := selectRevision(q, options.Revision); err != nil {
		return Page[DatasetItem]{}, err
	}
	q.Set("archived", strconv.FormatBool(options.Archived))
	q.Set("dataset", id)
	return call(ctx, d.client, request{method: "GET", path: "dataset_items/", query: q}, pageParser(options.Offset, d.item(itemScope{dataset: id, archived: &options.Archived, revision: options.Revision})))
}

// GetItem retrieves the case at a dataset revision. Use nil for its latest state.
func (d Datasets) GetItem(ctx context.Context, id string, revision *int) (DatasetItem, error) {
	id, err := idString(id)
	if err != nil {
		return DatasetItem{}, err
	}
	q := url.Values{}
	if err := selectRevision(q, revision); err != nil {
		return DatasetItem{}, err
	}
	return call(ctx, d.client, request{method: "GET", path: "dataset_items/" + id + "/", query: q}, d.item(itemScope{id: id, revision: revision}))
}
func itemContent(f Fields) error {
	for _, k := range []string{"input", "expected_output", "source_output", "metadata"} {
		if v, ok := f[k]; ok {
			value, err := jsonValue(v, 1)
			if err != nil {
				return err
			}
			if k == "input" && value == nil {
				return errors.New("evaluations: dataset input must be non-null")
			}
			if k == "metadata" {
				if _, ok := value.(Fields); !ok {
					return errors.New("evaluations: metadata must be an object")
				}
			}
			f[k] = value
		}
	}
	return nil
}

// CreateItem requires non-null input. It accepts expected_output, source_output,
// metadata, client_item_id, source_trace_id, source_event_id and source_timestamp.
// Supply a stable client_item_id to enable safe retries of this write.
func (d Datasets) CreateItem(ctx context.Context, datasetID string, fields Fields) (DatasetItem, error) {
	id, err := idString(datasetID)
	if err != nil {
		return DatasetItem{}, err
	}
	if err := allowed(fields, "input", "expected_output", "source_output", "metadata", "client_item_id", "source_trace_id", "source_event_id", "source_timestamp"); err != nil {
		return DatasetItem{}, err
	}
	f := copyFields(fields)
	f["dataset"] = id
	if f["input"] == nil {
		return DatasetItem{}, errors.New("evaluations: dataset input is required")
	}
	if err := itemContent(f); err != nil {
		return DatasetItem{}, err
	}
	if err := identifiers(f, "client_item_id", "source_trace_id", "source_event_id"); err != nil {
		return DatasetItem{}, err
	}
	trace := f["source_trace_id"] != nil
	if trace != (f["source_timestamp"] != nil) || (!trace && f["source_event_id"] != nil) {
		return DatasetItem{}, errors.New("evaluations: source provenance requires trace ID and timestamp")
	}
	if trace {
		f["source_timestamp"], err = instant(f["source_timestamp"])
		if err != nil {
			return DatasetItem{}, err
		}
	}
	active := false
	return write(ctx, d.client, "POST", "dataset_items/", f, f["client_item_id"] != nil, d.item(itemScope{dataset: id, archived: &active}))
}

// UpdateItem replaces supplied input, expected_output and metadata fields using
// the observed item version as a concurrency guard.
func (d Datasets) UpdateItem(ctx context.Context, id string, baseVersion int, fields Fields) (DatasetItem, error) {
	if err := allowed(fields, "input", "expected_output", "metadata"); err != nil {
		return DatasetItem{}, err
	}
	f := copyFields(fields)
	if err := itemContent(f); err != nil {
		return DatasetItem{}, err
	}
	return d.changeItem(ctx, id, baseVersion, "PATCH", "", f)
}
func (d Datasets) changeItem(ctx context.Context, id string, version int, method, suffix string, fields Fields) (DatasetItem, error) {
	id, err := idString(id)
	if err != nil {
		return DatasetItem{}, err
	}
	if version < 1 {
		return DatasetItem{}, errors.New("evaluations: base version must be positive")
	}
	fields["base_version"] = version
	archived := suffix == "archive/"
	return write(ctx, d.client, method, "dataset_items/"+id+"/"+suffix, fields, false, d.item(itemScope{id: id, archived: &archived}))
}

// ArchiveItem archives the case if baseVersion is still current.
func (d Datasets) ArchiveItem(ctx context.Context, id string, baseVersion int) (DatasetItem, error) {
	return d.changeItem(ctx, id, baseVersion, "POST", "archive/", Fields{})
}

// RestoreItem restores a case at its observed baseVersion. Supply sourceVersion
// to restore the content of a specific historical item version.
func (d Datasets) RestoreItem(ctx context.Context, id string, baseVersion int, sourceVersion *int) (DatasetItem, error) {
	f := Fields{}
	if sourceVersion != nil {
		if *sourceVersion < 1 {
			return DatasetItem{}, errors.New("evaluations: source version must be positive")
		}
		f["source_version"] = *sourceVersion
	}
	return d.changeItem(ctx, id, baseVersion, "POST", "restore/", f)
}

// ListItemVersions returns one page of historical versions for an individual case.
func (d Datasets) ListItemVersions(ctx context.Context, id string, options PageOptions) (Page[DatasetItem], error) {
	id, err := idString(id)
	if err != nil {
		return Page[DatasetItem]{}, err
	}
	q, err := options.query(25, 100)
	if err != nil {
		return Page[DatasetItem]{}, err
	}
	return call(ctx, d.client, request{method: "GET", path: "dataset_items/" + id + "/versions/", query: q}, pageParser(options.Offset, d.item(itemScope{id: id})))
}
