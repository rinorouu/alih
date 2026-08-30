package clickup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"alih/internal/connector"
)

type scanFixture struct {
	t         *testing.T
	token     string
	taskCalls []string
	mode      string
}

func (fixture *scanFixture) roundTrip(request *http.Request) (*http.Response, error) {
	fixture.t.Helper()
	if request.Method != http.MethodGet {
		fixture.t.Errorf("method = %s, want GET", request.Method)
	}
	if request.Header.Get("Authorization") != fixture.token {
		fixture.t.Error("request did not carry the exact test credential")
	}

	path := request.URL.Path
	query := request.URL.Query()
	switch path {
	case "/api/v2/team/w1/space":
		requireArchivedQuery(fixture.t, query)
		if query.Get("archived") == "false" {
			return fixtureResponse(http.StatusOK, `{"spaces":[{"id":"s1","name":"Space"}]}`), nil
		}
		return fixtureResponse(http.StatusOK, `{"spaces":[]}`), nil
	case "/api/v2/space/s1/folder":
		requireArchivedQuery(fixture.t, query)
		if query.Get("archived") == "false" {
			return fixtureResponse(http.StatusOK, `{"folders":[{"id":"f1","name":"Folder"}]}`), nil
		}
		return fixtureResponse(http.StatusOK, `{"folders":[]}`), nil
	case "/api/v2/space/s1/list":
		requireArchivedQuery(fixture.t, query)
		if query.Get("archived") == "false" {
			return fixtureResponse(http.StatusOK, `{"lists":[{"id":"l2","name":"Folderless"}]}`), nil
		}
		return fixtureResponse(http.StatusOK, `{"lists":[]}`), nil
	case "/api/v2/folder/f1/list":
		requireArchivedQuery(fixture.t, query)
		if query.Get("archived") == "false" {
			return fixtureResponse(http.StatusOK, `{"lists":[{"id":"l1","name":"Nested"}]}`), nil
		}
		return fixtureResponse(http.StatusOK, `{"lists":[]}`), nil
	case "/api/v2/team/w1/field":
		return fixtureResponse(http.StatusOK, `{"fields":[{"id":"cf-team","name":"Team Field"}]}`), nil
	case "/api/v2/space/s1/field":
		return fixtureResponse(http.StatusOK, `{"fields":[{"id":"cf-team","name":"Team Field"},{"id":"cf-space","name":"Space Field"}]}`), nil
	case "/api/v2/folder/f1/field":
		return fixtureResponse(http.StatusOK, `{"fields":[{"id":"cf-team","name":"Team Field"},{"id":"cf-folder","name":"Folder Field"}]}`), nil
	case "/api/v2/list/l1/field":
		return fixtureResponse(http.StatusOK, `{"fields":[{"id":"cf-team","name":"Team Field"},{"id":"cf-folder","name":"Folder Field"},{"id":"cf-list","name":"List Field"}]}`), nil
	case "/api/v2/list/l2/field":
		return fixtureResponse(http.StatusOK, `{"fields":[{"id":"cf-team","name":"Team Field"},{"id":"cf-space","name":"Space Field"}]}`), nil
	case "/api/v2/list/l1/task", "/api/v2/list/l2/task":
		return fixture.taskPage(request)
	case "/api/v2/task/t1":
		return fixtureResponse(http.StatusOK, taskDetailJSON("t1", "l1", "null", `[{"id":"a1"}]`, `[{"task_id":"t1","depends_on":"t2"}]`, `[{"task_id":"t1","link_id":"t3"}]`)), nil
	case "/api/v2/task/t2":
		return fixtureResponse(http.StatusOK, taskDetailJSON("t2", "l1", `"t1"`, `[]`, `[{"task_id":"t1","depends_on":"t2"}]`, `[]`)), nil
	case "/api/v2/task/t3":
		return fixtureResponse(http.StatusOK, taskDetailJSON("t3", "l2", "null", `[{"id":"a2"}]`, `[]`, `[{"task_id":"t1","link_id":"t3"}]`)), nil
	case "/api/v2/task/t1/comment":
		if query.Get("start") == "" {
			return fixtureResponse(http.StatusOK, `{"comments":[{"id":"c1","date":"1000","reply_count":"1"},{"id":"c2","date":"900","reply_count":"0"}]}`), nil
		}
		if query.Get("start") != "900" || query.Get("start_id") != "c2" {
			fixture.t.Errorf("unexpected t1 comment cursor: %s", query.Encode())
		}
		return fixtureResponse(http.StatusOK, `{"comments":[]}`), nil
	case "/api/v2/task/t2/comment":
		return fixtureResponse(http.StatusOK, `{"comments":[]}`), nil
	case "/api/v2/task/t3/comment":
		if query.Get("start") == "" {
			return fixtureResponse(http.StatusOK, `{"comments":[{"id":"c3","date":800,"reply_count":0}]}`), nil
		}
		if query.Get("start") != "800" || query.Get("start_id") != "c3" {
			fixture.t.Errorf("unexpected t3 comment cursor: %s", query.Encode())
		}
		return fixtureResponse(http.StatusOK, `{"comments":[]}`), nil
	case "/api/v2/comment/c1/reply":
		if fixture.mode == "reply_mismatch" {
			return fixtureResponse(http.StatusOK, `{"comments":[]}`), nil
		}
		return fixtureResponse(http.StatusOK, `{"comments":[{"id":"r1","date":"950"}]}`), nil
	default:
		fixture.t.Errorf("unexpected request: %s?%s", path, query.Encode())
		return fixtureResponse(http.StatusNotFound, `{}`), nil
	}
}

func (fixture *scanFixture) taskPage(request *http.Request) (*http.Response, error) {
	query := request.URL.Query()
	for _, field := range []string{"archived", "include_closed", "order_by", "reverse", "subtasks", "page"} {
		if query.Get(field) == "" {
			fixture.t.Errorf("task query missing %s: %s", field, query.Encode())
		}
	}
	if query.Get("include_closed") != "true" || query.Get("subtasks") != "true" {
		fixture.t.Errorf("task query omitted closed tasks or subtasks: %s", query.Encode())
	}
	if query.Get("order_by") != "id" || query.Get("reverse") != "false" {
		fixture.t.Errorf("task query did not request deterministic ID ordering: %s", query.Encode())
	}
	key := strings.TrimPrefix(request.URL.Path, "/api/v2/list/") + "?" + query.Encode()
	fixture.taskCalls = append(fixture.taskCalls, key)

	listID := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/v2/list/"), "/")[0]
	archived := query.Get("archived")
	page := query.Get("page")
	if fixture.mode == "page_failure" && listID == "l1" && archived == "false" && page == "1" {
		return fixtureResponse(http.StatusServiceUnavailable, `{"err":"maintenance","ECODE":"SERVICE_UNAVAILABLE"}`), nil
	}
	if fixture.mode == "duplicate" && listID == "l1" && archived == "false" {
		switch page {
		case "0", "1":
			return fixtureResponse(http.StatusOK, `{"tasks":[{"id":"t1","parent":null,"list":{"id":"l1"}}]}`), nil
		default:
			return fixtureResponse(http.StatusOK, `{"tasks":[]}`), nil
		}
	}
	if fixture.mode == "orphan" && listID == "l1" && archived == "false" && page == "1" {
		return fixtureResponse(http.StatusOK, `{"tasks":[{"id":"t2","parent":"missing-parent","list":{"id":"l1"}}]}`), nil
	}

	switch listID + ":" + archived + ":" + page {
	case "l1:false:0":
		return fixtureResponse(http.StatusOK, `{"tasks":[{"id":"t1","parent":null,"list":{"id":"l1"}}]}`), nil
	case "l1:false:1":
		return fixtureResponse(http.StatusOK, `{"tasks":[{"id":"t2","parent":"t1","list":{"id":"l1"}}]}`), nil
	case "l1:false:2", "l1:true:0", "l2:false:0":
		return fixtureResponse(http.StatusOK, `{"tasks":[]}`), nil
	case "l2:true:0":
		return fixtureResponse(http.StatusOK, `{"tasks":[{"id":"t3","parent":null,"list":{"id":"l2"}}]}`), nil
	case "l2:true:1":
		return fixtureResponse(http.StatusOK, `{"tasks":[]}`), nil
	default:
		fixture.t.Errorf("unexpected task page %s", listID+":"+archived+":"+page)
		return fixtureResponse(http.StatusNotFound, `{}`), nil
	}
}

func requireArchivedQuery(t *testing.T, query url.Values) {
	t.Helper()
	if value := query.Get("archived"); value != "true" && value != "false" {
		t.Errorf("archived query = %q", value)
	}
}

func taskDetailJSON(id, listID, parent, attachments, dependencies, links string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"parent":%s,"list":{"id":%q},"attachments":%s,"dependencies":%s,"linked_tasks":%s}`,
		id, "Task "+id, parent, listID, attachments, dependencies, links)
}

func TestScanTraversesHierarchyPaginationAndSupportedInventory(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "scan-secret"}
	client := fixtureClient(t, fixture.roundTrip)
	workspace := connector.Workspace{ID: "w1", Name: "Test Workspace"}

	result, err := client.Scan(context.Background(), fixture.token, workspace)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Workspace != workspace {
		t.Fatalf("Workspace = %#v, want %#v", result.Workspace, workspace)
	}
	want := connector.Inventory{
		Spaces: 1, Folders: 1, Lists: 2, Tasks: 2, Subtasks: 1,
		Comments: 4, Attachments: 2, CustomFields: 4, Relationships: 2,
	}
	if result.Inventory != want {
		t.Fatalf("Inventory = %#v, want %#v", result.Inventory, want)
	}
	if len(result.Capabilities) == 0 {
		t.Fatal("Capabilities are empty")
	}

	gotCalls := append([]string(nil), fixture.taskCalls...)
	sort.Strings(gotCalls)
	for _, expected := range []string{
		"l1/task?archived=false&include_closed=true&order_by=id&page=0&reverse=false&subtasks=true",
		"l1/task?archived=false&include_closed=true&order_by=id&page=1&reverse=false&subtasks=true",
		"l1/task?archived=false&include_closed=true&order_by=id&page=2&reverse=false&subtasks=true",
		"l2/task?archived=true&include_closed=true&order_by=id&page=1&reverse=false&subtasks=true",
	} {
		index := sort.SearchStrings(gotCalls, expected)
		if index == len(gotCalls) || gotCalls[index] != expected {
			t.Errorf("task pagination did not request %q: %#v", expected, gotCalls)
		}
	}
}

func TestScanFailsClosedOnTaskPaginationAPIFailure(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "scan-secret", mode: "page_failure"}
	client := fixtureClient(t, fixture.roundTrip)

	result, err := client.Scan(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Test"})
	if err == nil {
		t.Fatal("Scan() error = nil")
	}
	if !reflect.DeepEqual(result, connector.ScanResult{}) {
		t.Fatalf("failed Scan returned partial result: %#v", result)
	}
	var clickUpError *Error
	if !errors.As(err, &clickUpError) || clickUpError.Kind != ErrorAPI || clickUpError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %#v, want HTTP API failure", err)
	}
	if strings.Contains(err.Error(), fixture.token) {
		t.Fatal("scan failure exposed the credential")
	}
}

func TestScanRejectsDuplicateTaskAcrossPagination(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "scan-secret", mode: "duplicate"}
	client := fixtureClient(t, fixture.roundTrip)

	result, err := client.Scan(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Test"})
	if err == nil || !strings.Contains(err.Error(), "duplicate task id") {
		t.Fatalf("Scan() error = %v, want duplicate-task failure", err)
	}
	if !reflect.DeepEqual(result, connector.ScanResult{}) {
		t.Fatalf("failed Scan returned partial result: %#v", result)
	}
}

func TestScanRejectsSubtaskWithMissingParent(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "scan-secret", mode: "orphan"}
	client := fixtureClient(t, fixture.roundTrip)

	result, err := client.Scan(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Test"})
	if err == nil || !strings.Contains(err.Error(), "references missing parent Task") {
		t.Fatalf("Scan() error = %v, want missing-parent failure", err)
	}
	if !reflect.DeepEqual(result, connector.ScanResult{}) {
		t.Fatalf("failed Scan returned partial result: %#v", result)
	}
}

func TestScanRejectsThreadedReplyCountMismatch(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "scan-secret", mode: "reply_mismatch"}
	client := fixtureClient(t, fixture.roundTrip)

	result, err := client.Scan(context.Background(), fixture.token, connector.Workspace{ID: "w1", Name: "Test"})
	if err == nil || !strings.Contains(err.Error(), "declared 1 replies but API returned 0") {
		t.Fatalf("Scan() error = %v, want reply-count mismatch failure", err)
	}
	if !reflect.DeepEqual(result, connector.ScanResult{}) {
		t.Fatalf("failed Scan returned partial result: %#v", result)
	}
}

func TestScanRejectsMissingRequiredHierarchyArray(t *testing.T) {
	t.Parallel()

	client := fixtureClient(t, func(request *http.Request) (*http.Response, error) {
		return fixtureResponse(http.StatusOK, `{}`), nil
	})
	result, err := client.Scan(context.Background(), "token", connector.Workspace{ID: "w1", Name: "Test"})
	if err == nil || !strings.Contains(err.Error(), "missing required array") {
		t.Fatalf("Scan() error = %v, want missing-array failure", err)
	}
	if !reflect.DeepEqual(result, connector.ScanResult{}) {
		t.Fatalf("failed Scan returned partial result: %#v", result)
	}
}
