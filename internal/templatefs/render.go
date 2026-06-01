// Package templatefs renders embedded templates with text/template. Every
// template, including JSON and YAML, renders through this path; missing keys
// are treated as errors so template/data mismatches fail loudly.
package templatefs

import (
	"bytes"
	"text/template"

	assets "github.com/C5Hwang/singbox-deploy/template"
)

// Render parses the named embedded template and executes it against data.
func Render(name string, data any) (string, error) {
	b, err := assets.FS.ReadFile(name)
	if err != nil {
		return "", err
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(b))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
