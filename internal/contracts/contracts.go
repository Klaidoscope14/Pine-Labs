// Package contracts validates agent inputs and outputs against their JSON Schemas.
package contracts

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"aegis/schemas"
)

var (
	once     sync.Once
	compiled map[string]*jsonschema.Schema
	loadErr  error
)

func load() {
	compiled = map[string]*jsonschema.Schema{}
	c := jsonschema.NewCompiler()
	for name, path := range schemas.AgentSchemas {
		raw, err := schemas.FS.ReadFile(path)
		if err != nil {
			loadErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", path, err)
			return
		}
		if err := c.AddResource(path, doc); err != nil {
			loadErr = err
			return
		}
		s, err := c.Compile(path)
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", path, err)
			return
		}
		compiled[name] = s
	}
}

// Validate checks a JSON document against the named agent contract.
func Validate(contract string, jsonDoc []byte) error {
	once.Do(load)
	if loadErr != nil {
		return loadErr
	}
	s, ok := compiled[contract]
	if !ok {
		return fmt.Errorf("unknown contract %q", contract)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonDoc))
	if err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	return s.Validate(doc)
}
