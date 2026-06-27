package form

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/ui/common"
)

// DNSCredentialSaver persists a DNS credential set. Implemented by
// cluster.DNSStore; takes an interface so install flow tests can inject an
// in-memory fake.
type DNSCredentialSaver interface {
	Save(creds cluster.DNSCredentials) error
}

// DNSCredentialForm is the inline sub-form shown when an install or add-node
// flow encounters a domain that is not yet in domain management. The user
// fills the same four fields as the standalone "Certificate" add path;
// on save the parent re-runs its DNS lookup to confirm the new entry covers
// the install domain.
type DNSCredentialForm struct {
	ParameterForm

	Domain string             // the install/node domain that triggered the form
	Store  DNSCredentialSaver // injected for tests

	HeaderErr string // shown above the form when set (e.g. saved root does not cover domain)
	SaveErr   string // shown above the form when the last Save call failed

	Done      bool // either saved or cancelled — parent reads State()
	Saved     bool
	Cancelled bool
	Last      cluster.DNSCredentials // populated on Saved == true
}

// NewDNSCredentialForm builds an inline sub-form pre-filled with a best-effort
// guess of the registrable root domain (last two labels of presetDomain).
func NewDNSCredentialForm(presetDomain string, store DNSCredentialSaver) *DNSCredentialForm {
	f := &DNSCredentialForm{
		Domain: presetDomain,
		Store:  store,
	}
	f.ParameterForm = NewParameterForm(DNSCredentialFields(GuessRootDomain(presetDomain)))
	f.ParameterForm.Validate = ValidateDNSCredentialField
	f.ParameterForm.StartForm()
	return f
}

// DNSCredentialFields returns the fields the form collects. The provider
// selection gates which credential fields are visible: Cloudflare needs only
// an API token; Aliyun needs an AccessKey ID and Secret pair.
func DNSCredentialFields(presetRoot string) []Field {
	return []Field{
		{
			Key:   "root_domain",
			Label: "Root domain",
			Def:   presetRoot,
			Note:  "The domain you manage on the DNS provider — the TXT records for the cert challenge get written here. Usually the bare root, e.g. example.com.\nA subdomain like sub.example.com also works, as long as it shows up as its own zone in your Cloudflare / Aliyun console.",
		},
		{Key: "provider", Label: "DNS provider", Def: "cloudflare", Options: []string{"cloudflare", "aliyun"}},
		{
			Key:   "cf_token",
			Label: "Cloudflare API Token",
			Note:  "API Token with Zone:DNS:Edit permission on the root zone.",
			Skip:  func(vals map[string]string) bool { return vals["provider"] != "cloudflare" },
		},
		{
			Key:   "aliyun_access_key_id",
			Label: "Aliyun AccessKey ID",
			Skip:  func(vals map[string]string) bool { return vals["provider"] != "aliyun" },
		},
		{
			Key:   "aliyun_access_key_secret",
			Label: "Aliyun AccessKey Secret",
			Skip:  func(vals map[string]string) bool { return vals["provider"] != "aliyun" },
		},
	}
}

// ValidateDNSCredentialField enforces the required-field rules for the four
// DNS credential fields. It is also reused by cert manager validation.
func ValidateDNSCredentialField(f Field, val string, _ map[string]string) error {
	v := strings.TrimSpace(val)
	switch f.Key {
	case "root_domain":
		if v == "" {
			return fmt.Errorf("root domain is required")
		}
	case "provider":
		if v != "cloudflare" && v != "aliyun" {
			return fmt.Errorf("pick cloudflare or aliyun")
		}
	case "cf_token":
		if v == "" {
			return fmt.Errorf("api token is required")
		}
	case "aliyun_access_key_id":
		if v == "" {
			return fmt.Errorf("accesskey id is required")
		}
	case "aliyun_access_key_secret":
		if v == "" {
			return fmt.Errorf("accesskey secret is required")
		}
	}
	return nil
}

// GuessRootDomain returns the last two labels of host as a best-effort root,
// or host itself when it already has two labels or fewer. Wrong for hosts on
// multi-label TLDs like co.uk; the field is editable so the user can fix it.
func GuessRootDomain(host string) string {
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

// SetHeaderError attaches an above-the-form error (used when the saved root
// turns out not to cover the install domain — the parent re-pushes us with
// this message so the user knows to adjust the root field).
func (f *DNSCredentialForm) SetHeaderError(msg string) {
	f.HeaderErr = msg
	f.Saved = false
	f.Cancelled = false
	f.Done = false
}

// State returns (saved, cancelled, savedCredentials). Parent calls this after
// each Update to decide whether to resume the parent flow.
func (f *DNSCredentialForm) State() (saved bool, cancelled bool, creds cluster.DNSCredentials) {
	return f.Saved, f.Cancelled, f.Last
}

// Update advances the form one input. The parent uses State() after each call
// to detect saved / cancelled.
func (f *DNSCredentialForm) Update(msg tea.Msg) tea.Cmd {
	if f.Done {
		return nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.SetSize(msg.Width, msg.Height)
		return nil
	case tea.KeyMsg:
		cmd, _, handled := f.ParameterForm.HandleKey(msg, ParameterFormKeyHandlers{
			Complete: f.complete,
			Back: func() {
				f.HeaderErr = ""
				f.SaveErr = ""
				if !f.ParameterForm.PreviousField() {
					f.Cancelled = true
					f.Done = true
				}
			},
			Cancel: func() (tea.Cmd, bool) {
				f.Cancelled = true
				f.Done = true
				return nil, false
			},
		})
		if handled {
			return cmd
		}
	}
	if !f.CurrentFieldHasOptions() {
		return f.UpdateInput(msg)
	}
	return nil
}

func (f *DNSCredentialForm) complete() {
	creds := DNSCredentialsFromValues(f.Values)
	if err := f.Store.Save(creds); err != nil {
		f.SaveErr = err.Error()
		// Jump back to the last field so the user can fix and retry.
		f.ParameterForm.BackToLastField()
		return
	}
	f.SaveErr = ""
	f.Last = creds
	f.Saved = true
	f.Done = true
}

// View renders the form with an optional header banner.
func (f *DNSCredentialForm) View() string {
	var b strings.Builder
	b.WriteString(common.FlowTitle.Render("DNS credentials required"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("No DNS credentials configured for %s.\nAdd them here to continue, or press Esc to go back.", f.Domain))
	if f.HeaderErr != "" {
		b.WriteString("\n\n")
		b.WriteString(common.FlowErr.Render(f.HeaderErr))
	}
	if f.SaveErr != "" {
		b.WriteString("\n\n")
		b.WriteString(common.FlowErr.Render("Save failed: " + f.SaveErr))
	}
	b.WriteString("\n\n")
	b.WriteString(f.ParameterForm.View("DNS credentials"))
	return b.String()
}

func (f *DNSCredentialForm) FooterHints() []common.OperationHint {
	return f.ParameterForm.FooterHints()
}

// DNSCredentialsFromValues maps the provider-conditional form values back to
// the storage struct: Cloudflare's cf_token becomes APIToken; Aliyun's
// AccessKey ID/Secret become APIToken/APISecret. Shared with cert.go.
func DNSCredentialsFromValues(vals map[string]string) cluster.DNSCredentials {
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
