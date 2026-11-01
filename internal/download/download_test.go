package download

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchMultipartResumeAndVerify(t *testing.T) {
	files := map[string][]byte{
		"/setup_game.exe":   bytes.Repeat([]byte("A"), 300_000),
		"/setup_game-1.bin": bytes.Repeat([]byte("B"), 500_000),
		"/setup_game-2.bin": bytes.Repeat([]byte("C"), 200_000),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, r.URL.Path, time.Time{}, bytes.NewReader(b))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// Simulate an interrupted previous run for part 1.
	must(t, os.WriteFile(filepath.Join(dir, "setup_game-1.bin.part"), files["/setup_game-1.bin"][:123_456], 0o644))

	var parts []Part
	for _, name := range []string{"/setup_game.exe", "/setup_game-1.bin", "/setup_game-2.bin"} {
		sum := md5.Sum(files[name])
		link := Link{URL: srv.URL + name + "?token=x", MD5: hex.EncodeToString(sum[:])}
		parts = append(parts, Part{Name: name, Size: 1 << 20, // GOG reports rounded sizes
			Resolve: func(context.Context) (Link, error) { return link, nil }})
	}
	d := &Downloader{Parallel: 2, Retries: 2}
	paths, err := d.Fetch(context.Background(), dir, parts)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		got, _ := os.ReadFile(p)
		want := files["/"+filepath.Base(p)]
		if !bytes.Equal(got, want) {
			t.Fatalf("part %d (%s) content mismatch", i, p)
		}
	}
	// Second run should skip everything (already complete & verified).
	if _, err := d.Fetch(context.Background(), dir, parts); err != nil {
		t.Fatal(err)
	}

	// Bad checksum must fail.
	bad := parts[2]
	bad.Resolve = func(context.Context) (Link, error) {
		return Link{URL: srv.URL + "/setup_game-2.bin", MD5: strings.Repeat("0", 32)}, nil
	}
	os.Remove(paths[2])
	if _, err := d.Fetch(context.Background(), dir, []Part{bad}); err == nil || !strings.Contains(err.Error(), "md5 mismatch") {
		t.Fatalf("expected md5 mismatch, got %v", err)
	}
}

func TestFilenameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://cdn/x/setup_foo_%281.0%29.exe?a=b": "setup_foo_(1.0).exe",
		"https://cdn/x/..%2F..%2Fetc%2Fpasswd":      "passwd",
		"https://cdn/":                              "fb",
	}
	for in, want := range cases {
		if got := FilenameFromURL(in, "fb"); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
