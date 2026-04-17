package sdk

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// sseEvent represents a single Server-Sent Event.
type sseEvent struct {
	event string
	data  string
	id    string
}

func (e *sseEvent) Event() string { return e.event }
func (e *sseEvent) Data() string  { return e.data }
func (e *sseEvent) Id() string    { return e.id }

// sseDecoder reads SSE events from a stream.
type sseDecoder struct {
	scanner *bufio.Scanner
}

func newSSEDecoder(r io.Reader) *sseDecoder {
	return &sseDecoder{scanner: bufio.NewScanner(r)}
}

// Decode reads the next complete SSE event from the stream.
// An event is terminated by a blank line.
func (d *sseDecoder) Decode() (*sseEvent, error) {
	ev := &sseEvent{}
	var dataLines []string
	gotField := false

	for d.scanner.Scan() {
		line := d.scanner.Text()

		if line == "" {
			// Blank line = end of event
			if gotField {
				ev.data = strings.Join(dataLines, "\n")
				return ev, nil
			}
			continue
		}

		gotField = true

		if strings.HasPrefix(line, ":") {
			continue // comment
		}

		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			ev.event = value
		case "data":
			dataLines = append(dataLines, value)
		case "id":
			ev.id = value
		}
	}

	if err := d.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("sse: stream ended")
}
