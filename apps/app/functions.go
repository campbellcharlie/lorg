package app

import (
	"log"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
)

func (backend *Backend) GetRecord(collectionName string, filter string) (*lorgdb.Record, error) {
	r, err := backend.DB.FindFirstRecord(collectionName, filter)
	return r, err
}

func (backend *Backend) SaveRecordToCollection(collectionName string, data map[string]any) (*lorgdb.Record, error) {

	log.Println("SaveRecordToCollection: ", collectionName, data)

	record := lorgdb.NewRecord(collectionName)

	for key, value := range data {
		record.Set(key, value)
	}

	err := backend.DB.SaveRecord(record)

	if err != nil {
		log.Printf("[SaveRecordToCollection] Error saving to %s: %v", collectionName, err)
		return nil, err
	}

	return record, nil
}
