package save

import (
	"log"

	"github.com/campbellcharlie/lorg/internal/save/file"
	"github.com/campbellcharlie/lorg/internal/types"
)

type OptionsLogger struct {
	OutputFolder string
}

type Store interface {
	Save(data types.OutputData) error
}

type Logger struct {
	options    *OptionsLogger
	asyncqueue chan types.OutputData
	Store      []Store
}

// NewLogger instance
func NewLogger(options *OptionsLogger) *Logger {
	logger := &Logger{
		options:    options,
		asyncqueue: make(chan types.OutputData, 500),
	}

	store := file.New(&file.Options{
		OutputFolder: options.OutputFolder,
	})
	logger.Store = append(logger.Store, store)

	go logger.AsyncWrite()

	return logger
}

// AsyncWrite data
func (l *Logger) AsyncWrite() {
	for outputdata := range l.asyncqueue {
		for _, store := range l.Store {
			err := store.Save(outputdata)
			if err != nil {
				log.Printf("[Logger] Warning: Error while logging: %s", err)
			}
		}
	}
}

// Save logs request and user data
func (l *Logger) Save(folder string, userdata types.UserData) error {
	l.asyncqueue <- types.OutputData{Folder: folder, Userdata: userdata}
	return nil
}

// Close logger instance
func (l *Logger) Close() {
	close(l.asyncqueue)
}
