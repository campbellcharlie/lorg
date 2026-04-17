// Test utility for exercising the lorg SDK client.
// Usage: go run ./cmd/test/
package main

import (
	"encoding/json"
	"fmt"

	"github.com/campbellcharlie/lorg/internal/sdk"
	"github.com/campbellcharlie/lorg/internal/types"
)

func testPlaygroundAdd(lorgdb *sdk.Client, id string, playgroundAddData string) {
	fmt.Println("PlaygroundAddData: ", playgroundAddData)

	var playgroundAdd types.PlaygroundAdd
	err := json.Unmarshal([]byte(playgroundAddData), &playgroundAdd)
	if err != nil {
		fmt.Println("Error: ", err)
	}
	fmt.Println("playgroundAdd: ", playgroundAdd)
	pgData, err := lorgdb.PlaygroundAddChild(playgroundAdd)
	fmt.Println("Returned data: ", pgData)
	if err != nil {
		fmt.Println("Error: ", err)
	}
}

func main() {
	var lorgdb = sdk.NewClient(
		"http://127.0.0.1:8091",
		sdk.WithAdminEmailPassword("new@example.com", "1234567890"))

	// Create a new playground
	playgroundNewData := `{
		"name": "test pg",
		"type": "playground",
		"parent_id": "",
		"expanded":   true
	}`

	var playgroundNew types.PlaygroundNew
	json.Unmarshal([]byte(playgroundNewData), &playgroundNew)
	pgData, err := lorgdb.PlaygroundNew(playgroundNew)
	fmt.Println("Returned data: ", pgData)
	if err != nil {
		fmt.Println("Error: ", err)
	}

	id := pgData.(map[string]any)["id"].(string)
	fmt.Println("ID: ", id)

	playgroundAddData := `{
		"parent_id": "` + id + `",
		"items": [
			{
			"name": "test repeater",
			"type": "repeater",
			"tool_data": {
				"url": "http://example.com/test",
				"req": "GET /test HTTP/1.1\nHost: example.com",
				"resp": "HTTP/1.1 200 OK\nContent-Type: text/plain\n\nHello World",
				"method": "GET",
				"path": "/test",
				"headers": {
					"Host": "example.com"
					}
				}
			}
		]
	}`

	testPlaygroundAdd(lorgdb, id, playgroundAddData)

	playgroundAddData2 := `{
		"parent_id": "` + id + `",
		"items": [
		{
			"name": "test repeater",
			"type": "repeater",
			"tool_data": {
				"url": "http://example.com/test",
				"req": "GET /test HTTP/1.1\nHost: example.com",
				"resp": "HTTP/1.1 200 OK\nContent-Type: text/plain\n\nHello World",
				"method": "GET",
				"path": "/test",
				"headers": {
					"Host": "example.com"
					}
				}
			},
			{
			"name": "test note",
			"type": "note",
			"tool_data": {
				"content": "test note"
				}
			}
		]
	}`

	testPlaygroundAdd(lorgdb, id, playgroundAddData2)
}
