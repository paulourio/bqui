package results

import (
	"container/list"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ChunkRows is the chunk size for in-memory sources.
const ChunkRows = 1024

const (
	defaultChunkCache = 32  // decoded chunks kept in memory
	defaultRowCache   = 512 // rendered rows kept in memory
)

// ErrOutOfRange is returned for lines or rows beyond the loaded data.
var ErrOutOfRange = errors.New("results: out of range")

// ChunkStore loads the rows of a chunk (from memory or a spill file).
type ChunkStore interface {
	Load(chunk int) ([][]any, error)
}

type chunkMeta struct {
	rows      int
	lines     int64
	startRow  int64
	startLine int64
}

type loadedChunk struct {
	rows   [][]any
	prefix []int64 // prefix[i] = first line of row i, relative to the chunk
}

type renderedRow struct {
	cells [][]Cell
	lines int
}

// Grid indexes a growing result as chunks of rows and answers visual-line
// queries for the console layout. Only per-chunk metadata is kept for the
// whole result; rows are loaded and rendered on demand through small LRU
// caches, so memory stays bounded for any result size.
//
// It is safe for concurrent use: an ingest goroutine appends chunks while the
// UI thread reads.
type Grid struct {
	fields []Field
	leaves []Column
	store  ChunkStore

	mu         sync.Mutex
	chunks     []chunkMeta
	totalRows  int64
	totalLines int64
	complete   bool
	err        error

	chunkCache *lru[int, *loadedChunk]
	rowCache   *lru[int64, *renderedRow]
}

// NewGrid creates an empty grid over store.
func NewGrid(fields []Field, store ChunkStore) *Grid {
	return &Grid{
		fields: fields, leaves: Leaves(fields), store: store,
		chunkCache: newLRU[int, *loadedChunk](defaultChunkCache),
		rowCache:   newLRU[int64, *renderedRow](defaultRowCache),
	}
}

// Fields returns the result schema.
func (g *Grid) Fields() []Field { return g.fields }

// Columns returns the flattened leaf columns.
func (g *Grid) Columns() []Column { return g.leaves }

// AddChunk registers a chunk the store can now load (ingest side).
func (g *Grid) AddChunk(rows int, lines int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.chunks = append(g.chunks, chunkMeta{rows: rows, lines: lines, startRow: g.totalRows, startLine: g.totalLines})
	g.totalRows += int64(rows)
	g.totalLines += lines
}

// Finish marks the result complete (err != nil if ingestion failed).
func (g *Grid) Finish(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.complete, g.err = true, err
}

// Size returns loaded rows and visual lines, and whether loading finished.
func (g *Grid) Size() (rows, lines int64, complete bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.totalRows, g.totalLines, g.complete
}

// Err returns the ingestion error, if any.
func (g *Grid) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.err
}

// Pos locates a visual line.
type Pos struct {
	Row   int64 // logical row (0-based)
	Sub   int   // line within the row
	Lines int   // lines the row occupies
}

// First reports whether this is the row's first line.
func (p Pos) First() bool { return p.Sub == 0 }

// Last reports whether this is the row's last line.
func (p Pos) Last() bool { return p.Sub == p.Lines-1 }

// Cell returns the cell at a visual line and leaf column.
func (g *Grid) Cell(line int64, col int) (Cell, Pos, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	pos, rr, err := g.renderLocked(line)
	if err != nil {
		return Cell{}, Pos{}, err
	}

	if col < 0 || col >= len(rr.cells) {
		return Cell{}, pos, fmt.Errorf("%w: column %d", ErrOutOfRange, col)
	}

	return rr.cells[col][pos.Sub], pos, nil
}

// Locate maps a visual line to its row.
func (g *Grid) Locate(line int64) (Pos, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	pos, _, err := g.locateLocked(line)

	return pos, err
}

// Row returns a logical row's values.
func (g *Grid) Row(row int64) ([]any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ci := sort.Search(len(g.chunks), func(i int) bool { return g.chunks[i].startRow+int64(g.chunks[i].rows) > row })
	if row < 0 || ci == len(g.chunks) {
		return nil, fmt.Errorf("%w: row %d", ErrOutOfRange, row)
	}

	lc, err := g.chunkLocked(ci)
	if err != nil {
		return nil, err
	}

	return lc.rows[row-g.chunks[ci].startRow], nil
}

// FirstLine returns the first visual line of a logical row.
func (g *Grid) FirstLine(row int64) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ci := sort.Search(len(g.chunks), func(i int) bool { return g.chunks[i].startRow+int64(g.chunks[i].rows) > row })
	if row < 0 || ci == len(g.chunks) {
		return 0, fmt.Errorf("%w: row %d", ErrOutOfRange, row)
	}

	lc, err := g.chunkLocked(ci)
	if err != nil {
		return 0, err
	}

	return g.chunks[ci].startLine + lc.prefix[row-g.chunks[ci].startRow], nil
}

func (g *Grid) locateLocked(line int64) (Pos, *loadedChunk, error) {
	if line < 0 || line >= g.totalLines {
		return Pos{}, nil, fmt.Errorf("%w: line %d of %d", ErrOutOfRange, line, g.totalLines)
	}

	ci := sort.Search(len(g.chunks), func(i int) bool { return g.chunks[i].startLine+g.chunks[i].lines > line })
	meta := g.chunks[ci]

	lc, err := g.chunkLocked(ci)
	if err != nil {
		return Pos{}, nil, err
	}

	rel := line - meta.startLine
	ri := sort.Search(len(lc.prefix), func(i int) bool { return lc.prefix[i] > rel }) - 1

	next := meta.lines
	if ri+1 < len(lc.prefix) {
		next = lc.prefix[ri+1]
	}

	return Pos{Row: meta.startRow + int64(ri), Sub: int(rel - lc.prefix[ri]), Lines: int(next - lc.prefix[ri])}, lc, nil
}

func (g *Grid) renderLocked(line int64) (Pos, *renderedRow, error) {
	pos, lc, err := g.locateLocked(line)
	if err != nil {
		return Pos{}, nil, err
	}

	if rr, ok := g.rowCache.get(pos.Row); ok {
		return pos, rr, nil
	}

	ci := sort.Search(len(g.chunks), func(i int) bool { return g.chunks[i].startRow+int64(g.chunks[i].rows) > pos.Row })
	row := lc.rows[pos.Row-g.chunks[ci].startRow]
	rr := &renderedRow{cells: Render(g.fields, row), lines: pos.Lines}
	g.rowCache.put(pos.Row, rr)

	return pos, rr, nil
}

func (g *Grid) chunkLocked(ci int) (*loadedChunk, error) {
	if lc, ok := g.chunkCache.get(ci); ok {
		return lc, nil
	}

	rows, err := g.store.Load(ci)
	if err != nil {
		return nil, fmt.Errorf("results: load chunk %d: %w", ci, err)
	}

	lc := &loadedChunk{rows: rows, prefix: make([]int64, len(rows))}

	var acc int64
	for i, r := range rows {
		lc.prefix[i] = acc
		acc += int64(Lines(g.fields, r))
	}

	g.chunkCache.put(ci, lc)

	return lc, nil
}

// MemoryStore keeps decoded rows in memory (REST results).
type MemoryStore struct {
	mu     sync.RWMutex
	chunks [][][]any
}

// Load implements ChunkStore.
func (m *MemoryStore) Load(i int) ([][]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if i < 0 || i >= len(m.chunks) {
		return nil, fmt.Errorf("%w: chunk %d", ErrOutOfRange, i)
	}

	return m.chunks[i], nil
}

// AppendRows stores rows in ChunkRows-sized chunks and registers them.
func AppendRows(g *Grid, m *MemoryStore, rows [][]any) {
	for len(rows) > 0 {
		n := min(len(rows), ChunkRows)
		chunk := rows[:n]
		rows = rows[n:]

		var lines int64
		for _, r := range chunk {
			lines += int64(Lines(g.fields, r))
		}

		m.mu.Lock()
		m.chunks = append(m.chunks, chunk)
		m.mu.Unlock()

		g.AddChunk(n, lines)
	}
}

// lru is a minimal least-recently-used cache (callers hold their own lock).
type lru[K comparable, V any] struct {
	max   int
	ll    *list.List
	items map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	k K
	v V
}

func newLRU[K comparable, V any](n int) *lru[K, V] {
	return &lru[K, V]{max: n, ll: list.New(), items: make(map[K]*list.Element)}
}

func (c *lru[K, V]) get(k K) (V, bool) {
	if e, ok := c.items[k]; ok {
		c.ll.MoveToFront(e)

		return entry[K, V](e).v, true
	}

	var zero V

	return zero, false
}

// entry unwraps a list element; only *lruEntry values are ever stored.
func entry[K comparable, V any](e *list.Element) *lruEntry[K, V] {
	en, _ := e.Value.(*lruEntry[K, V])

	return en
}

func (c *lru[K, V]) put(k K, v V) {
	if e, ok := c.items[k]; ok {
		entry[K, V](e).v = v
		c.ll.MoveToFront(e)

		return
	}

	c.items[k] = c.ll.PushFront(&lruEntry[K, V]{k: k, v: v})

	if c.ll.Len() > c.max {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.items, entry[K, V](old).k)
	}
}
