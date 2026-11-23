package evaluations

import (
	"context"
	"errors"
)

// DatasetSnapshot pins item iteration to one revision. Dataset is the metadata
// observed when the snapshot was opened and may be edited locally.
type DatasetSnapshot struct {
	Dataset        Dataset
	datasets       Datasets
	id, revisionID string
	revision       int
}

func (s *DatasetSnapshot) Revision() int      { return s.revision }
func (s *DatasetSnapshot) RevisionID() string { return s.revisionID }

func (d Datasets) Snapshot(ctx context.Context, id string, revision *int) (*DatasetSnapshot, error) {
	if revision != nil && *revision < 1 {
		return nil, errors.New("evaluations: revision must be positive")
	}
	dataset, err := d.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if dataset.CurrentRevision == nil {
		return nil, errors.New("evaluations: dataset has no revision")
	}
	s := &DatasetSnapshot{Dataset: dataset, datasets: d, id: dataset.ID, revision: *dataset.CurrentRevision, revisionID: *dataset.CurrentRevisionID}
	if revision == nil || *revision == s.revision {
		return s, nil
	}
	if *revision > s.revision {
		return nil, errors.New("evaluations: dataset revision does not exist")
	}
	// New revisions can shift offsets between requests. Verify the numeric
	// revision before accepting its UUID and scan history if the estimate moved.
	page := PageOptions{Limit: 1, Offset: s.revision - *revision}
	for {
		revisions, err := d.ListRevisions(ctx, dataset.ID, page)
		if err != nil {
			return nil, err
		}
		for _, r := range revisions.Results {
			if r.Revision == *revision {
				s.revision = r.Revision
				s.revisionID = r.ID
				return s, nil
			}
		}
		if page.Limit == 1 {
			page = PageOptions{Limit: 100}
			continue
		}
		if revisions.NextOffset == nil || revisions.Results[len(revisions.Results)-1].Revision < *revision {
			return nil, errors.New("evaluations: dataset revision does not exist")
		}
		page.Offset = *revisions.NextOffset
	}
}

// ForEach fetches active item versions one page at a time. Returning an error
// from visit stops iteration. Each call starts again at the same pinned revision.
func (s *DatasetSnapshot) ForEach(ctx context.Context, pageSize int, visit func(DatasetItem) error) error {
	if visit == nil {
		return errors.New("evaluations: item visitor required")
	}
	options := ItemListOptions{PageOptions: PageOptions{Limit: pageSize}, Revision: &s.revision}
	for {
		page, err := s.datasets.ListItems(ctx, s.id, options)
		if err != nil {
			return err
		}
		for _, item := range page.Results {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(item); err != nil {
				return err
			}
		}
		if page.NextOffset == nil {
			return nil
		}
		options.Offset = *page.NextOffset
	}
}

// ItemFromDataset creates a fresh execution. Optional fields are output, id,
// case_key, trial, model_version and application_trace_id.
func ItemFromDataset(row DatasetItem, fields Fields) (Item, error) {
	if row.Archived {
		return Item{}, errors.New("evaluations: cannot evaluate archived dataset items")
	}
	if err := allowed(fields, "output", "id", "case_key", "trial", "model_version", "application_trace_id"); err != nil {
		return Item{}, err
	}
	id, err := idString(row.ID)
	if err != nil {
		return Item{}, err
	}
	version, err := idString(row.VersionID)
	if err != nil {
		return Item{}, err
	}
	f := copyFields(fields)
	if f["case_key"] == nil {
		f["case_key"] = id
		if row.ClientItemID != nil {
			f["case_key"] = *row.ClientItemID
		}
	}
	f["input"] = row.Input
	f["expected_output"] = row.ExpectedOutput
	f["metadata"] = row.Metadata
	f["dataset_item_identifier"] = id
	f["dataset_item_version_identifier"] = version
	f["dataset_item_version_id"] = version
	return NewItem(f)
}
