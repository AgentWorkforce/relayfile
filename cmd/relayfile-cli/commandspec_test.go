package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// These tests are the relayfile half of the CLI-surface drift guard. The TS
// side (packages/sdk/typescript/src/relay-cli) asserts its snapshot matches
// what `relayfile __command-spec --json` emits; here we assert that emitted
// tree matches what the Go code actually routes and actually parses.
//
// Top-level names need no test: run() dispatches through the same table, so a
// top-level command cannot be declared without being routable. Nested
// subcommands still route through per-group switches, and every leaf still
// builds its own flag.FlagSet, so those two are checked against the source.

// commandNameRe matches the contract's conformance rule for command names.
var commandNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// flagStringRe matches the contract's conformance rule for flag strings.
var flagStringRe = regexp.MustCompile(`^(-[A-Za-z0-9], )?--[a-z0-9][a-z0-9-]*( [<\[][^>\]]+[>\]])?$`)

func TestCommandSpecNamesAndFlagsSatisfyContract(t *testing.T) {
	// The emitted spec is consumed by @agent-relay/cli-surface's
	// assertSurfaceConforms. Catch a violation here, where the failure names
	// the Go table, instead of in the TS drift test.
	walkSpec(publicCommandSpec(), nil, func(path []string, command cliCommandSpec) {
		label := strings.Join(path, " ")
		if !commandNameRe.MatchString(command.Name) {
			t.Errorf("command %q: name is not lowercase kebab-case", label)
		}
		if strings.TrimSpace(command.Description) == "" {
			t.Errorf("command %q: description must not be empty", label)
		}
		for _, alias := range command.Aliases {
			if !commandNameRe.MatchString(alias) {
				t.Errorf("command %q: alias %q is not lowercase kebab-case", label, alias)
			}
		}

		sawOptional := false
		for index, arg := range command.Args {
			if arg.Required && sawOptional {
				t.Errorf("command %q: required arg %q cannot follow an optional arg", label, arg.Name)
			}
			if !arg.Required {
				sawOptional = true
			}
			if arg.Variadic && index != len(command.Args)-1 {
				t.Errorf("command %q: variadic arg %q must be the last positional", label, arg.Name)
			}
			if strings.TrimSpace(arg.Description) == "" {
				t.Errorf("command %q: arg %q needs a description", label, arg.Name)
			}
		}

		seen := map[string]bool{}
		for _, option := range command.Options {
			if !flagStringRe.MatchString(option.Flags) {
				t.Errorf("command %q: flags %q is not a commander flag string", label, option.Flags)
			}
			long := optionLongName(option.Flags)
			if long == "" {
				continue
			}
			if seen[long] {
				t.Errorf("command %q: duplicate flag --%s", label, long)
			}
			seen[long] = true
			if strings.TrimSpace(option.Description) == "" {
				t.Errorf("command %q: flag --%s needs a description", label, long)
			}
		}
	})
}

func TestCommandSpecIsJSONSerializable(t *testing.T) {
	payload, err := json.Marshal(publicCommandSpec())
	if err != nil {
		t.Fatalf("marshal command spec: %v", err)
	}
	var roundTripped []map[string]any
	if err := json.Unmarshal(payload, &roundTripped); err != nil {
		t.Fatalf("unmarshal command spec: %v", err)
	}
	if len(roundTripped) != len(publicCommandSpec()) {
		t.Fatalf("round-trip changed command count: %d -> %d", len(publicCommandSpec()), len(roundTripped))
	}
}

func TestCommandSpecExcludesOnlyInternalHooks(t *testing.T) {
	// `help` and `__command-spec` are deliberately routable-but-unpublished:
	// the host CLI renders help from the spec, and __command-spec is the
	// introspection hook that produces the spec. Anything else missing from
	// the published tree is drift.
	published := map[string]bool{}
	for _, command := range publicCommandSpec() {
		published[command.Name] = true
	}
	var unpublished []string
	for _, command := range relayfileCommands() {
		if !published[command.Name] {
			unpublished = append(unpublished, command.Name)
		}
	}
	sort.Strings(unpublished)
	want := []string{commandSpecCommandName, "help"}
	sort.Strings(want)
	if strings.Join(unpublished, ",") != strings.Join(want, ",") {
		t.Fatalf("unpublished top-level commands = %v, want %v", unpublished, want)
	}
}

func TestEveryDeclaredCommandIsDispatchable(t *testing.T) {
	for _, command := range relayfileCommands() {
		if command.dispatch == nil {
			t.Errorf("top-level command %q declares no dispatch", command.Name)
		}
	}
}

func TestMountSealSubcommandsMatchDispatch(t *testing.T) {
	declared := map[string]bool{}
	for _, command := range mountSealCommands() {
		declared[command.Name] = true
	}
	for name := range mountSealDispatch {
		if !declared[name] {
			t.Errorf("mount subcommand %q is routable but not declared", name)
		}
	}
	for name := range declared {
		if _, ok := mountSealDispatch[name]; !ok {
			t.Errorf("mount subcommand %q is declared but not routable", name)
		}
	}
}

// TestSubcommandsMatchSourceSwitches parses each group's dispatch function out
// of the source and asserts its switch cases are exactly the subcommands the
// table declares (names plus aliases).
func TestSubcommandsMatchSourceSwitches(t *testing.T) {
	sources := parseCommandSources(t)

	walkSpec(publicCommandSpec(), nil, func(path []string, command cliCommandSpec) {
		if command.dispatchSource == "" {
			if len(command.Subcommands) > 0 && !isMountCommand(path) {
				t.Errorf("command %q declares subcommands but names no dispatchSource", strings.Join(path, " "))
			}
			return
		}

		routed, ok := sources.switchCases(command.dispatchSource)
		if !ok {
			t.Fatalf("command %q: dispatchSource %q not found in cmd/relayfile-cli", strings.Join(path, " "), command.dispatchSource)
		}

		declared := map[string]bool{}
		for _, sub := range command.Subcommands {
			declared[sub.Name] = true
			for _, alias := range sub.Aliases {
				declared[alias] = true
			}
		}

		for _, name := range routed {
			if !declared[name] {
				t.Errorf("%s routes %q but the command table does not declare it", command.dispatchSource, name)
			}
		}
		for name := range declared {
			if !contains(routed, name) {
				t.Errorf("command table declares %q under %q but %s does not route it", name, strings.Join(path, " "), command.dispatchSource)
			}
		}
	})
}

// TestOptionsMatchSourceFlagSets parses each command's flag.FlagSet out of the
// source and asserts the declared options cover exactly the flags it
// registers. Flags whose names are not kebab-case cannot be expressed by the
// contract, so they are allowed to exist undeclared as long as a kebab-case
// alias for them is declared (see --opId / --op-id).
//
// A command may also withhold a registered flag on purpose — `supervisor
// install` shares runListen's flag set but must not advertise the flags that
// detach the process it installs. Those are listed in withheldFlags and
// checked from both sides below, so the exception cannot become a hiding
// place for real drift.
func TestOptionsMatchSourceFlagSets(t *testing.T) {
	sources := parseCommandSources(t)

	walkSpec(publicCommandSpec(), nil, func(path []string, command cliCommandSpec) {
		label := strings.Join(path, " ")
		if command.flagSource == "" {
			if len(command.Options) > 0 {
				t.Errorf("command %q declares options but names no flagSource", label)
			}
			return
		}

		registered, ok := sources.flagNames(command.flagSource)
		if !ok {
			t.Fatalf("command %q: flagSource %q not found in cmd/relayfile-cli", label, command.flagSource)
		}

		declared := map[string]bool{}
		for _, option := range command.Options {
			if long := optionLongName(option.Flags); long != "" {
				declared[long] = true
			}
		}

		withheld := map[string]bool{}
		for _, name := range command.withheldFlags {
			withheld[name] = true
			if !contains(registered, name) {
				t.Errorf("command %q withholds --%s but %s does not register it", label, name, command.flagSource)
			}
			if declared[name] {
				t.Errorf("command %q withholds --%s and declares it too", label, name)
			}
		}

		for _, name := range registered {
			if declared[name] || withheld[name] {
				continue
			}
			if !commandNameRe.MatchString(name) {
				// Not expressible as a contract flag string; a kebab-case
				// alias must be declared in its place.
				continue
			}
			t.Errorf("%s registers --%s but command %q does not declare it", command.flagSource, name, label)
		}
		for name := range declared {
			if !contains(registered, name) {
				t.Errorf("command %q declares --%s but %s does not register it", label, name, command.flagSource)
			}
		}
	})
}

// TestDeclaredArgsCoverSourcePositionals parses each command's flag.FlagSet
// out of the source and asserts that a command whose parser reads a positional
// value declares at least one positional argument.
//
// TestOptionsMatchSourceFlagSets covers flags only, so a command could read
// fs.Arg(0) while declaring no args — and `listen`, `dev` and
// `workspace status` all did. The binary accepts those invocations (run()
// forwards argv untouched), but a host that routes from the emitted spec —
// `agent-relay file` builds its parser from it — rejects them before the
// binary is ever reached, which is how the drift stayed invisible to every
// relayfile-side test.
//
// Only this direction is checked. fs.NArg() is deliberately not treated as a
// read: several commands call it solely to reject positionals. And a command
// may legitimately declare args it consumes before parsing (runStop and
// friends read args[0] directly), so "declares but no fs.Arg" is not drift.
func TestDeclaredArgsCoverSourcePositionals(t *testing.T) {
	sources := parseCommandSources(t)

	walkSpec(publicCommandSpec(), nil, func(path []string, command cliCommandSpec) {
		if command.flagSource == "" {
			return
		}
		label := strings.Join(path, " ")
		reads, ok := sources.readsPositional(command.flagSource)
		if !ok {
			t.Fatalf("command %q: flagSource %q not found in cmd/relayfile-cli", label, command.flagSource)
		}
		if reads && len(command.Args) == 0 {
			t.Errorf("%s reads a positional argument but command %q declares none", command.flagSource, label)
		}
	})
}

func isMountCommand(path []string) bool {
	return len(path) == 1 && path[0] == "mount"
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func walkSpec(commands []cliCommandSpec, prefix []string, visit func(path []string, command cliCommandSpec)) {
	for _, command := range commands {
		path := append(append([]string{}, prefix...), command.Name)
		visit(path, command)
		walkSpec(command.Subcommands, path, visit)
	}
}

// commandSources holds the parsed cmd/relayfile-cli source, indexed by
// function name.
type commandSources struct {
	functions map[string]*ast.FuncDecl
}

func parseCommandSources(t *testing.T) *commandSources {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/relayfile-cli: %v", err)
	}
	sources := &commandSources{functions: map[string]*ast.FuncDecl{}}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			sources.functions[fn.Name.Name] = fn
		}
	}
	if len(sources.functions) == 0 {
		t.Fatal("parsed no functions from cmd/relayfile-cli")
	}
	return sources
}

// switchCases returns the string case values of every switch statement in the
// named function, in source order.
func (s *commandSources) switchCases(function string) ([]string, bool) {
	fn, ok := s.functions[function]
	if !ok {
		return nil, false
	}
	var cases []string
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range clause.List {
			if value, ok := stringLiteral(expr); ok {
				cases = append(cases, value)
			}
		}
		return true
	})
	return cases, true
}

// flagNames returns every flag name registered on a flag.FlagSet inside the
// named function: fs.String("x", ...), fs.Bool, fs.Int, fs.Duration,
// fs.Float64, fs.Var(&v, "x", ...) and friends.
func (s *commandSources) flagNames(function string) ([]string, bool) {
	fn, ok := s.functions[function]
	if !ok {
		return nil, false
	}
	kinds := map[string]int{
		"String":   0,
		"Bool":     0,
		"Int":      0,
		"Int64":    0,
		"Uint":     0,
		"Uint64":   0,
		"Float64":  0,
		"Duration": 0,
		"Var":      1, // fs.Var(value, name, usage)
		"Func":     0,
	}
	var names []string
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || !isFlagSetReceiver(receiver.Name) {
			return true
		}
		index, ok := kinds[selector.Sel.Name]
		if !ok || len(call.Args) <= index {
			return true
		}
		if name, ok := stringLiteral(call.Args[index]); ok {
			names = append(names, name)
		}
		return true
	})
	return names, true
}

// readsPositional reports whether the named function reads a positional
// argument off its FlagSet: fs.Arg(i) or fs.Args().
func (s *commandSources) readsPositional(function string) (bool, bool) {
	fn, ok := s.functions[function]
	if !ok {
		return false, false
	}
	reads := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || !isFlagSetReceiver(receiver.Name) {
			return true
		}
		if selector.Sel.Name == "Arg" || selector.Sel.Name == "Args" {
			reads = true
		}
		return true
	})
	return reads, true
}

// isFlagSetReceiver reports whether an identifier is one of the FlagSet
// variables the CLI uses. `peek` is runDev's throwaway pre-parse set, which
// registers a subset of runListen's flags; excluding it keeps runDev's
// declared options tied to the real parser.
func isFlagSetReceiver(name string) bool {
	return name == "fs"
}

func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// TestCommandSpecJSONShape locks the wire shape the TS snapshot consumes.
func TestCommandSpecJSONShape(t *testing.T) {
	payload, err := json.Marshal(publicCommandSpec())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tree []map[string]any
	if err := json.Unmarshal(payload, &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{
		"name": true, "description": true, "aliases": true, "args": true,
		"options": true, "subcommands": true, "hidden": true, "deprecated": true,
	}
	var check func(nodes []map[string]any, path string)
	check = func(nodes []map[string]any, path string) {
		for _, node := range nodes {
			name, _ := node["name"].(string)
			label := strings.TrimPrefix(path+" "+name, " ")
			for key := range node {
				if !allowed[key] {
					t.Errorf("command %q emits unexpected key %q", label, key)
				}
			}
			if _, ok := node["description"].(string); !ok {
				t.Errorf("command %q emits no description", label)
			}
			if raw, ok := node["subcommands"]; ok {
				list, ok := raw.([]any)
				if !ok {
					t.Fatalf("command %q: subcommands is %T", label, raw)
				}
				children := make([]map[string]any, 0, len(list))
				for _, item := range list {
					child, ok := item.(map[string]any)
					if !ok {
						t.Fatalf("command %q: subcommand is %T", label, item)
					}
					children = append(children, child)
				}
				check(children, label)
			}
		}
	}
	check(tree, "")
}

// TestCommandSpecCommandEmitsJSON exercises the introspection subcommand the
// SDK's generator shells out to.
func TestCommandSpecCommandEmitsJSON(t *testing.T) {
	var out strings.Builder
	if err := run([]string{commandSpecCommandName, "--json"}, nil, &out, &out); err != nil {
		t.Fatalf("run %s --json: %v", commandSpecCommandName, err)
	}
	var tree []cliCommandSpec
	if err := json.Unmarshal([]byte(out.String()), &tree); err != nil {
		t.Fatalf("decode emitted spec: %v\n%s", err, out.String())
	}
	if len(tree) != len(publicCommandSpec()) {
		t.Fatalf("emitted %d commands, table has %d", len(tree), len(publicCommandSpec()))
	}
	if fmt.Sprint(tree[0].Name) != publicCommandSpec()[0].Name {
		t.Fatalf("first emitted command = %q, want %q", tree[0].Name, publicCommandSpec()[0].Name)
	}

	var bare strings.Builder
	if err := run([]string{commandSpecCommandName}, nil, &bare, &bare); err == nil {
		t.Fatal("expected an error without --json")
	}
}
