// Package form carries validation messages from the rules back to the page that shows them.
package form

import (
	"sort"
	"strings"
)

// Errors maps a form field name to the message shown next to it, in Portuguese.
//
//	errs := form.Errors{}
//	errs["login"] = "Este login já existe."
//	if len(errs) > 0 { return errs }
type Errors map[string]string

func (e Errors) Error() string {
	fields := make([]string, 0, len(e))
	for field := range e {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	parts := make([]string, 0, len(e))
	for _, field := range fields {
		parts = append(parts, field+": "+e[field])
	}
	return "invalid form: " + strings.Join(parts, "; ")
}
