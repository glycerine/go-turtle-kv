package bench

import (
	"flag"
	wormhole "github.com/glycerine/go-wormhole-kv"
)

var wormholeMetaCache = flag.Bool("wormhole-meta-cache", false, "enable Wormhole two-byte metadata cache")

type benchWormhole struct {
	store *wormhole.Map[uint64, uint64]
	ref   *wormhole.Ref[uint64, uint64]
}

func newBenchWormhole() *benchWormhole {
	m := wormhole.NewWithOptions[uint64, uint64](wormhole.Options{MetaCache: *wormholeMetaCache})
	return &benchWormhole{store: m, ref: m.Ref()}
}
func (m *benchWormhole) Get(k uint64) uint64 { v, _ := m.ref.Get(k); return v }
func (m *benchWormhole) Put(k, v uint64) {
	if err := m.ref.Put(k, v); err != nil {
		panic(err)
	}
}
func (m *benchWormhole) Len() int { return int(m.store.Len()) }
func (m *benchWormhole) Close()   { m.ref.Close() }
func (m *benchWormhole) Scan(start uint64, length int, visit func(uint64, uint64) bool) {
	m.ref.Scan(start, length, visit)
}

func closeComparison(idx any) {
	if c, ok := idx.(interface{ Close() }); ok {
		c.Close()
	}
}
