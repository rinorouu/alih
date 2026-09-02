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

import "alih/internal/connector"

// CapabilityContract declares what this adapter implements, independently of
// what any single run observed. It performs no network access.
//
// The declaration is deliberately narrower than Notion's API, and lists only
// what this version actually establishes. Capabilities this connector does not
// implement -- comments, files, relations, history, permissions -- are absent
// rather than declared unsupported: Core reconciles an archive against every
// declared capability, so declaring one that is never attempted would make
// every run look degraded and would advertise a surface that does not exist.
// What this version does not cover is recorded as an archive limitation
// instead, where a person reading the archive will find it.
func (c *Client) CapabilityContract() connector.CapabilityContract {
	capabilities := []connector.Capability{
		required(connector.CapabilityWorkspaceData, "Shared workspace content", connector.CapabilityPartial,
			"only pages and data sources explicitly connected to this integration are visible; Notion exposes no way to prove the rest"),
		required(connector.CapabilityItems, "Pages and blocks", connector.CapabilitySupported,
			"database rows and the block tree beneath each page, traversed to exhaustion"),
		required(connector.CapabilityCustomFields, "Data source properties", connector.CapabilitySupported,
			"property schema per data source and observed property values per page"),
		required(connector.CapabilityRawEvidence, "Raw evidence", connector.CapabilitySupported,
			"exact successful response bytes and a sanitized request-attempt ledger"),
	}
	return connector.CapabilityContract{
		SchemaVersion: connector.CapabilitySchemaVersion,
		Connector:     c.Name(),
		Capabilities:  connector.CanonicalCapabilities(connector.CapabilitySchemaVersion, capabilities),
	}
}

func required(id connector.CapabilityID, name string, implementation connector.CapabilityState, note string) connector.Capability {
	return connector.Capability{
		ID: id, Name: name, Requirement: connector.CapabilityRequired,
		Implementation: implementation, Availability: connector.CapabilityAvailabilityUnknown,
		State: implementation, Note: note,
	}
}

// observedCapabilities records what a completed traversal actually established.
// A capability this adapter does not implement stays UNAVAILABLE rather than
// being quietly reported as available.
func (c *Client) observedCapabilities(withEvidence bool) ([]connector.Capability, error) {
	observations := map[connector.CapabilityID]connector.CapabilityAvailability{
		connector.CapabilityWorkspaceData: connector.CapabilityAvailabilityAvailable,
		connector.CapabilityItems:         connector.CapabilityAvailabilityAvailable,
		connector.CapabilityCustomFields:  connector.CapabilityAvailabilityAvailable,
	}
	if withEvidence {
		observations[connector.CapabilityRawEvidence] = connector.CapabilityAvailabilityAvailable
	}
	return connector.ObserveCapabilities(c.CapabilityContract(), observations)
}
