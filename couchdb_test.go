package couchdb

import (
	"testing"

	"github.com/flimzy/diff"
	"github.com/flimzy/testy"

	"github.com/go-kivik/kivik"
)

func TestNewClient(t *testing.T) {
	type ncTest struct {
		name       string
		driver     *Couch
		dsn        string
		expectedUA []string
		status     int
		err        string
	}
	tests := []ncTest{
		{
			name:   "invalid url",
			dsn:    "foo.com/%xxx",
			status: kivik.StatusBadAPICall,
			err:    `parse http://foo.com/%xxx: invalid URL escape "%xx"`,
		},
		{
			name: "success",
			dsn:  "http://foo.com/",
			expectedUA: []string{
				"Kivik/" + kivik.KivikVersion,
				"Kivik CouchDB driver/" + Version,
			},
		},
		{
			name:   "User Agent",
			dsn:    "http://foo.com/",
			driver: &Couch{UserAgent: "test/foo"},
			expectedUA: []string{
				"Kivik/" + kivik.KivikVersion,
				"Kivik CouchDB driver/" + Version,
				"test/foo",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver := test.driver
			if driver == nil {
				driver = &Couch{}
			}
			result, err := driver.NewClient(test.dsn)
			testy.StatusError(t, test.err, test.status, err)
			client, ok := result.(*client)
			if !ok {
				t.Errorf("Unexpected type returned: %t", result)
			}
			if d := diff.Interface(test.expectedUA, client.Client.UserAgents); d != nil {
				t.Error(d)
			}
		})
	}
}

func TestDB(t *testing.T) {
	tests := []struct {
		name     string
		client   *client
		dbName   string
		options  map[string]interface{}
		expected *db
	}{
		{
			name:   "no full commit",
			dbName: "foo",
			expected: &db{
				dbName: "foo",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.client.DB(test.dbName, test.options)
			if _, ok := result.(*db); !ok {
				t.Errorf("Unexpected result type: %T", result)
			}
		})
	}
}
