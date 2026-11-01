// Package download fetches multipart installers in parallel with resume
// support and md5 verification.
package download

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stvnksslr/gogloader/internal/ui"
)

// Link is a resolved download URL and optional expected md5.
type Link struct {
	URL string
	MD5 string
}

// Part is one file to download. Resolve is called before every attempt
// because CDN links are signed and expire.
type Part struct {
	Name    string // display name / fallback filename
	Size    int64  // approximate size (for progress only; GOG's API rounds it)
	Resolve func(ctx context.Context) (Link, error)
}

// Downloader downloads parts into a directory.
type Downloader struct {
	Parallel int
	Retries  int
	Client   *http.Client
	Progress io.Writer // nil disables progress output
}

// Fetch downloads all parts into dir and returns their local paths in the
// same order as parts.
func (d *Downloader) Fetch(ctx context.Context, dir string, parts []Part) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	parallel := max(d.Parallel, 1)
	retries := max(d.Retries, 1)
	client := d.Client
	if client == nil {
		client = &http.Client{}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	prog := newProgress(parts)
	stopProg := prog.run(d.Progress)

	paths := make([]string, len(parts))
	errs := make([]error, len(parts))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := range parts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()

			var err error
			for attempt := 1; attempt <= retries; attempt++ {
				paths[i], err = d.fetchOne(ctx, client, dir, parts[i], &prog.done[i], &prog.size[i])
				if err == nil || ctx.Err() != nil {
					break
				}
				prog.logf("%s: attempt %d/%d failed: %v", parts[i].Name, attempt, retries, err)
				select {
				case <-time.After(time.Duration(attempt) * 2 * time.Second):
				case <-ctx.Done():
				}
			}
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", parts[i].Name, err)
				cancel()
				return
			}
			prog.complete.Add(1)
		}(i)
	}
	wg.Wait()
	stopProg()

	if err := errors.Join(filterCanceled(errs)...); err != nil {
		return nil, err
	}
	return paths, nil
}

// filterCanceled drops context.Canceled errors caused by a sibling failure so
// the real error is reported, unless cancellation is all there is.
func filterCanceled(errs []error) []error {
	var real, canceled []error
	for _, e := range errs {
		switch {
		case e == nil:
		case errors.Is(e, context.Canceled):
			canceled = append(canceled, e)
		default:
			real = append(real, e)
		}
	}
	if len(real) > 0 {
		return real
	}
	if len(canceled) > 0 {
		return canceled[:1]
	}
	return nil
}

func (d *Downloader) fetchOne(ctx context.Context, client *http.Client, dir string, p Part, done, size *atomic.Int64) (string, error) {
	link, err := p.Resolve(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve link: %w", err)
	}
	name := FilenameFromURL(link.URL, p.Name)
	dst := filepath.Join(dir, name)
	partial := dst + ".part"

	// Already complete from an earlier run? Files are only renamed from
	// .part once fully downloaded, so existence means complete; still
	// re-verify when a checksum is available.
	if st, err := os.Stat(dst); err == nil {
		done.Store(st.Size())
		size.Store(st.Size())
		if err := verifyMD5(dst, link.MD5); err == nil {
			return dst, nil
		}
		_ = os.Remove(dst)
		done.Store(0)
	}

	if err := download(ctx, client, link.URL, partial, done, size); err != nil {
		return "", err
	}
	if err := verifyMD5(partial, link.MD5); err != nil {
		_ = os.Remove(partial)
		done.Store(0)
		return "", err
	}
	if err := os.Rename(partial, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func download(ctx context.Context, client *http.Client, rawURL, partial string, done, size *atomic.Int64) error {
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	done.Store(offset)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var total int64 = -1
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Content-Range: bytes start-end/total
		cr := resp.Header.Get("Content-Range")
		var start, end int64
		if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total); err != nil {
			total = -1
		} else if start != offset {
			return fmt.Errorf("server returned range starting at %d, wanted %d", start, offset)
		}
	case http.StatusOK:
		total = resp.ContentLength
		// Server ignored the range; restart from zero.
		if offset > 0 {
			if err := f.Truncate(0); err != nil {
				return err
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			done.Store(0)
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// Content-Range: bytes */total
		var t int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes */%d", &t); err == nil && t == offset {
			size.Store(t)
			return nil // already complete
		}
		_ = f.Truncate(0)
		done.Store(0)
		return errors.New("stale partial file, restarting")
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if total > 0 {
		size.Store(total)
	}
	w := &countingWriter{w: f, n: done}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return err
	}
	if total > 0 {
		if got := done.Load(); got != total {
			return fmt.Errorf("short download: got %d bytes, want %d", got, total)
		}
	}
	return f.Sync()
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

func verifyMD5(file, want string) error {
	if want == "" {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("md5 mismatch for %s: got %s, want %s", filepath.Base(file), got, want)
	}
	return nil
}

// FilenameFromURL derives a safe local filename from a CDN URL.
// The result is always a single path element, the same on every OS.
func FilenameFromURL(rawURL, fallback string) string {
	seg := ""
	if u, err := url.Parse(rawURL); err == nil {
		seg = path.Base(u.Path)
		if un, err := url.PathUnescape(seg); err == nil {
			seg = un
		}
	}
	if name := safeName(seg); name != "" {
		return name
	}
	if name := safeName(fallback); name != "" {
		return name
	}
	return "download"
}

// safeName reduces s to its last path element (treating both / and \ as
// separators, whatever the host OS) and replaces characters that Windows
// does not allow in file names. It returns "" if nothing usable is left.
func safeName(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, s)
	// Windows silently drops trailing dots and spaces.
	s = strings.TrimRight(s, ". ")
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}

// ---------------------------------------------------------------------------
// Progress

type progress struct {
	size []atomic.Int64 // estimated, replaced by the real size once known
	done []atomic.Int64
	mu   sync.Mutex
	out  io.Writer
	st   ui.Styler
	// complete counts parts fully downloaded.
	complete atomic.Int32
}

func newProgress(parts []Part) *progress {
	p := &progress{done: make([]atomic.Int64, len(parts)), size: make([]atomic.Int64, len(parts))}
	for i, pt := range parts {
		p.size[i].Store(pt.Size)
	}
	return p
}

func (p *progress) sum() int64 {
	var s int64
	for i := range p.done {
		s += p.done[i].Load()
	}
	return s
}

func (p *progress) total() int64 {
	var s int64
	for i := range p.size {
		s += max(p.size[i].Load(), p.done[i].Load())
	}
	return s
}

func (p *progress) logf(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	if p.st.Enabled() {
		fmt.Fprint(p.out, "\r\033[K")
	}
	fmt.Fprintln(p.out, p.st.Yellow("! ")+fmt.Sprintf(format, args...))
}

func (p *progress) run(out io.Writer) (stop func()) {
	if out == nil {
		return func() {}
	}
	p.out = out
	p.st = ui.For(out)
	// Redraw in place on a terminal; otherwise (logs, pipes) emit a plain
	// line now and then.
	interval := 250 * time.Millisecond
	if !p.st.Enabled() {
		interval = 10 * time.Second
	}
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(interval)
		defer t.Stop()
		start := time.Now()
		last, lastT := p.sum(), start
		startBytes := last
		var speed float64
		for {
			select {
			case <-quit:
				p.summary(time.Since(start), p.sum()-startBytes)
				return
			case now := <-t.C:
				cur := p.sum()
				inst := float64(cur-last) / now.Sub(lastT).Seconds()
				if speed == 0 {
					speed = inst
				} else {
					speed = 0.8*speed + 0.2*inst
				}
				last, lastT = cur, now
				p.draw(speed)
			}
		}
	}()
	return func() { close(quit); <-finished }
}

const barWidth = 28

func (p *progress) draw(speed float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur, total := p.sum(), p.total()
	frac := 0.0
	if total > 0 {
		frac = min(float64(cur)/float64(total), 1)
	}
	eta := "--"
	if speed > 0 && total > cur {
		eta = formatDuration(time.Duration(float64(total-cur) / speed * float64(time.Second)))
	}
	parts := fmt.Sprintf("%d/%d parts", p.complete.Load(), len(p.done))
	stats := fmt.Sprintf("%5.1f%%  %s / %s  %s/s  ETA %s  %s",
		frac*100, HumanBytes(cur), HumanBytes(total), HumanBytes(int64(speed)), eta, parts)

	if !p.st.Enabled() {
		fmt.Fprintln(p.out, stats)
		return
	}
	filled := int(frac * barWidth)
	bar := p.st.Cyan(strings.Repeat("█", filled)) + p.st.Dim(strings.Repeat("░", barWidth-filled))
	fmt.Fprintf(p.out, "\r\033[K  %s %s", bar, stats)
}

func (p *progress) summary(elapsed time.Duration, transferred int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st.Enabled() {
		fmt.Fprint(p.out, "\r\033[K")
	}
	n := p.complete.Load()
	if int(n) != len(p.done) {
		fmt.Fprintf(p.out, "  %s %d/%d parts finished\n", p.st.Red("✗"), n, len(p.done))
		return
	}
	line := fmt.Sprintf("%s in %s", HumanBytes(p.sum()), ui.Plural(len(p.done), "part"))
	if transferred <= 0 {
		line += ", already downloaded"
	} else {
		line += fmt.Sprintf(", %s", formatDuration(elapsed))
		if s := elapsed.Seconds(); s >= 1 {
			line += fmt.Sprintf(" (%s/s)", HumanBytes(int64(float64(transferred)/s)))
		}
	}
	fmt.Fprintf(p.out, "  %s %s\n", p.st.Green("✓"), line)
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// HumanBytes formats a byte count, e.g. 1.5 GiB.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
