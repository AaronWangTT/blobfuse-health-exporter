package source_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/AaronWangTT/blobfuse-health-exporter/internal/source"
)

func TestDecoderWaitsForCompleteRecordAndDelimiter(t *testing.T) {
	decoder := newDecoder(t, 1024)
	appendAtReadOffset(t, decoder, []byte("["))

	if _, err := decoder.Next(); !errors.Is(err, source.ErrNeedMore) {
		t.Fatalf("Next() error = %v, want ErrNeedMore", err)
	}
	if got := decoder.CommittedOffset(); got != 1 {
		t.Fatalf("CommittedOffset() = %d, want 1", got)
	}

	appendAtReadOffset(t, decoder, []byte(`{"value":9007199254740992`))
	if _, err := decoder.Next(); !errors.Is(err, source.ErrNeedMore) {
		t.Fatalf("Next() error = %v, want ErrNeedMore for partial object", err)
	}

	appendAtReadOffset(t, decoder, []byte("},\n"))
	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if got := decoder.CommittedOffset(); got != 1 {
		t.Fatalf("CommittedOffset() before Commit = %d, want 1", got)
	}

	var value struct {
		Value json.Number `json:"value"`
	}
	if err := record.Decode(&value); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got := value.Value.String(); got != "9007199254740992" {
		t.Fatalf("decoded number = %q, want exact lexical value", got)
	}

	if err := decoder.Commit(record); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if got := decoder.CommittedOffset(); got != record.EndOffset {
		t.Fatalf("CommittedOffset() = %d, want %d", got, record.EndOffset)
	}
	if _, err := decoder.Next(); !errors.Is(err, source.ErrNeedMore) {
		t.Fatalf("Next() error = %v, want ErrNeedMore after trailing comma", err)
	}
}

func TestDecoderRetainsPendingRecordUntilCommit(t *testing.T) {
	decoder := newDecoder(t, 1024)
	appendAtReadOffset(t, decoder, []byte(`[{"value":1},{"value":2}]`))

	first, err := decoder.Next()
	if err != nil {
		t.Fatalf("first Next() error = %v", err)
	}
	again, err := decoder.Next()
	if err != nil {
		t.Fatalf("second Next() error = %v", err)
	}
	if first.EndOffset != again.EndOffset || first.Fingerprint != again.Fingerprint {
		t.Fatal("Next() did not return the pending record")
	}

	if err := decoder.Commit(first); err != nil {
		t.Fatalf("first Commit() error = %v", err)
	}
	second, err := decoder.Next()
	if err != nil {
		t.Fatalf("third Next() error = %v", err)
	}
	if !second.ClosesArray {
		t.Fatal("last record does not close the array")
	}
	if err := decoder.Commit(second); err != nil {
		t.Fatalf("second Commit() error = %v", err)
	}
	if !decoder.Closed() {
		t.Fatal("decoder is not closed")
	}
	if _, err := decoder.Next(); !errors.Is(err, source.ErrClosed) {
		t.Fatalf("Next() error = %v, want ErrClosed", err)
	}
}

func TestDecoderHandlesJSONSyntaxInsideStrings(t *testing.T) {
	decoder := newDecoder(t, 1024)
	appendAtReadOffset(t, decoder, []byte("[{\"path\":\"a}b{c\\\"d\\\\e\\nf\"}]"))

	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if err := decoder.Commit(record); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

func TestDecoderRejectsOversizedIncompleteRecord(t *testing.T) {
	decoder := newDecoder(t, 8)
	appendAtReadOffset(t, decoder, []byte(`[{"value":`))

	if _, err := decoder.Next(); !errors.Is(err, source.ErrRecordTooLarge) {
		t.Fatalf("Next() error = %v, want ErrRecordTooLarge", err)
	}
}

func TestDecoderRejectsInvalidFraming(t *testing.T) {
	decoder := newDecoder(t, 1024)
	appendAtReadOffset(t, decoder, []byte(`{"value":1}`))

	if _, err := decoder.Next(); !errors.Is(err, source.ErrInvalidFraming) {
		t.Fatalf("Next() error = %v, want ErrInvalidFraming", err)
	}
}

func TestDecoderRejectsTrailingCommaBeforeClose(t *testing.T) {
	decoder := newDecoder(t, 1024)
	appendAtReadOffset(t, decoder, []byte(`[{"value":1},]`))

	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if err := decoder.Commit(record); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := decoder.Next(); !errors.Is(err, source.ErrInvalidFraming) {
		t.Fatalf("Next() error = %v, want ErrInvalidFraming", err)
	}
}

func TestDecoderRejectsDataAfterClose(t *testing.T) {
	decoder := newDecoder(t, 1024)
	appendAtReadOffset(t, decoder, []byte(`[{"value":1}]x`))

	if _, err := decoder.Next(); !errors.Is(err, source.ErrInvalidFraming) {
		t.Fatalf("Next() error = %v, want ErrInvalidFraming", err)
	}
}

func TestDecoderCommitsWhitespaceAfterClose(t *testing.T) {
	decoder := newDecoder(t, 1024)
	data := []byte("[{\"value\":1}] \n")
	appendAtReadOffset(t, decoder, data)

	record, err := decoder.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if err := decoder.Commit(record); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if got := decoder.CommittedOffset(); got != int64(len(data)) {
		t.Fatalf("CommittedOffset() = %d, want %d", got, len(data))
	}
}

func TestDecoderRejectsMismatchedAppendOffset(t *testing.T) {
	decoder := newDecoder(t, 1024)
	if err := decoder.Append(1, []byte("[")); !errors.Is(err, source.ErrOffsetMismatch) {
		t.Fatalf("Append() error = %v, want ErrOffsetMismatch", err)
	}
}

func TestDecoderClosesEmptyArray(t *testing.T) {
	decoder := newDecoder(t, 1024)
	appendAtReadOffset(t, decoder, []byte("[\n]"))

	if _, err := decoder.Next(); !errors.Is(err, source.ErrClosed) {
		t.Fatalf("Next() error = %v, want ErrClosed", err)
	}
	if !decoder.Closed() {
		t.Fatal("decoder is not closed")
	}
	if got := decoder.CommittedOffset(); got != 3 {
		t.Fatalf("CommittedOffset() = %d, want 3", got)
	}
}

func newDecoder(t *testing.T, maxRecordBytes int) *source.Decoder {
	t.Helper()
	decoder, err := source.NewDecoder(maxRecordBytes)
	if err != nil {
		t.Fatalf("NewDecoder() error = %v", err)
	}
	return decoder
}

func appendAtReadOffset(t *testing.T, decoder *source.Decoder, data []byte) {
	t.Helper()
	if err := decoder.Append(decoder.ReadOffset(), data); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
}
