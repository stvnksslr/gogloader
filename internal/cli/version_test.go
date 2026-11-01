package cli

import "testing"

func TestBuildInfoString(t *testing.T) {
	cases := []struct {
		in   BuildInfo
		want string
	}{
		{BuildInfo{Version: "0.1.0"}, "0.1.0"},
		{BuildInfo{Version: "0.1.0", Commit: "46695fd524b889cc2e534f365bf3dec061c4fe7c", Date: "2026-11-01T12:00:00Z"},
			"0.1.0 (46695fd524b8, 2026-11-01T12:00:00Z)"},
		{BuildInfo{Version: "0.1.0", Commit: "abc"}, "0.1.0 (abc)"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("%+v: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestResolveKeepsInjectedValues(t *testing.T) {
	in := BuildInfo{Version: "1.2.3", Commit: "c0ffee", Date: "2026-01-01T00:00:00Z"}
	if got := in.resolve(); got != in {
		t.Fatalf("resolve overrode ldflags values: %+v", got)
	}
	if got := (BuildInfo{}).resolve(); got.Version == "" {
		t.Fatal("empty version should resolve to something")
	}
}
