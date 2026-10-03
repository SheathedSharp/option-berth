package groups

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestClaimAncestorIndexTracksManifestRebuilds(t *testing.T) {
	root := tempTree(t)
	child := mkdir(t, root, "child")
	index := NewIndex()
	index.Add(&Config{Name: "outer", Dir: root, Ports: []int{8080}})
	index.Add(&Config{Name: "foreign", Dir: mkdir(t, tempTree(t), "foreign"), Ports: []int{8080}})
	r := newResolutionPass(index)
	p := state.Port{Port: 8080, Cwd: child}
	check := func(want string) {
		t.Helper()
		cfg, service, ok := index.matchPort(p, r.matchClaims)
		expected, es, eok := index.MatchPort(p)
		if !ok || cfg.Name != want || cfg != expected || service != es || ok != eok {
			t.Fatalf("got=%+v; oracle=%+v, want %s", cfg, expected, want)
		}
	}
	check("outer")
	index.Add(&Config{Name: "inner", Dir: child, Ports: []int{8080}})
	check("inner")
	index.Add(&Config{Name: "inner", Dir: child, Ports: []int{9090}})
	check("outer")
	// The preexisting no-cwd ambiguity rule never consults the ancestor index.
	p.Cwd = ""
	if _, _, ok := index.matchPort(p, r.matchClaims); ok {
		t.Fatal("ambiguous pathless port claimed")
	}
}

func TestClaimAncestorIndexPreservesRawPriorityForSymlinks(t *testing.T) {
	root := tempTree(t)
	child := mkdir(t, root, "child")
	alias := filepath.Join(tempTree(t), "very-long-alias-name-to-the-outer-directory")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	x := NewIndex()
	x.Add(&Config{Name: "alias-outer", Dir: alias, Ports: []int{8080}})
	x.Add(&Config{Name: "inner", Dir: child, Ports: []int{8080}})
	p := state.Port{Port: 8080, Cwd: child}
	want, ws, wok := x.MatchPort(p)
	got, gs, gok := x.matchPort(p, newResolutionPass(x).matchClaims)
	if got != want || gs != ws || gok != wok || got.Name != "alias-outer" {
		t.Fatalf("priority differs: %v vs %v", got, want)
	}
}

func TestClaimAncestorIndexHasNoFilesystemWalkDepthCutoff(t *testing.T) {
	root := tempTree(t)
	deep := root
	for i := 0; i < maxWalk+5; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	x := NewIndex()
	x.Add(&Config{Name: "root", Dir: root, Ports: []int{8080}})
	x.Add(&Config{Name: "foreign", Dir: tempTree(t), Ports: []int{8080}})
	p := state.Port{Port: 8080, Cwd: deep}
	got, _, ok := x.matchPort(p, newResolutionPass(x).matchClaims)
	if !ok || got.Name != "root" {
		t.Fatal("lexical ancestor matching inherited the unrelated Locate depth limit")
	}
}

func TestDirectoryKeysAgreeWithPlatformPathEquality(t *testing.T) {
	root := tempTree(t)
	for _, pair := range [][2]string{{"abc", "ABC"}, {"s", "ſ"}, {"K", "K"}, {"σ", "ς"}, {"a", "b"}, {"straße", "STRASSE"}} {
		a, b := filepath.Join(root, pair[0]), filepath.Join(root, pair[1])
		rel, err := filepath.Rel(a, b)
		want := err == nil && rel == "."
		if got := directoryKey(a) == directoryKey(b); got != want {
			t.Fatalf("%q/%q: key equality %v, Rel=%q (%v)", a, b, got, rel, err)
		}
	}
	if runtime.GOOS != "windows" && directoryKey("ſ") != "ſ" {
		t.Fatal("Unix filename was case folded")
	}
	if strings.Contains(directoryKey(root), "\x00") {
		t.Fatal("invalid path key")
	}
}

// The optimized ancestor lookup must agree with the direct containment scan,
// including a rebuild within one pass. Direct matching is the independent
// oracle; no expected outcome is produced by the optimized lookup itself.
func FuzzClaimAncestorIndex(f *testing.F) {
	base := f.TempDir()
	dirs := []string{base}
	for _, name := range []string{"a", "a/b", "a/b/c", "ab", "k", "K", "s", "ſ", "σ", "ς", "a/child with space"} {
		dirs = append(dirs, filepath.Join(base, filepath.FromSlash(name)))
	}
	for _, seed := range [][]byte{{}, {1, 2, 3}, {4, 4, 4, 0, 8}, {9, 2, 10, 3, 11, 7}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128 {
			data = data[:128]
		}
		x := NewIndex()
		for i, b := range data {
			dir := dirs[int(b)%len(dirs)]
			x.Add(&Config{Name: dir, Dir: dir, Services: []Service{{Name: "api", Port: 8080 + i%2}}})
		}
		r := newResolutionPass(x)
		check := func() {
			t.Helper()
			for _, dir := range append([]string{""}, dirs...) {
				for _, port := range []int{8080, 8081, 9999} {
					p := state.Port{Port: port, Cwd: dir}
					want, ws, wok := x.MatchPort(p)
					got, gs, gok := x.matchPort(p, r.matchClaims)
					if want != got || ws != gs || wok != gok {
						t.Fatalf("dir=%q port=%d: indexed=(%+v,%q,%v), direct=(%+v,%q,%v)", dir, port, got, gs, gok, want, ws, wok)
					}
				}
			}
		}
		check()
		x.Add(&Config{Name: "replacement", Dir: base, Ports: []int{8080, 8081}})
		// Another consumer may already have rebuilt the port index.
		x.MatchPort(state.Port{Port: 8081})
		check()
	})
}
