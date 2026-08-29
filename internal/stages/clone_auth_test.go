package stages

import (
	"strings"
	"testing"
)

func TestAuthedRemote(t *testing.T) {
	got, safe := authedRemote("https://github.com/acme/app.git", "tok123")
	if got != "https://x-access-token:tok123@github.com/acme/app.git" {
		t.Fatalf("authed remote = %q", got)
	}
	if strings.Contains(safe, "tok123") {
		t.Fatalf("token leaked in log form: %q", safe)
	}
	if safe != "https://x-access-token:***@github.com/acme/app.git" {
		t.Fatalf("log-safe form = %q", safe)
	}

	if r, s := authedRemote("https://github.com/acme/app.git", ""); r != "https://github.com/acme/app.git" || s != r {
		t.Fatalf("empty token must be unchanged: remote=%q safe=%q", r, s)
	}

	if r, _ := authedRemote("https://gitlab.com/a/b.git", "tok"); r != "https://gitlab.com/a/b.git" {
		t.Fatalf("non-github host must be unchanged: %q", r)
	}

	if r, _ := authedRemote("git@github.com:acme/app.git", "tok"); r != "git@github.com:acme/app.git" {
		t.Fatalf("non-https (ssh) must be unchanged: %q", r)
	}

	if r, _ := authedRemote("http://github.com/acme/app.git", "tok"); r != "http://github.com/acme/app.git" {
		t.Fatalf("non-https (http) must be unchanged: %q", r)
	}
}

func TestIsGitHubHTTPS(t *testing.T) {
	cases := map[string]bool{
		"https://github.com/acme/app.git":   true,
		"https://GitHub.com/acme/app.git":   true,
		"http://github.com/acme/app.git":    false,
		"git@github.com:acme/app.git":       false,
		"https://gitlab.com/acme/app.git":   false,
		"https://git.acme.internal/a/b.git": false,
		"":                                  false,
	}
	for url, want := range cases {
		if got := isGitHubHTTPS(url); got != want {
			t.Errorf("isGitHubHTTPS(%q) = %v, want %v", url, got, want)
		}
	}
}
