package gog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// LibraryTTL is how long the cached library listing is considered fresh.
const LibraryTTL = 10 * time.Minute

type libraryCache struct {
	UserID    string           `json:"user_id"`
	FetchedAt time.Time        `json:"fetched_at"`
	Products  []LibraryProduct `json:"products"`
}

func libraryCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gogloader", "library.json"), nil
}

// ClearCache removes cached API data.
func ClearCache() error {
	p, err := libraryCachePath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *Client) userID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.UserID
}

// readLibraryCache returns cached products if fresh and owned by the current
// user. Any problem (missing, stale, corrupt) is just a cache miss.
func (c *Client) readLibraryCache() ([]LibraryProduct, bool) {
	p, err := libraryCachePath()
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var lc libraryCache
	if json.Unmarshal(b, &lc) != nil {
		return nil, false
	}
	if lc.UserID != c.userID() || time.Since(lc.FetchedAt) > LibraryTTL || lc.FetchedAt.After(time.Now()) {
		return nil, false
	}
	return lc.Products, true
}

// writeLibraryCache is best-effort; failures only cost a refetch next time.
func (c *Client) writeLibraryCache(products []LibraryProduct) {
	p, err := libraryCachePath()
	if err != nil {
		return
	}
	b, err := json.Marshal(libraryCache{UserID: c.userID(), FetchedAt: time.Now(), Products: products})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// CachedLibrary returns the library, served from the on-disk cache when it
// is younger than LibraryTTL unless refresh is set.
func (c *Client) CachedLibrary(ctx context.Context, refresh bool) ([]LibraryProduct, error) {
	if !refresh {
		if lib, ok := c.readLibraryCache(); ok {
			return lib, nil
		}
	}
	lib, err := c.Library(ctx)
	if err != nil {
		return nil, err
	}
	c.writeLibraryCache(lib)
	return lib, nil
}
