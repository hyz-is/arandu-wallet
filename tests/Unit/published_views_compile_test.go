package unit_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arandu-io/framework/foundation"
)

// viewCompiler is the command line whose view compiler the published views are
// held against, at the release an application builds them with.
//
// It is a version and never "latest", so this test changes its answer when this
// line changes and never because somebody published something. Moving it is
// how a stricter compiler is taken on: raise it, run the suite, and fix the
// view it refuses here rather than in every application that publishes it.
const viewCompiler = "github.com/arandu-io/aru@v0.72.0"

// TestEveryPublishedViewCompiles publishes the views into a project of their
// own, builds them with the view compiler an application runs, and compiles
// what it wrote.
//
// Nothing else in this repository reads the markup. The sources open with a
// build tag, so go build, go vet and go test never look past it, and the view
// compiler refuses what the Go compiler would accept -- a value written into an
// address behind text it cannot read, a value in a position no escape covers.
// A view it refuses is a view that stops the build of every application that
// publishes it, and without this test the first place to say so is somebody
// else's terminal.
//
// The project is laid out as an application's is once it has published: each
// file where the publication writes it, and a go.mod that resolves this package
// to the checkout under test. What the view compiler writes then goes through
// the Go compiler, which is what catches a field the markup names and the page
// data does not have.
//
// It does not skip. A gate that steps aside when its input is missing is how
// the views stopped compiling with nothing here saying so.
func TestEveryPublishedViewCompiles(t *testing.T) {
	root := packageRoot(t)
	project := t.TempDir()

	publications, err := foundation.Publications(module(t))
	if err != nil {
		t.Fatalf("reading what the module publishes: %v", err)
	}
	var published int
	for _, publication := range publications {
		from := publication.From
		if from == "" {
			from = "."
		}
		err := fs.WalkDir(publication.Files, from, func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			relative := name
			if from != "." {
				relative = strings.TrimPrefix(strings.TrimPrefix(name, from), "/")
			}
			body, err := fs.ReadFile(publication.Files, name)
			if err != nil {
				return err
			}
			target := filepath.Join(project, filepath.FromSlash(path.Join(publication.To, relative)))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			published++
			return os.WriteFile(target, body, 0o644)
		})
		if err != nil {
			t.Fatalf("publishing the %s files: %v", publication.Tag, err)
		}
	}
	if published == 0 {
		t.Fatal("the module publishes no file, so this gate would compile nothing")
	}

	// The application's go.mod names this package and points it at the
	// checkout, so the views compile against the code beside them rather than
	// against a release. The sums come from here: the application needs no
	// module this package does not already require.
	goMod := readReleaseFile(t, root, "go.mod")
	language := captureReleaseValue(t, goMod, `(?m)^go ([0-9.]+)$`, "go directive in go.mod")
	modulePath := captureReleaseValue(t, goMod, `(?m)^module (\S+)$`, "module path in go.mod")
	manifest := "module example.com/application\n\n" +
		"go " + language + "\n\n" +
		"require " + modulePath + " v0.0.0-00010101000000-000000000000\n\n" +
		"replace " + modulePath + " => " + strconv.Quote(root) + "\n"
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.sum"), []byte(readReleaseFile(t, root, "go.sum")), 0o644); err != nil {
		t.Fatal(err)
	}

	inProject(t, project, "the published views do not compile with "+viewCompiler+", so no application that publishes them can build",
		"go", "run", viewCompiler, "view:build")
	inProject(t, project, "the views "+viewCompiler+" compiled do not build against this package",
		"go", "build", "./...")
}

// inProject runs one command in the scratch project, outside any workspace,
// and fails with what it printed.
//
// -mod=mod lets the go command fill in the requirements the application's
// go.mod leaves to this package's, which is what an application's own `go mod
// tidy` would have written.
func inProject(t *testing.T, dir, failure string, name string, args ...string) {
	t.Helper()

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS="+strings.TrimSpace(os.Getenv("GOFLAGS")+" -mod=mod"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s:\n$ %s %s\n%s", failure, name, strings.Join(args, " "), output)
	}
}
