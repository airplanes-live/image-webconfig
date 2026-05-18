package main

import "testing"

// TestResolveVersion locks in the contract that resolveVersion appends a
// short commit-SHA suffix when commitSha is stamped at link time, and is a
// no-op otherwise. The suffix is what makes /health byte-change after a
// dev→dev self-update (the moving "dev-latest" tag is reused across builds);
// without it, the SPA's post-update poller never observes a /health change
// and times out at 90s — see web/assets/app.js:webconfigUpdateProgress.
func TestResolveVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		version   string
		commitSha string
		want      string
	}{
		{"no commit sha → version as-is", "dev", "", "dev"},
		{"tag + 40-char sha → 7-char suffix", "dev-latest", "2f50bfe6f9129bffcc9ab1ece9ad257c5463f91f", "dev-latest+2f50bfe"},
		{"tag + exactly 7 chars → full sha", "v0.1.2", "abc1234", "v0.1.2+abc1234"},
		{"tag + 5 chars → use what we have", "v0.1.2", "abcde", "v0.1.2+abcde"},
	}
	origVersion := version
	origCommit := commitSha
	t.Cleanup(func() {
		version = origVersion
		commitSha = origCommit
	})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			version = c.version
			commitSha = c.commitSha
			if got := resolveVersion(); got != c.want {
				t.Errorf("resolveVersion() = %q, want %q", got, c.want)
			}
		})
	}
}
