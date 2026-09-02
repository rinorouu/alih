// Copyright 2025 rinorouu
//
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

package notion

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The fake credential used everywhere in these tests. It is obviously not a
// real token, and tests assert it never escapes into an artifact.
const fakeToken = "secret_notion_fake_token_never_real"

// notionFixture is a hand-authored Notion. Responses are written by hand rather
// than recorded, which is what Alih's documented HTTP injection points are for.
//
// The shape is deliberately not ClickUp's: one database holding one data
// source, whose rows are pages, whose content is a block tree four levels deep.
type notionFixture struct {
	t *testing.T
	// requests records every path called, so tests can assert traversal order
	// and that pagination actually paginated.
	requests []string
	// failures maps a path prefix to a status returned instead of a body.
	failures map[string]int
	// rateLimitOnce makes the first call to a path 429 and the next succeed.
	rateLimitOnce map[string]bool
}

func newFixture(t *testing.T) *notionFixture {
	return &notionFixture{t: t, failures: map[string]int{}, rateLimitOnce: map[string]bool{}}
}

func (f *notionFixture) client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Notion-Version"); got != apiVersion {
			f.t.Errorf("request omitted or changed the pinned Notion-Version: %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+fakeToken {
			f.t.Errorf("request did not carry the integration token: %q", got)
		}
		path := request.URL.Path
		f.requests = append(f.requests, request.Method+" "+path)

		if status, failing := f.failures[path]; failing {
			return jsonResponse(request, status, `{"object":"error","code":"validation_error"}`)
		}
		if f.rateLimitOnce[path] {
			f.rateLimitOnce[path] = false
			return jsonResponse(request, http.StatusTooManyRequests, `{"object":"error","code":"rate_limited"}`)
		}
		return f.route(request)
	})}
}

func (f *notionFixture) route(request *http.Request) (*http.Response, error) {
	path := request.URL.Path
	body := ""
	if request.Body != nil {
		raw, _ := io.ReadAll(request.Body)
		body = string(raw)
	}
	switch {
	case path == "/v1/users/me":
		return jsonResponse(request, 200, `{"object":"user","id":"bot-1","name":"Alih Test Bot",
			"type":"bot","bot":{"workspace_id":"ws-notion-1","workspace_name":"Example Notion Workspace"}}`)

	case path == "/v1/search":
		// Two pages of search results, to prove cursor pagination is real.
		if strings.Contains(body, `"start_cursor":"cursor-search-2"`) {
			return jsonResponse(request, 200, `{"object":"list","results":[
				{"object":"data_source","id":"ds-2"}],"has_more":false,"next_cursor":null}`)
		}
		return jsonResponse(request, 200, `{"object":"list","results":[
			{"object":"data_source","id":"ds-1"}],"has_more":true,"next_cursor":"cursor-search-2"}`)

	case path == "/v1/data_sources/ds-1":
		return jsonResponse(request, 200, `{"object":"data_source","id":"ds-1","name":"Field notes",
			"parent":{"type":"database_id","database_id":"db-1"},
			"properties":{
				"Name":{"id":"title","name":"Name","type":"title","title":{}},
				"Stage":{"id":"p-stage","name":"Stage","type":"select","select":{"options":[{"name":"draft"},{"name":"final"}]}},
				"Score":{"id":"p-score","name":"Score","type":"formula","formula":{"expression":"1+1"}}}}`)

	case path == "/v1/data_sources/ds-2":
		return jsonResponse(request, 200, `{"object":"data_source","id":"ds-2","name":"Second source",
			"parent":{"type":"database_id","database_id":"db-1"},"properties":{}}`)

	case path == "/v1/databases/db-1":
		return jsonResponse(request, 200, `{"object":"database","id":"db-1",
			"title":[{"plain_text":"Research"}],
			"data_sources":[{"id":"ds-1","name":"Field notes"},{"id":"ds-2","name":"Second source"}]}`)

	case path == "/v1/data_sources/ds-1/query":
		return jsonResponse(request, 200, `{"object":"list","results":[
			{"object":"page","id":"page-1","created_time":"2026-01-01T00:00:00.000Z",
			 "last_edited_time":"2026-01-02T00:00:00.000Z","archived":false,
			 "properties":{
				"Name":{"id":"title","type":"title","title":[{"plain_text":"First note"}]},
				"Stage":{"id":"p-stage","type":"select","select":{"name":"draft"}},
				"Score":{"id":"p-score","type":"formula","formula":{"type":"number","number":2}}}}],
			"has_more":false,"next_cursor":null}`)

	case path == "/v1/data_sources/ds-2/query":
		return jsonResponse(request, 200, `{"object":"list","results":[],"has_more":false,"next_cursor":null}`)

	// The block tree: page-1 -> b1 -> b2 -> b3 -> b4. Four levels, which no
	// fixed-hierarchy assumption could walk.
	case path == "/v1/blocks/page-1/children":
		return jsonResponse(request, 200, `{"object":"list","results":[
			{"object":"block","id":"b1","type":"heading_1","has_children":true,
			 "heading_1":{"rich_text":[{"plain_text":"Top heading"}]}}],
			"has_more":false,"next_cursor":null}`)
	case path == "/v1/blocks/b1/children":
		return jsonResponse(request, 200, `{"object":"list","results":[
			{"object":"block","id":"b2","type":"paragraph","has_children":true,
			 "paragraph":{"rich_text":[{"plain_text":"Level two"}]}}],
			"has_more":false,"next_cursor":null}`)
	case path == "/v1/blocks/b2/children":
		return jsonResponse(request, 200, `{"object":"list","results":[
			{"object":"block","id":"b3","type":"toggle","has_children":true,
			 "toggle":{"rich_text":[{"plain_text":"Level three"}]}}],
			"has_more":false,"next_cursor":null}`)
	case path == "/v1/blocks/b3/children":
		return jsonResponse(request, 200, `{"object":"list","results":[
			{"object":"block","id":"b4","type":"paragraph","has_children":false,
			 "paragraph":{"rich_text":[{"plain_text":"Level four"}]}}],
			"has_more":false,"next_cursor":null}`)
	case strings.HasPrefix(path, "/v1/blocks/"):
		return jsonResponse(request, 200, `{"object":"list","results":[],"has_more":false,"next_cursor":null}`)
	}
	f.t.Errorf("fixture received an unexpected request: %s", path)
	return jsonResponse(request, 404, `{"object":"error","code":"object_not_found"}`)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(request *http.Request, status int, body string) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status, Header: header, Request: request,
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
	}, nil
}

func (f *notionFixture) called(method, path string) int {
	count := 0
	for _, request := range f.requests {
		if request == method+" "+path {
			count++
		}
	}
	return count
}

func (f *notionFixture) describe() string {
	return fmt.Sprintf("%d requests: %s", len(f.requests), strings.Join(f.requests, ", "))
}
