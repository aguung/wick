package state

import (
	"sync"
	"time"

	"github.com/yogasw/wick/internal/shardedlog"
)

// IndexEntry is the row shape stored in the per-slug index. Kept
// lean so a 100-row shard stays well under 10KB.
type IndexEntry struct {
	ID         string     `json:"id"`
	Status     string     `json:"status,omitempty"`
	StartedAt  time.Time  `json:"at"`
	EndedAt    *time.Time `json:"end,omitempty"`
	DurationMs int64      `json:"ms,omitempty"`
}

// indexStores caches one shardedlog.Store per slug so concurrent
// appends share the per-Store mutex (otherwise two fresh Stores
// would race on the shard-roll decision).
var (
	indexStoresMu sync.Mutex
	indexStores   = map[string]*shardedlog.Store[IndexEntry]{}
)

func (s *FileStore) indexStore(slug string) *shardedlog.Store[IndexEntry] {
	indexStoresMu.Lock()
	defer indexStoresMu.Unlock()
	key := s.Layout.WorkflowIndexDir(slug)
	if v, ok := indexStores[key]; ok {
		return v
	}
	v := &shardedlog.Store[IndexEntry]{Dir: key}
	indexStores[key] = v
	return v
}

// IndexAppend persists one summary row to the slug's sharded index.
// Constant-time regardless of total run history (touches only the
// current shard).
func (s *FileStore) IndexAppend(slug string, entry IndexEntry) error {
	return s.indexStore(slug).Append(entry)
}

// IndexList returns one page of summaries, newest first.
// pageSize defaults to shardedlog.DefaultShardMax. hasMore=true
// when older pages exist.
func (s *FileStore) IndexList(slug string, page, pageSize int) ([]IndexEntry, bool, error) {
	return s.indexStore(slug).Page(page, pageSize)
}
