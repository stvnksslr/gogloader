package cli

import (
	"runtime/debug"
	"strings"
)

// BuildInfo describes the running binary. GoReleaser fills it in through
// ldflags on main.version, main.commit and main.date.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// resolve fills unset fields from the Go module build info, so that
// `go install …@vX.Y.Z` and plain `go build` binaries still report something
// useful.
func (b BuildInfo) resolve() BuildInfo {
	if b.Version == "" {
		b.Version = "dev"
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	if b.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		b.Version = strings.TrimPrefix(bi.Main.Version, "v")
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if b.Commit == "" {
				b.Commit = s.Value
			}
		case "vcs.time":
			if b.Date == "" {
				b.Date = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" && b.Version == "dev" {
				b.Version = "dev+dirty"
			}
		}
	}
	return b
}

func (b BuildInfo) String() string {
	var extra []string
	if b.Commit != "" {
		c := b.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		extra = append(extra, c)
	}
	if b.Date != "" {
		extra = append(extra, b.Date)
	}
	if len(extra) == 0 {
		return b.Version
	}
	return b.Version + " (" + strings.Join(extra, ", ") + ")"
}
