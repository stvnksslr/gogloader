package gog

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	embedBase   = "https://embed.gog.com"
	apiBase     = "https://api.gog.com"
	catalogBase = "https://catalog.gog.com"
)

// UserAgent is sent with every GOG request; the CLI sets it to include the
// build version.
var UserAgent = "gogloader/dev"

var apiHTTP = &http.Client{Timeout: 60 * time.Second}

// Client is an authenticated GOG API client.
type Client struct {
	mu    sync.Mutex
	token *Token
}

// NewClient loads the stored token. It returns ErrNotLoggedIn if none exists.
func NewClient() (*Client, error) {
	t, err := loadToken()
	if err != nil {
		return nil, err
	}
	return &Client{token: t}, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.expired() {
		nt, err := refresh(ctx, c.token)
		if err != nil {
			return "", err
		}
		c.token = nt
	}
	return c.token.AccessToken, nil
}

func (c *Client) getJSON(ctx context.Context, rawURL string, auth bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	if auth {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := apiHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &HTTPError{URL: stripQuery(rawURL), Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// HTTPError is a non-200 API response.
type HTTPError struct {
	URL    string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GET %s: HTTP %d %s", e.URL, e.Status, e.Body)
}

func stripQuery(s string) string {
	if i := strings.IndexByte(s, '?'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// Library

// LibraryProduct is a game owned by the user.
type LibraryProduct struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Slug    string `json:"slug"`
	WorksOn struct {
		Windows bool `json:"Windows"`
		Mac     bool `json:"Mac"`
		Linux   bool `json:"Linux"`
	} `json:"worksOn"`
}

// Platforms returns a compact platform list, e.g. "win,mac".
func (p LibraryProduct) Platforms() string {
	var out []string
	if p.WorksOn.Windows {
		out = append(out, "win")
	}
	if p.WorksOn.Mac {
		out = append(out, "mac")
	}
	if p.WorksOn.Linux {
		out = append(out, "linux")
	}
	return strings.Join(out, ",")
}

// Library returns every game in the user's library.
func (c *Client) Library(ctx context.Context) ([]LibraryProduct, error) {
	var all []LibraryProduct
	for page := 1; ; page++ {
		var resp struct {
			TotalPages int              `json:"totalPages"`
			Products   []LibraryProduct `json:"products"`
		}
		u := fmt.Sprintf("%s/account/getFilteredProducts?mediaType=1&sortBy=title&page=%d", embedBase, page)
		if err := c.getJSON(ctx, u, true, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Products...)
		if page >= resp.TotalPages || len(resp.Products) == 0 {
			break
		}
	}
	return all, nil
}

// ---------------------------------------------------------------------------
// Resolving a store name to a product

var storeURLRe = regexp.MustCompile(`gog\.com/(?:[a-z]{2}/)?game/([^/?#]+)`)

// NormalizeName turns a store URL or slug into a bare slug.
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	if m := storeURLRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return name
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func squash(s string) string { return nonAlnum.ReplaceAllString(strings.ToLower(s), "") }

// AmbiguousError lists candidate matches for a name.
type AmbiguousError struct {
	Name       string
	Candidates []LibraryProduct
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches multiple games in your library, be more specific:\n", e.Name)
	for _, p := range e.Candidates {
		fmt.Fprintf(&b, "  %-40s %s\n", p.Slug, p.Title)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Resolve finds an owned product by store slug, store URL, numeric id or title.
func (c *Client) Resolve(ctx context.Context, name string) (LibraryProduct, error) {
	slug := NormalizeName(name)
	key := squash(slug)
	lib, err := c.CachedLibrary(ctx, false)
	if err != nil {
		return LibraryProduct{}, err
	}
	if p, ok := exactMatch(lib, slug); ok {
		return p, nil
	}
	// No exact hit in a possibly cached copy: the game may be a recent
	// purchase, so retry once against a fresh listing.
	if lib, err = c.CachedLibrary(ctx, true); err != nil {
		return LibraryProduct{}, err
	}
	if p, ok := exactMatch(lib, slug); ok {
		return p, nil
	}

	// The store page slug sometimes differs from the library slug; ask the
	// public catalog which product id that store slug belongs to.
	if id, err := c.catalogIDForSlug(ctx, slug); err == nil {
		for _, p := range lib {
			if p.ID == id {
				return p, nil
			}
		}
		return LibraryProduct{}, fmt.Errorf("%q (product %d) exists on the store but is not in your library", slug, id)
	}

	var cands []LibraryProduct
	for _, p := range lib {
		if key != "" && (strings.Contains(squash(p.Title), key) || strings.Contains(squash(p.Slug), key)) {
			cands = append(cands, p)
		}
	}
	switch len(cands) {
	case 0:
		return LibraryProduct{}, fmt.Errorf("no game matching %q in your library (try `gogloader list`)", name)
	case 1:
		return cands[0], nil
	default:
		sort.Slice(cands, func(i, j int) bool { return cands[i].Slug < cands[j].Slug })
		return LibraryProduct{}, &AmbiguousError{Name: name, Candidates: cands}
	}
}

// exactMatch finds a product by numeric id, exact slug, or squashed
// title/slug equality.
func exactMatch(lib []LibraryProduct, slug string) (LibraryProduct, bool) {
	if id, err := strconv.ParseInt(slug, 10, 64); err == nil {
		for _, p := range lib {
			if p.ID == id {
				return p, true
			}
		}
	}
	for _, p := range lib {
		if strings.EqualFold(p.Slug, slug) {
			return p, true
		}
	}
	key := squash(slug)
	for _, p := range lib {
		if squash(p.Title) == key || squash(p.Slug) == key {
			return p, true
		}
	}
	return LibraryProduct{}, false
}

func (c *Client) catalogIDForSlug(ctx context.Context, slug string) (int64, error) {
	q := url.Values{
		"limit":       {"48"},
		"query":       {"like:" + strings.ReplaceAll(slug, "_", " ")},
		"productType": {"in:game,pack,dlc"},
	}
	var resp struct {
		Products []struct {
			ID   json.Number `json:"id"`
			Slug string      `json:"slug"`
		} `json:"products"`
	}
	if err := c.getJSON(ctx, catalogBase+"/v1/catalog?"+q.Encode(), false, &resp); err != nil {
		return 0, err
	}
	for _, p := range resp.Products {
		if strings.EqualFold(p.Slug, slug) {
			return p.ID.Int64()
		}
	}
	return 0, errors.New("not found in catalog")
}

// ---------------------------------------------------------------------------
// Product details & installers

// Product is the api.gog.com product document.
type Product struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Slug      string `json:"slug"`
	Downloads struct {
		Installers []Installer `json:"installers"`
	} `json:"downloads"`
	ExpandedDLCs []Product `json:"expanded_dlcs"`
}

// Installer is one installer for an OS/language, made of one or more files
// (multipart installers have several, e.g. setup.exe + setup-1.bin ...).
type Installer struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	OS           string          `json:"os"`
	Language     string          `json:"language"`
	LanguageFull string          `json:"language_full"`
	Version      string          `json:"version"`
	TotalSize    int64           `json:"total_size"`
	Files        []InstallerFile `json:"files"`
}

// InstallerFile is a single part of an installer.
type InstallerFile struct {
	ID       string `json:"id"`
	Size     int64  `json:"size"`
	Downlink string `json:"downlink"`
}

// Product fetches product details including installers and DLCs.
func (c *Client) Product(ctx context.Context, id int64) (*Product, error) {
	var p Product
	u := fmt.Sprintf("%s/products/%d?expand=downloads,expanded_dlcs&locale=en-US", apiBase, id)
	if err := c.getJSON(ctx, u, false, &p); err != nil {
		return nil, err
	}
	// expanded_dlcs is not always populated with downloads; fill them in.
	for i, d := range p.ExpandedDLCs {
		if len(d.Downloads.Installers) > 0 {
			continue
		}
		full, err := c.Product(ctx, d.ID)
		if err != nil {
			return nil, fmt.Errorf("dlc %d: %w", d.ID, err)
		}
		full.ExpandedDLCs = nil
		p.ExpandedDLCs[i] = *full
	}
	return &p, nil
}

// SelectInstaller picks an installer matching os and lang. If lang is not
// available it falls back to English, then to whatever exists.
func SelectInstaller(installers []Installer, osName, lang string) (*Installer, error) {
	var forOS []Installer
	for _, in := range installers {
		if in.OS == osName && len(in.Files) > 0 {
			forOS = append(forOS, in)
		}
	}
	if len(forOS) == 0 {
		return nil, fmt.Errorf("no %s installer available", osName)
	}
	for _, want := range []string{lang, "en"} {
		for i := range forOS {
			if strings.EqualFold(forOS[i].Language, want) {
				return &forOS[i], nil
			}
		}
	}
	return &forOS[0], nil
}

// Link is a resolved, short-lived CDN download link.
type Link struct {
	URL string
	MD5 string // may be empty if GOG does not publish a checksum
}

// ResolveFile turns an installer file's downlink into a CDN URL and md5.
func (c *Client) ResolveFile(ctx context.Context, f InstallerFile) (Link, error) {
	var dl struct {
		Downlink string `json:"downlink"`
		Checksum string `json:"checksum"`
	}
	if err := c.getJSON(ctx, f.Downlink, true, &dl); err != nil {
		return Link{}, err
	}
	if dl.Downlink == "" {
		return Link{}, fmt.Errorf("empty downlink for %s", f.ID)
	}
	link := Link{URL: dl.Downlink}
	if dl.Checksum != "" {
		// Checksums are best-effort; a failure here shouldn't block a download.
		if md5, err := fetchMD5(ctx, dl.Checksum); err == nil {
			link.MD5 = md5
		}
	}
	return link, nil
}

func fetchMD5(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := apiHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum: HTTP %d", resp.StatusCode)
	}
	var x struct {
		MD5 string `xml:"md5,attr"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&x); err != nil {
		return "", err
	}
	return strings.ToLower(x.MD5), nil
}

// UserData is the logged-in account as reported by embed.gog.com.
type UserData struct {
	IsLoggedIn bool   `json:"isLoggedIn"`
	Username   string `json:"username"`
	UserID     string `json:"userId"`
	Email      string `json:"email"`
}

// User fetches the account behind the stored token (refreshing it if needed).
func (c *Client) User(ctx context.Context) (*UserData, error) {
	var u UserData
	if err := c.getJSON(ctx, embedBase+"/userData.json", true, &u); err != nil {
		return nil, err
	}
	if !u.IsLoggedIn {
		return nil, errors.New("GOG reports the session as logged out: run `gogloader auth login`")
	}
	return &u, nil
}

// TokenExpiry returns when the current access token expires.
func (c *Client) TokenExpiry() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.ExpiresAt
}
