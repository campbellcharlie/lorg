package schemas

import (
	"github.com/glitchedgitz/pocketbase/models/schema"
)

var MCPTemplates = schema.NewSchema(
	&schema.SchemaField{
		Name:     "name",
		Type:     schema.FieldTypeText,
		Required: true,
	},
	&schema.SchemaField{
		Name: "tls",
		Type: schema.FieldTypeBool,
	},
	&schema.SchemaField{
		Name: "host",
		Type: schema.FieldTypeText,
	},
	&schema.SchemaField{
		Name: "port",
		Type: schema.FieldTypeNumber,
	},
	&schema.SchemaField{
		Name: "http_version",
		Type: schema.FieldTypeNumber,
	},
	// Raw HTTP request template with ${VAR} placeholders
	&schema.SchemaField{
		Name: "request_template",
		Type: schema.FieldTypeText,
	},
	// Variable definitions and defaults (JSON object)
	&schema.SchemaField{
		Name: "variables",
		Type: schema.FieldTypeJson,
		Options: &schema.JsonOptions{
			MaxSize: 100000,
		},
	},
	&schema.SchemaField{
		Name: "description",
		Type: schema.FieldTypeText,
	},
	&schema.SchemaField{
		Name: "inject_session",
		Type: schema.FieldTypeBool,
	},
	&schema.SchemaField{
		Name: "json_escape_vars",
		Type: schema.FieldTypeBool,
	},
	&schema.SchemaField{
		Name: "extract_regex",
		Type: schema.FieldTypeText,
	},
	&schema.SchemaField{
		Name: "extract_group",
		Type: schema.FieldTypeNumber,
	},
)
