package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"

	schemaassets "github.com/agentworkforce/relayfile/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrUnknownPath is returned when no canonical schema is registered for the
// given VFS path. Callers can check for this with errors.Is to distinguish
// "not validated" from "valid".
var ErrUnknownPath = errors.New("no canonical schema registered for path")

type registration struct {
	pattern *regexp.Regexp
	file    string
}

var registrations = []registration{
	{
		pattern: regexp.MustCompile(`^/github/repos/[^/]+/[^/]+/issues/\d+/meta\.json$`),
		file:    "github/issue.schema.json",
	},
}

var (
	compilerOnce sync.Once
	// TODO: compilerErr is a global kill switch — if any single schema fails to
	// compile during init, all validation is blocked. Refactor to per-schema error
	// tracking when the second schema is added.
	compilerErr error
	compiled    sync.Map
)

// ValidateContent checks whether content conforms to the canonical schema for a
// registered VFS path. Returns ErrUnknownPath (checkable via errors.Is) when no
// schema is registered for the path pattern, nil when validation passes, or a
// descriptive error for invalid JSON or schema violations.
func ValidateContent(path string, content []byte) error {
	schemaPath := registeredSchema(path)
	if schemaPath == "" {
		return fmt.Errorf("%w: %s", ErrUnknownPath, path)
	}

	sch, err := loadSchema(schemaPath)
	if err != nil {
		return err
	}

	var value any
	if err := json.Unmarshal(content, &value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}

	if err := sch.Validate(value); err != nil {
		return fmt.Errorf("validate %s against %s: %w", path, schemaPath, err)
	}
	return nil
}

func registeredSchema(path string) string {
	for _, item := range registrations {
		if item.pattern.MatchString(path) {
			return item.file
		}
	}
	return ""
}

func loadSchema(path string) (*jsonschema.Schema, error) {
	initCompiler()
	if compilerErr != nil {
		return nil, compilerErr
	}
	if cached, ok := compiled.Load(path); ok {
		return cached.(*jsonschema.Schema), nil
	}

	// NOTE: This fallback path creates a fresh compiler that does not share
	// state with initCompiler(). If schemas ever use $ref to reference each
	// other, this will fail to resolve cross-schema references. Refactor to a
	// single shared compiler instance when the schema set grows.
	data, err := schemaassets.FS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", path, err)
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse schema %s: %w", path, err)
	}

	compiler := newCompiler()
	if err := compiler.AddResource(path, doc); err != nil {
		return nil, fmt.Errorf("register schema %s: %w", path, err)
	}

	sch, err := compiler.Compile(path)
	if err != nil {
		return nil, fmt.Errorf("compile schema %s: %w", path, err)
	}

	actual, _ := compiled.LoadOrStore(path, sch)
	return actual.(*jsonschema.Schema), nil
}

func initCompiler() {
	compilerOnce.Do(func() {
		for _, item := range registrations {
			data, err := schemaassets.FS.ReadFile(item.file)
			if err != nil {
				compilerErr = fmt.Errorf("read schema %s: %w", item.file, err)
				return
			}
			var doc any
			if err := json.Unmarshal(data, &doc); err != nil {
				compilerErr = fmt.Errorf("parse schema %s: %w", item.file, err)
				return
			}
			compiler := newCompiler()
			if err := compiler.AddResource(item.file, doc); err != nil {
				compilerErr = fmt.Errorf("register schema %s: %w", item.file, err)
				return
			}
			sch, err := compiler.Compile(item.file)
			if err != nil {
				compilerErr = fmt.Errorf("compile schema %s: %w", item.file, err)
				return
			}
			compiled.Store(item.file, sch)
		}
	})
}

func newCompiler() *jsonschema.Compiler {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	return compiler
}
