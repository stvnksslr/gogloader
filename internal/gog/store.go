package gog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
)

// Token storage.
//
// The token is kept in the OS credential store when one is available:
//   - macOS:   login Keychain (generic password, service "gogloader")
//   - Windows: Credential Manager (DPAPI-encrypted, per user)
//   - Linux:   Secret Service over D-Bus (GNOME Keyring, KWallet, KeePassXC)
//
// When none is available (headless Linux, no D-Bus session) it falls back to
// a 0600 file in the user config dir. GOGLOADER_TOKEN_STORE=keyring|file
// forces one backend; the default "auto" prefers the keyring and moves an
// existing token file into it.

const (
	keyringService = "gogloader"
	keyringUser    = "oauth-token"
	storeEnv       = "GOGLOADER_TOKEN_STORE"
)

// Warnf reports non-fatal storage notices (fallbacks, migrations,
// permission fixes). The CLI replaces it to match its output style.
var Warnf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "! "+format+"\n", args...)
}

var errNoToken = errors.New("no stored token")

type tokenStore interface {
	describe() string
	load() ([]byte, error) // errNoToken if absent
	save([]byte) error
	remove() error
}

// ---------------------------------------------------------------------------
// OS credential store

type keyringStore struct{}

func (keyringStore) describe() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS Keychain (service " + keyringService + ")"
	case "windows":
		return "Windows Credential Manager (" + keyringService + ")"
	default:
		return "Secret Service keyring (service " + keyringService + ")"
	}
}

func (keyringStore) load() ([]byte, error) {
	s, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, errNoToken
	}
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

func (keyringStore) save(b []byte) error { return keyring.Set(keyringService, keyringUser, string(b)) }

func (keyringStore) remove() error {
	if err := keyring.Delete(keyringService, keyringUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// File fallback

type fileStore struct{ path string }

// TokenPath returns where the fallback token file lives. On Windows this is
// %LocalAppData% rather than the roaming %AppData%, so the token does not
// sync to other machines with a roaming profile.
func TokenPath() (string, error) {
	var dir string
	if runtime.GOOS == "windows" {
		dir = os.Getenv("LocalAppData")
	}
	if dir == "" {
		var err error
		if dir, err = os.UserConfigDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "gogloader", "token.json"), nil
}

func (f fileStore) describe() string { return f.path }

func (f fileStore) load() ([]byte, error) {
	fi, err := os.Lstat(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoToken
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to read token: %s is not a regular file", f.path)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(f.path, 0o600); err != nil {
			return nil, fmt.Errorf("token file %s is readable by other users (%v) and could not be fixed: %w",
				f.path, fi.Mode().Perm(), err)
		}
		Warnf("token file %s had permissions %v; tightened to -rw-------", f.path, fi.Mode().Perm())
	}
	return os.ReadFile(f.path)
}

func (f fileStore) save(b []byte) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		// MkdirAll leaves an existing directory's mode alone.
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	// CreateTemp always creates a new, uniquely named 0600 file, so a stale
	// or planted temp file can't donate looser permissions to the token.
	tmp, err := os.CreateTemp(dir, ".token-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

func (f fileStore) remove() error {
	if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Selection

var (
	activeMu    sync.Mutex
	activeStore tokenStore // the backend the current token was loaded from / saved to
)

func setActive(s tokenStore) {
	activeMu.Lock()
	activeStore = s
	activeMu.Unlock()
}

// TokenLocation describes where the current token is stored, for display.
func TokenLocation() string {
	activeMu.Lock()
	defer activeMu.Unlock()
	if activeStore == nil {
		return "(not stored)"
	}
	return activeStore.describe()
}

func storeMode() (string, error) {
	switch m := os.Getenv(storeEnv); m {
	case "", "auto":
		return "auto", nil
	case "keyring", "file":
		return m, nil
	default:
		return "", fmt.Errorf("%s=%q: want auto, keyring or file", storeEnv, m)
	}
}

func fallbackFile() (fileStore, error) {
	p, err := TokenPath()
	return fileStore{path: p}, err
}

func loadTokenBytes() ([]byte, error) {
	mode, err := storeMode()
	if err != nil {
		return nil, err
	}
	file, err := fallbackFile()
	if err != nil {
		return nil, err
	}
	kr := keyringStore{}

	switch mode {
	case "file":
		b, err := file.load()
		if err == nil {
			setActive(file)
		}
		return b, err
	case "keyring":
		b, err := kr.load()
		if err == nil {
			setActive(kr)
		}
		return b, err
	}

	b, err := kr.load()
	switch {
	case err == nil:
		setActive(kr)
		return b, nil
	case errors.Is(err, errNoToken):
		// Keyring works but is empty: migrate a token file if there is one.
		b, ferr := file.load()
		if ferr != nil {
			return nil, ferr
		}
		if serr := kr.save(b); serr != nil {
			Warnf("could not move token into %s: %v", kr.describe(), serr)
			setActive(file)
			return b, nil
		}
		if rerr := file.remove(); rerr != nil {
			Warnf("token copied to %s but %s could not be deleted: %v", kr.describe(), file.path, rerr)
		} else {
			Warnf("moved token from %s into %s", file.path, kr.describe())
		}
		setActive(kr)
		return b, nil
	default:
		// No usable keyring (e.g. headless Linux): use the file.
		b, ferr := file.load()
		if ferr == nil {
			setActive(file)
		}
		return b, ferr
	}
}

// saveTokenBytes stores b. When announce is set, a fallback to the file
// store is reported (used at login; silent on routine refreshes).
func saveTokenBytes(b []byte, announce bool) error {
	mode, err := storeMode()
	if err != nil {
		return err
	}
	file, err := fallbackFile()
	if err != nil {
		return err
	}
	kr := keyringStore{}

	switch mode {
	case "file":
		if err := file.save(b); err != nil {
			return err
		}
		setActive(file)
		return nil
	case "keyring":
		if err := kr.save(b); err != nil {
			return fmt.Errorf("save token to %s: %w", kr.describe(), err)
		}
		setActive(kr)
		return nil
	}

	if kerr := kr.save(b); kerr == nil {
		setActive(kr)
		// Don't leave an older plaintext copy behind.
		if err := file.remove(); err != nil {
			Warnf("could not delete old token file %s: %v", file.path, err)
		}
		return nil
	} else if announce {
		Warnf("no system keyring available (%v); storing token in %s", kerr, file.path)
	}
	if err := file.save(b); err != nil {
		return err
	}
	setActive(file)
	return nil
}

func removeStoredToken() error {
	file, err := fallbackFile()
	if err != nil {
		return err
	}
	var errs []error
	if mode, _ := storeMode(); mode != "file" {
		kr := keyringStore{}
		// An unavailable keyring can't hold a token, so only report a failed
		// delete when the keyring is reachable (or explicitly selected).
		_, lerr := kr.load()
		if err := kr.remove(); err != nil && (lerr == nil || mode == "keyring") {
			errs = append(errs, fmt.Errorf("delete token from %s: %w", kr.describe(), err))
		}
	}
	errs = append(errs, file.remove())
	setActive(nil)
	return errors.Join(errs...)
}
