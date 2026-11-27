package results

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// DecodeFunc turns one stored blob (e.g. a raw Arrow batch) into rows.
type DecodeFunc func(data []byte) ([][]any, error)

// ErrClosed is returned after Close.
var ErrClosed = errors.New("results: spill store closed")

// SpillStore keeps chunks as raw blobs in a temporary file and decodes them
// on demand. With Storage Read API batches the blobs are the LZ4-compressed
// bytes BigQuery sent, so disk usage stays near the wire size and nothing is
// re-encoded. Memory holds only offsets plus the Grid's small chunk cache.
type SpillStore struct {
	decode DecodeFunc

	mu     sync.RWMutex
	f      *os.File
	offs   []int64
	lens   []int
	size   int64
	closed bool
}

// NewSpillStore creates a spill file in dir ("" = the OS temp dir).
func NewSpillStore(dir string, decode DecodeFunc) (*SpillStore, error) {
	f, err := os.CreateTemp(dir, "bqui-result-*.spill")
	if err != nil {
		return nil, fmt.Errorf("results: create spill: %w", err)
	}

	return &SpillStore{decode: decode, f: f}, nil
}

// Append stores a blob as the next chunk and returns its decoded rows (the
// caller uses them to count lines, then drops them).
func (s *SpillStore) Append(data []byte) ([][]any, error) {
	rows, err := s.decode(data)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrClosed
	}

	if _, err := s.f.WriteAt(data, s.size); err != nil {
		return nil, fmt.Errorf("results: spill write: %w", err)
	}

	s.offs = append(s.offs, s.size)
	s.lens = append(s.lens, len(data))
	s.size += int64(len(data))

	return rows, nil
}

// Load implements ChunkStore.
func (s *SpillStore) Load(i int) ([][]any, error) {
	s.mu.RLock()

	if s.closed {
		s.mu.RUnlock()

		return nil, ErrClosed
	}

	if i < 0 || i >= len(s.offs) {
		s.mu.RUnlock()

		return nil, fmt.Errorf("%w: chunk %d", ErrOutOfRange, i)
	}

	buf := make([]byte, s.lens[i])
	_, err := s.f.ReadAt(buf, s.offs[i])
	s.mu.RUnlock()

	if err != nil {
		return nil, fmt.Errorf("results: spill read: %w", err)
	}

	return s.decode(buf)
}

// Bytes reports the spill file size.
func (s *SpillStore) Bytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.size
}

// Close deletes the spill file.
func (s *SpillStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	name := s.f.Name()

	return errors.Join(s.f.Close(), os.Remove(name))
}

// AppendSpilled stores a blob in the spill store and registers its chunk.
func AppendSpilled(g *Grid, s *SpillStore, data []byte) error {
	rows, err := s.Append(data)
	if err != nil {
		return err
	}

	var lines int64
	for _, r := range rows {
		lines += int64(Lines(g.fields, r))
	}

	g.AddChunk(len(rows), lines)

	return nil
}
