//go:build linux

package source

import "fmt"

// generationRegistry owns every descriptor and decoder observed during one
// source session. A generation remains registered after its pathname rotates
// away so unread appended bytes can still be consumed.
type generationRegistry struct {
	maxRecordBytes int
	readers        map[GenerationID]*generationReader
}

func newGenerationRegistry(maxRecordBytes int) (*generationRegistry, error) {
	if _, err := NewDecoder(maxRecordBytes); err != nil {
		return nil, err
	}
	return &generationRegistry{
		maxRecordBytes: maxRecordBytes,
		readers:        make(map[GenerationID]*generationReader),
	}, nil
}

// register consumes ownership of every discovered report descriptor.
func (registry *generationRegistry) register(discovered []DiscoveredGeneration) (int, error) {
	if registry == nil || registry.readers == nil {
		closeDiscovered(discovered)
		return 0, fmt.Errorf("generation registry is closed")
	}

	registered := 0
	for index, generation := range discovered {
		if generation.Report == nil {
			closeDiscovered(discovered[index+1:])
			return registered, fmt.Errorf("discovered generation is missing its descriptor")
		}

		generationID := generation.Report.Generation
		if existing, found := registry.readers[generationID]; found {
			if generation.Rotation > existing.rotation {
				existing.rotation = generation.Rotation
			}
			generation.Report.Close()
			continue
		}

		reader, err := newGenerationReader(generation, registry.maxRecordBytes)
		if err != nil {
			generation.Report.Close()
			closeDiscovered(discovered[index+1:])
			return registered, err
		}
		registry.readers[generationID] = reader
		registered++
	}
	return registered, nil
}

func (registry *generationRegistry) oldestFirst() []*generationReader {
	if registry == nil {
		return nil
	}

	discovered := make([]DiscoveredGeneration, 0, len(registry.readers))
	for _, reader := range registry.readers {
		discovered = append(discovered, DiscoveredGeneration{
			Rotation: reader.rotation,
			Report:   reader.report,
		})
	}
	ordered := OrderGenerationsOldestFirst(discovered)

	readers := make([]*generationReader, 0, len(ordered))
	for _, generation := range ordered {
		readers = append(readers, registry.readers[generation.Report.Generation])
	}
	return readers
}

func (registry *generationRegistry) watermarks() []GenerationWatermark {
	readers := registry.oldestFirst()
	watermarks := make([]GenerationWatermark, 0, len(readers))
	for _, reader := range readers {
		watermarks = append(watermarks, reader.watermark())
	}
	return watermarks
}

func (registry *generationRegistry) close() error {
	if registry == nil || registry.readers == nil {
		return nil
	}

	var firstError error
	for generationID, reader := range registry.readers {
		if err := reader.close(); err != nil && firstError == nil {
			firstError = err
		}
		delete(registry.readers, generationID)
	}
	registry.readers = nil
	return firstError
}
