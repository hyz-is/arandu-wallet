package unit_test

import (
	"os/exec"
	"strings"
	"testing"
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
