// Package types provides shared domain types used across the platform.
package types

import (
	"time"
)

// ID is a typed identifier for domain entities.
type ID string

// String returns the string representation of the ID.
func (id ID) String() string { return string(id) }

// IsEmpty returns true if the ID is empty.
func (id ID) IsEmpty() bool { return id == "" }

// Page represents a paginated result set.
type Page[T any] struct {
	// NextCursor is the opaque cursor for fetching the next page, if available.
	NextCursor string `json:"next_cursor,omitempty"`

	// Items is the slice of results for the current page.
	Items []T `json:"items"`

	// Total is the total number of items across all pages.
	Total int64 `json:"total"`

	// PageSize is the maximum number of items per page.
	PageSize int `json:"page_size"`

	// PageNumber is the current page number (1-based).
	PageNumber int `json:"page_number"`

	// TotalIsEstimate is true when Total is a planner estimate (the bounded count hit its
	// cap) rather than an exact count. It is false for an exact count and for a
	// keyset page (which carries no Total — NextCursor is its has-more signal).
	TotalIsEstimate bool `json:"total_is_estimate,omitempty"`
}

// HasMore returns true if there are more pages available. A non-empty NextCursor is the
// authoritative signal (both the offset and keyset pagers set it iff further rows exist),
// so it is checked first — a keyset page carries no Total, and an offset page built with
// Count:false has Total==0, yet either can still have a next page. The Total comparison
// is the fallback for an offset page that computed a total but (defensively) left
// NextCursor empty.
func (p Page[T]) HasMore() bool {
	return p.NextCursor != "" || int64(p.PageNumber*p.PageSize) < p.Total
}

// PageRequest specifies pagination parameters.
type PageRequest struct {
	// Cursor is an opaque token for cursor-based pagination.
	Cursor string `json:"cursor,omitempty"`

	// PageSize is the maximum number of items to return per page.
	PageSize int `json:"page_size"`

	// PageNumber is the requested page number (1-based).
	PageNumber int `json:"page_number"`
}

// ListRequest is a container holding a [types.PageRequest] and a set of parameters for a
// list request.
type ListRequest[T any] struct {
	// Params holds list parameters specific to the target resource.
	Params T

	// PageRequest holds pagination parameters for the list request.
	PageRequest PageRequest
}

// Timestamps holds common timestamp fields for entities.
type Timestamps struct {
	// CreatedAt is the time the entity was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the time the entity was last updated.
	UpdatedAt time.Time `json:"updated_at"`

	// DeletedAt is the time the entity was soft-deleted, or nil if active.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// SoftDelete marks the entity as deleted.
func (t *Timestamps) SoftDelete() {
	now := time.Now()
	t.DeletedAt = &now
}

// IsDeleted returns true if the entity has been soft-deleted.
func (t *Timestamps) IsDeleted() bool {
	return t.DeletedAt != nil
}
