package schemas

import (
	"github.com/glitchedgitz/pocketbase/models/schema"
)

var Sessions = schema.NewSchema(
	&schema.SchemaField{
		Name:     "name",
		Type:     schema.FieldTypeText,
		Required: true,
	},
	&schema.SchemaField{
		Name: "cookies",
		Type: schema.FieldTypeJson,
		Options: &schema.JsonOptions{
			MaxSize: 100000,
		},
	},
	&schema.SchemaField{
		Name: "headers",
		Type: schema.FieldTypeJson,
		Options: &schema.JsonOptions{
			MaxSize: 100000,
		},
	},
	&schema.SchemaField{
		Name: "csrf_token",
		Type: schema.FieldTypeText,
	},
	&schema.SchemaField{
		Name: "csrf_field",
		Type: schema.FieldTypeText,
	},
	&schema.SchemaField{
		Name: "active",
		Type: schema.FieldTypeBool,
	},
)
