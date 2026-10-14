module github.com/glycerine/go-turtle-kv/bench

go 1.26.4

require (
	github.com/glycerine/bufftree v0.0.3
	github.com/glycerine/insdict v0.14.6
	github.com/glycerine/rbtree v0.2.2
	github.com/glycerine/go-turtle-kv v0.1.0
	github.com/glycerine/go-wormhole-kv v0.1.0
	github.com/tidwall/btree v1.8.1
)

require github.com/cespare/xxhash/v2 v2.3.0 // indirect

// Compare the same local BPTree implementation as bufftree/bench.
//replace github.com/glycerine/bufftree => ../../../bufftree

// Compare the working checkout used by ~/wormhole/go.
//replace github.com/glycerine/wormhole/go => ../../../../wuxb45/wormhole/go
