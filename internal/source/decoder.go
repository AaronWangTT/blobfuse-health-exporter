package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var (
	ErrNeedMore       = errors.New("source record is incomplete")
	ErrClosed         = errors.New("source generation is closed")
	ErrInvalidFraming = errors.New("invalid source framing")
	ErrRecordTooLarge = errors.New("source record exceeds size limit")
	ErrOffsetMismatch = errors.New("source append offset does not match decoder offset")
	ErrInvalidCommit  = errors.New("source record commit does not match pending record")
)

// Record is one complete top-level report object and its commit boundary.
type Record struct {
	Raw         json.RawMessage
	StartOffset int64
	EndOffset   int64
	Fingerprint [sha256.Size]byte
	ClosesArray bool
	token       uint64
}

// Decode decodes a record without converting JSON numbers through float64.
func (r Record) Decode(target any) error {
	decoder := json.NewDecoder(bytes.NewReader(r.Raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode source record: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode source record: %w", ErrInvalidFraming)
		}
		return fmt.Errorf("decode source record: %w", err)
	}

	return nil
}

// Decoder incrementally frames objects from one bfusemon report generation.
// Bytes are retained until the consumer commits the corresponding record.
type Decoder struct {
	maxRecordBytes int
	buffer         []byte
	committed      int64
	opened         bool
	closed         bool
	recordsSeen    bool
	pending        *Record
	nextToken      uint64
}

func NewDecoder(maxRecordBytes int) (*Decoder, error) {
	if maxRecordBytes <= 0 {
		return nil, fmt.Errorf("max record bytes must be positive")
	}

	return &Decoder{maxRecordBytes: maxRecordBytes}, nil
}

// Append adds bytes read from offset. Callers must provide each byte once and
// retain the decoder across appends for the lifetime of the file generation.
func (d *Decoder) Append(offset int64, data []byte) error {
	if offset != d.ReadOffset() {
		return fmt.Errorf("%w: got %d, want %d", ErrOffsetMismatch, offset, d.ReadOffset())
	}
	if d.closed {
		if !isWhitespace(data) {
			return fmt.Errorf("%w: data follows closing bracket", ErrInvalidFraming)
		}
		d.committed += int64(len(data))
		return nil
	}
	if d.pending != nil && d.pending.ClosesArray && !isWhitespace(data) {
		return fmt.Errorf("%w: data follows closing bracket", ErrInvalidFraming)
	}

	d.buffer = append(d.buffer, data...)
	return nil
}

func (d *Decoder) ReadOffset() int64 {
	return d.committed + int64(len(d.buffer))
}

func (d *Decoder) CommittedOffset() int64 {
	return d.committed
}

func (d *Decoder) Closed() bool {
	return d.closed
}

// Next returns the next complete record. ErrNeedMore means the active report
// can be retried after appending bytes. A returned record remains pending until
// Commit succeeds.
func (d *Decoder) Next() (Record, error) {
	if d.pending != nil {
		return cloneRecord(*d.pending), nil
	}
	if d.closed {
		return Record{}, ErrClosed
	}
	if !d.opened {
		if err := d.consumeOpeningBracket(); err != nil {
			return Record{}, err
		}
	}

	start := skipWhitespace(d.buffer, 0)
	if start == len(d.buffer) {
		return Record{}, ErrNeedMore
	}
	if d.buffer[start] == ']' {
		if d.recordsSeen {
			return Record{}, fmt.Errorf("%w: trailing comma before closing bracket", ErrInvalidFraming)
		}
		if !isWhitespace(d.buffer[start+1:]) {
			return Record{}, fmt.Errorf("%w: data follows closing bracket", ErrInvalidFraming)
		}
		d.consumeStructuralBytes(len(d.buffer))
		d.closed = true
		return Record{}, ErrClosed
	}
	if d.buffer[start] != '{' {
		return Record{}, fmt.Errorf("%w: expected report object", ErrInvalidFraming)
	}

	raw, objectEnd, err := d.decodeObject(start)
	if err != nil {
		return Record{}, err
	}

	delimiter := skipWhitespace(d.buffer, objectEnd)
	if delimiter == len(d.buffer) {
		return Record{}, ErrNeedMore
	}
	if d.buffer[delimiter] != ',' && d.buffer[delimiter] != ']' {
		return Record{}, fmt.Errorf("%w: expected object delimiter", ErrInvalidFraming)
	}
	if d.buffer[delimiter] == ']' && !isWhitespace(d.buffer[delimiter+1:]) {
		return Record{}, fmt.Errorf("%w: data follows closing bracket", ErrInvalidFraming)
	}

	d.nextToken++
	record := Record{
		Raw:         bytes.Clone(raw),
		StartOffset: d.committed + int64(start),
		EndOffset:   d.committed + int64(delimiter+1),
		Fingerprint: sha256.Sum256(raw),
		ClosesArray: d.buffer[delimiter] == ']',
		token:       d.nextToken,
	}
	d.pending = &record

	return cloneRecord(record), nil
}

// Commit advances the durable in-memory boundary after a record has been
// applied to metric state.
func (d *Decoder) Commit(record Record) error {
	if d.pending == nil || record.token != d.pending.token || record.EndOffset != d.pending.EndOffset {
		return ErrInvalidCommit
	}

	consumed := int(d.pending.EndOffset - d.committed)
	d.buffer = d.buffer[consumed:]
	d.committed = d.pending.EndOffset
	d.closed = d.pending.ClosesArray
	d.recordsSeen = true
	d.pending = nil
	if d.closed {
		d.consumeStructuralBytes(len(d.buffer))
	}
	return nil
}

func (d *Decoder) consumeOpeningBracket() error {
	start := skipWhitespace(d.buffer, 0)
	if start == len(d.buffer) {
		return ErrNeedMore
	}
	if d.buffer[start] != '[' {
		return fmt.Errorf("%w: expected opening bracket", ErrInvalidFraming)
	}

	d.consumeStructuralBytes(start + 1)
	d.opened = true
	return nil
}

func (d *Decoder) consumeStructuralBytes(count int) {
	d.buffer = d.buffer[count:]
	d.committed += int64(count)
}

func (d *Decoder) decodeObject(start int) (json.RawMessage, int, error) {
	decoder := json.NewDecoder(bytes.NewReader(d.buffer[start:]))
	decoder.UseNumber()

	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		if isIncomplete(err, len(d.buffer)-start) {
			if len(d.buffer)-start > d.maxRecordBytes {
				return nil, 0, ErrRecordTooLarge
			}
			return nil, 0, ErrNeedMore
		}
		return nil, 0, fmt.Errorf("%w: malformed report object", ErrInvalidFraming)
	}
	if len(raw) > d.maxRecordBytes {
		return nil, 0, ErrRecordTooLarge
	}

	return raw, start + int(decoder.InputOffset()), nil
}

func isIncomplete(err error, available int) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var syntaxError *json.SyntaxError
	return errors.As(err, &syntaxError) && syntaxError.Offset >= int64(available)
}

func skipWhitespace(data []byte, start int) int {
	for start < len(data) {
		switch data[start] {
		case ' ', '\t', '\r', '\n':
			start++
		default:
			return start
		}
	}
	return start
}

func isWhitespace(data []byte) bool {
	return skipWhitespace(data, 0) == len(data)
}

func cloneRecord(record Record) Record {
	record.Raw = bytes.Clone(record.Raw)
	return record
}
