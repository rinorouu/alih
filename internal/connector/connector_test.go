package connector

import "testing"

type stubConnector struct{}

func (stubConnector) Name() string { return "stub" }

func TestConnectorContract(t *testing.T) {
	t.Parallel()

	var source Connector = stubConnector{}
	if source.Name() != "stub" {
		t.Fatalf("Name() = %q, want %q", source.Name(), "stub")
	}
}
