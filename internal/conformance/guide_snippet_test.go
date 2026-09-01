package conformance

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// TestGuideSnippetCompilesAndWorks compiles the fake-transport snippet printed
// in docs/connectors.md and drives the real pipeline contract with it, so the
// guide cannot show an example that does not work.
type guideRoundTripFunc func(*http.Request) (*http.Response, error)

func (f guideRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGuideSnippetCompilesAndWorks(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: guideRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader([]byte(fakeAttachment))), Request: r,
		}, nil
	})}

	subject := referenceSubject()
	subject.HTTPClient = client
	AssertArchivePipeline(t, subject)
}
