package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
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

// validator compiles and caches the canonical schemas for a registration set.
// Compile failures are tracked per schema file, so one broken schema only
// blocks validation for its own paths; unrelated paths validate normally.
type validator struct {
	registrations []registration
	fsys          fs.FS

	once sync.Once
	// compileErrs maps schema file path -> compile/load error from init.
	compileErrs map[string]error
	compiled    sync.Map
}

var defaultValidator = &validator{
	registrations: registrations,
	fsys:          schemaassets.FS,
}

// ValidateContent checks whether content conforms to the canonical schema for a
// registered VFS path. Returns ErrUnknownPath (checkable via errors.Is) when no
// schema is registered for the path pattern, nil when validation passes, or a
// descriptive error for invalid JSON or schema violations.
//
// If the schema registered for the path failed to compile, the compile error is
// returned (fail-closed for that path); schemas for other paths are unaffected.
func ValidateContent(path string, content []byte) error {
	return defaultValidator.ValidateContent(path, content)
}

func (v *validator) ValidateContent(path string, content []byte) error {
	schemaPath := v.registeredSchema(path)
	if schemaPath == "" {
		return fmt.Errorf("%w: %s", ErrUnknownPath, path)
	}

	sch, err := v.loadSchema(schemaPath)
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

func (v *validator) registeredSchema(path string) string {
	for _, item := range v.registrations {
		if item.pattern.MatchString(path) {
			return item.file
		}
	}
	return ""
}

func (v *validator) loadSchema(path string) (*jsonschema.Schema, error) {
	v.init()
	if err, ok := v.compileErrs[path]; ok {
		return nil, err
	}
	if cached, ok := v.compiled.Load(path); ok {
		return cached.(*jsonschema.Schema), nil
	}

	// NOTE: This fallback path creates a fresh compiler that does not share
	// state with init(). If schemas ever use $ref to reference each other,
	// this will fail to resolve cross-schema references. Refactor to a
	// single shared compiler instance when the schema set grows.
	sch, err := compileSchemaFile(v.fsys, path)
	if err != nil {
		return nil, err
	}

	actual, _ := v.compiled.LoadOrStore(path, sch)
	return actual.(*jsonschema.Schema), nil
}

func (v *validator) init() {
	v.once.Do(func() {
		v.compileErrs = make(map[string]error)
		for _, item := range v.registrations {
			sch, err := compileSchemaFile(v.fsys, item.file)
			if err != nil {
				// Track the failure per schema so only paths registered to
				// this schema are blocked; other schemas validate normally.
				v.compileErrs[item.file] = err
				log.Printf("schema: %v; validation for paths registered to %s will fail until fixed", err, item.file)
				continue
			}
			v.compiled.Store(item.file, sch)
		}
	})
}

func compileSchemaFile(fsys fs.FS, path string) (*jsonschema.Schema, error) {
	data, err := fs.ReadFile(fsys, path)
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
	return sch, nil
}

func newCompiler() *jsonschema.Compiler {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	return compiler
}
