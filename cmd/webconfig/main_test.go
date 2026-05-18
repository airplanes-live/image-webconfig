package main

import "testing"

// TestComposeVersion locks in the contract that composeVersion appends a
// short commit-SHA suffix when sha is non-empty, and is a no-op otherwise.
// The suffix is what makes /health byte-change after a dev→dev self-update
// (the moving "dev-latest" tag is reused across builds); without it, the
// SPA's post-update poller never observes a /health change and times out at
// 90s — see web/assets/app.js:webconfigUpdateProgress.
func TestComposeVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tag  string
		sha  string
		want string
	}{
		{"no sha → tag as-is", "dev", "", "dev"},
		{"40-char sha → 7-char suffix", "dev-latest", "2f50bfe6f9129bffcc9ab1ece9ad257c5463f91f", "dev-latest+2f50bfe"},
		{"exactly 7 chars → full sha", "v0.1.2", "abc1234", "v0.1.2+abc1234"},
		{"5 chars → use what we have", "v0.1.2", "abcde", "v0.1.2+abcde"},
		{"whitespace-only sha → tag as-is", "dev", "  \n", "dev"},
		{"trailing newline in sha → trimmed", "dev-latest", "2f50bfe\n", "dev-latest+2f50bfe"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := composeVersion(c.tag, c.sha); got != c.want {
				t.Errorf("composeVersion(%q, %q) = %q, want %q", c.tag, c.sha, got, c.want)
			}
		})
	}
}
