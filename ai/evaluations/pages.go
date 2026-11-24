package evaluations

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// PageOptions selects one page by limit and offset. A zero limit uses the
// endpoint's default. Use the returned NextOffset to fetch the next page.
type PageOptions struct{ Limit, Offset int }

// ListOptions filters a dataset or scorer listing. Archived selects archived
// records. TagMatch accepts "any" (the default) or "all".
type ListOptions struct {
	PageOptions
	Search, OrderBy string
	Archived        bool
	Tags            []string
	TagMatch        string
}
type Page[T any] struct {
	Count          int64
	Results        []T
	NextOffset     *int
	Next, Previous *string
}

func (p PageOptions) query(defaultLimit, maximum int) (url.Values, error) {
	limit := p.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if p.Offset < 0 || limit < 1 || limit > maximum {
		return nil, errors.New("evaluations: invalid pagination")
	}
	return url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(p.Offset)}}, nil
}
func (p ListOptions) filters(q url.Values, orders ...string) error {
	if p.OrderBy != "" {
		if !enum(strings.TrimPrefix(p.OrderBy, "-"), orders...) {
			return errors.New("evaluations: invalid listing order")
		}
		q.Set("order_by", p.OrderBy)
	}
	if p.Search != "" {
		if !validText(p.Search) {
			return errors.New("evaluations: invalid search")
		}
		q.Set("search", p.Search)
	}
	q.Set("archived", strconv.FormatBool(p.Archived))
	mode := p.TagMatch
	if mode == "" {
		mode = "any"
	}
	if !enum(mode, "any", "all") {
		return errors.New("evaluations: invalid tag match mode")
	}
	if p.Tags != nil {
		list, err := checkTags(p.Tags, 20)
		if err != nil {
			return err
		}
		b, err := encode(list)
		if err != nil {
			return err
		}
		q.Set("tags", string(b))
	}
	if p.Tags != nil || mode != "any" {
		q.Set("tag_match", mode)
	}
	return nil
}
func rows[T any](f Fields, parse func(Fields) (T, error)) ([]T, error) {
	list, ok := f["results"].([]any)
	if !ok {
		return nil, errRecord
	}
	out := make([]T, len(list))
	for i, v := range list {
		row, ok := v.(Fields)
		if !ok {
			return nil, errRecord
		}
		item, err := parse(row)
		if err != nil {
			return nil, err
		}
		out[i] = item
	}
	return out, nil
}
func pageParser[T any](offset int, parse func(Fields) (T, error)) func(Fields) (Page[T], error) {
	return func(f Fields) (Page[T], error) {
		var p Page[T]
		var err error
		p.Count, err = count(f["count"], 0)
		if err != nil {
			return p, err
		}
		p.Results, err = rows(f, parse)
		if err != nil {
			return p, err
		}
		if !present(f, "next") {
			return p, errRecord
		}
		if v := f["previous"]; v != nil {
			s, ok := v.(string)
			if !ok {
				return p, errRecord
			}
			p.Previous = &s
		}
		if v := f["next"]; v != nil {
			link, ok := v.(string)
			if !ok {
				return p, errRecord
			}
			u, err := url.Parse(link)
			if err != nil {
				return p, err
			}
			query, err := url.ParseQuery(u.RawQuery)
			if err != nil {
				return p, err
			}
			values := query["offset"]
			if len(values) != 1 || len(p.Results) == 0 {
				return p, errRecord
			}
			next, err := strconv.Atoi(values[0])
			if err != nil || next <= offset {
				return p, errRecord
			}
			p.NextOffset = &next
			p.Next = &link
		}
		return p, nil
	}
}
