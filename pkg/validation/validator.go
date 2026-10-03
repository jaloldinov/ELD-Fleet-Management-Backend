// Package validation collects input-validation errors and provides common,
// project-independent checks (email, time zone, US state code).
//
// It knows nothing about our domain: business rules (which fields a truck
// needs, which roles a user may have...) stay in internal/service and use
// this package as a tool.
package validation

import (
	"fmt"
	"sort"
	"strings"
)

// ValidationError lists every invalid input field and why it is invalid.
// Keys are the API field names (e.g. "dot_number") so clients can show each
// message next to the right form input. The handler turns it into HTTP 400.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys) // maps have random order; sort for stable messages

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, e.Fields[k]))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Validator collects field errors so we can report all of them at once
// instead of stopping at the first one.
type Validator struct {
	fields map[string]string
}

// Check records msg for field when ok is false. Only the first message per
// field is kept.
func (v *Validator) Check(ok bool, field, msg string) {
	if ok {
		return
	}
	if v.fields == nil {
		v.fields = make(map[string]string)
	}
	if _, exists := v.fields[field]; !exists {
		v.fields[field] = msg
	}
}

// Err returns a *ValidationError if any check failed, otherwise nil.
func (v *Validator) Err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: v.fields}
}
