package project

import "testing"

func TestNormalizeRemoteMatrix(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"git@GitHub.COM:Org/Repo.git":       "github.com/Org/Repo",
		"https://GitHub.com/Org/Repo.git/":  "github.com/Org/Repo",
		"ssh://git@GitHub.com/Org/Repo.git": "github.com/Org/Repo",
		"file:///tmp/repo.git":              "",
		"/tmp/repo.git":                     "",
		"C:\\Users\\Name\\repo.git":         "",
		"":                                  "",
	}
	for input, want := range tests {
		if got := NormalizeRemote(input); got != want {
			t.Fatalf("NormalizeRemote(%q) = %q, want %q", input, got, want)
		}
	}
}
