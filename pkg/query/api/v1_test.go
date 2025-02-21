// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/go-kit/kit/log"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/prometheus/common/route"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/timestamp"
	"github.com/prometheus/prometheus/promql"
	tsdb_labels "github.com/prometheus/prometheus/tsdb/labels"
	"github.com/thanos-io/thanos/pkg/compact"
	"github.com/thanos-io/thanos/pkg/component"
	extpromhttp "github.com/thanos-io/thanos/pkg/extprom/http"
	"github.com/thanos-io/thanos/pkg/query"
	"github.com/thanos-io/thanos/pkg/store"
	"github.com/thanos-io/thanos/pkg/testutil"
)

func TestEndpoints(t *testing.T) {
	defer leaktest.CheckTimeout(t, 10*time.Second)()

	lbls := []tsdb_labels.Labels{
		tsdb_labels.Labels{
			tsdb_labels.Label{Name: "__name__", Value: "test_metric1"},
			tsdb_labels.Label{Name: "foo", Value: "bar"},
		},
		tsdb_labels.Labels{
			tsdb_labels.Label{Name: "__name__", Value: "test_metric1"},
			tsdb_labels.Label{Name: "foo", Value: "boo"},
		},
		tsdb_labels.Labels{
			tsdb_labels.Label{Name: "__name__", Value: "test_metric2"},
			tsdb_labels.Label{Name: "foo", Value: "boo"},
		},
		tsdb_labels.Labels{
			tsdb_labels.Label{Name: "__name__", Value: "test_metric_replica1"},
			tsdb_labels.Label{Name: "foo", Value: "bar"},
			tsdb_labels.Label{Name: "replica", Value: "a"},
		},
		tsdb_labels.Labels{
			tsdb_labels.Label{Name: "__name__", Value: "test_metric_replica1"},
			tsdb_labels.Label{Name: "foo", Value: "boo"},
			tsdb_labels.Label{Name: "replica", Value: "a"},
		},
		tsdb_labels.Labels{
			tsdb_labels.Label{Name: "__name__", Value: "test_metric_replica1"},
			tsdb_labels.Label{Name: "foo", Value: "boo"},
			tsdb_labels.Label{Name: "replica", Value: "b"},
		},
		tsdb_labels.Labels{
			tsdb_labels.Label{Name: "__name__", Value: "test_metric_replica1"},
			tsdb_labels.Label{Name: "foo", Value: "boo"},
			tsdb_labels.Label{Name: "replica1", Value: "a"},
		},
	}

	db, err := testutil.NewTSDB()
	defer func() { testutil.Ok(t, db.Close()) }()
	testutil.Ok(t, err)

	app := db.Appender()
	for _, lbl := range lbls {
		for i := int64(0); i < 10; i++ {
			_, err := app.Add(lbl, i*60000, float64(i))
			testutil.Ok(t, err)
		}
	}
	testutil.Ok(t, app.Commit())

	now := time.Now()
	api := &API{
		queryableCreate: query.NewQueryableCreator(nil, store.NewTSDBStore(nil, nil, db, component.Query, nil)),
		queryEngine: promql.NewEngine(promql.EngineOpts{
			Logger:        nil,
			Reg:           nil,
			MaxConcurrent: 20,
			MaxSamples:    10000,
			Timeout:       100 * time.Second,
		}),
		now: func() time.Time { return now },
	}

	start := time.Unix(0, 0)

	var tests = []struct {
		endpoint ApiFunc
		params   map[string]string
		query    url.Values
		method   string
		response interface{}
		errType  ErrorType
	}{
		{
			endpoint: api.query,
			query: url.Values{
				"query": []string{"2"},
				"time":  []string{"123.4"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeScalar,
				Result: promql.Scalar{
					V: 2,
					T: timestamp.FromTime(start.Add(123*time.Second + 400*time.Millisecond)),
				},
			},
		},
		{
			endpoint: api.query,
			query: url.Values{
				"query": []string{"0.333"},
				"time":  []string{"1970-01-01T00:02:03Z"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeScalar,
				Result: promql.Scalar{
					V: 0.333,
					T: timestamp.FromTime(start.Add(123 * time.Second)),
				},
			},
		},
		{
			endpoint: api.query,
			query: url.Values{
				"query": []string{"0.333"},
				"time":  []string{"1970-01-01T01:02:03+01:00"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeScalar,
				Result: promql.Scalar{
					V: 0.333,
					T: timestamp.FromTime(start.Add(123 * time.Second)),
				},
			},
		},
		// Query endpoint without deduplication.
		{
			endpoint: api.query,
			query: url.Values{
				"query": []string{"test_metric_replica1"},
				"time":  []string{"1970-01-01T01:02:03+01:00"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeVector,
				Result: promql.Vector{
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "bar",
							},
							{
								Name:  "replica",
								Value: "a",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "boo",
							},
							{
								Name:  "replica",
								Value: "a",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "boo",
							},
							{
								Name:  "replica",
								Value: "b",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "boo",
							},
							{
								Name:  "replica1",
								Value: "a",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
				},
			},
		},
		// Query endpoint with single deduplication label.
		{
			endpoint: api.query,
			query: url.Values{
				"query":           []string{"test_metric_replica1"},
				"time":            []string{"1970-01-01T01:02:03+01:00"},
				"replicaLabels[]": []string{"replica"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeVector,
				Result: promql.Vector{
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "bar",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "boo",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "boo",
							},
							{
								Name:  "replica1",
								Value: "a",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
				},
			},
		},
		// Query endpoint with multiple deduplication label.
		{
			endpoint: api.query,
			query: url.Values{
				"query":           []string{"test_metric_replica1"},
				"time":            []string{"1970-01-01T01:02:03+01:00"},
				"replicaLabels[]": []string{"replica", "replica1"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeVector,
				Result: promql.Vector{
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "bar",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
					{
						Metric: labels.Labels{
							{
								Name:  "__name__",
								Value: "test_metric_replica1",
							},
							{
								Name:  "foo",
								Value: "boo",
							},
						},
						Point: promql.Point{
							T: 123000,
							V: 2,
						},
					},
				},
			},
		},
		{
			endpoint: api.query,
			query: url.Values{
				"query": []string{"0.333"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeScalar,
				Result: promql.Scalar{
					V: 0.333,
					T: timestamp.FromTime(now),
				},
			},
		},
		// Bad dedup parameter.
		{
			endpoint: api.query,
			query: url.Values{
				"query": []string{"0.333"},
				"dedup": []string{"sdfsf"},
			},
			errType: errorBadData,
		},
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"start": []string{"0"},
				"end":   []string{"2"},
				"step":  []string{"1"},
			},
			response: &queryData{
				ResultType: promql.ValueTypeMatrix,
				Result: promql.Matrix{
					promql.Series{
						Points: []promql.Point{
							{V: 0, T: timestamp.FromTime(start)},
							{V: 1, T: timestamp.FromTime(start.Add(1 * time.Second))},
							{V: 2, T: timestamp.FromTime(start.Add(2 * time.Second))},
						},
						Metric: nil,
					},
				},
			},
		},
		// Missing query params in range queries.
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"end":   []string{"2"},
				"step":  []string{"1"},
			},
			errType: errorBadData,
		},
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"start": []string{"0"},
				"step":  []string{"1"},
			},
			errType: errorBadData,
		},
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"start": []string{"0"},
				"end":   []string{"2"},
			},
			errType: errorBadData,
		},
		// Bad query expression.
		{
			endpoint: api.query,
			query: url.Values{
				"query": []string{"invalid][query"},
				"time":  []string{"1970-01-01T01:02:03+01:00"},
			},
			errType: errorBadData,
		},
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"invalid][query"},
				"start": []string{"0"},
				"end":   []string{"100"},
				"step":  []string{"1"},
			},
			errType: errorBadData,
		},
		// Invalid step.
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"start": []string{"1"},
				"end":   []string{"2"},
				"step":  []string{"0"},
			},
			errType: errorBadData,
		},
		// Start after end.
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"start": []string{"2"},
				"end":   []string{"1"},
				"step":  []string{"1"},
			},
			errType: errorBadData,
		},
		// Start overflows int64 internally.
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"start": []string{"148966367200.372"},
				"end":   []string{"1489667272.372"},
				"step":  []string{"1"},
			},
			errType: errorBadData,
		},
		// Bad dedup parameter
		{
			endpoint: api.queryRange,
			query: url.Values{
				"query": []string{"time()"},
				"start": []string{"0"},
				"end":   []string{"2"},
				"step":  []string{"1"},
				"dedup": []string{"sdfsf-range"},
			},
			errType: errorBadData,
		},
		{
			endpoint: api.labelValues,
			params: map[string]string{
				"name": "__name__",
			},
			response: []string{
				"test_metric1",
				"test_metric2",
				"test_metric_replica1",
			},
		},
		{
			endpoint: api.labelValues,
			params: map[string]string{
				"name": "foo",
			},
			response: []string{
				"bar",
				"boo",
			},
		},
		// Bad name parameter.
		{
			endpoint: api.labelValues,
			params: map[string]string{
				"name": "not!!!allowed",
			},
			errType: errorBadData,
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
		},
		// Series that does not exist should return an empty array.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`foobar`},
			},
			response: []labels.Labels{},
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric1{foo=~".+o"}`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric1", "foo", "boo"),
			},
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric1{foo=~".+o$"}`, `test_metric1{foo=~".+o"}`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric1", "foo", "boo"),
			},
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric1{foo=~".+o"}`, `none`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric1", "foo", "boo"),
			},
		},
		// Start and end before series starts.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"-2"},
				"end":     []string{"-1"},
			},
			response: []labels.Labels{},
		},
		// Start and end after series ends.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"100000"},
				"end":     []string{"100001"},
			},
			response: []labels.Labels{},
		},
		// Start before series starts, end after series ends.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"-1"},
				"end":     []string{"100000"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
		},
		// Start and end within series.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"1"},
				"end":     []string{"100"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
		},
		// Start within series, end after.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"1"},
				"end":     []string{"100000"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
		},
		// Start before series, end within series.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"-1"},
				"end":     []string{"1"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
		},
		// Missing match[] query params in series requests.
		{
			endpoint: api.series,
			errType:  errorBadData,
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"dedup":   []string{"sdfsf-series"},
			},
			errType: errorBadData,
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric1{foo=~".+o"}`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric1", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric1{foo=~".+o$"}`, `test_metric1{foo=~".+o"}`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric1", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric1{foo=~".+o"}`, `none`},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric1", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		// Start and end before series starts.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"-2"},
				"end":     []string{"-1"},
			},
			response: []labels.Labels{},
		},
		// Start and end after series ends.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"100000"},
				"end":     []string{"100001"},
			},
			response: []labels.Labels{},
		},
		// Start before series starts, end after series ends.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"-1"},
				"end":     []string{"100000"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		// Start and end within series.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"1"},
				"end":     []string{"100"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		// Start within series, end after.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"1"},
				"end":     []string{"100000"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		// Start before series, end within series.
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"start":   []string{"-1"},
				"end":     []string{"1"},
			},
			response: []labels.Labels{
				labels.FromStrings("__name__", "test_metric2", "foo", "boo"),
			},
			method: http.MethodPost,
		},
		// Missing match[] query params in series requests.
		{
			endpoint: api.series,
			errType:  errorBadData,
			method:   http.MethodPost,
		},
		{
			endpoint: api.series,
			query: url.Values{
				"match[]": []string{`test_metric2`},
				"dedup":   []string{"sdfsf-series"},
			},
			errType: errorBadData,
			method:  http.MethodPost,
		},
	}

	for _, test := range tests {
		if ok := t.Run(test.query.Encode(), func(t *testing.T) {
			// Build a context with the correct request params.
			ctx := context.Background()
			for p, v := range test.params {
				ctx = route.WithParam(ctx, p, v)
			}

			reqURL := "http://example.com"
			params := test.query.Encode()

			var body io.Reader
			if test.method == http.MethodPost {
				body = strings.NewReader(params)
			} else if test.method == "" {
				test.method = "ANY"
				reqURL += "?" + params
			}

			req, err := http.NewRequest(test.method, reqURL, body)
			if err != nil {
				t.Fatal(err)
			}

			if body != nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}

			resp, _, apiErr := test.endpoint(req.WithContext(ctx))
			if apiErr != nil {
				if test.errType == errorNone {
					t.Fatalf("Unexpected error: %s", apiErr)
				}
				if test.errType != apiErr.Typ {
					t.Fatalf("Expected error of type %q but got type %q", test.errType, apiErr.Typ)
				}
				return
			}
			if test.errType != errorNone {
				t.Fatalf("Expected error of type %q but got none", test.errType)
			}

			if !reflect.DeepEqual(resp, test.response) {
				t.Fatalf("Response does not match, expected:\n%+v\ngot:\n%+v", test.response, resp)
			}
		}); !ok {
			return
		}

	}
}

func TestRespondSuccess(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Respond(w, "test", nil)
	}))
	defer s.Close()

	resp, err := http.Get(s.URL)
	if err != nil {
		t.Fatalf("Error on test request: %s", err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	defer func() { testutil.Ok(t, resp.Body.Close()) }()
	if err != nil {
		t.Fatalf("Error reading response body: %s", err)
	}

	if resp.StatusCode != 200 {
		t.Fatalf("Return code %d expected in success response but got %d", 200, resp.StatusCode)
	}
	if h := resp.Header.Get("Content-Type"); h != "application/json" {
		t.Fatalf("Expected Content-Type %q but got %q", "application/json", h)
	}

	var res response
	if err = json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("Error unmarshaling JSON body: %s", err)
	}

	exp := &response{
		Status: statusSuccess,
		Data:   "test",
	}
	if !reflect.DeepEqual(&res, exp) {
		t.Fatalf("Expected response \n%v\n but got \n%v\n", res, exp)
	}
}

func TestRespondError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		RespondError(w, &ApiError{errorTimeout, errors.New("message")}, "test")
	}))
	defer s.Close()

	resp, err := http.Get(s.URL)
	if err != nil {
		t.Fatalf("Error on test request: %s", err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	defer func() { testutil.Ok(t, resp.Body.Close()) }()
	if err != nil {
		t.Fatalf("Error reading response body: %s", err)
	}

	if want, have := http.StatusServiceUnavailable, resp.StatusCode; want != have {
		t.Fatalf("Return code %d expected in error response but got %d", want, have)
	}
	if h := resp.Header.Get("Content-Type"); h != "application/json" {
		t.Fatalf("Expected Content-Type %q but got %q", "application/json", h)
	}

	var res response
	if err = json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("Error unmarshaling JSON body: %s", err)
	}

	exp := &response{
		Status:    statusError,
		Data:      "test",
		ErrorType: errorTimeout,
		Error:     "message",
	}
	if !reflect.DeepEqual(&res, exp) {
		t.Fatalf("Expected response \n%v\n but got \n%v\n", res, exp)
	}
}

func TestParseTime(t *testing.T) {
	ts, err := time.Parse(time.RFC3339Nano, "2015-06-03T13:21:58.555Z")
	if err != nil {
		panic(err)
	}

	var tests = []struct {
		input  string
		fail   bool
		result time.Time
	}{
		{
			input: "",
			fail:  true,
		}, {
			input: "abc",
			fail:  true,
		}, {
			input: "30s",
			fail:  true,
		}, {
			input:  "123",
			result: time.Unix(123, 0),
		}, {
			input:  "123.123",
			result: time.Unix(123, 123000000),
		}, {
			input:  "2015-06-03T13:21:58.555Z",
			result: ts,
		}, {
			input:  "2015-06-03T14:21:58.555+01:00",
			result: ts,
		},
	}

	for _, test := range tests {
		ts, err := parseTime(test.input)
		if err != nil && !test.fail {
			t.Errorf("Unexpected error for %q: %s", test.input, err)
			continue
		}
		if err == nil && test.fail {
			t.Errorf("Expected error for %q but got none", test.input)
			continue
		}
		if !test.fail && !ts.Equal(test.result) {
			t.Errorf("Expected time %v for input %q but got %v", test.result, test.input, ts)
		}
	}
}

func TestParseDuration(t *testing.T) {
	var tests = []struct {
		input  string
		fail   bool
		result time.Duration
	}{
		{
			input: "",
			fail:  true,
		}, {
			input: "abc",
			fail:  true,
		}, {
			input: "2015-06-03T13:21:58.555Z",
			fail:  true,
		}, {
			// Internal int64 overflow.
			input: "-148966367200.372",
			fail:  true,
		}, {
			// Internal int64 overflow.
			input: "148966367200.372",
			fail:  true,
		}, {
			input:  "123",
			result: 123 * time.Second,
		}, {
			input:  "123.333",
			result: 123*time.Second + 333*time.Millisecond,
		}, {
			input:  "15s",
			result: 15 * time.Second,
		}, {
			input:  "5m",
			result: 5 * time.Minute,
		},
	}

	for _, test := range tests {
		d, err := parseDuration(test.input)
		if err != nil && !test.fail {
			t.Errorf("Unexpected error for %q: %s", test.input, err)
			continue
		}
		if err == nil && test.fail {
			t.Errorf("Expected error for %q but got none", test.input)
			continue
		}
		if !test.fail && d != test.result {
			t.Errorf("Expected duration %v for input %q but got %v", test.result, test.input, d)
		}
	}
}

func TestOptionsMethod(t *testing.T) {
	r := route.New()
	api := &API{}
	api.Register(r, &opentracing.NoopTracer{}, log.NewNopLogger(), extpromhttp.NewNopInstrumentationMiddleware())

	s := httptest.NewServer(r)
	defer s.Close()

	req, err := http.NewRequest("OPTIONS", s.URL+"/any_path", nil)
	if err != nil {
		t.Fatalf("Error creating OPTIONS request: %s", err)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Error executing OPTIONS request: %s", err)
	}

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Expected status %d, got %d", http.StatusNoContent, resp.StatusCode)
	}

	for h, v := range corsHeaders {
		if resp.Header.Get(h) != v {
			t.Fatalf("Expected %q for header %q, got %q", v, h, resp.Header.Get(h))
		}
	}
}

func BenchmarkQueryResultEncoding(b *testing.B) {
	var mat promql.Matrix
	for i := 0; i < 1000; i++ {
		lset := labels.FromStrings(
			"__name__", "my_test_metric_name",
			"instance", fmt.Sprintf("abcdefghijklmnopqrstuvxyz-%d", i),
			"job", "test-test",
			"method", "ABCD",
			"status", "199",
			"namespace", "something",
			"long-label", "34grnt83j0qxj309je9rgt9jf2jd-92jd-92jf9wrfjre",
		)
		var points []promql.Point
		for j := 0; j < b.N/1000; j++ {
			points = append(points, promql.Point{
				T: int64(j * 10000),
				V: rand.Float64(),
			})
		}
		mat = append(mat, promql.Series{
			Metric: lset,
			Points: points,
		})
	}
	input := &queryData{
		ResultType: promql.ValueTypeMatrix,
		Result:     mat,
	}
	b.ResetTimer()

	c, err := json.Marshal(&input)
	testutil.Ok(b, err)
	fmt.Println(len(c))
}

func TestParseDownsamplingParamMillis(t *testing.T) {
	var tests = []struct {
		maxSourceResolutionParam string
		result                   int64
		step                     time.Duration
		fail                     bool
		enableAutodownsampling   bool
	}{
		{
			maxSourceResolutionParam: "0s",
			enableAutodownsampling:   false,
			step:                     time.Hour,
			result:                   int64(compact.ResolutionLevelRaw),
			fail:                     false,
		},
		{
			maxSourceResolutionParam: "5m",
			step:                     time.Hour,
			enableAutodownsampling:   false,
			result:                   int64(compact.ResolutionLevel5m),
			fail:                     false,
		},
		{
			maxSourceResolutionParam: "1h",
			step:                     time.Hour,
			enableAutodownsampling:   false,
			result:                   int64(compact.ResolutionLevel1h),
			fail:                     false,
		},
		{
			maxSourceResolutionParam: "",
			enableAutodownsampling:   true,
			step:                     time.Hour,
			result:                   int64(time.Hour / (5 * 1000 * 1000)),
			fail:                     false,
		},
		{
			maxSourceResolutionParam: "",
			enableAutodownsampling:   true,
			step:                     time.Hour,
			result:                   int64((1 * time.Hour) / 6),
			fail:                     true,
		},
		{
			maxSourceResolutionParam: "",
			enableAutodownsampling:   true,
			step:                     time.Hour,
			result:                   int64((1 * time.Hour) / 6),
			fail:                     true,
		},
	}

	for i, test := range tests {
		api := API{enableAutodownsampling: test.enableAutodownsampling}
		v := url.Values{}
		v.Set("max_source_resolution", test.maxSourceResolutionParam)
		r := http.Request{PostForm: v}

		// If no max_source_resolution is specified fit at least 5 samples between steps.
		maxResMillis, _ := api.parseDownsamplingParamMillis(&r, test.step/5)
		if test.fail == false {
			testutil.Assert(t, maxResMillis == test.result, "case %v: expected %v to be equal to %v", i, maxResMillis, test.result)
		} else {
			testutil.Assert(t, maxResMillis != test.result, "case %v: expected %v not to be equal to %v", i, maxResMillis, test.result)
		}

	}
}
