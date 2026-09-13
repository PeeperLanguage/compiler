package constantresult

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/semantics/symbols"
)

func TestResultSeparatesCachedAndPublishedConstants(t *testing.T) {
	result := New()
	id := symbols.SymbolID(1)
	cached, ok := constvalue.NewIntText("1", "i32")
	if !ok {
		t.Fatal("create cached constant")
	}
	published, ok := constvalue.NewIntText("2", "i32")
	if !ok {
		t.Fatal("create published constant")
	}

	result.Cache(id, cached)
	if got, found := result.Cached(id); !found || got != cached {
		t.Fatalf("cached value = %#v, %t; want %#v, true", got, found, cached)
	}
	if got := result.Published(id); got != nil {
		t.Fatalf("cached value leaked into published constants: %#v", got)
	}

	result.Publish(id, published)
	if got := result.Published(id); got != published {
		t.Fatalf("published value = %#v, want %#v", got, published)
	}
	if got, found := result.Cached(id); found || got != nil {
		t.Fatalf("publication left duplicate cache entry: %#v, %t", got, found)
	}

	result.ClearPublished()
	if got := result.Published(id); got != nil {
		t.Fatalf("ClearPublished retained %#v", got)
	}
}

func TestResultDiscardCachedDoesNotChangePublishedValue(t *testing.T) {
	result := New()
	id := symbols.SymbolID(1)
	published, ok := constvalue.NewIntText("7", "i32")
	if !ok {
		t.Fatal("create published constant")
	}
	cachedID := symbols.SymbolID(2)
	cached, ok := constvalue.NewIntText("9", "i32")
	if !ok {
		t.Fatal("create cached constant")
	}

	result.Publish(id, published)
	result.Cache(cachedID, cached)
	result.DiscardCached(cachedID)

	if got := result.Published(id); got != published {
		t.Fatalf("published value changed after cache discard: %#v", got)
	}
	if _, found := result.Cached(cachedID); found {
		t.Fatal("DiscardCached retained cached value")
	}
}
