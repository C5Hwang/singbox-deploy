package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
)

// dnsCredentialSaver persists a DNS credential set. Implemented by
// cluster.DNSStore; takes an interface so install_flow_test.go can inject an
// in-memory fake.
type dnsCredentialSaver interface {
	Save(creds cluster.DNSCredentials) error
}

// dnsCredentialForm is the inline sub-form shown when an install or add-node
// flow encounters a domain that is not yet in domain management. The user
// fills the same four fields as the standalone "Certificate & site" add path;
// on save the parent re-runs its DNS lookup to confirm the new entry covers
// the install domain.
type dnsCredentialForm struct {
	parameterForm

	domain string             // the install/node domain that triggered the form
	store  dnsCredentialSaver // injected for tests
	width  int
	height int

	headerErr string // shown above the form when set (e.g. saved root does not cover domain)
	saveErr   string // shown above the form when the last Save call failed

	done      bool // either saved or cancelled — parent reads State()
	saved     bool
	cancelled bool
	last      cluster.DNSCredentials // populated on saved == true
}

// newDNSCredentialForm builds an inline sub-form pre-filled with a best-effort
// guess of the registrable root domain (last two labels of presetDomain).
func newDNSCredentialForm(presetDomain string, store dnsCredentialSaver) *dnsCredentialForm {
	f := &dnsCredentialForm{
		domain: presetDomain,
		store:  store,
	}
	f.parameterForm = newParameterForm(dnsCredentialFields(guessRootDomain(presetDomain)))
	f.parameterForm.validate = validateCertField
	f.parameterForm.startForm()
	return f
}

// dnsCredentialFields returns the fields the form collects. The provider
// selection gates which credential fields are visible: Cloudflare needs only
// an API token; Aliyun needs an AccessKey ID and Secret pair.
func dnsCredentialFields(presetRoot string) []field {
	return []field{
		{
			key:   "root_domain",
			label: "Root domain",
			def:   presetRoot,
			note:  "Root zone where the DNS-01 TXT records will be written (e.g. example.com). Adjust for multi-label TLDs such as co.uk; use the punycode form for IDN.",
		},
		{key: "provider", label: "DNS provider", def: "cloudflare", options: []string{"cloudflare", "aliyun"}},
		{
			key:   "cf_token",
			label: "Cloudflare API Token",
			note:  "API Token with Zone:DNS:Edit permission on the root zone.",
			skip:  func(vals map[string]string) bool { return vals["provider"] != "cloudflare" },
		},
		{
			key:   "aliyun_access_key_id",
			label: "Aliyun AccessKey ID",
			skip:  func(vals map[string]string) bool { return vals["provider"] != "aliyun" },
		},
		{
			key:   "aliyun_access_key_secret",
			label: "Aliyun AccessKey Secret",
			skip:  func(vals map[string]string) bool { return vals["provider"] != "aliyun" },
		},
	}
}

// guessRootDomain returns the last two labels of host as a best-effort root,
// or host itself when it already has two labels or fewer. Wrong for hosts on
// multi-label TLDs like co.uk; the field is editable so the user can fix it.
func guessRootDomain(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

func (f *dnsCredentialForm) setSize(width, height int) {
	f.width = width
	f.height = height
	f.parameterForm.setSize(width, height)
}

// SetHeaderError attaches an above-the-form error (used when the saved root
// turns out not to cover the install domain — the parent re-pushes us with
// this message so the user knows to adjust the root field).
func (f *dnsCredentialForm) SetHeaderError(msg string) {
	f.headerErr = msg
	f.saved = false
	f.cancelled = false
	f.done = false
}

// State returns (saved, cancelled, savedCredentials). Parent calls this after
// each Update to decide whether to resume the parent flow.
func (f *dnsCredentialForm) State() (saved bool, cancelled bool, creds cluster.DNSCredentials) {
	return f.saved, f.cancelled, f.last
}

// Update advances the form one input. The parent uses State() after each call
// to detect saved / cancelled.
func (f *dnsCredentialForm) Update(msg tea.Msg) tea.Cmd {
	if f.done {
		return nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.setSize(msg.Width, msg.Height)
		return nil
	case tea.KeyMsg:
		cmd, _, handled := f.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: f.complete,
			Back: func() {
				f.headerErr = ""
				f.saveErr = ""
				if !f.parameterForm.previousField() {
					f.cancelled = true
					f.done = true
				}
			},
			Cancel: func() (tea.Cmd, bool) {
				f.cancelled = true
				f.done = true
				return nil, false
			},
		})
		if handled {
			return cmd
		}
	}
	if !f.currentFieldHasOptions() {
		return f.updateInput(msg)
	}
	return nil
}

func (f *dnsCredentialForm) complete() {
	creds := dnsCredentialsFromValues(f.values)
	if err := f.store.Save(creds); err != nil {
		f.saveErr = err.Error()
		// Jump back to the last field so the user can fix and retry.
		f.parameterForm.backToLastField()
		return
	}
	f.saveErr = ""
	f.last = creds
	f.saved = true
	f.done = true
}

// View renders the form with an optional header banner.
func (f *dnsCredentialForm) View() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("DNS credentials required"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("No DNS credentials configured for %s.\nAdd them here to continue, or press Esc to go back.", f.domain))
	if f.headerErr != "" {
		b.WriteString("\n\n")
		b.WriteString(flowErr.Render(f.headerErr))
	}
	if f.saveErr != "" {
		b.WriteString("\n\n")
		b.WriteString(flowErr.Render("Save failed: " + f.saveErr))
	}
	b.WriteString("\n\n")
	b.WriteString(f.parameterForm.View("DNS credentials"))
	return b.String()
}

func (f *dnsCredentialForm) footerHints() []operationHint {
	return f.parameterForm.footerHints()
}

// dnsCredentialsFromValues maps the provider-conditional form values back to
// the storage struct: Cloudflare's cf_token becomes APIToken; Aliyun's
// AccessKey ID/Secret become APIToken/APISecret. Shared with cert.go.
func dnsCredentialsFromValues(vals map[string]string) cluster.DNSCredentials {
	provider := strings.TrimSpace(vals["provider"])
	creds := cluster.DNSCredentials{
		RootDomain: strings.TrimSpace(vals["root_domain"]),
		Provider:   provider,
	}
	switch provider {
	case "cloudflare":
		creds.APIToken = strings.TrimSpace(vals["cf_token"])
	case "aliyun":
		creds.APIToken = strings.TrimSpace(vals["aliyun_access_key_id"])
		creds.APISecret = strings.TrimSpace(vals["aliyun_access_key_secret"])
	}
	return creds
}
