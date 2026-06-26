package ui

import (
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/ui/form"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

// Aliases let existing ui code keep embedded field names (parameterForm,
// dnsCredentialForm) lowercase while the underlying types live in
// internal/ui/form.
type (
	parameterForm            = form.ParameterForm
	parameterFormKeyHandlers = form.ParameterFormKeyHandlers
	field                    = form.Field
	dnsCredentialForm        = form.DNSCredentialForm
	reorderForm              = form.ReorderForm
	reorderItem              = form.ReorderItem
)

func newParameterForm(fields []field) parameterForm { return form.NewParameterForm(fields) }

func newDNSCredentialForm(presetDomain string, store form.DNSCredentialSaver) *dnsCredentialForm {
	return form.NewDNSCredentialForm(presetDomain, store)
}

func newReorderForm(items []reorderItem) reorderForm { return form.NewReorderForm(items) }

func fieldFromParameter(f uiparams.Field) field { return form.FieldFromParameter(f) }

func fieldsFromParameters(params []uiparams.Field) []field {
	return form.FieldsFromParameters(params)
}

func parameterFromField(f field) uiparams.Field { return form.ParameterFromField(f) }

func dnsCredentialsFromValues(vals map[string]string) cluster.DNSCredentials {
	return form.DNSCredentialsFromValues(vals)
}

func optionIndex(options []string, value string) int {
	for i, opt := range options {
		if opt == value {
			return i
		}
	}
	return 0
}

func selectedOptions(value string) map[string]bool {
	selected := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			selected[part] = true
		}
	}
	return selected
}

func selectedOptionsValue(options []string, selected map[string]bool) string {
	values := make([]string, 0, len(options))
	for _, opt := range options {
		if selected[opt] {
			values = append(values, opt)
		}
	}
	return strings.Join(values, ",")
}
