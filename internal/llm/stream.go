package llm

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent represents a single Server-Sent Events event.
type sseEvent struct {
	Event string // from "event:" line
	Data  string // from "data:" line(s), joined with newline
}

// sseReader is a pull-based SSE parser.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{scanner: bufio.NewScanner(r)}
}

// Next returns the next SSE event, or io.EOF when the stream ends.
func (s *sseReader) Next() (*sseEvent, error) {
	var ev sseEvent
	var hasData bool

	for s.scanner.Scan() {
		line := s.scanner.Text()

		// Empty line = event boundary.
		if line == "" {
			if hasData {
				return &ev, nil
			}
			continue
		}

		// Comment lines start with ':'.
		if strings.HasPrefix(line, ":") {
			continue
		}

		if strings.HasPrefix(line, "event:") {
			ev.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if hasData {
				ev.Data += "\n" + value
			} else {
				ev.Data = value
				hasData = true
			}
		}
	}

	if err := s.scanner.Err(); err != nil {
		return nil, err
	}

	// If we accumulated data without a trailing empty line, yield it.
	if hasData {
		return &ev, nil
	}

	return nil, io.EOF
}
