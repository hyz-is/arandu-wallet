package unit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryPublishedViewCompiles builds the generated views, which no other
// gate reaches.
//
// The build output lands under a directory named "vendor", and the go command
// skips any directory with that name at any depth -- so `go build ./...`,
// `go vet ./...` and `go test ./...` all walk past it. A type error in a view
// would surface when somebody opened the page, and nowhere earlier.
//
// The test copies the tree to a path with no such segment and compiles it
// there, which is the only way to put those files in front of a compiler
// without renaming the directory the publishing convention names.
func TestEveryPublishedViewCompiles(t *testing.T) {
	root := packageRoot(t)
	generated := filepath.Join(root, "storage", "framework", "views")
	if _, err := os.Stat(generated); err != nil {
		t.Skip("no views have been built; run aru view:build")
	}

	var sources []string
	err := filepath.WalkDir(generated, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") {
			sources = append(sources, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the generated views: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("the view directory holds no Go file, so this gate compiled nothing")
	}

	// The copy lands inside the module so its imports resolve against the real
	// go.mod. A temporary directory elsewhere is not part of any module, and
	// the compiler refuses it before it reads a single view.
	staging := filepath.Join(root, ".views-compile-check")
	t.Cleanup(func() { _ = os.RemoveAll(staging) })
	if err := os.RemoveAll(staging); err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		relative, err := filepath.Rel(generated, source)
		if err != nil {
			t.Fatal(err)
		}
		// The point of the copy: drop the "vendor" segment the go command skips.
		target := filepath.Join(staging, strings.ReplaceAll(relative, "vendor"+string(filepath.Separator), ""))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = staging
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the published views do not compile, and no other gate would have said so:\n%s", output)
	}
}
