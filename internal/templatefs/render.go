// Package templatefs renders embedded templates with text/template. Every
// template, including JSON and YAML, renders through this path; missing keys
// are treated as errors so template/data mismatches fail loudly.
package templatefs

import (
	"bytes"
	"encoding/json"
	"text/template"

	"github.com/C5Hwang/singbox-deploy/assets"
)

// funcMap exposes helpers to every template. "json" marshals a value to its
// JSON literal form so JSON/YAML templates can safely interpolate strings,
// numbers, and arrays without ad-hoc quoting.
var funcMap = template.FuncMap{
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	},
}

// Render parses the named embedded template and executes it against data.
func Render(name string, data any) (string, error) {
	b, err := assets.FS.ReadFile(name)
	if err != nil {
		return "", err
	}
	t, err := template.New(name).Funcs(funcMap).Option("missingkey=error").Parse(string(b))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
