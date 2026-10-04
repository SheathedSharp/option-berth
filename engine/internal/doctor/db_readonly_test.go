package doctor

import (
	"bytes"
	"database/sql"
	"fmt"
	"github.com/sheathedsharp/option-berth/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorNeverRepairsCorruptDatabase(t *testing.T) {
	env := fakeEnv(t)
	before := []byte("synthetic invalid sqlite database\n")
	write(t, env.DBPath, string(before))
	got := run(t, env, checkDBOK)
	after, err := os.ReadFile(env.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("diagnosis replaced the original database")
	}
	matches, _ := filepath.Glob(env.DBPath + ".corrupt-*")
	if len(matches) != 0 {
		t.Fatal("diagnosis renamed the original database")
	}
	wantStatus(t, got, StatusFail)
}

func TestDoctorDoesNotMigrateSchema(t *testing.T) {
	for _, version := range []int{1, store.LatestVersion() + 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			env := fakeEnv(t)
			if err := os.MkdirAll(filepath.Dir(env.DBPath), 0700); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", env.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`CREATE TABLE schema_version(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO schema_version VALUES(?, 'fixture', 'synthetic')`, version); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(env.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			got := run(t, env, checkDBOK)
			after, err := os.ReadFile(env.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("diagnosis migrated or changed the database")
			}
			want := StatusWarn
			if version > store.LatestVersion() {
				want = StatusFail
			}
			wantStatus(t, got, want)
		})
	}
}
