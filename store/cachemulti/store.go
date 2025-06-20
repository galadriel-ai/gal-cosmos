package cachemulti

import (
	"fmt"
	"io"

	abci "github.com/tendermint/tendermint/abci/types"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/store/cachekv"
	"github.com/cosmos/cosmos-sdk/store/dbadapter"
	"github.com/cosmos/cosmos-sdk/store/listenkv"
	"github.com/cosmos/cosmos-sdk/store/tracekv"
	"github.com/cosmos/cosmos-sdk/store/types"
)

//----------------------------------------
// Store

// Store holds many branched stores.
// Implements MultiStore.
// NOTE: a Store (and MultiStores in general) should never expose the
// keys for the substores.
type Store struct {
	db     types.CacheKVStore
	stores map[types.StoreKey]types.CacheWrap
	keys   map[string]types.StoreKey

	traceWriter  io.Writer
	traceContext types.TraceContext

	listeners map[types.StoreKey][]types.WriteListener
	closers   []io.Closer
}

var _ types.CacheMultiStore = Store{}

// NewFromKVStore creates a new Store object from a mapping of store keys to
// CacheWrapper objects and a KVStore as the database. Each CacheWrapper store
// is a branched store.
func NewFromKVStore(
	store types.KVStore, stores map[types.StoreKey]types.CacheWrapper,
	keys map[string]types.StoreKey, traceWriter io.Writer, traceContext types.TraceContext,
	listeners map[types.StoreKey][]types.WriteListener,
) Store {
	if listeners == nil {
		listeners = make(map[types.StoreKey][]types.WriteListener)
	}
	cms := Store{
		db:           cachekv.NewStore(store, nil, types.DefaultCacheSizeLimit),
		stores:       make(map[types.StoreKey]types.CacheWrap, len(stores)),
		keys:         keys,
		traceWriter:  traceWriter,
		traceContext: traceContext,
		listeners:    listeners,
		closers:      []io.Closer{},
	}

	for key, store := range stores {
		if cms.TracingEnabled() {
			store = tracekv.NewStore(store.(types.KVStore), cms.traceWriter, cms.traceContext)
		}
		if cms.ListeningEnabled(key) {
			store = listenkv.NewStore(store.(types.KVStore), key, listeners[key])
		}
		cms.stores[key] = cachekv.NewStore(store.(types.KVStore), key, types.DefaultCacheSizeLimit)
	}

	return cms
}

// NewStore creates a new Store object from a mapping of store keys to
// CacheWrapper objects. Each CacheWrapper store is a branched store.
func NewStore(
	db dbm.DB, stores map[types.StoreKey]types.CacheWrapper, keys map[string]types.StoreKey,
	traceWriter io.Writer, traceContext types.TraceContext, listeners map[types.StoreKey][]types.WriteListener,
) Store {

	return NewFromKVStore(dbadapter.Store{DB: db}, stores, keys, traceWriter, traceContext, listeners)
}

func newCacheMultiStoreFromCMS(cms Store) Store {
	stores := make(map[types.StoreKey]types.CacheWrapper)
	for k, v := range cms.stores {
		stores[k] = v
	}

	return NewFromKVStore(cms.db, stores, nil, cms.traceWriter, cms.traceContext, nil)
}

// SetTracer sets the tracer for the MultiStore that the underlying
// stores will utilize to trace operations. A MultiStore is returned.
func (cms Store) SetTracer(w io.Writer) types.MultiStore {
	cms.traceWriter = w
	return cms
}

// SetTracingContext updates the tracing context for the MultiStore by merging
// the given context with the existing context by key. Any existing keys will
// be overwritten. It is implied that the caller should update the context when
// necessary between tracing operations. It returns a modified MultiStore.
func (cms Store) SetTracingContext(tc types.TraceContext) types.MultiStore {
	if cms.traceContext != nil {
		for k, v := range tc {
			cms.traceContext[k] = v
		}
	} else {
		cms.traceContext = tc
	}

	return cms
}

// TracingEnabled returns if tracing is enabled for the MultiStore.
func (cms Store) TracingEnabled() bool {
	return cms.traceWriter != nil
}

// AddListeners adds listeners for a specific KVStore
func (cms Store) AddListeners(key types.StoreKey, listeners []types.WriteListener) {
	if ls, ok := cms.listeners[key]; ok {
		cms.listeners[key] = append(ls, listeners...)
	} else {
		cms.listeners[key] = listeners
	}
}

// ListeningEnabled returns if listening is enabled for a specific KVStore
func (cms Store) ListeningEnabled(key types.StoreKey) bool {
	if ls, ok := cms.listeners[key]; ok {
		return len(ls) != 0
	}
	return false
}

// GetStoreType returns the type of the store.
func (cms Store) GetStoreType() types.StoreType {
	return types.StoreTypeMulti
}

// Write calls Write on each underlying store.
func (cms Store) Write() {
	cms.db.Write()
	for _, store := range cms.stores {
		store.Write()
	}
}

func (cms Store) GetEvents() []abci.Event {
	events := []abci.Event{}
	for _, store := range cms.stores {
		events = append(events, store.GetEvents()...)
	}
	return events
}

func (cms Store) ResetEvents() {
	for _, store := range cms.stores {
		store.ResetEvents()
	}
}

// Implements CacheWrapper.
func (cms Store) CacheWrap(_ types.StoreKey) types.CacheWrap {
	return cms.CacheMultiStore().(types.CacheWrap)
}

// CacheWrapWithTrace implements the CacheWrapper interface.
func (cms Store) CacheWrapWithTrace(storeKey types.StoreKey, _ io.Writer, _ types.TraceContext) types.CacheWrap {
	return cms.CacheWrap(storeKey)
}

// CacheWrapWithListeners implements the CacheWrapper interface.
func (cms Store) CacheWrapWithListeners(storeKey types.StoreKey, _ []types.WriteListener) types.CacheWrap {
	return cms.CacheWrap(storeKey)
}

// Implements MultiStore.
func (cms Store) CacheMultiStore() types.CacheMultiStore {
	return newCacheMultiStoreFromCMS(cms)
}

// CacheMultiStoreWithVersion implements the MultiStore interface. It will panic
// as an already cached multi-store cannot load previous versions.
//
// TODO: The store implementation can possibly be modified to support this as it
// seems safe to load previous versions (heights).
func (cms Store) CacheMultiStoreWithVersion(_ int64) (types.CacheMultiStore, error) {
	panic("cannot branch cached multi-store with a version")
}

// GetStore returns an underlying Store by key.
func (cms Store) GetStore(key types.StoreKey) types.Store {
	s := cms.stores[key]
	if key == nil || s == nil {
		panic(fmt.Sprintf("kv store with key %v has not been registered in stores", key))
	}
	return s.(types.Store)
}

// GetKVStore returns an underlying KVStore by key.
func (cms Store) GetKVStore(key types.StoreKey) types.KVStore {
	store := cms.stores[key]
	if key == nil || store == nil {
		panic(fmt.Sprintf("kv store with key %v has not been registered in stores", key))
	}
	return store.(types.KVStore)
}

func (cms Store) GetWorkingHash() ([]byte, error) {
	panic("should never attempt to get working hash from cache multi store")
}

// StoreKeys returns a list of all store keys
func (cms Store) StoreKeys() []types.StoreKey {
	keys := make([]types.StoreKey, 0, len(cms.stores))
	for _, key := range cms.keys {
		keys = append(keys, key)
	}
	return keys
}

// SetKVStores sets the underlying KVStores via a handler for each key
func (cms Store) SetKVStores(handler func(sk types.StoreKey, s types.KVStore) types.CacheWrap) types.MultiStore {
	for k, s := range cms.stores {
		cms.stores[k] = handler(k, s.(types.KVStore))
	}
	return cms
}

func (cms Store) CacheMultiStoreForExport(_ int64) (types.CacheMultiStore, error) {
	panic("Not implemented")
}

func (cms Store) AddCloser(closer io.Closer) {
	cms.closers = append(cms.closers, closer)
}

func (cms Store) Close() {
	for _, closer := range cms.closers {
		closer.Close()
	}
}

func (cms Store) GetEarliestVersion() int64 {
	panic("not implemented")
}
