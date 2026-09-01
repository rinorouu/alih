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

package clickup

import "alih/internal/connector"

// CapabilityContract declares only capabilities exercised by the current
// ClickUp implementation. It performs no network access and contains no API
// endpoint identities.
func (c *Client) CapabilityContract() connector.CapabilityContract {
	capabilities := []connector.Capability{
		capability(connector.CapabilityWorkspaceData, "Workspace hierarchy", connector.CapabilitySupported, "accessible Spaces, Folders, and Lists, including archived hierarchy"),
		capability(connector.CapabilityItems, "Tasks/subtasks", connector.CapabilitySupported, "active, closed, and archived home-List tasks and subtasks"),
		capability(connector.CapabilityComments, "Task comments", connector.CapabilitySupported, "paginated task comments and threaded replies"),
		capability(connector.CapabilityAttachmentMetadata, "Attachment metadata", connector.CapabilitySupported, "attachment identity and metadata observed on task details"),
		capability(connector.CapabilityAttachmentContent, "Attachment content", connector.CapabilitySupported, "bounded HTTPS retrieval during portable archive construction"),
		capability(connector.CapabilityCustomFields, "Custom fields", connector.CapabilitySupported, "accessible definitions across hierarchy levels and observed task values"),
		capability(connector.CapabilityRelationships, "Task relationships", connector.CapabilityPartial, "dependencies and task links only"),
		capability(connector.CapabilityRawEvidence, "Raw evidence", connector.CapabilitySupported, "exact successful response bytes and a sanitized request-attempt ledger"),
	}
	return connector.CapabilityContract{
		SchemaVersion: connector.CapabilitySchemaVersion,
		Connector:     c.Name(),
		Capabilities:  connector.CanonicalCapabilities(connector.CapabilitySchemaVersion, capabilities),
	}
}

func capability(id connector.CapabilityID, name string, implementation connector.CapabilityState, note string) connector.Capability {
	return connector.Capability{
		ID: id, Name: name, Requirement: connector.CapabilityRequired,
		Implementation: implementation, Availability: connector.CapabilityAvailabilityUnknown,
		State: implementation, Note: note,
	}
}

func (c *Client) scanCapabilities() ([]connector.Capability, error) {
	return connector.ObserveCapabilities(c.CapabilityContract(), map[connector.CapabilityID]connector.CapabilityAvailability{
		connector.CapabilityWorkspaceData:      connector.CapabilityAvailabilityAvailable,
		connector.CapabilityItems:              connector.CapabilityAvailabilityAvailable,
		connector.CapabilityComments:           connector.CapabilityAvailabilityAvailable,
		connector.CapabilityAttachmentMetadata: connector.CapabilityAvailabilityAvailable,
		connector.CapabilityCustomFields:       connector.CapabilityAvailabilityAvailable,
		connector.CapabilityRelationships:      connector.CapabilityAvailabilityAvailable,
	})
}

func (c *Client) extractionCapabilities() ([]connector.Capability, error) {
	return connector.ObserveCapabilities(c.CapabilityContract(), map[connector.CapabilityID]connector.CapabilityAvailability{
		connector.CapabilityWorkspaceData:      connector.CapabilityAvailabilityAvailable,
		connector.CapabilityItems:              connector.CapabilityAvailabilityAvailable,
		connector.CapabilityComments:           connector.CapabilityAvailabilityAvailable,
		connector.CapabilityAttachmentMetadata: connector.CapabilityAvailabilityAvailable,
		connector.CapabilityCustomFields:       connector.CapabilityAvailabilityAvailable,
		connector.CapabilityRelationships:      connector.CapabilityAvailabilityAvailable,
		connector.CapabilityRawEvidence:        connector.CapabilityAvailabilityAvailable,
	})
}

var _ connector.CapabilityProvider = (*Client)(nil)
