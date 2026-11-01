package gog

import "testing"

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.gog.com/en/game/kenshi":      "kenshi",
		"https://www.gog.com/game/kenshi?pp=1":    "kenshi",
		"gog.com/de/game/the_witcher_3_wild_hunt": "the_witcher_3_wild_hunt",
		"kenshi": "kenshi",
	} {
		if got := NormalizeName(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}
