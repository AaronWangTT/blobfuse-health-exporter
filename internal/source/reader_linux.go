//go:build linux

package source

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var ErrGenerationTruncated = errors.New("report generation was truncated")

const generationReadBufferBytes = 64 * 1024

// GenerationWatermark is the last record boundary applied by one reader.
type GenerationWatermark struct {
	Generation      GenerationID
	CommittedOffset int64
	LastFingerprint [sha256.Size]byte
	HasFingerprint  bool
	Closed          bool
}

type drainResult struct {
	records int
	bytes   int
	closed  bool
}

// generationReader retains decoder state and the opened descriptor for one
// report generation across appends and path rotation.
type generationReader struct {
	report             *ReportFile
	decoder            *Decoder
	rotation           int
	lastFingerprint    [sha256.Size]byte
	hasLastFingerprint bool
}

func newGenerationReader(discovered DiscoveredGeneration, maxRecordBytes int) (*generationReader, error) {
	if discovered.Report == nil {
		return nil, fmt.Errorf("report generation is missing its descriptor")
	}
	decoder, err := NewDecoder(maxRecordBytes)
	if err != nil {
		return nil, err
	}
	return &generationReader{
		report:   discovered.Report,
		decoder:  decoder,
		rotation: discovered.Rotation,
	}, nil
}

// drain applies all currently complete records. A record remains pending when
// apply fails, so retrying drain presents the same record before reading on.
func (reader *generationReader) drain(apply func(Record) error) (drainResult, error) {
	if reader == nil || reader.report == nil || reader.decoder == nil {
		return drainResult{}, os.ErrClosed
	}
	if apply == nil {
		return drainResult{}, fmt.Errorf("record handler is required")
	}

	var result drainResult
	buffer := make([]byte, generationReadBufferBytes)
	for {
		record, err := reader.decoder.Next()
		switch {
		case err == nil:
			if err := apply(record); err != nil {
				return result, err
			}
			if err := reader.decoder.Commit(record); err != nil {
				return result, fmt.Errorf("commit report record: %w", err)
			}
			reader.lastFingerprint = record.Fingerprint
			reader.hasLastFingerprint = true
			result.records++
			continue

		case errors.Is(err, ErrClosed):
			result.closed = true
			return result, nil

		case !errors.Is(err, ErrNeedMore):
			return result, err
		}

		size, err := reader.currentSize()
		if err != nil {
			return result, err
		}
		readOffset := reader.decoder.ReadOffset()
		if size < readOffset {
			return result, fmt.Errorf(
				"%w: size %d is before read offset %d",
				ErrGenerationTruncated,
				size,
				readOffset,
			)
		}
		if size == readOffset {
			return result, nil
		}

		readLength := size - readOffset
		if readLength > int64(len(buffer)) {
			readLength = int64(len(buffer))
		}
		readBytes, readErr := reader.report.ReadAt(buffer[:readLength], readOffset)
		if readBytes > 0 {
			if err := reader.decoder.Append(readOffset, buffer[:readBytes]); err != nil {
				return result, err
			}
			result.bytes += readBytes
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return result, fmt.Errorf("read report generation: %w", readErr)
		}
		return result, fmt.Errorf("read report generation: %w", io.ErrUnexpectedEOF)
	}
}

func (reader *generationReader) watermark() GenerationWatermark {
	return GenerationWatermark{
		Generation:      reader.report.Generation,
		CommittedOffset: reader.decoder.CommittedOffset(),
		LastFingerprint: reader.lastFingerprint,
		HasFingerprint:  reader.hasLastFingerprint,
		Closed:          reader.decoder.Closed(),
	}
}

func (reader *generationReader) currentSize() (int64, error) {
	if reader.report.file == nil {
		return 0, os.ErrClosed
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(reader.report.file.Fd()), &stat); err != nil {
		return 0, fmt.Errorf("inspect report generation descriptor: %w", err)
	}
	return stat.Size, nil
}

func (reader *generationReader) close() error {
	if reader == nil || reader.report == nil {
		return nil
	}
	return reader.report.Close()
}
