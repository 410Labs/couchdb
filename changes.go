package couchdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-kivik/couchdb/chttp"
	"github.com/go-kivik/kivik"
	"github.com/go-kivik/kivik/driver"
)

// Changes returns the changes stream for the database.
func (d *db) Changes(ctx context.Context, opts map[string]interface{}) (driver.Changes, error) {
	key := "results"
	if f, ok := opts["feed"]; ok {
		if f == "eventsource" {
			return nil, &kivik.Error{HTTPStatus: http.StatusBadRequest, Err: errors.New("kivik: eventsource feed not supported, use 'continuous'")}
		}
		if f == "continuous" {
			key = ""
		}
	}
	query, err := optionsToParams(opts)
	if err != nil {
		return nil, err
	}
	options := &chttp.Options{
		Query: query,
	}
	resp, err := d.Client.DoReq(ctx, kivik.MethodGet, d.path("_changes"), options)
	if err != nil {
		return nil, err
	}
	if err = chttp.ResponseError(resp); err != nil {
		return nil, err
	}
	return newChangesRows(key, resp.Body), nil
}

type continuousChangesParser struct{}

func (p *continuousChangesParser) parseMeta(i interface{}, dec *json.Decoder, key string) error {
	meta := i.(*changesMeta)
	return meta.parseMeta(key, dec)
}

func (p *continuousChangesParser) decodeItem(i interface{}, dec *json.Decoder) error {
	row := i.(*driver.Change)
	ch := &change{Change: row}
	if err := dec.Decode(ch); err != nil {
		return &kivik.Error{HTTPStatus: http.StatusBadGateway, Err: err}
	}
	ch.Change.Seq = string(ch.Seq)
	return nil
}

type changesMeta struct {
	lastSeq sequenceID
	pending int64
}

// parseMeta parses result metadata
func (m *changesMeta) parseMeta(key string, dec *json.Decoder) error {
	switch key {
	case "last_seq":
		return dec.Decode(&m.lastSeq)
	case "pending":
		return dec.Decode(&m.pending)
	}
	return &kivik.Error{HTTPStatus: http.StatusBadGateway, Err: fmt.Errorf("Unexpected key: %s", key)}
}

type changesRows struct {
	*iter
	*changesMeta
}

func newChangesRows(key string, r io.ReadCloser) *changesRows {
	var meta *changesMeta
	if key != "" {
		meta = &changesMeta{}
	}
	return &changesRows{
		iter: newIter(meta, key, r, &continuousChangesParser{}),
	}
}

var _ driver.Changes = &changesRows{}

type change struct {
	*driver.Change
	Seq sequenceID `json:"seq"`
}

func (r *changesRows) Next(row *driver.Change) error {
	return r.iter.next(row)
}

// LastSeq returns an empty string.
func (r *changesRows) LastSeq() string {
	return string(r.lastSeq)
}

// Pending returns 0.
func (r *changesRows) Pending() int64 {
	return r.pending
}
