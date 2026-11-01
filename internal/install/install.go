// Package install unpacks / installs downloaded GOG installers.
//
//   - windows (.exe + .bin parts): runs the Inno Setup installer silently on
//     Windows; elsewhere extracts it with innoextract (or runs it under wine).
//   - linux (.sh makeself/mojosetup): extracts the embedded zip's
//     data/noarch payload, no interaction needed, works on any host.
//   - mac (.pkg): expands with pkgutil and copies the payload (macOS only).
package install

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Options controls an install.
type Options struct {
	OS      string   // installer platform: windows, mac, linux
	Files   []string // all downloaded parts of one installer
	Dest    string   // target directory
	Log     io.Writer
	UseWine bool // on non-Windows hosts, run the real installer under wine instead of innoextract
}

// Install installs one installer into opts.Dest.
func Install(ctx context.Context, opts Options) error {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	dest, err := filepath.Abs(opts.Dest)
	if err != nil {
		return err
	}
	opts.Dest = dest
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	switch opts.OS {
	case "windows":
		return installWindows(ctx, opts)
	case "linux":
		return installLinux(opts)
	case "mac":
		return installMac(ctx, opts)
	default:
		return fmt.Errorf("unsupported installer os %q", opts.OS)
	}
}

func findBySuffix(files []string, suffix string) (string, error) {
	for _, f := range files {
		if strings.EqualFold(filepath.Ext(f), suffix) {
			return f, nil
		}
	}
	return "", fmt.Errorf("no %s file among downloaded parts", suffix)
}

func run(ctx context.Context, log io.Writer, name string, args ...string) error {
	fmt.Fprintf(log, "$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	return cmd.Run()
}

// ---------------------------------------------------------------------------
// Windows (Inno Setup, multipart .exe + .bin)

func installWindows(ctx context.Context, o Options) error {
	exe, err := findBySuffix(o.Files, ".exe")
	if err != nil {
		return err
	}
	// Inno Setup locates the .bin parts next to the .exe automatically.
	innoArgs := func(dir string) []string {
		return []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-", "/NOICONS", "/DIR=" + dir}
	}

	if runtime.GOOS == "windows" {
		return run(ctx, o.Log, exe, innoArgs(o.Dest)...)
	}
	if o.UseWine {
		wine, err := exec.LookPath("wine")
		if err != nil {
			return errors.New("wine not found in PATH")
		}
		winDir := "Z:" + strings.ReplaceAll(o.Dest, "/", `\`)
		return run(ctx, o.Log, wine, append([]string{exe}, innoArgs(winDir)...)...)
	}

	inno, err := exec.LookPath("innoextract")
	if err != nil {
		return errors.New("innoextract not found: install it (e.g. `brew install innoextract` / `apt install innoextract`) or pass --wine")
	}
	tmp, err := os.MkdirTemp(filepath.Dir(o.Dest), ".gogloader-extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := run(ctx, o.Log, inno, "--gog", "--output-dir", tmp, exe); err != nil {
		return fmt.Errorf("innoextract: %w", err)
	}
	// innoextract puts {app} files under "app/"; flatten that into dest.
	src := tmp
	if st, err := os.Stat(filepath.Join(tmp, "app")); err == nil && st.IsDir() {
		src = filepath.Join(tmp, "app")
	}
	return mergeMove(src, o.Dest)
}

// ---------------------------------------------------------------------------
// Linux (.sh = shell stub + zip; game files live under data/noarch/)

func installLinux(o Options) error {
	sh, err := findBySuffix(o.Files, ".sh")
	if err != nil {
		return err
	}
	f, err := os.Open(sh)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	// archive/zip handles the prepended shell script by computing the base offset.
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return fmt.Errorf("read embedded zip in %s: %w", filepath.Base(sh), err)
	}
	const prefix = "data/noarch/"
	n := 0
	for _, zf := range zr.File {
		if !strings.HasPrefix(zf.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(zf.Name, prefix)
		if rel == "" {
			continue
		}
		target, err := safeJoin(o.Dest, rel)
		if err != nil {
			return err
		}
		if err := extractZipFile(zf, target); err != nil {
			return fmt.Errorf("extract %s: %w", zf.Name, err)
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("%s contains no %s payload", filepath.Base(sh), prefix)
	}
	fmt.Fprintf(o.Log, "extracted %d entries\n", n)
	return nil
}

func safeJoin(root, rel string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(rel))
	if r, err := filepath.Rel(root, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path in archive: %q", rel)
	}
	return target, nil
}

func extractZipFile(zf *zip.File, target string) error {
	mode := zf.Mode()
	switch {
	case mode.IsDir():
		return os.MkdirAll(target, 0o755)
	case mode&os.ModeSymlink != 0:
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		link, err := io.ReadAll(io.LimitReader(rc, 4096))
		rc.Close()
		if err != nil {
			return err
		}
		ln := string(link)
		if filepath.IsAbs(ln) {
			return fmt.Errorf("refusing absolute symlink %q", ln)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		_ = os.Remove(target)
		return os.Symlink(ln, target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ---------------------------------------------------------------------------
// macOS (.pkg)

func installMac(ctx context.Context, o Options) error {
	if runtime.GOOS != "darwin" {
		return errors.New("mac .pkg installers can only be installed on macOS")
	}
	pkg, err := findBySuffix(o.Files, ".pkg")
	if err != nil {
		return err
	}
	parent, err := os.MkdirTemp(filepath.Dir(o.Dest), ".gogloader-pkg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	tmp := filepath.Join(parent, "x") // pkgutil requires the target not to exist
	if err := run(ctx, o.Log, "pkgutil", "--expand-full", pkg, tmp); err != nil {
		return fmt.Errorf("pkgutil: %w", err)
	}
	var payloads []string
	err = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "Payload" {
			payloads = append(payloads, p)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(payloads) == 0 {
		return errors.New("no Payload found inside pkg")
	}
	for _, p := range payloads {
		if err := mergeMove(p, o.Dest); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------

// mergeMove moves the contents of src into dst, merging directories and
// overwriting files (so DLC installs layer on top of the base game).
func mergeMove(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		info, err := os.Lstat(d)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Rename(s, d); err != nil {
				return err
			}
		case err != nil:
			return err
		case e.IsDir() && info.IsDir():
			if err := mergeMove(s, d); err != nil {
				return err
			}
		default:
			if err := os.RemoveAll(d); err != nil {
				return err
			}
			if err := os.Rename(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}
