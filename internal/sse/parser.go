package sse

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Event is a single Server-Sent Event from the Anthropic stream.
type Event struct {
	// Type is the value of the `event:` field. For Anthropic this is one
	// of message_start, message_delta, message_stop, content_block_start,
	// content_block_delta, content_block_stop, ping, error.
	Type string
	// RawData is the concatenated `data:` lines (joined with newlines if
	// multi-line). It is the raw JSON payload — call one of the AsXxx
	// helpers to decode.
	RawData []byte
}

// Parser yields Events from an SSE stream. It is not safe for concurrent
// use; use one Parser per stream.
type Parser struct {
	r *bufio.Reader
	// IncludePings, when true, surfaces `ping` events to callers. Default
	// false because nothing in the agent loop needs them.
	IncludePings bool
}

// NewParser wraps r in a Parser. r is read once-through; the parser does
// not buffer beyond what bufio.Reader keeps.
func NewParser(r io.Reader) *Parser {
	return &Parser{r: bufio.NewReader(r)}
}

// Next returns the next non-skipped event. Returns io.EOF when the stream
// ends cleanly. Returns *Error for `error` events surfaced by the API.
//
// Comments (lines starting with `:`), empty lines, and unknown fields are
// silently absorbed per the SSE spec.
func (p *Parser) Next() (Event, error) {
	for {
		ev, err := p.readEvent()
		if err != nil {
			return Event{}, err
		}
		if ev == nil {
			// EOF reached after partial input — treat as clean EOF if
			// nothing was buffered, otherwise the caller already got the
			// last event.
			return Event{}, io.EOF
		}
		if ev.Type == "error" {
			return Event{}, decodeAPIError(ev.RawData)
		}
		if ev.Type == "ping" && !p.IncludePings {
			continue
		}
		return *ev, nil
	}
}

// readEvent reads one event block from the stream. Returns (nil, nil) on
// clean EOF, (nil, err) on error, (ev, nil) on a parsed event.
func (p *Parser) readEvent() (*Event, error) {
	var (
		typ      string
		data     strings.Builder
		hasData  bool
		hasField bool
	)

	for {
		line, err := p.r.ReadString('\n')
		if errors.Is(err, io.EOF) {
			if line == "" && !hasField {
				return nil, nil
			}
			// Treat the trailing partial event as if a blank line follows.
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if !hasField {
					return nil, nil
				}
				return assemble(typ, data, hasData), nil
			}
			// Process the unterminated line then synthesize a blank.
			parseLine(line, &typ, &data, &hasData, &hasField)
			return assemble(typ, data, hasData), nil
		}
		if err != nil {
			return nil, fmt.Errorf("sse: read: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		// Blank line terminates the event.
		if line == "" {
			if !hasField {
				// Skip stray blank lines between events.
				continue
			}
			return assemble(typ, data, hasData), nil
		}
		parseLine(line, &typ, &data, &hasData, &hasField)
	}
}

// parseLine consumes one non-blank line, updating the in-progress event
// fields. Per the SSE spec, lines that begin with `:` are comments.
func parseLine(line string, typ *string, data *strings.Builder, hasData, hasField *bool) {
	if strings.HasPrefix(line, ":") {
		return
	}
	// SSE field syntax: `<field>` (treat whole line as field with empty
	// value), `<field>: <value>`, or `<field>:<value>`.
	idx := strings.IndexByte(line, ':')
	var field, value string
	switch {
	case idx == -1:
		field = line
	case idx == len(line)-1:
		field = line[:idx]
	default:
		field = line[:idx]
		value = line[idx+1:]
		value = strings.TrimPrefix(value, " ")
	}
	*hasField = true
	switch field {
	case "event":
		*typ = value
	case "data":
		if *hasData {
			data.WriteByte('\n')
		}
		data.WriteString(value)
		*hasData = true
	default:
		// id, retry, or unknown — ignore for our use case.
	}
}

func assemble(typ string, data strings.Builder, hasData bool) *Event {
	ev := &Event{Type: typ}
	if hasData {
		ev.RawData = []byte(strings.TrimRight(data.String(), " \t"))
	}
	return ev
}

// Error is the typed error decoded from an SSE `error` event.
type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("sse: %s: %s", e.Type, e.Message)
}

func decodeAPIError(data []byte) error {
	var env struct {
		Type  string `json:"type"`
		Error Error  `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("sse: decode error event: %w", err)
	}
	if env.Error.Type == "" && env.Type != "" {
		// Some error variants put fields at the top level.
		return &Error{Type: env.Type, Message: string(data)}
	}
	return &env.Error
}
