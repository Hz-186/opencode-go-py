package pathx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalResolvesSymlinksUnicodeAndSpaces(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "中文 Project")
	target := filepath.Join(root, "Real Directory")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("make target: %v", err)
	}
	link := filepath.Join(root, "Linked Directory")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("make symlink: %v", err)
	}

	got, err := Canonical(link, CaseSensitive)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("canonical path = %q, want %q", got, filepath.Clean(want))
	}
}

func TestContainsUsesPathBoundariesAndExplicitCaseMode(t *testing.T) {
	t.Parallel()

	base := filepath.Join(string(filepath.Separator), "Users", "Name", "Project")
	if !Contains(base, filepath.Join(base, "src", "main.go"), CaseSensitive) {
		t.Fatal("child path was not contained")
	}
	if Contains(base, base+"-other", CaseSensitive) {
		t.Fatal("prefix sibling was treated as contained")
	}
	caseOnly := filepath.Join(string(filepath.Separator), "users", "name", "project", "src")
	if Contains(base, caseOnly, CaseSensitive) {
		t.Fatal("case-only path matched in sensitive mode")
	}
	if !Contains(base, caseOnly, CaseInsensitive) {
		t.Fatal("case-only path did not match in insensitive mode")
	}
}

func TestPortablePathMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		platform Platform
		input    string
		want     string
	}{
		{name: "linux preserves case", platform: Linux, input: "/Home/User/Project/../Repo", want: "/Home/User/Repo"},
		{name: "mac folds case", platform: Darwin, input: "/Users/Name/Project/../Repo", want: "/users/name/repo"},
		{name: "windows separators and case", platform: Windows, input: `C:\Users\Name\Project\..\Repo`, want: "c:/users/name/repo"},
		{name: "windows UNC", platform: Windows, input: `\\Server\Share\Folder\..\Repo`, want: "//server/share/repo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PortableKey(test.platform, test.input); got != test.want {
				t.Fatalf("PortableKey(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestCanonicalReportsSymlinkLoop(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	if err := os.Symlink(right, left); err != nil {
		t.Fatalf("make left symlink: %v", err)
	}
	if err := os.Symlink(left, right); err != nil {
		t.Fatalf("make right symlink: %v", err)
	}
	if _, err := Canonical(left, CaseSensitive); !errors.Is(err, ErrSymlink) {
		t.Fatalf("canonical loop error = %v, want ErrSymlink", err)
	}
}
