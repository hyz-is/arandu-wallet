package unit_test

import (
	"go/ast"
	"go/build/constraint"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What this package proves about itself, before anybody installs it.
//
// `aru doctor` audits the application it is run inside. It walks that project's
// own tree, skips vendor/, reads the one arandu.mod.toml at its root, and
// refuses a directory that is not an application at all. It never loads a
// dependency. So nothing an installed package does is audited by the
// application that installed it: a package that reached a table without a
// Grant, took a tenant out of a request, or declared no capabilities and then
// opened a socket would pass every check the installer runs.
//
// The package is therefore the only place that check can happen, and this is
// it. These tests read this package's own Go files as syntax and hold four
// properties:
//
//  1. every exported Service method calls Authorize before it reaches the
//     configured Model;
//  2. the tenant comes from the Grant, and nothing a request carried is read as
//     one;
//  3. the Model remains tenant-scoped and CRUD does not grow a second data path;
//  4. what arandu.mod.toml declares is what the code does.
//
// The fifth is held where the routes exist rather than here, because a prefix
// arrives through configuration and syntax cannot follow it:
// TestNoRouteLandsInTheFrameworkNamespace, in tests/Feature/routes_test.go,
// registers the module and reads the table back.
//
// What these tests do not reach is worth as much as what they do. They read
// syntax: a call hidden behind an interface, a wrapper around the named Model
// entry point, and anything reached by reflection are all invisible to them. A
// green run means no such thing was found written down, not that none exists.
// What is absolute is what the compiler holds alongside them -- a Model terminal
// with no Grant in its call does not compile.

// buildable reports whether the compiler ever reads this file.
//
// It asks the build constraint rather than the file name, so a file excluded by
// a tag is left out of the audit whatever it is called: what the compiler never
// builds is not part of what the package does, and reporting it as such would
// be reporting a capability nobody can reach.
func buildable(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			break
		}
		for _, comment := range group.List {
			expression, err := constraint.Parse(comment.Text)
			if err != nil {
				continue
			}
			// No tag set, which is what the gates run with.
			if !expression.Eval(func(string) bool { return false }) {
				return false
			}
		}
	}
	return true
}

// linked reports whether an application that installs this package compiles
// this file into its binary.
//
// A command is not linked. `go get` of a library never pulls in the main
// package beside it, and `go build` of the application never reaches it, so
// what a command does is not a capability anybody who installs this agreed to.
// Auditing one would make the manifest declare a capability that no running
// application has -- which is the same defect as declaring one nothing uses,
// pointed the other way.
//
// The package carries no command today, and the rule is here rather than in the
// commit that adds one: a capability audit that grows a hole the first time
// somebody needs it is an audit whose answer depends on who ran it.
//
// Every rule in this file reads what the installer links, and this is where
// that is decided once.
func linked(file *ast.File) bool { return file.Name.Name != "main" }

// auditedFiles is every Go file an application that installs this package
// compiles into its binary.
func auditedFiles(t *testing.T) []parsedGoFile {
	t.Helper()

	out := []parsedGoFile{}
	for _, source := range productionGoFiles(t, packageRoot(t)) {
		if buildable(source.file) && linked(source.file) {
			out = append(out, source)
		}
	}
	// An audit with nothing to read passes, and a test that passes by finding
	// nothing is the failure mode of every rule below.
	if len(out) == 0 {
		t.Fatal("no buildable Go file was found, so everything below would pass by having nothing to read")
	}
	return out
}

// selectorName is the trailing name of a selector, or the name of an
// identifier.
func selectorName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.SelectorExpr:
		return expression.Sel.Name
	case *ast.Ident:
		return expression.Name
	}
	return ""
}

// calledName is the trailing name of whatever a call names, so a call can be
// recognised without resolving what it is called on.
func calledName(call *ast.CallExpr) string {
	return selectorName(call.Fun)
}

// firstCallTo is where a body first calls something by this name.
func firstCallTo(body *ast.BlockStmt, name string) token.Pos {
	found := token.NoPos
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || calledName(call) != name {
			return true
		}
		if found == token.NoPos || call.Pos() < found {
			found = call.Pos()
		}
		return true
	})
	return found
}

// TestEveryServiceMethodAuthorizesBeforeTheModel holds the mandatory path on
// every exported use case rather than on the three that exist today.
//
// A nil-database denial test proves the closed path. This syntax audit is its
// twin for an allowed path: Model construction is visible in the method body,
// and moving it above Authorize fails even when no terminal is executed.
func TestEveryServiceMethodAuthorizesBeforeTheModel(t *testing.T) {
	t.Parallel()

	audited := 0
	for _, source := range auditedFiles(t) {
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || receiverType(function) != "WalletService" ||
				!function.Name.IsExported() {
				continue
			}
			audited++

			decided := firstCallTo(function.Body, "Authorize")
			reach := firstModelReach(function.Body)
			if decided == token.NoPos {
				t.Errorf("%s: %s never calls security.Authorize, so no Policy decided whether the Model may run",
					source.path, function.Name.Name)
			}
			if reach == token.NoPos {
				t.Errorf("%s: %s never reaches the configured Model, so this audit found no data boundary to order",
					source.path, function.Name.Name)
			}
			if decided != token.NoPos && reach != token.NoPos && decided > reach {
				t.Errorf("%s: %s reaches the Model before security.Authorize",
					source.path, function.Name.Name)
			}
		}
	}
	if audited == 0 {
		t.Fatal("no exported WalletService method was found, so this test proved nothing")
	}
}

// firstModelReach is where a Service first constructs one of the configured
// Models or calls a promoted write terminal. The constructors themselves count:
// moving only the construction before Authorize is the mutation this audit
// exists to reject.
//
// All six are named, and that is what makes the audit hold as the package
// grows: a use case that reached the ledger, the operations table, the recorded
// rates, the recorded charges or the purchases without touching a wallet would
// otherwise be a method this test read as having no data boundary at all, and
// would report so instead of failing.
func firstModelReach(body *ast.BlockStmt) token.Pos {
	entries := map[string]bool{
		"Wallets": true, "Operations": true, "Entries": true,
		"Conversions": true, "Charges": true, "Purchases": true,
	}
	terminals := map[string]bool{
		"Save": true, "Delete": true, "Restore": true, "Touch": true,
	}
	found := token.NoPos
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calledName(call)
		if !entries[name] && !terminals[name] {
			return true
		}
		if found == token.NoPos || call.Pos() < found {
			found = call.Pos()
		}
		return true
	})
	return found
}

// requestAccessors are the ways a value that arrived with the request is read.
// A tenant taken through any of them is a tenant the caller chose.
var requestAccessors = map[string]bool{
	"Param":         true,
	"Query":         true,
	"Input":         true,
	"FormValue":     true,
	"PostFormValue": true,
	"PathValue":     true,
	"Cookie":        true,
	"Get":           true,
}

// TestNoTenantIsReadOutOfTheRequest is the rule with no exception in it. A
// tenant the client can name is a client who chooses whose rows they read, and
// every other check in the package passes while it happens.
func TestNoTenantIsReadOutOfTheRequest(t *testing.T) {
	t.Parallel()

	for _, source := range auditedFiles(t) {
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !requestAccessors[selector.Sel.Name] {
				return true
			}
			for _, argument := range call.Args {
				literal, ok := argument.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				if strings.Contains(strings.ToLower(literal.Value), "tenant") {
					t.Errorf("%s: %s(%s) reads a tenant out of the request; it comes from the Grant, which came from the session",
						source.path, selector.Sel.Name, literal.Value)
				}
			}
			return true
		})
	}
}

// TestTheServiceWritesTenantOnlyFromTheGrant holds the write half of tenant
// isolation. The proposed value is policy input; the value persisted after
// Authorize must take TenantID directly from data.Tenant(g).
func TestTheServiceWritesTenantOnlyFromTheGrant(t *testing.T) {
	t.Parallel()

	audited := 0
	for _, source := range auditedFiles(t) {
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || receiverType(function) != "WalletService" {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				assignment, ok := node.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, target := range assignment.Lhs {
					if i >= len(assignment.Rhs) || selectorName(target) != "TenantID" {
						continue
					}
					audited++
					call, ok := assignment.Rhs[i].(*ast.CallExpr)
					if !ok || calledName(call) != "Tenant" {
						t.Errorf("%s: %s writes TenantID from something other than data.Tenant(g)",
							source.path, function.Name.Name)
					}
				}
				return true
			})
		}
	}
	if audited == 0 {
		t.Fatal("no Service tenant assignment was found, so this test proved nothing")
	}
}

// capabilities is what arandu.mod.toml declares, and what the code does, under
// the same four names.
type capabilities struct {
	network    bool
	filesystem bool
	exec       bool
	migrations bool
}

// TestTheDeclaredCapabilitiesAreWhatTheCodeDoes is the audit the installer
// cannot run. `aru doctor` compares an application's manifest against the
// application's code; the manifest of a package it installed is never opened,
// so this comparison exists here or nowhere.
//
// Both directions fail. Using more than was declared is the one that costs
// somebody something, and the doctor calls it an error too. Declaring more than
// is used is milder -- a warning, there -- and it fails here because `go test`
// has one outcome and because asking for what is not needed is how a
// declaration stops being worth reading.
func TestTheDeclaredCapabilitiesAreWhatTheCodeDoes(t *testing.T) {
	t.Parallel()

	declared := declaredCapabilities(t)
	used := usedCapabilities(auditedFiles(t))

	for _, capability := range []struct {
		name        string
		declared    bool
		used        bool
		consequence string
	}{
		{"network", declared.network, used.network,
			"the code makes calls that leave the process, and whoever installs it agreed to a package that does not"},
		{"filesystem", declared.filesystem, used.filesystem,
			"the code reads or writes files outside the database, which nothing in its API shows"},
		{"exec", declared.exec, used.exec,
			"the code runs another program, which is the widest capability there is"},
		{"migrations", declared.migrations, used.migrations,
			"the code owns tables, and an installer who did not expect that will not know to migrate before deploying"},
	} {
		switch {
		case capability.used && !capability.declared:
			t.Errorf("arandu.mod.toml declares %s = false and the code uses it: %s. Declare it, or remove what needs it",
				capability.name, capability.consequence)
		case capability.declared && !capability.used:
			t.Errorf("arandu.mod.toml declares %s = true and nothing uses it: set %s = false, or the declaration stops being worth reading",
				capability.name, capability.name)
		}
	}
}

// declaredCapabilities reads the [permissions] block.
//
// By hand, because the alternative is a third dependency in a module that is
// compiled into other people's builds, and four booleans do not earn one.
func declaredCapabilities(t *testing.T) capabilities {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(packageRoot(t), "arandu.mod.toml"))
	if err != nil {
		t.Fatalf("reading arandu.mod.toml: %v", err)
	}

	var declared capabilities
	seen := map[string]bool{}
	inside := false

	for _, line := range strings.Split(string(body), "\n") {
		if comment := strings.IndexByte(line, '#'); comment >= 0 {
			line = line[:comment]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inside = line == "[permissions]"
			continue
		}
		if !inside {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		seen[key] = true
		switch key {
		case "network":
			declared.network = value == "true"
		case "filesystem":
			declared.filesystem = value == "true"
		case "exec":
			declared.exec = value == "true"
		case "migrations":
			declared.migrations = value == "true"
		}
	}

	for _, required := range []string{"network", "filesystem", "exec", "migrations"} {
		if !seen[required] {
			t.Errorf("arandu.mod.toml leaves %s undeclared under [permissions]; silence reads as false to whoever installs this, and a capability nobody declared is one nobody agreed to",
				required)
		}
	}
	return declared
}

// usedCapabilities is what the code actually does, by syntax.
//
// It reads calls rather than imports, for the reason the manifest gives: this
// package imports net/http for a method constant and a request type, and an
// import says nothing about whether anything leaves the process.
func usedCapabilities(files []parsedGoFile) capabilities {
	var used capabilities

	for _, source := range files {
		if strings.Contains(source.path, "migrations/") {
			used.migrations = true
		}
		for _, imported := range source.file.Imports {
			switch strings.Trim(imported.Path.Value, `"`) {
			case "os/exec":
				used.exec = true
			case "net/smtp", "net/rpc":
				used.network = true
			case "io/ioutil":
				used.filesystem = true
			}
		}

		standard := standardImports(source.file)
		ast.Inspect(source.file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				switch name := qualifiedName(node, standard); {
				case name == "http.Get", name == "http.Post", name == "http.Head",
					name == "http.PostForm", name == "http.NewRequest",
					name == "http.NewRequestWithContext",
					name == "net.Dial", name == "net.DialTimeout",
					strings.HasSuffix(name, ".Do"):
					used.network = true

				case name == "os.Open", name == "os.OpenFile", name == "os.Create",
					name == "os.ReadFile", name == "os.WriteFile", name == "os.Remove",
					name == "os.RemoveAll", name == "os.Mkdir", name == "os.MkdirAll",
					name == "os.Rename", name == "os.ReadDir",
					name == "filepath.Walk", name == "filepath.WalkDir":
					used.filesystem = true

				case name == "exec.Command", name == "exec.CommandContext", name == "syscall.Exec":
					used.exec = true
				}
			case *ast.FuncDecl:
				if node.Recv != nil && node.Name.Name == "Migrations" {
					used.migrations = true
				}
			}
			return true
		})
	}
	return used
}

// standardImports maps the local name of every standard library import to the
// package's own name, so an aliased import is recognised by what it is.
//
// Only the standard library, because the names matched above are its names. A
// third-party package whose last element is "http" is not net/http, and reading
// it as one reports a router helper as an outbound call.
func standardImports(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
			continue
		}

		name := path
		if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
			name = path[slash+1:]
		}
		local := name
		if imported.Name != nil {
			local = imported.Name.Name
		}
		out[local] = name
	}
	return out
}

// qualifiedName is a call as "package.Function" where the receiver is an
// imported package, and ".Method" where it is a value.
func qualifiedName(call *ast.CallExpr, standard map[string]string) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		if identifier, ok := call.Fun.(*ast.Ident); ok {
			return identifier.Name
		}
		return ""
	}
	owner, ok := selector.X.(*ast.Ident)
	if !ok {
		return "." + selector.Sel.Name
	}
	if name, imported := standard[owner.Name]; imported {
		return name + "." + selector.Sel.Name
	}
	return owner.Name + "." + selector.Sel.Name
}

// The two properties below are about money rather than about authorization, and
// they are here for the same reason the others are: they are visible in the
// syntax, and a mutation that removed either of them passed every behavioural
// test this package runs against SQLite.

// TestOnlyOneStatementInThePackageWritesABalance holds the reach half of the
// decision that makes concurrent spending safe.
//
// The other half -- that the one statement carries its predicate -- is held
// beside the code, in statement_internal_test.go, because it asks an unexported
// function for the string the database is sent. What that one cannot see is a
// second place that moves a balance some other way, and this is that: whatever
// composes an update of the balance column has to be moveStatement, and nothing
// may reach the column through the Model's increment and decrement either.
//
// Both halves were written after the mutation that proves they are worth
// having. Replacing the predicate with a read and an if left every SQLite test
// passing, because SQLite serializes writers; on PostgreSQL the same code let
// twenty-six withdrawals of one unit through against a balance of twenty.
func TestOnlyOneStatementInThePackageWritesABalance(t *testing.T) {
	t.Parallel()

	// Every identifier in a balance statement goes through the connection's
	// grammar now, because MySQL quotes with backticks and PostgreSQL with
	// double quotes. So the column is named by a call rather than spelled into
	// a literal, and what this walks is the call: any function asking the
	// quoter for "balance" is a function composing SQL about the balance.
	//
	// It is a stricter question than the literal it replaced in one way and a
	// narrower one in another. Stricter, because the literal form could only
	// see the quoting the author happened to use and this sees the column in
	// every spelling. Narrower, because it recognises the quoter by the name
	// the package gives it. The reach is held by the test below, which asks
	// what composes an update of that table at all and does not care how the
	// columns in it were named.
	namers := map[string]int{}
	for _, source := range auditedFiles(t) {
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				// Through the quoter, which the package names q everywhere it
				// holds one. A migration also names the column -- it creates it
				// -- and does so through the Blueprint, which is why the callee
				// is asked for and not only the argument.
				callee, ok := call.Fun.(*ast.Ident)
				if !ok || callee.Name != "q" {
					return true
				}
				literal, ok := call.Args[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING || literal.Value != `"balance"` {
					return true
				}
				namers[function.Name.Name]++
				return true
			})
		}
	}

	if len(namers) == 0 {
		t.Fatal("nothing in this package names the balance column in a statement, so this test proved nothing")
	}

	// moveStatement composes the write. movedColumns names the same column in
	// the list the row is read back through, on both paths, and composes no
	// update of its own.
	allowed := map[string]bool{"moveStatement": true, "movedColumns": true}
	for name := range namers {
		if !allowed[name] {
			t.Errorf("%s composes SQL naming the balance column: the write lives in moveStatement or nowhere, because a second site is a second guard to keep right",
				name)
		}
	}
	if namers["moveStatement"] == 0 {
		t.Error("moveStatement does not name the balance column, so the statement that moves money is somewhere else now")
	}
}

// TestOnlyOneFunctionComposesAnUpdateOfTheWalletsTable is the reach the audit
// above cannot have on its own.
//
// A second writer that named its column through a variable rather than a
// literal would pass the walk above. What it could not do is compose an update
// of that table without naming the table, which is what this asks.
func TestOnlyOneFunctionComposesAnUpdateOfTheWalletsTable(t *testing.T) {
	t.Parallel()

	composers := map[string]bool{}
	for _, source := range auditedFiles(t) {
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			var namesTable, opensUpdate bool
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.Ident:
					if n.Name == "walletsTable" {
						namesTable = true
					}
				case *ast.BasicLit:
					if n.Kind == token.STRING && strings.HasPrefix(n.Value, `"update `) {
						opensUpdate = true
					}
				}
				return true
			})
			if namesTable && opensUpdate {
				composers[function.Name.Name] = true
			}
		}
	}

	if len(composers) != 1 || !composers["moveStatement"] {
		t.Errorf("the wallets table is updated from %v, and it is updated from moveStatement or from nowhere", composers)
	}
}

// TestNothingReachesTheBalanceThroughTheModel is the other way in, closed.
//
// Increment and Decrement compose their own assignment to the column, so a call
// to either on "balance" would move money through a statement the audit above
// cannot read and the test beside the code never sees. There is no such call
// and there is not meant to be one.
func TestNothingReachesTheBalanceThroughTheModel(t *testing.T) {
	t.Parallel()

	for _, source := range auditedFiles(t) {
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch name := calledName(call); name {
			case "Increment", "Decrement":
				// Increment(ctx, g, column, amount, extra): the column is third.
				if len(call.Args) >= 3 && isStringLiteral(call.Args[2], "balance") {
					t.Errorf("%s: %s moves the balance through the Model, and the one statement that moves it is moveStatement",
						source.path, name)
				}
			case "Update":
				for _, argument := range call.Args {
					composite, ok := argument.(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, element := range composite.Elts {
						pair, ok := element.(*ast.KeyValueExpr)
						if ok && isStringLiteral(pair.Key, "balance") {
							t.Errorf("%s: an Update writes the balance column, and the one statement that writes it is moveStatement",
								source.path)
						}
					}
				}
			}
			return true
		})
	}
}

// TestTheLedgerIsAppendOnly holds the other half of the same decision: an entry
// is written once and never changed, so what happened stays readable after it is
// undone.
//
// The recorded rates, the charges and the purchases are held to the same rule
// and for a sharper reason. Each exists so that a movement can be reproduced; a
// number that could be corrected in place is a number that says what somebody
// later wished it had been, and the row would still look exactly as
// trustworthy.
//
// The schema holds it too -- none of the four tables has an updated_at, so an
// update through the Model fails on a column that does not exist -- and this is
// the half that says so before anything runs.
func TestTheLedgerIsAppendOnly(t *testing.T) {
	t.Parallel()

	forbidden := map[string]bool{"Update": true, "Delete": true, "ForceDelete": true, "Upsert": true, "Touch": true}
	appendOnly := map[string]string{
		"Entries":     "the ledger",
		"Conversions": "the recorded rates",
		"Charges":     "the recorded charges",
		"Purchases":   "the record of what was bought",
	}

	for _, source := range auditedFiles(t) {
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !forbidden[calledName(call)] {
				return true
			}
			for entry, what := range appendOnly {
				if chainStartsAt(call, entry) {
					t.Errorf("%s: %s is called on %s, which is appended to and never rewritten",
						source.path, calledName(call), what)
				}
			}
			return true
		})
	}
}

// isStringLiteral reports whether an expression is exactly this string.
func isStringLiteral(expression ast.Expr, want string) bool {
	literal, ok := expression.(*ast.BasicLit)
	return ok && literal.Kind == token.STRING && literal.Value == `"`+want+`"`
}

// chainGuards reports whether a builder chain ending in this call passes
// through a Where on the given column.
func chainGuards(call *ast.CallExpr, column string) bool {
	for node := receiverOf(call); node != nil; node = receiverOf(node) {
		if calledName(node) != "Where" || len(node.Args) == 0 {
			continue
		}
		if isStringLiteral(node.Args[0], column) {
			return true
		}
	}
	return false
}

// chainStartsAt reports whether a builder chain ending in this call was started
// by a call of this name.
func chainStartsAt(call *ast.CallExpr, name string) bool {
	for node := receiverOf(call); node != nil; node = receiverOf(node) {
		if calledName(node) == name {
			return true
		}
	}
	return false
}

// receiverOf is the call a method was called on, and nil where the receiver is
// not itself a call.
func receiverOf(call *ast.CallExpr) *ast.CallExpr {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	inner, ok := selector.X.(*ast.CallExpr)
	if !ok {
		return nil
	}
	return inner
}
