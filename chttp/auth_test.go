package chttp

import (
	"context"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/publicsuffix"

	"github.com/flimzy/diff"
	"github.com/flimzy/kivik"
	"github.com/flimzy/testy"
)

func TestDefaultAuth(t *testing.T) {
	dsn, err := url.Parse(dsn(t))
	if err != nil {
		t.Fatalf("Failed to parse DSN '%s': %s", dsn, err)
	}
	user := dsn.User.Username()
	client := getClient(t)

	if name := getAuthName(client, t); name != user {
		t.Errorf("Unexpected authentication name. Expected '%s', got '%s'", user, name)
	}

	if err = client.Logout(context.Background()); err != nil {
		t.Errorf("Failed to de-authenticate: %s", err)
	}

	if name := getAuthName(client, t); name != "" {
		t.Errorf("Unexpected authentication name after logout '%s'", name)
	}
}

func TestBasicAuth(t *testing.T) {
	dsn, err := url.Parse(dsn(t))
	if err != nil {
		t.Fatalf("Failed to parse DSN '%s': %s", dsn, err)
	}
	user := dsn.User
	dsn.User = nil
	client, err := New(context.Background(), dsn.String())
	if err != nil {
		t.Fatalf("Failed to connect: %s", err)
	}
	if name := getAuthName(client, t); name != "" {
		t.Errorf("Unexpected authentication name '%s'", name)
	}

	if err = client.Logout(context.Background()); err == nil {
		t.Errorf("Logout should have failed prior to login")
	}

	password, _ := user.Password()
	ba := &BasicAuth{
		Username: user.Username(),
		Password: password,
	}
	if err = client.Auth(context.Background(), ba); err != nil {
		t.Errorf("Failed to authenticate: %s", err)
	}
	if err = client.Auth(context.Background(), ba); err == nil {
		t.Errorf("Expected error trying to double-auth")
	}
	if name := getAuthName(client, t); name != user.Username() {
		t.Errorf("Unexpected auth name. Expected '%s', got '%s'", user.Username(), name)
	}

	if err = client.Logout(context.Background()); err != nil {
		t.Errorf("Failed to de-authenticate: %s", err)
	}

	if name := getAuthName(client, t); name != "" {
		t.Errorf("Unexpected authentication name after logout '%s'", name)
	}
}

func getAuthName(client *Client, t *testing.T) string {
	result := struct {
		Ctx struct {
			Name string `json:"name"`
		} `json:"userCtx"`
	}{}
	if _, err := client.DoJSON(context.Background(), "GET", "/_session", nil, &result); err != nil {
		t.Errorf("Failed to check session info: %s", err)
	}
	return result.Ctx.Name
}

type mockRT struct {
	resp *http.Response
	err  error
}

var _ http.RoundTripper = &mockRT{}

func (rt *mockRT) RoundTrip(_ *http.Request) (*http.Response, error) {
	return rt.resp, rt.err
}

func TestCookieAuthAuthenticate(t *testing.T) {
	tests := []struct {
		name           string
		auth           *CookieAuth
		client         *Client
		err            string
		expectedCookie *http.Cookie
	}{
		{
			name: "standard request",
			auth: &CookieAuth{
				Username: "foo",
				Password: "bar",
			},
			client: &Client{
				Client: &http.Client{
					Transport: &mockRT{
						resp: &http.Response{
							Header: http.Header{
								"Set-Cookie": []string{
									"AuthSession=cm9vdDo1MEJCRkYwMjq0LO0ylOIwShrgt8y-UkhI-c6BGw; Version=1; Path=/; HttpOnly",
								},
							},
							Body: ioutil.NopCloser(strings.NewReader(`{"userCtx":{"name":"foo"}}`)),
						},
					},
				},
				dsn: &url.URL{Scheme: "http", Host: "foo.com"},
			},
			expectedCookie: &http.Cookie{
				Name:  kivik.SessionCookieName,
				Value: "cm9vdDo1MEJCRkYwMjq0LO0ylOIwShrgt8y-UkhI-c6BGw",
			},
		},
		{
			name: "Invalid JSON response",
			auth: &CookieAuth{
				Username: "foo",
				Password: "bar",
			},
			client: &Client{
				Client: &http.Client{
					Jar: &cookiejar.Jar{},
					Transport: &mockRT{
						resp: &http.Response{
							Body: ioutil.NopCloser(strings.NewReader(`{"asdf"}`)),
						},
					},
				},
				dsn: &url.URL{Scheme: "http", Host: "foo.com"},
			},
			err: "invalid character '}' after object key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.auth.Authenticate(context.Background(), test.client)
			var errMsg string
			if err != nil {
				errMsg = err.Error()
			}
			if errMsg != test.err {
				t.Errorf("Unexpected error: %s", errMsg)
			}
			if err != nil {
				return
			}
			cookie, ok := test.auth.Cookie()
			if !ok {
				t.Errorf("Expected cookie")
				return
			}
			if d := diff.Interface(test.expectedCookie, cookie); d != nil {
				t.Error(d)
			}
		})
	}
}

func TestBasicAuthAuthenticate(t *testing.T) {
	tests := []struct {
		name     string
		auth     *BasicAuth
		client   *Client
		expected *Client
		status   int
		err      string
	}{
		{
			name:   "network error",
			auth:   &BasicAuth{},
			client: newTestClient(nil, errors.New("net error")),
			status: kivik.StatusInternalServerError,
			err:    "Get http://example.com/_session: net error",
		},
		{
			name: "error response 1.6.1",
			auth: &BasicAuth{Username: "invalid", Password: "invalid"},
			client: newTestClient(&http.Response{
				StatusCode: 401,
				Header: http.Header{
					"Server":         {"CouchDB/1.6.1 (Erlang OTP/17)"},
					"Date":           {"Tue, 31 Oct 2017 11:34:32 GMT"},
					"Content-Type":   {"application/json"},
					"Content-Length": {"67"},
					"Cache-Control":  {"must-revalidate"},
				},
				ContentLength: 67,
				Request:       &http.Request{Method: "GET"},
				Body:          Body(`{"error":"unauthorized","reason":"Name or password is incorrect."}`),
			}, nil),
			status: kivik.StatusUnauthorized,
			err:    "Unauthorized: Name or password is incorrect.",
		},
		{
			name: "invalid JSON response",
			auth: &BasicAuth{Username: "invalid", Password: "invalid"},
			client: newTestClient(&http.Response{
				StatusCode: 200,
				Header: http.Header{
					"Server":         {"CouchDB/1.6.1 (Erlang OTP/17)"},
					"Date":           {"Tue, 31 Oct 2017 11:34:32 GMT"},
					"Content-Type":   {"application/json"},
					"Content-Length": {"13"},
					"Cache-Control":  {"must-revalidate"},
				},
				ContentLength: 13,
				Request:       &http.Request{Method: "GET"},
				Body:          Body(`invalid json`),
			}, nil),
			status: kivik.StatusBadResponse,
			err:    "invalid character 'i' looking for beginning of value",
		},
		{
			name: "wrong user name in response",
			auth: &BasicAuth{Username: "admin", Password: "password"},
			client: newTestClient(&http.Response{
				StatusCode: 200,
				Header: http.Header{
					"Server":         {"CouchDB/1.6.1 (Erlang OTP/17)"},
					"Date":           {"Tue, 31 Oct 2017 11:34:32 GMT"},
					"Content-Type":   {"application/json"},
					"Content-Length": {"177"},
					"Cache-Control":  {"must-revalidate"},
				},
				ContentLength: 177,
				Request:       &http.Request{Method: "GET"},
				Body:          Body(`{"ok":true,"userCtx":{"name":"other","roles":["_admin"]},"info":{"authentication_db":"_users","authentication_handlers":["oauth","cookie","default"],"authenticated":"default"}}`),
			}, nil),
			status: kivik.StatusBadResponse,
			err:    "authentication failed",
		},
		{
			name: "Success 1.6.1",
			auth: &BasicAuth{Username: "admin", Password: "password"},
			client: newTestClient(&http.Response{
				StatusCode: 200,
				Header: http.Header{
					"Server":         {"CouchDB/1.6.1 (Erlang OTP/17)"},
					"Date":           {"Tue, 31 Oct 2017 11:34:32 GMT"},
					"Content-Type":   {"application/json"},
					"Content-Length": {"177"},
					"Cache-Control":  {"must-revalidate"},
				},
				ContentLength: 177,
				Request:       &http.Request{Method: "GET"},
				Body:          Body(`{"ok":true,"userCtx":{"name":"admin","roles":["_admin"]},"info":{"authentication_db":"_users","authentication_handlers":["oauth","cookie","default"],"authenticated":"default"}}`),
			}, nil),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.auth.Authenticate(context.Background(), test.client)
			testy.StatusError(t, test.err, test.status, err)
			if test.client.Client.Transport != test.auth {
				t.Errorf("transport not set as expected")
			}
		})
	}
}

func TestCookie(t *testing.T) {
	tests := []struct {
		name     string
		auth     *CookieAuth
		expected *http.Cookie
		found    bool
	}{
		{
			name:     "No cookie jar",
			auth:     &CookieAuth{},
			expected: nil,
			found:    false,
		},
		{
			name:     "No dsn",
			auth:     &CookieAuth{jar: &cookiejar.Jar{}},
			expected: nil,
			found:    false,
		},
		{
			name:     "no cookies",
			auth:     &CookieAuth{jar: &cookiejar.Jar{}, dsn: &url.URL{}},
			expected: nil,
			found:    false,
		},
		{
			name: "cookie found",
			auth: func() *CookieAuth {
				dsn, err := url.Parse("http://example.com/")
				if err != nil {
					t.Fatal(err)
				}
				jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
				if err != nil {
					t.Fatal(err)
				}
				jar.SetCookies(dsn, []*http.Cookie{
					{Name: kivik.SessionCookieName, Value: "foo"},
					{Name: "other", Value: "bar"},
				})
				return &CookieAuth{
					jar: jar,
					dsn: dsn,
				}
			}(),
			expected: &http.Cookie{Name: kivik.SessionCookieName, Value: "foo"},
			found:    true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, found := test.auth.Cookie()
			if found != test.found {
				t.Errorf("Unexpected found: %T", found)
			}
			if d := diff.Interface(test.expected, result); d != nil {
				t.Error(d)
			}
		})
	}
}
