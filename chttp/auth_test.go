package chttp

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/flimzy/diff"
	"github.com/flimzy/testy"
	"golang.org/x/net/publicsuffix"

	"github.com/go-kivik/kivik"
)

func TestBasicAuthRoundTrip(t *testing.T) {
	type rtTest struct {
		name     string
		auth     *BasicAuth
		req      *http.Request
		expected *http.Response
		cleanup  func()
	}
	tests := []rtTest{
		{
			name: "Provided transport",
			req:  httptest.NewRequest("GET", "/", nil),
			auth: &BasicAuth{
				Username: "foo",
				Password: "bar",
				transport: customTransport(func(req *http.Request) (*http.Response, error) {
					u, p, ok := req.BasicAuth()
					if !ok {
						t.Error("BasicAuth not set in request")
					}
					if u != "foo" || p != "bar" {
						t.Errorf("Unexpected user/password: %s/%s", u, p)
					}
					return &http.Response{StatusCode: 200}, nil
				}),
			},
			expected: &http.Response{StatusCode: 200},
		},
		func() rtTest {
			h := func(w http.ResponseWriter, r *http.Request) {
				u, p, ok := r.BasicAuth()
				if !ok {
					t.Error("BasicAuth not set in request")
				}
				if u != "foo" || p != "bar" {
					t.Errorf("Unexpected user/password: %s/%s", u, p)
				}
				w.Header().Set("Date", "Wed, 01 Nov 2017 19:32:41 GMT")
				w.Header().Set("Content-Type", "application/json")
			}
			s := httptest.NewServer(http.HandlerFunc(h))
			return rtTest{
				name: "default transport",
				auth: &BasicAuth{Username: "foo", Password: "bar"},
				req:  httptest.NewRequest("GET", s.URL, nil),
				expected: &http.Response{
					Status:     "200 OK",
					StatusCode: 200,
					Proto:      "HTTP/1.1",
					ProtoMajor: 1,
					ProtoMinor: 1,
					Header: http.Header{
						"Content-Length": {"0"},
						"Content-Type":   {"application/json"},
						"Date":           {"Wed, 01 Nov 2017 19:32:41 GMT"},
					},
				},
				cleanup: func() { s.Close() },
			}
		}(),
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := test.auth.RoundTrip(test.req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body = nil
			res.Request = nil
			if d := diff.Interface(test.expected, res); d != nil {
				t.Error(d)
			}
		})
	}
}

type mockRT struct {
	resp *http.Response
	err  error
}

var _ http.RoundTripper = &mockRT{}

func (rt *mockRT) RoundTrip(_ *http.Request) (*http.Response, error) {
	return rt.resp, rt.err
}

func TestAuthenticate(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close() // nolint: errcheck
		var authed bool
		if auth := r.Header.Get("Authorization"); auth == "Basic YWRtaW46YWJjMTIz" {
			authed = true
		}
		if r.Method == http.MethodPost {
			var result struct {
				Name     string
				Password string
			}
			_ = json.NewDecoder(r.Body).Decode(&result)
			if result.Name == "admin" && result.Password == "abc123" {
				authed = true
				http.SetCookie(w, &http.Cookie{
					Name:     kivik.SessionCookieName,
					Value:    "auth-token",
					Path:     "/",
					HttpOnly: true,
				})
			}
		}
		if ses := r.Header.Get("Cookie"); ses == "AuthSession=auth-token" {
			authed = true
		}
		if !authed {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/_session" {
			_, _ = w.Write([]byte(`{"userCtx":{"name":"admin"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"foo":123}`))
	}))

	type authTest struct {
		addr       string
		jar        http.CookieJar
		auther     Authenticator
		authErr    string
		authStatus int
		err        string
		status     int
	}
	tests := testy.NewTable()
	tests.Cleanup(s.Close)
	tests.Add("unauthorized", authTest{
		addr:   s.URL,
		err:    "Unauthorized",
		status: http.StatusUnauthorized,
	})
	tests.Add("basic auth", authTest{
		addr:   s.URL,
		auther: &BasicAuth{Username: "admin", Password: "abc123"},
	})
	tests.Add("cookie auth", authTest{
		addr:   s.URL,
		auther: &CookieAuth{Username: "admin", Password: "abc123"},
	})
	tests.Add("failed basic auth", authTest{
		addr:   s.URL,
		auther: &BasicAuth{Username: "foo"},
		err:    "Unauthorized",
		status: http.StatusUnauthorized,
	})
	tests.Add("failed cookie auth", authTest{
		addr:       s.URL,
		auther:     &CookieAuth{Username: "foo"},
		authErr:    "Unauthorized",
		authStatus: http.StatusUnauthorized,
		err:        "Unauthorized",
		status:     http.StatusUnauthorized,
	})
	tests.Add("already authenticated with cookie", func() interface{} {
		jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(s.URL)
		jar.SetCookies(u, []*http.Cookie{{
			Name:     kivik.SessionCookieName,
			Value:    "auth-token",
			Path:     "/",
			HttpOnly: true,
		}})
		return authTest{
			addr: s.URL,
			jar:  jar,
		}
	})

	tests.Run(t, func(t *testing.T, test authTest) {
		ctx := context.Background()
		c, err := New(ctx, test.addr)
		if err != nil {
			t.Fatal(err)
		}
		if test.jar != nil {
			c.Client.Jar = test.jar
		}
		if test.auther != nil {
			e := c.Auth(ctx, test.auther)
			testy.StatusError(t, test.authErr, test.authStatus, e)
		}
		_, err = c.DoError(ctx, "GET", "/foo", nil)
		testy.StatusError(t, test.err, test.status, err)
	})
}

func TestCookieAuthAuthenticate(t *testing.T) {
	tests := []struct {
		name           string
		auth           *CookieAuth
		client         *Client
		status         int
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
			status: kivik.StatusBadResponse,
			err:    "invalid character '}' after object key",
		},
		{
			name: "names don't match",
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
							Body: ioutil.NopCloser(strings.NewReader(`{"userCtx":{"name":"notfoo"}}`)),
						},
					},
				},
				dsn: &url.URL{Scheme: "http", Host: "foo.com"},
			},
			status: kivik.StatusBadResponse,
			err:    "auth response for unexpected user",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.auth.Authenticate(context.Background(), test.client)
			testy.StatusError(t, test.err, test.status, err)
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
