package gog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// isolate points config dirs at a temp dir and captures warnings.
func isolate(t *testing.T) *[]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("LocalAppData", filepath.Join(home, "local"))
	t.Setenv("AppData", filepath.Join(home, "roaming"))
	t.Setenv(storeEnv, "")
	var warns []string
	old := Warnf
	Warnf = func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { Warnf = old; setActive(nil) })
	return &warns
}

var tok = &Token{AccessToken: "a", RefreshToken: "r", UserID: "u", ExpiresAt: time.Now().Add(time.Hour)}

func TestKeyringPreferredAndFileMigrated(t *testing.T) {
	warns := isolate(t)
	keyring.MockInit()

	// A legacy plaintext file with loose permissions.
	p, _ := TokenPath()
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(`{"access_token":"a","refresh_token":"r","user_id":"u"}`), 0o644))

	got, err := loadToken()
	if err != nil || got.RefreshToken != "r" {
		t.Fatalf("load: %+v %v", got, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plaintext token file should be deleted after migration")
	}
	if s, err := keyring.Get(keyringService, keyringUser); err != nil || !strings.Contains(s, `"r"`) {
		t.Fatalf("keyring: %q %v", s, err)
	}
	if !strings.Contains(TokenLocation(), keyringService) {
		t.Fatalf("location = %q", TokenLocation())
	}
	if !strings.Contains(strings.Join(*warns, "\n"), "moved token") {
		t.Fatalf("expected migration notice, got %v", *warns)
	}

	// Saving again keeps it in the keyring and never recreates the file.
	if err := saveToken(tok, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("save recreated plaintext file")
	}

	if err := Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(keyringService, keyringUser); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("logout left token in keyring")
	}
	if _, err := loadToken(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("after logout: %v", err)
	}
}

func TestFallbackToHardenedFile(t *testing.T) {
	warns := isolate(t)
	keyring.MockInitWithError(errors.New("no D-Bus session"))

	p, _ := TokenPath()
	dir := filepath.Dir(p)
	must(t, os.MkdirAll(dir, 0o755)) // pre-existing, too open
	// A stale temp-looking file with loose perms must not leak its mode.
	must(t, os.WriteFile(p+".tmp", []byte("junk"), 0o644))

	if err := saveToken(tok, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(*warns, "\n"), "no system keyring") {
		t.Fatalf("expected fallback notice, got %v", *warns)
	}
	if TokenLocation() != p {
		t.Fatalf("location = %q, want %q", TokenLocation(), p)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Fatalf("token dir mode %v", fi.Mode().Perm())
		}
		// Loosened later? Load tightens it again.
		must(t, os.Chmod(p, 0o644))
		if _, err := loadToken(); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Fatalf("load did not tighten mode: %v", fi.Mode().Perm())
		}
	}
	if err := Logout(); err != nil {
		t.Fatalf("logout with unavailable keyring should succeed: %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("logout left token file")
	}
}

func TestForcedKeyringModeDoesNotFallBack(t *testing.T) {
	isolate(t)
	t.Setenv(storeEnv, "keyring")
	keyring.MockInitWithError(errors.New("locked"))
	if err := saveToken(tok, true); err == nil {
		t.Fatal("expected error when keyring is forced but unavailable")
	}
	p, _ := TokenPath()
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("forced keyring mode wrote a plaintext file")
	}
}

func TestRefusesSymlinkTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	isolate(t)
	t.Setenv(storeEnv, "file")
	p, _ := TokenPath()
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	must(t, os.WriteFile(target, []byte(`{}`), 0o600))
	must(t, os.Symlink(target, p))
	if _, err := loadToken(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}

func TestBadStoreEnv(t *testing.T) {
	isolate(t)
	t.Setenv(storeEnv, "plaintext")
	if _, err := loadToken(); err == nil {
		t.Fatal("expected error for invalid store mode")
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
