package install

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallLinuxShell(t *testing.T) {
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for name, body := range map[string]string{
		"data/noarch/start.sh":      "#!/bin/sh\n",
		"data/noarch/game/data.pak": "pak",
		"scripts/config.lua":        "ignored",
	} {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		must(t, err)
		_, err = w.Write([]byte(body))
		must(t, err)
	}
	must(t, zw.Close())

	dir := t.TempDir()
	sh := filepath.Join(dir, "game_1.0.sh")
	stub := []byte("#!/bin/sh\n# makeself stub\nexit 0\n" + string(bytes.Repeat([]byte("x"), 5000)))
	must(t, os.WriteFile(sh, append(stub, zbuf.Bytes()...), 0o755))

	dest := filepath.Join(dir, "out")
	if err := Install(context.Background(), Options{OS: "linux", Files: []string{sh}, Dest: dest}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "game", "data.pak")); err != nil || string(b) != "pak" {
		t.Fatalf("data.pak: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts")); !os.IsNotExist(err) {
		t.Fatal("non-payload files should not be extracted")
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
