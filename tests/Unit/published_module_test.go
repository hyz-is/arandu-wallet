package unit_test

import (
	"os/exec"
	"strings"
	"testing"

	wallet "github.com/hyz-is/arandu-wallet"
)

// TestTheArchiveSurvivesBeingPublished reads the module the way the proxy packs
// it, rather than the way a build here reads it.
//
// Every other gate compiles this repository, where the files are present. The
// go command drops any path with a segment named "vendor" when it packs a
// module, so a tree kept under one is in the repository and absent from what
// anybody downloads: the embed then matches nothing, and the first person to
// import the package gets "pattern resources/...: no matching files found" on a
// build that is green here.
//
// This is the only check that looks at the module from outside itself.
func TestTheArchiveSurvivesBeingPublished(t *testing.T) {
	t.Parallel()
	assertNoVendorSegment(t, packageRoot(t))
}

// assertNoVendorSegment reads the paths the module would publish and refuses one
// the packer would drop.
//
// It walks what git tracks rather than the working tree, because an untracked
// file is not published either and would report a difference that does not
// exist.
func assertNoVendorSegment(t *testing.T, root string) {
	t.Helper()

	list := exec.Command("git", "ls-files", "resources")
	list.Dir = root
	out, err := list.Output()
	if err != nil {
		t.Skipf("git is unavailable, so what the packer would carry cannot be read: %v", err)
	}

	var dropped []string
	var carried int
	for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if path == "" {
			continue
		}
		carried++
		for _, segment := range strings.Split(path, "/") {
			if segment == "vendor" {
				dropped = append(dropped, path)
				break
			}
		}
	}
	if carried == 0 {
		t.Fatal("no resource file is tracked, so this gate read nothing")
	}
	if len(dropped) > 0 {
		t.Fatalf("go mod drops every path with a vendor segment, so these are in the repository and not in the published module:\n\t%s\n"+
			"An import of this package then fails on the embed, and no gate that compiles this repository would say so.",
			strings.Join(dropped, "\n\t"))
	}
}

// TestEveryTrackedViewSourceIsPublished holds the embed pattern to the tree it
// is meant to carry.
//
// The pattern names the sources rather than the directory holding them, so that
// what the view compiler writes beside them here cannot reach a project. What
// naming files costs is reach: a source the pattern does not match is in the
// repository, in the published module, and in no publication -- a screen the
// handler renders by a name nothing registered, which is a 500 on the page and
// silence everywhere before it.
func TestEveryTrackedViewSourceIsPublished(t *testing.T) {
	t.Parallel()

	list := exec.Command("git", "ls-files", "resources")
	list.Dir = packageRoot(t)
	out, err := list.Output()
	if err != nil {
		t.Skipf("git is unavailable, so what the module carries cannot be read: %v", err)
	}

	var sources []string
	for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasSuffix(path, ".kyse.go") {
			sources = append(sources, path)
		}
	}
	if len(sources) == 0 {
		t.Fatal("no view source is tracked, so this gate read nothing")
	}

	published := wallet.PublishedPaths()
	for _, source := range sources {
		name := source[strings.LastIndexByte(source, '/')+1:]
		found := false
		for _, path := range published {
			if strings.HasSuffix(path, "/"+name) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is tracked and published nowhere: the embed pattern does not reach it", source)
		}
	}
	if len(published) != len(sources) {
		t.Errorf("%d view source(s) tracked and %d published", len(sources), len(published))
	}
}
