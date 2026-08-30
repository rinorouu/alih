package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"alih/internal/connector"
)

// EvidenceResponse is one validated successful response from a completed M3
// snapshot. Body is loaded byte-for-byte and is never rewritten by the reader.
type EvidenceResponse struct {
	Sequence   int
	Operation  string
	Method     string
	Path       string
	Query      string
	Attempt    int
	StatusCode int
	RawPath    string
	SHA256     string
	Body       []byte
}

// Evidence contains the validated M3 control metadata needed by an M4 source
// adapter. It is input evidence, not a portable model.
type Evidence struct {
	RootPath  string
	Connector string
	Workspace connector.Workspace
	// ExtractedBy is the authenticated account that produced the snapshot. It
	// is empty for snapshots written before Alih recorded it.
	ExtractedBy   connector.Identity
	StartedAt     time.Time
	FinishedAt    time.Time
	LogicalDigest string
	Inventory     connector.Inventory
	Capabilities  []connector.Capability
	SourceObjects []connector.SourceObject
	Responses     []EvidenceResponse
}

// LoadComplete validates the M3 control files and every successful raw-response
// checksum before normalization. This is an input safety check, not M5 archive
// verification.
func LoadComplete(rootPath string) (Evidence, error) {
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return Evidence{}, fmt.Errorf("resolve raw snapshot path: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return Evidence{}, fmt.Errorf("inspect raw snapshot: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Evidence{}, errors.New("raw snapshot path must be a real directory")
	}

	var run runRecord
	if err := readJSONFile(filepath.Join(absolute, "run.json"), &run); err != nil {
		return Evidence{}, fmt.Errorf("read raw snapshot run record: %w", err)
	}
	if run.SchemaVersion != schemaVersion || run.Kind != "raw_extraction_run" {
		return Evidence{}, errors.New("unsupported raw snapshot run schema")
	}
	if run.Status != "COMPLETE" {
		return Evidence{}, fmt.Errorf("raw snapshot status is %q, not COMPLETE", run.Status)
	}
	if strings.TrimSpace(run.Connector) == "" || strings.TrimSpace(run.Source.WorkspaceID) == "" {
		return Evidence{}, errors.New("raw snapshot run record is missing source identity")
	}

	var inventory inventoryRecord
	if err := readJSONFile(filepath.Join(absolute, "inventory.json"), &inventory); err != nil {
		return Evidence{}, fmt.Errorf("read raw snapshot inventory: %w", err)
	}
	if inventory.SchemaVersion != schemaVersion || inventory.Kind != "raw_source_inventory" {
		return Evidence{}, errors.New("unsupported raw snapshot inventory schema")
	}
	if inventory.Connector != run.Connector || inventory.WorkspaceID != run.Source.WorkspaceID {
		return Evidence{}, errors.New("raw snapshot source identity is inconsistent")
	}
	objects := append([]connector.SourceObject(nil), inventory.SourceObjects...)
	sortSourceObjects(objects)
	if err := validateSourceObjects(objects); err != nil {
		return Evidence{}, fmt.Errorf("validate raw source index: %w", err)
	}
	digest, err := logicalDigest(inventory.Connector, inventory.WorkspaceID, inventory.Counts, objects)
	if err != nil {
		return Evidence{}, err
	}
	if digest != inventory.LogicalDigest || digest != run.LogicalDigest {
		return Evidence{}, errors.New("raw snapshot logical inventory digest mismatch")
	}

	var requests []requestRecord
	if err := readJSONFile(filepath.Join(absolute, "requests.json"), &requests); err != nil {
		return Evidence{}, fmt.Errorf("read raw snapshot request ledger: %w", err)
	}
	responses := make([]EvidenceResponse, 0, run.Requests.SuccessfulResponses)
	failedAttempts := 0
	retriedAttempts := 0
	for index, request := range requests {
		if request.Sequence != index+1 {
			return Evidence{}, fmt.Errorf("request ledger sequence %d is not contiguous", request.Sequence)
		}
		switch request.Outcome {
		case "SUCCESS":
			body, err := readRawBody(absolute, request)
			if err != nil {
				return Evidence{}, fmt.Errorf("request %d raw evidence: %w", request.Sequence, err)
			}
			responses = append(responses, EvidenceResponse{
				Sequence: request.Sequence, Operation: request.Operation, Method: request.Method,
				Path: request.Path, Query: request.Query, Attempt: request.Attempt,
				StatusCode: request.StatusCode, RawPath: request.RawPath, SHA256: request.SHA256, Body: body,
			})
		case "FAILED_ATTEMPT":
			failedAttempts++
			if request.Retrying {
				retriedAttempts++
			}
		default:
			return Evidence{}, fmt.Errorf("complete snapshot contains unresolved request outcome %q", request.Outcome)
		}
	}
	if len(requests) != run.Requests.Attempts || len(responses) != run.Requests.SuccessfulResponses ||
		failedAttempts != run.Requests.FailedAttempts || retriedAttempts != run.Requests.RetriedAttempts {
		return Evidence{}, errors.New("raw snapshot request summary does not match request ledger")
	}

	var extractedBy connector.Identity
	if run.ExtractedBy != nil {
		extractedBy = connector.Identity{ID: run.ExtractedBy.ID, Name: run.ExtractedBy.Name}
	}
	return Evidence{
		RootPath: absolute, Connector: run.Connector,
		Workspace:   connector.Workspace{ID: run.Source.WorkspaceID, Name: run.Source.WorkspaceName},
		ExtractedBy: extractedBy,
		StartedAt:   run.StartedAt, FinishedAt: run.FinishedAt, LogicalDigest: digest,
		Inventory: inventory.Counts, Capabilities: append([]connector.Capability(nil), inventory.Capabilities...),
		SourceObjects: objects, Responses: responses,
	}, nil
}

func readRawBody(root string, request requestRecord) ([]byte, error) {
	if request.RawPath == "" || filepath.IsAbs(request.RawPath) {
		return nil, errors.New("missing or absolute raw path")
	}
	clean := filepath.Clean(filepath.FromSlash(request.RawPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("raw path escapes snapshot root")
	}
	absolute := filepath.Join(root, clean)
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("raw evidence path is not a regular file")
	}
	body, err := os.ReadFile(absolute)
	if err != nil {
		return nil, err
	}
	if len(body) != request.Bytes {
		return nil, fmt.Errorf("byte count %d does not match ledger %d", len(body), request.Bytes)
	}
	digest := sha256.Sum256(body)
	actual := "sha256:" + hex.EncodeToString(digest[:])
	if actual != request.SHA256 {
		return nil, errors.New("checksum does not match request ledger")
	}
	return body, nil
}

func readJSONFile(path string, destination any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("control path is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, destination); err != nil {
		return err
	}
	return nil
}

// SortedResponses returns response metadata in request-ledger order.
func (evidence Evidence) SortedResponses() []EvidenceResponse {
	responses := append([]EvidenceResponse(nil), evidence.Responses...)
	sort.Slice(responses, func(i, j int) bool { return responses[i].Sequence < responses[j].Sequence })
	return responses
}
