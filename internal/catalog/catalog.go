// Package catalog resolves stable finding keys (e.g. "ambiguity.vague_quantifier")
// into human-readable text. It is the zero-retention bridge: Postgres stores
// keys + enums + anchors, the catalog turns them into sentences at read time.
//
// Entries live in catalog_data.go as a Go map literal. Unknown keys are hard
// errors — never silent fallback. Param values are validated against a
// whitelist pattern so user-authored text can never leak via params.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// Role selects a text variant. The "default" variant is always present; PM,
// QA, and Dev variants are added in Phase 3. Resolving an unset role falls
// back to default.
type Role string

const (
	RoleDefault Role = ""
	RolePM      Role = "pm"
	RoleQA      Role = "qa"
	RoleDev     Role = "dev"
)

// paramValuePattern restricts param values that come from the LLM. Numeric
// strings, field names, and short enum values match; free-form sentences
// do not. This is the last-line defense against issue text leaking via
// params into persisted rows.
var paramValuePattern = regexp.MustCompile(`^[A-Za-z0-9_.:/ -]{0,48}$`)

// ErrUnknownKey is returned when Resolve is called with a key not in the
// catalog. The worker treats this as a fail-the-job condition.
var ErrUnknownKey = errors.New("catalog: unknown message key")

// ErrParamMismatch is returned when the params supplied to Resolve do not
// match the entry's declared param set.
var ErrParamMismatch = errors.New("catalog: params do not match entry")

// ErrParamValueRejected is returned when a param value fails the whitelist
// pattern — almost certainly the LLM trying to smuggle issue text.
var ErrParamValueRejected = errors.New("catalog: param value rejected by whitelist")

// Catalog is the resolved, ready-to-use message catalog. Load once at boot
// and share across goroutines — it is read-only after Load.
type Catalog struct {
	entries map[string]*entry
}

type entry struct {
	params    []string
	paramSet  map[string]struct{}
	templates map[Role]*template.Template // always has RoleDefault; optional pm/qa/dev
}

// rawEntry is the form authors write in catalog_data.go.
type rawEntry struct {
	Params  []string
	Default string
	PM      string
	QA      string
	Dev     string
}

// Load compiles every entry's templates and returns a usable Catalog. Call
// once at startup; treat the result as immutable.
func Load() (*Catalog, error) {
	if len(rawEntries) == 0 {
		return nil, errors.New("catalog: no entries")
	}
	cat := &Catalog{entries: make(map[string]*entry, len(rawEntries))}
	for key, re := range rawEntries {
		e, err := buildEntry(key, re)
		if err != nil {
			return nil, err
		}
		cat.entries[key] = e
	}
	return cat, nil
}

func buildEntry(key string, re rawEntry) (*entry, error) {
	if re.Default == "" {
		return nil, fmt.Errorf("catalog: entry %q missing default template", key)
	}
	paramSet := make(map[string]struct{}, len(re.Params))
	for _, p := range re.Params {
		paramSet[p] = struct{}{}
	}
	templates := make(map[Role]*template.Template, 4)
	for role, body := range map[Role]string{
		RoleDefault: re.Default,
		RolePM:      re.PM,
		RoleQA:      re.QA,
		RoleDev:     re.Dev,
	} {
		if body == "" {
			continue
		}
		tmpl, err := template.New(string(role) + ":" + key).Option("missingkey=error").Parse(body)
		if err != nil {
			return nil, fmt.Errorf("catalog: entry %q role %q template: %w", key, role, err)
		}
		templates[role] = tmpl
	}
	return &entry{params: re.Params, paramSet: paramSet, templates: templates}, nil
}

// Resolve renders the human text for a finding. Returns an error if the key
// is unknown, if params do not match the entry's declared set, or if any
// param value fails the whitelist. Unknown role falls back to default.
func (c *Catalog) Resolve(key string, params map[string]string, role Role) (string, error) {
	e, ok := c.entries[key]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	for _, p := range e.params {
		if _, ok := params[p]; !ok {
			return "", fmt.Errorf("%w: %q missing param %q", ErrParamMismatch, key, p)
		}
	}
	for k, v := range params {
		if _, ok := e.paramSet[k]; !ok {
			return "", fmt.Errorf("%w: %q has unexpected param %q", ErrParamMismatch, key, k)
		}
		if !paramValuePattern.MatchString(v) {
			return "", fmt.Errorf("%w: %q.%q", ErrParamValueRejected, key, k)
		}
	}
	tmpl, ok := e.templates[role]
	if !ok {
		tmpl = e.templates[RoleDefault]
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("catalog: render %q: %w", key, err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// Keys returns the sorted list of registered keys.
func (c *Catalog) Keys() []string {
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
