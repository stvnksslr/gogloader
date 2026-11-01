package gog

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLibraryCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())           // darwin UserCacheDir
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // linux UserCacheDir
	t.Setenv("LocalAppData", t.TempDir())   // windows UserCacheDir

	c := &Client{token: &Token{UserID: "u1"}}
	want := []LibraryProduct{{ID: 1, Slug: "kenshi", Title: "Kenshi"}}
	c.writeLibraryCache(want)

	got, err := c.CachedLibrary(context.Background(), false)
	if err != nil || len(got) != 1 || got[0].Slug != "kenshi" {
		t.Fatalf("fresh cache: got %v, %v", got, err)
	}

	// Another account must not see u1's library.
	other := &Client{token: &Token{UserID: "u2"}}
	if _, ok := other.readLibraryCache(); ok {
		t.Fatal("cache leaked across users")
	}

	// Expired entries are a miss.
	p, _ := libraryCachePath()
	old := time.Now().Add(-LibraryTTL - time.Minute)
	writeAt(t, c, want, old)
	if _, ok := c.readLibraryCache(); ok {
		t.Fatalf("stale cache at %s was served", p)
	}

	if err := ClearCache(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.readLibraryCache(); ok {
		t.Fatal("cache survived ClearCache")
	}
}

func writeAt(t *testing.T, c *Client, products []LibraryProduct, at time.Time) {
	t.Helper()
	c.writeLibraryCache(products)
	p, _ := libraryCachePath()
	b := mustJSON(t, libraryCache{UserID: c.userID(), FetchedAt: at, Products: products})
	if err := writeFile(p, b); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
