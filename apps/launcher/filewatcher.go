package launcher

import (
	"encoding/json"
	"log"
	"os"

	"github.com/fsnotify/fsnotify"
	"github.com/labstack/echo/v4"
)

func (launcher *Launcher) FileWatcher(e *echo.Echo) {
	e.GET("/api/filewatcher", func(c echo.Context) error {

		settingsFilePath := os.Getenv("LORG_TEMPLATE_DIR")
		// If LORG_TEMPLATE_DIR isn't configured, skip file watching instead of crashing.
		if settingsFilePath == "" {
			return c.NoContent(204)
		}

		c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
		c.Response().Header().Set(echo.HeaderCacheControl, "no-cache")
		c.Response().Header().Set(echo.HeaderConnection, "keep-alive")

		updateChan := make(chan fsnotify.Event)

		go func() {
			watcher, err := fsnotify.NewWatcher()
			if err != nil {
				log.Fatal(err)
			}
			defer watcher.Close()

			// Create a channel to send updates
			if err := watcher.Add(settingsFilePath); err != nil {
				log.Printf("filewatcher: failed to watch %q: %v", settingsFilePath, err)
				close(updateChan)
				return
			}
			for {
				select {
				case event := <-watcher.Events:
					log.Println("New File Watcher Event:", event)
					updateChan <- event
				case <-c.Request().Context().Done():
					close(updateChan)
					return
				}
			}
		}()

		for newSettings := range updateChan {
			data, err := json.Marshal(newSettings)
			if err != nil {
				log.Printf("Failed to marshal settings: %v", err)
				continue
			}
			c.Response().Write([]byte("data: " + string(data) + "\n\n"))
			c.Response().Flush()
		}

		return nil
	})
}
