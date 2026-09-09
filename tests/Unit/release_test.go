package unit_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTheManifestFrameworkFloorMatchesGoMod(t *testing.T) {
	root := packageRoot(t)
	goMod := readReleaseFile(t, root, "go.mod")
	manifest := readReleaseFile(t, root, "arandu.mod.toml")

	required := captureReleaseValue(t, goMod,
		`(?m)^\s*github\.com/arandu-io/framework v([0-9]+\.[0-9]+)\.[0-9]+\s*$`,
		"Framework version in go.mod")
	declared := captureReleaseValue(t, manifest,
		`(?m)^framework = ">= ([0-9]+\.[0-9]+)"$`,
		"Framework floor in arandu.mod.toml")

	if declared != required {
		t.Fatalf("manifest Framework floor = %s, want %s from go.mod", declared, required)
	}
}

func TestTheReleaseSkillUsesTheManifestFrameworkFloor(t *testing.T) {
	root := packageRoot(t)
	manifest := readReleaseFile(t, root, "arandu.mod.toml")
	skill := readReleaseFile(t, root, ".agents/skills/wallet-release/SKILL.md")
	declared := captureReleaseValue(t, manifest,
		`(?m)^framework = ">= ([0-9]+\.[0-9]+)"$`,
		"Framework floor in arandu.mod.toml")

	want := `framework = ">= ` + declared + `"`
	if !strings.Contains(skill, want) {
		t.Fatalf("release skill does not teach manifest floor %q", want)
	}
}

// releasedChangelog is CHANGELOG.md with the [Unreleased] section removed.
//
// Everything a tag shipped has to be under a version heading. The section above
// the first one is where work waits, and a release that forgets to move it is a
// published version whose own changelog calls its contents unreleased -- which
// is what v0.4.0 of this package did.
func releasedChangelog(t *testing.T) string {
	t.Helper()
	body := readReleaseFile(t, packageRoot(t), "CHANGELOG.md")
	first := regexp.MustCompile(`(?m)^## \[[0-9]`).FindStringIndex(body)
	if first == nil {
		t.Fatal("CHANGELOG.md has no version heading")
	}
	return body[first[0]:]
}

// TestEveryActionIsNamedInAReleasedChangelogEntry is the gate that catches a
// tag pushed without filing what it shipped.
//
// An action is added in the same change that adds the capability behind it, so
// an action still sitting in [Unreleased] means the version that introduced it
// went out undocumented. It is the cheapest signal of that, and it needs no git
// history to read.
func TestEveryActionIsNamedInAReleasedChangelogEntry(t *testing.T) {
	policy := readReleaseFile(t, packageRoot(t), "policy.go")
	released := releasedChangelog(t)

	names := regexp.MustCompile(`(?m)^\t([A-Z][A-Za-z]*) security\.Action = `).FindAllStringSubmatch(policy, -1)
	if len(names) == 0 {
		t.Fatal("policy.go declares no actions")
	}
	for _, name := range names {
		if !strings.Contains(released, "`"+name[1]+"`") {
			t.Errorf("no released changelog entry names %s", name[1])
		}
	}
}

// TestEveryMigrationIsNamedInAReleasedChangelogEntry holds the same for schema.
//
// A migration is the one thing an operator has to run before a version serves,
// so a version that shipped one and did not say so is a version that fails at
// the first request against a column that is not there.
func TestEveryMigrationIsNamedInAReleasedChangelogEntry(t *testing.T) {
	module := readReleaseFile(t, packageRoot(t), "module.go")
	released := releasedChangelog(t)

	ids := regexp.MustCompile(`"([0-9]{8}_[0-9]{4}_[a-z_]+)"`).FindAllStringSubmatch(module, -1)
	if len(ids) == 0 {
		t.Fatal("module.go declares no migrations")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id[1]] {
			continue
		}
		seen[id[1]] = true
		if !strings.Contains(released, id[1]) {
			t.Errorf("no released changelog entry names migration %s", id[1])
		}
	}
}

// TestEveryChangelogVersionHasUpgradeNotes keeps the two files describing the
// same set of releases.
//
// They drifted once in the other direction: this repository carried three
// changelog sections and two upgrade sections belonging to the package template
// it was configured from, numbered over the tags of the same name here.
func TestEveryChangelogVersionHasUpgradeNotes(t *testing.T) {
	root := packageRoot(t)
	changelog := readReleaseFile(t, root, "CHANGELOG.md")
	upgrade := readReleaseFile(t, root, "UPGRADE.md")

	inChangelog := regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\] - `).FindAllStringSubmatch(changelog, -1)
	inUpgrade := regexp.MustCompile(`(?m)^## v([0-9]+\.[0-9]+\.[0-9]+)$`).FindAllStringSubmatch(upgrade, -1)
	if len(inChangelog) == 0 || len(inUpgrade) == 0 {
		t.Fatal("one of the two release files has no version heading")
	}

	versions := func(matches [][]string) map[string]bool {
		out := map[string]bool{}
		for _, m := range matches {
			out[m[1]] = true
		}
		return out
	}
	logged, upgraded := versions(inChangelog), versions(inUpgrade)
	for v := range logged {
		if !upgraded[v] {
			t.Errorf("CHANGELOG.md has %s and UPGRADE.md has no notes for it", v)
		}
	}
	for v := range upgraded {
		if !logged[v] {
			t.Errorf("UPGRADE.md has notes for %s and CHANGELOG.md has no entry for it", v)
		}
	}
}

// TestVersion020NamesEveryIncompatibility holds the promise for the release
// that gave the package rates, fees, settlement and a credit limit.
func TestVersion020NamesEveryIncompatibility(t *testing.T) {
	root := packageRoot(t)
	upgrade := readReleaseFile(t, root, "UPGRADE.md")
	changelog := readReleaseFile(t, root, "CHANGELOG.md")

	if got := strings.Count(changelog, "## [0.2.0] - "); got != 1 {
		t.Fatalf("v0.2.0 changelog headings = %d, want exactly one", got)
	}
	if !strings.Contains(upgrade, "## v0.2.0") {
		t.Fatal("UPGRADE.md has no v0.2.0 entry")
	}

	for _, incompatibility := range []string{
		"`Config.CSRF`",
		"`NewWalletService`",
		"`RateProvider`",
		"`Operation.ReversesID`",
		"`NewEntryResource`",
		"`NewReceiptResource`",
	} {
		if !strings.Contains(upgrade, incompatibility) {
			t.Errorf("UPGRADE.md does not name %s", incompatibility)
		}
		if !strings.Contains(changelog, incompatibility) {
			t.Errorf("v0.2.0 notes do not name %s", incompatibility)
		}
	}
}

// TestVersion030NamesEveryIncompatibility holds it for the release that refused
// an unverified engine and started freezing a wallet its ledger stopped
// explaining.
//
// The CI job that runs apidiff makes the same promise, and only there: it needs
// the release tag and the git history, so a working tree that dropped a symbol
// without saying so is green locally until a pull request opens. This is the
// half that fails where the change is written.
func TestVersion030NamesEveryIncompatibility(t *testing.T) {
	root := packageRoot(t)
	upgrade := readReleaseFile(t, root, "UPGRADE.md")
	changelog := readReleaseFile(t, root, "CHANGELOG.md")

	if got := strings.Count(changelog, "## [0.3.0] - "); got != 1 {
		t.Fatalf("v0.3.0 changelog headings = %d, want exactly one", got)
	}
	if !strings.Contains(upgrade, "## v0.3.0") {
		t.Fatal("UPGRADE.md has no v0.3.0 entry")
	}

	for _, incompatibility := range []string{
		"`ErrUnsupportedDialect`",
		"`WalletReconcile`",
		"`ErrConcurrencyConflict`",
		"`ErrWalletFrozen`",
	} {
		if !strings.Contains(upgrade, incompatibility) {
			t.Errorf("UPGRADE.md does not name %s", incompatibility)
		}
		if !strings.Contains(changelog, incompatibility) {
			t.Errorf("v0.3.0 notes do not name %s", incompatibility)
		}
	}
}

// TestVersion040NamesEveryIncompatibility holds it for the release that let a
// wallet be named, described and taken out of service.
func TestVersion040NamesEveryIncompatibility(t *testing.T) {
	root := packageRoot(t)
	upgrade := readReleaseFile(t, root, "UPGRADE.md")
	changelog := readReleaseFile(t, root, "CHANGELOG.md")

	if got := strings.Count(changelog, "## [0.4.0] - "); got != 1 {
		t.Fatalf("v0.4.0 changelog headings = %d, want exactly one", got)
	}
	if !strings.Contains(upgrade, "## v0.4.0") {
		t.Fatal("UPGRADE.md has no v0.4.0 entry")
	}

	for _, incompatibility := range []string{
		"`WalletDescribe`",
		"`WalletClose`",
		"`ErrWalletClosed`",
		"`ErrBalanceEmpty`",
	} {
		if !strings.Contains(upgrade, incompatibility) {
			t.Errorf("UPGRADE.md does not name %s", incompatibility)
		}
		if !strings.Contains(changelog, incompatibility) {
			t.Errorf("v0.4.0 notes do not name %s", incompatibility)
		}
	}
}

func TestCIGuardsIncompatibleAPIChanges(t *testing.T) {
	ci := readReleaseFile(t, packageRoot(t), ".github/workflows/ci.yml")
	required := []string{
		"fetch-depth: 0",
		"name: api diff against the last release",
		`modpath=$(GOWORK=off go list -m -f '{{.Path}}')`,
		`git show "${tag}:go.mod"`,
		"git worktree add",
		"golang.org/x/exp/cmd/apidiff@latest -m -w",
		"-m -incompatible",
		`git diff --quiet "$latest" -- UPGRADE.md`,
		`added=$(git diff "$latest" -- UPGRADE.md`,
	}
	for _, want := range required {
		if !strings.Contains(ci, want) {
			t.Errorf("CI does not contain the API compatibility gate %q", want)
		}
	}
}

func TestTheReleasePublishesThePreVersionedChangelogEntryOnce(t *testing.T) {
	root := packageRoot(t)
	release := readReleaseFile(t, root, ".github/workflows/release.yml")
	changelog := readReleaseFile(t, root, "CHANGELOG.md")

	if got := strings.Count(changelog, "## [0.2.0] - "); got != 1 {
		t.Fatalf("v0.2.0 changelog headings = %d, want one", got)
	}
	required := []string{
		`tags: ["v*"]`,
		"contents: write",
		`version="${TAG#v}"`,
		`heading="## [$version] - "`,
		`gh release create "$TAG"`,
		"--verify-tag",
		`--notes-file "$notes"`,
	}
	for _, want := range required {
		if !strings.Contains(release, want) {
			t.Errorf("release workflow does not contain %q", want)
		}
	}
	if got := strings.Count(release, `gh release create "$TAG"`); got != 1 {
		t.Errorf("release creation commands = %d, want exactly one", got)
	}
	for _, forbidden := range []string{
		"Write it into the changelog",
		"git commit",
		"git push",
		"git tag",
		"gh release delete",
		"CHANGELOG.next",
	} {
		if strings.Contains(release, forbidden) {
			t.Errorf("release workflow mutates the pre-versioned changelog through %q", forbidden)
		}
	}
}

func readReleaseFile(t *testing.T, root, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

func captureReleaseValue(t *testing.T, body, pattern, label string) string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("%s is missing", label)
	}
	return match[1]
}
