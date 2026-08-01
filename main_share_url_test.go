package main

import "testing"

func TestLooksLikeShareURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://s.share2.us/J957vJ9_VTBFnHLvjjl2Zw", true},
		{"http://s.share2.us/abc", true},
		{"https://s.staging.share2.us/abc", true},
		{"https://s.share2.us/abc#key", true},
		{"https://s.share2.us/", false},        // no code
		{"https://s.share2.us", false},         // no path
		{"https://share2.us/abc", false},       // not the s. host
		{"https://app.share2.us/abc", false},   // different subdomain
		{"https://evil.com/s.share2.us", false},// share host only in path
		{"s.share2.us/abc", false},             // no scheme
		{"./localfile.txt", false},
		{"report.log", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeShareURL(c.in); got != c.want {
			t.Errorf("looksLikeShareURL(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
