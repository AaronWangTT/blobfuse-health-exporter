package source

import (
	"bytes"
	"errors"
	"testing"
)

func BenchmarkDecoderOneMiBActiveReport(b *testing.B) {
	object := []byte(`{"Timestamp":"2026-08-20T12:00:00Z","BlobfuseStats":[{"componentName":"azstorage","value":{"Bytes Downloaded":1048576,"OpenFileHandles":2}}]}`)
	report := []byte{'['}
	for len(report) < 1024*1024 {
		report = append(report, object...)
		report = append(report, ',', '\n')
	}
	b.SetBytes(int64(len(report)))
	b.ReportAllocs()
	b.ResetTimer()

	for iteration := 0; iteration < b.N; iteration++ {
		decoder, err := NewDecoder(16 * 1024 * 1024)
		if err != nil {
			b.Fatal(err)
		}
		if err := decoder.Append(0, report); err != nil {
			b.Fatal(err)
		}
		for {
			record, err := decoder.Next()
			if errors.Is(err, ErrNeedMore) {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			if err := decoder.Commit(record); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkNormalizeAggregateRecord(b *testing.B) {
	record := Record{Raw: []byte(`{
		"Timestamp":"2026-08-20T12:00:00Z",
		"BlobfuseStats":[
			{"componentName":"azstorage","value":{"Bytes Downloaded":1048576,"Bytes Uploaded":524288,"OpenFileHandles":2}},
			{"componentName":"libfuse","value":{"CreateFile":8,"DeleteFile":2,"OpenFileHandles":3}},
			{"componentName":"file_cache","value":{"Cache Usage":"12.500000 MB","Usage Percent":"25.00%","Files Downloaded":4,"Files served from cache":3}}
		],
		"MemoryUsage":"1234m"
	}`)}
	b.SetBytes(int64(len(record.Raw)))
	b.ReportAllocs()
	b.ResetTimer()

	for iteration := 0; iteration < b.N; iteration++ {
		normalized, err := NormalizeRecord(record)
		if err != nil {
			b.Fatal(err)
		}
		if len(normalized.Counters) != 6 || len(normalized.Gauges) != 5 {
			b.Fatalf("unexpected normalized result: %#v", normalized)
		}
	}
}

func BenchmarkDecoderPartialTailAppend(b *testing.B) {
	first := []byte(`[{"value":1},{"value":`)
	second := []byte(`2},`)
	b.ReportAllocs()
	b.ResetTimer()

	for iteration := 0; iteration < b.N; iteration++ {
		decoder, err := NewDecoder(1024)
		if err != nil {
			b.Fatal(err)
		}
		if err := decoder.Append(0, first); err != nil {
			b.Fatal(err)
		}
		firstRecord, err := decoder.Next()
		if err != nil {
			b.Fatal(err)
		}
		if err := decoder.Commit(firstRecord); err != nil {
			b.Fatal(err)
		}
		if _, err := decoder.Next(); !errors.Is(err, ErrNeedMore) {
			b.Fatalf("partial Next() error = %v", err)
		}
		if err := decoder.Append(decoder.ReadOffset(), second); err != nil {
			b.Fatal(err)
		}
		secondRecord, err := decoder.Next()
		if err != nil {
			b.Fatal(err)
		}
		if !bytes.Equal(secondRecord.Raw, []byte(`{"value":2}`)) {
			b.Fatalf("record = %s", secondRecord.Raw)
		}
	}
}
