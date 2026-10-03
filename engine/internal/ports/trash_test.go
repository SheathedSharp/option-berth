package ports

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInTrashIsAboutWhereNotWhat pins the fact this is: a path either starts
// under a trash root or it does not.
//
// The case that prompted it is real — a redis-server whose working directory is
// `~/.Trash/bookkeeping-acceptance/redis`, still holding port 16379 for a
// project that no longer exists. The point of the check is not to classify it
// (a redis in the trash is still a redis) but to be able to say "this one can
// be killed", which the default `list` table cannot show because it has no
// directory column.
func TestInTrashIsAboutWhereNotWhat(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to build paths from")
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"the trash itself", filepath.Join(home, ".Trash"), true},
		{"a deleted project", filepath.Join(home, ".Trash", "bookkeeping-acceptance", "redis"), true},
		{"the freedesktop layout", filepath.Join(home, ".local", "share", "Trash", "files", "x"), true},
		{"a mounted volume's trash", "/Volumes/Backup/.Trashes/501/proj/data", true},
		{"the trash volume's own trash", "/Volumes/Backup/.Trashes/501", true},

		{"a project directory", filepath.Join(home, "code", "shop"), false},
		{"a directory called Trash", filepath.Join(home, "code", "Trash"), false},
		{"a directory with trash in the name", filepath.Join(home, "code", "trashy"), false},
		{"something else on a volume", "/Volumes/Backup/projects/shop", false},
		{"a relative path", "some/where", false},
		{"nothing", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InTrash(tc.path); got != tc.want {
				t.Errorf("InTrash(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestInTrashDoesNotMatchAPrefixOfAName: `~/.Trashy` is not in the trash, and a
// prefix check without a separator would say it is.
func TestInTrashDoesNotMatchAPrefixOfAName(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to build paths from")
	}

	for _, name := range []string{".Trashy", ".Trash-old", ".Trashx"} {
		if InTrash(filepath.Join(home, name, "proj")) {
			t.Errorf("InTrash treated %s/ as the trash", name)
		}
	}
}
