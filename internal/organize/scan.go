// Copyright 2025 rinorouu
// Licensed under the Apache License, Version 2.0.

package organize

import (
	"database/sql"
	"fmt"
)

type scanner interface{ Scan(...any) error }

func scanHierarchy(row scanner, _ bool) (hierarchy, error) {
	var result hierarchy
	var parent namedEntity
	var parentID, parentKind, parentName, parentParentID sql.NullString
	var parentProvider, parentType, parentSourceID, parentRawPath sql.NullString
	err := row.Scan(
		&result.Container.ID, &result.Container.Kind, &result.Container.Name, &result.Container.ParentID,
		&result.Container.Source.Provider, &result.Container.Source.Type, &result.Container.Source.ID, &result.Container.Source.RawPath,
		&parentID, &parentKind, &parentName, &parentParentID, &parentProvider, &parentType, &parentSourceID, &parentRawPath,
	)
	if err != nil {
		return hierarchy{}, fmt.Errorf("scan container hierarchy: %w", err)
	}
	if parentID.Valid {
		parent.ID, parent.Kind = parentID.String, parentKind.String
		parent.Name = nullStringPointer(parentName)
		parent.ParentID = nullStringPointer(parentParentID)
		parent.Source = sourceRef{Provider: parentProvider.String, Type: parentType.String, ID: parentSourceID.String, RawPath: parentRawPath.String}
		result.ParentContainer = &parent
	}
	return result, nil
}

func scanCollectionHierarchy(row scanner) (hierarchy, error) {
	var collection namedEntity
	var container namedEntity
	var parent namedEntity
	var parentID, parentKind, parentName, parentParentID sql.NullString
	var parentProvider, parentType, parentSourceID, parentRawPath sql.NullString
	err := row.Scan(
		&collection.ID, &collection.Name, &collection.Source.Provider, &collection.Source.Type, &collection.Source.ID, &collection.Source.RawPath,
		&container.ID, &container.Kind, &container.Name, &container.ParentID, &container.Source.Provider, &container.Source.Type, &container.Source.ID, &container.Source.RawPath,
		&parentID, &parentKind, &parentName, &parentParentID, &parentProvider, &parentType, &parentSourceID, &parentRawPath,
	)
	if err != nil {
		return hierarchy{}, fmt.Errorf("scan collection hierarchy: %w", err)
	}
	result := hierarchy{Container: container, Collection: &collection}
	if parentID.Valid {
		parent.ID, parent.Kind, parent.Name, parent.ParentID = parentID.String, parentKind.String, nullStringPointer(parentName), nullStringPointer(parentParentID)
		parent.Source = sourceRef{Provider: parentProvider.String, Type: parentType.String, ID: parentSourceID.String, RawPath: parentRawPath.String}
		result.ParentContainer = &parent
	}
	return result, nil
}

func scanRecordHierarchy(row scanner) (record, hierarchy, error) {
	var item record
	var archived sql.NullBool
	var collection namedEntity
	var container namedEntity
	var parent namedEntity
	var parentID, parentKind, parentName, parentParentID sql.NullString
	var parentProvider, parentType, parentSourceID, parentRawPath sql.NullString
	err := row.Scan(
		&item.ID, &item.Kind, &item.WorkspaceID, &item.CollectionID, &item.ParentRecordID, &item.Title, &item.Description, &item.TextContent,
		&item.Status, &item.StatusType, &item.Priority, &archived, &item.Created, &item.Updated, &item.Closed, &item.Done, &item.Start, &item.Due,
		&item.EstimateMS, &item.SpentMS, &item.Points, &item.Source.Provider, &item.Source.Type, &item.Source.ID, &item.Source.RawPath,
		&collection.ID, &collection.Name, &collection.Source.Provider, &collection.Source.Type, &collection.Source.ID, &collection.Source.RawPath,
		&container.ID, &container.Kind, &container.Name, &container.ParentID, &container.Source.Provider, &container.Source.Type, &container.Source.ID, &container.Source.RawPath,
		&parentID, &parentKind, &parentName, &parentParentID, &parentProvider, &parentType, &parentSourceID, &parentRawPath,
	)
	if err != nil {
		return record{}, hierarchy{}, fmt.Errorf("scan record hierarchy: %w", err)
	}
	if archived.Valid {
		item.Archived = &archived.Bool
	}
	result := hierarchy{Container: container, Collection: &collection}
	if parentID.Valid {
		parent.ID, parent.Kind, parent.Name, parent.ParentID = parentID.String, parentKind.String, nullStringPointer(parentName), nullStringPointer(parentParentID)
		parent.Source = sourceRef{Provider: parentProvider.String, Type: parentType.String, ID: parentSourceID.String, RawPath: parentRawPath.String}
		result.ParentContainer = &parent
	}
	return item, result, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
