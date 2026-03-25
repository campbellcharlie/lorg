package migrations

import (
	"log"

	"github.com/campbellcharlie/lorg/internal/schemas"
	"github.com/glitchedgitz/pocketbase/daos"
	m "github.com/glitchedgitz/pocketbase/migrations"
	"github.com/glitchedgitz/pocketbase/models"
	pbTypes "github.com/glitchedgitz/pocketbase/tools/types"
	"github.com/pocketbase/dbx"
)

func init() {
	m.Register(func(db dbx.Builder) error {
		dao := daos.New(db)

		// Create _sessions collection
		sessions := &models.Collection{
			Name:       "_sessions",
			Type:       models.CollectionTypeBase,
			ListRule:   pbTypes.Pointer(""),
			ViewRule:   pbTypes.Pointer(""),
			CreateRule: pbTypes.Pointer(""),
			UpdateRule: pbTypes.Pointer(""),
			DeleteRule: pbTypes.Pointer(""),
			Schema:     schemas.Sessions,
		}
		if err := dao.SaveCollection(sessions); err != nil {
			log.Println("[migration][sessions] Error creating collection: ", err)
			return err
		}

		// Create unique index on session name
		if _, err := db.NewQuery("CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_name ON _sessions (name)").Execute(); err != nil {
			log.Println("[migration][sessions] Error creating index: ", err)
		}

		log.Println("[migration][sessions] Successfully created _sessions collection")

		// Create _mcp_templates collection
		templates := &models.Collection{
			Name:       "_mcp_templates",
			Type:       models.CollectionTypeBase,
			ListRule:   pbTypes.Pointer(""),
			ViewRule:   pbTypes.Pointer(""),
			CreateRule: pbTypes.Pointer(""),
			UpdateRule: pbTypes.Pointer(""),
			DeleteRule: pbTypes.Pointer(""),
			Schema:     schemas.MCPTemplates,
		}
		if err := dao.SaveCollection(templates); err != nil {
			log.Println("[migration][templates] Error creating collection: ", err)
			return err
		}

		// Create unique index on template name
		if _, err := db.NewQuery("CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_templates_name ON _mcp_templates (name)").Execute(); err != nil {
			log.Println("[migration][templates] Error creating index: ", err)
		}

		log.Println("[migration][templates] Successfully created _mcp_templates collection")
		return nil
	}, func(db dbx.Builder) error {
		dao := daos.New(db)

		// Rollback: delete both collections
		if col, err := dao.FindCollectionByNameOrId("_mcp_templates"); err == nil {
			dao.DeleteCollection(col)
		}
		if col, err := dao.FindCollectionByNameOrId("_sessions"); err == nil {
			dao.DeleteCollection(col)
		}

		return nil
	})
}
