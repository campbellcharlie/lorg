package types

import (
	"time"
)

// SchemaField describes a single field in a collection schema.
// This replaces the PocketBase schema.SchemaField type.
type SchemaField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Options  any    `json:"options,omitempty"`
}

// Collection is a struct that holds information about a collection.
type Collection struct {
	ID             string        `json:"id,omitempty"`
	Name           string        `json:"name"`
	Type           string        `json:"type"`
	Schema         []SchemaField `json:"schema,omitempty"`
	System         bool          `json:"system,omitempty"`
	ListRule       string        `json:"listRule,omitempty"`
	ViewRule       string        `json:"viewRule,omitempty"`
	CreateRule     string        `json:"createRule,omitempty"`
	UpdateRule     string        `json:"updateRule,omitempty"`
	DeleteRule     string        `json:"deleteRule,omitempty"`
	ManageRule     string        `json:"manageRule,omitempty"`
	AllowOAuth2    bool          `json:"allowOAuth2Auth,omitempty"`
	AllowUsername  bool          `json:"allowUsernameAuth,omitempty"`
	AllowEmail     bool          `json:"allowEmailAuth,omitempty"`
	RequireEmail   bool          `json:"requireEmail,omitempty"`
	ExceptEmail    []string      `json:"exceptEmailDomains,omitempty"`
	OnlyEmail      []string      `json:"onlyEmailDomains,omitempty"`
	MinPasswordLen int           `json:"minPasswordLength,omitempty"`
	CreatedAt      time.Time     `json:"createdAt,omitempty"`
	UpdatedAt      time.Time     `json:"updatedAt,omitempty"`
}
