package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type certPhase int

const (
	certPhaseList certPhase = iota
	certPhaseForm
	certPhaseCredList
	certPhaseCredForm
	certPhaseCertPick
	certPhaseCredPick
	certPhaseRunning
	certPhaseDone
)

// certManager is the Certificate & DNS-credential management page. It exposes
// the certmgr inventory: issue/renew a certificate (DNS-01), delete one, and
// manage the DNS credentials whose base domains authorize issuance by suffix
// match. Entering a domain not covered by any credential redirects to the
// credential form and resumes issuance once a covering credential is added.
type certManager struct {
	run  commandRun
	form parameterForm

	layout    paths.Layout
	phase     certPhase
	inventory []certmgr.CertInfo
	creds     []certmgr.DNSCredential

	actionCursor     int
	credActionCursor int
	pickCursor       int

	// Issuance continuation after adding a credential mid-flow.
	pendingDomain        string
	pendingEmail         string
	resumeIssueAfterCred bool
	// returnAfterIssue is set when another form (hub install / add spoke)
	// redirected here because its domain was not managed yet. After issuance,
	// the next key returns directly to the suspended caller.
	returnAfterIssue bool

	result   string
	loadErr  error
	startCmd tea.Cmd // waitForRun command produced when a run starts inside a callback
}

var certActions = []string{"Add / force renew certificate now", "Delete certificate", "Manage DNS credentials"}
var credActions = []string{"Add DNS credential", "Delete DNS credential"}

func newCertManager() *certManager {
	m := &certManager{
		run:    newCommandRun(),
		form:   newParameterForm(nil),
		layout: paths.DefaultLayout(),
		phase:  certPhaseList,
	}
	m.reload()
	return m
}

func newCertManagerForDomain(domain, email string) *certManager {
	m := newCertManager()
	m.returnAfterIssue = true
	m.beginCertFormWithSeed(domain, email)
	return m
}

func (m *certManager) reload() {
	if err := certmgr.SeedLegacyCredentials(m.layout); err != nil {
		m.loadErr = err
	}
	inv, err := certmgr.Inventory(m.layout)
	if err != nil {
		m.loadErr = err
	}
	m.inventory = inv
	creds, err := certmgr.LoadCredentials(m.layout)
	if err != nil {
		m.loadErr = err
	}
	m.creds = creds
}

func (m *certManager) runState() *commandRun { return &m.run }
func (m *certManager) markRunFailed()        { m.phase = certPhaseDone }

func (m *certManager) setSize(w, h int) {
	m.run.setSize(w, h)
	m.form.setSize(w, h)
}

func (m *certManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m.phase {
	case certPhaseRunning:
		return m.updateRunning(msg)
	case certPhaseDone:
		if _, ok := msg.(tea.KeyMsg); ok {
			if m.returnAfterIssue {
				return nil, true
			}
			m.reload()
			m.phase = certPhaseList
		}
		return nil, false
	case certPhaseForm, certPhaseCredForm:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateForm(key)
		}
		return nil, false
	case certPhaseList:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateList(key)
		}
	case certPhaseCredList:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateCredList(key)
		}
	case certPhaseCertPick:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateCertPick(key)
		}
	case certPhaseCredPick:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateCredPick(key)
		}
	}
	return nil, false
}

func (m *certManager) updateRunning(msg tea.Msg) (tea.Cmd, bool) {
	if rm, ok := msg.(runMsg); ok {
		cmd := handleCommandRun(m, rm)
		if rm.done {
			if rm.err == nil {
				m.result = "certificate issued"
			}
			m.phase = certPhaseDone
			m.reload()
		}
		return cmd, false
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		m.run.handleScrollKey(key.String(), m.run.logViewportHeight())
	}
	return nil, false
}

func (m *certManager) updateList(key tea.KeyMsg) (tea.Cmd, bool) {
	_, done, _ := handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.actionCursor = moveSelection(m.actionCursor, len(certActions), d) },
		Confirm: func() (tea.Cmd, bool) {
			switch m.actionCursor {
			case 0:
				m.beginCertForm()
			case 1:
				if len(m.inventory) == 0 {
					m.result = "no certificates to delete"
					return nil, false
				}
				m.pickCursor = 0
				m.phase = certPhaseCertPick
			case 2:
				m.credActionCursor = 0
				m.phase = certPhaseCredList
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { return nil, true },
	})
	return nil, done
}

func (m *certManager) updateCredList(key tea.KeyMsg) (tea.Cmd, bool) {
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.credActionCursor = moveSelection(m.credActionCursor, len(credActions), d) },
		Confirm: func() (tea.Cmd, bool) {
			switch m.credActionCursor {
			case 0:
				m.beginCredForm("")
			case 1:
				if len(m.creds) == 0 {
					m.result = "no credentials to delete"
					return nil, false
				}
				m.pickCursor = 0
				m.phase = certPhaseCredPick
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { m.phase = certPhaseList; return nil, false },
	})
	return nil, false
}

func (m *certManager) updateCertPick(key tea.KeyMsg) (tea.Cmd, bool) {
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.pickCursor = moveSelection(m.pickCursor, len(m.inventory), d) },
		Confirm: func() (tea.Cmd, bool) {
			if idx, ok := selectedIndex(m.pickCursor, len(m.inventory)); ok {
				domain := m.inventory[idx].Domain
				consumers, err := (&hubctl.Controller{Layout: m.layout}).CertificateConsumers(domain)
				if err != nil {
					m.result = "delete failed: " + err.Error()
				} else if len(consumers) > 0 {
					m.result = fmt.Sprintf("cannot delete %s: certificate is used by %s", domain, strings.Join(consumers.Labels(), ", "))
				} else if err := certmgr.Deregister(m.layout, domain); err != nil {
					m.result = "delete failed: " + err.Error()
				} else {
					m.result = "deleted certificate " + domain
				}
				m.reload()
			}
			m.phase = certPhaseList
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { m.phase = certPhaseList; return nil, false },
	})
	return nil, false
}

func (m *certManager) updateCredPick(key tea.KeyMsg) (tea.Cmd, bool) {
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.pickCursor = moveSelection(m.pickCursor, len(m.creds), d) },
		Confirm: func() (tea.Cmd, bool) {
			if idx, ok := selectedIndex(m.pickCursor, len(m.creds)); ok {
				if err := certmgr.DeleteCredential(m.layout, m.creds[idx].Domain); err != nil {
					m.result = "delete failed: " + err.Error()
				} else {
					m.result = "deleted DNS credential"
				}
				m.reload()
			}
			m.phase = certPhaseCredList
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { m.phase = certPhaseCredList; return nil, false },
	})
	return nil, false
}

func (m *certManager) updateForm(key tea.KeyMsg) (tea.Cmd, bool) {
	m.startCmd = nil
	cmd, _, _ := m.form.handleKey(key, parameterFormKeyHandlers{
		Complete: m.completeForm,
		Cancel: func() (tea.Cmd, bool) {
			if m.returnAfterIssue {
				return nil, true
			}
			// Cancel returns to the originating list.
			if m.phase == certPhaseCredForm {
				m.phase = certPhaseCredList
			} else {
				m.phase = certPhaseList
			}
			return nil, false
		},
	})
	// completeForm may have started an issuance run; its waitForRun command must
	// be returned so Bubble Tea drains the run channel.
	if m.startCmd != nil {
		return m.startCmd, false
	}
	return cmd, false
}

func (m *certManager) beginCertForm() {
	m.beginCertFormWithSeed("", "")
}

func (m *certManager) beginCertFormWithSeed(domain, email string) {
	seed := map[string]string{}
	if domain != "" {
		seed["domain"] = domain
	}
	if email != "" {
		seed["email"] = email
	}
	m.form.begin([]field{
		{key: "domain", label: "Certificate domain", note: "Must be covered by a DNS credential (longest suffix match). Continuing forces a fresh DNS-01 issuance now; repeated issuance is subject to Let's Encrypt rate limits. If no credential covers it, you'll be sent to add one."},
		{key: "email", label: "ACME account email (optional)", note: "Let's Encrypt contact for expiry notices."},
	}, seed, validateCertField)
	m.phase = certPhaseForm
}

func (m *certManager) beginCredForm(seedDomain string) {
	seed := map[string]string{}
	if seedDomain != "" {
		seed["domain"] = seedDomain
	}
	m.form.begin([]field{
		{key: "domain", label: "Base domain", note: "Authorizes this domain and every subdomain (e.g. example.com covers a.example.com)."},
		{key: "provider", label: "DNS provider", def: certmgr.ProviderCloudflare, options: []string{certmgr.ProviderCloudflare, certmgr.ProviderAliyun}},
		{key: "credential", label: "API credential", secret: true, noteFunc: credentialNote},
		{key: "email", label: "ACME account email (optional)", note: "Used as the Let's Encrypt account contact when issuing under this domain."},
	}, seed, validateCredField)
	m.phase = certPhaseCredForm
}

func (m *certManager) completeForm() {
	if m.phase == certPhaseCredForm {
		m.completeCredForm()
		return
	}
	domain := strings.TrimSpace(m.form.values["domain"])
	email := strings.TrimSpace(m.form.values["email"])
	if !certmgr.CredentialCovers(m.creds, domain) {
		// Redirect to add a covering credential, then resume issuance.
		m.pendingDomain = domain
		m.pendingEmail = email
		m.resumeIssueAfterCred = true
		m.result = "no DNS credential covers " + domain + "; add one to continue"
		m.beginCredForm(domain)
		return
	}
	m.startIssue(domain, email)
}

func (m *certManager) completeCredForm() {
	cred := certmgr.DNSCredential{
		Domain:     strings.TrimSpace(m.form.values["domain"]),
		Provider:   strings.TrimSpace(m.form.values["provider"]),
		Credential: strings.TrimSpace(m.form.values["credential"]),
		Email:      strings.TrimSpace(m.form.values["email"]),
	}
	if err := certmgr.UpsertCredential(m.layout, cred); err != nil {
		m.result = "save credential failed: " + err.Error()
		m.reload()
		m.phase = certPhaseCredList
		return
	}
	m.reload()
	if m.resumeIssueAfterCred {
		m.resumeIssueAfterCred = false
		if certmgr.CredentialCovers(m.creds, m.pendingDomain) {
			m.startIssue(m.pendingDomain, m.pendingEmail)
			return
		}
	}
	m.result = "saved DNS credential for " + cred.Domain
	m.phase = certPhaseCredList
}

func (m *certManager) startIssue(domain, email string) {
	m.phase = certPhaseRunning
	ch := make(chan runMsg, 64)
	m.run.resetRun(ch)
	logs := &logWriter{ch: ch}
	mgr := &certmgr.Manager{Layout: m.layout, Output: logs}
	go func() {
		fmt.Fprintf(logs, "issuing certificate for %s via DNS-01...\n", domain)
		_, err := mgr.Issue(context.Background(), domain, email)
		if err == nil {
			ctrl := &hubctl.Controller{Layout: m.layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion}
			err = ctrl.DistributeCertificate(context.Background(), domain, logs)
		}
		ch <- runMsg{done: true, err: err}
	}()
	m.startCmd = m.run.waitForRun()
}

func (m *certManager) View() string {
	switch m.phase {
	case certPhaseRunning:
		return commandRunningView(m, "Issuing certificate")
	case certPhaseDone:
		if m.run.runErr != nil {
			return commandFailedView(m, "Certificate issuance failed")
		}
		return flowTitle.Render("Certificate issued") + "\n\n" + flowOK.Render("Press any key to return")
	case certPhaseForm, certPhaseCredForm:
		title := "Add / force renew certificate now"
		if m.phase == certPhaseCredForm {
			title = "Add DNS credential"
		}
		return m.form.View(title)
	case certPhaseCredList:
		return m.credListView()
	case certPhaseCertPick:
		return m.pickView("Delete certificate", certInfoLabels(m.inventory))
	case certPhaseCredPick:
		return m.pickView("Delete DNS credential", credLabels(m.creds))
	default:
		return m.listView()
	}
}

func (m *certManager) listView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Certificate management") + "\n\n")
	if len(m.inventory) == 0 {
		b.WriteString(dimStyle.Render("No managed certificates yet.") + "\n\n")
	} else {
		b.WriteString(titleStyle.Render("Certificates") + "\n")
		now := time.Now()
		for _, c := range m.inventory {
			b.WriteString("  " + renderCertRow(c, now) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf("DNS credentials: %d", len(m.creds))) + "\n\n")
	b.WriteString(renderActionMenu(certActions, m.actionCursor))
	if m.result != "" {
		b.WriteString("\n\n" + summaryInfo.Render(m.result))
	}
	return b.String()
}

func (m *certManager) credListView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("DNS credentials") + "\n\n")
	if len(m.creds) == 0 {
		b.WriteString(dimStyle.Render("No DNS credentials yet. Add one to authorize certificate issuance.") + "\n\n")
	} else {
		for _, c := range m.creds {
			b.WriteString("  " + credLabel(c) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(renderActionMenu(credActions, m.credActionCursor))
	return b.String()
}

func (m *certManager) pickView(title string, labels []string) string {
	var b strings.Builder
	b.WriteString(flowTitle.Render(title) + "\n\n")
	for i, label := range labels {
		row := "  " + label
		if i == m.pickCursor {
			row = selStyle.Render("> " + label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (m *certManager) footerHints() []operationHint {
	switch m.phase {
	case certPhaseRunning:
		return runningFooterHints(false)
	case certPhaseDone:
		return doneFooterHints(m.run.runErr != nil)
	case certPhaseForm, certPhaseCredForm:
		return m.form.footerHints()
	case certPhaseCertPick, certPhaseCredPick:
		return actionFooterHints("Delete")
	case certPhaseCredList:
		return actionBackFooterHints("Select")
	default:
		return actionFooterHints("Select")
	}
}

// --- rendering helpers ---

func renderActionMenu(actions []string, cursor int) string {
	var rows []string
	for i, a := range actions {
		row := "  " + a
		if i == cursor {
			row = selStyle.Render("> " + a)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func renderCertRow(c certmgr.CertInfo, now time.Time) string {
	if c.NeedsDNSCredential {
		return c.Domain + "  " + statusWarn.Render("needs DNS credential")
	}
	if !c.Present {
		return c.Domain + "  " + statusWarn.Render("not issued")
	}
	days := c.RemainingDays(now)
	status := statusOK
	label := fmt.Sprintf("%d days left", days)
	switch {
	case !c.Valid || days < 0:
		status = statusBad
		label = "expired/invalid"
	case days < 14:
		status = statusWarn
	}
	return fmt.Sprintf("%s  %s  %s", c.Domain, status.Render(label), dimStyle.Render(c.NotAfter.Format("2006-01-02")))
}

func certInfoLabels(inv []certmgr.CertInfo) []string {
	labels := make([]string, len(inv))
	for i, c := range inv {
		labels[i] = c.Domain
	}
	return labels
}

func credLabel(c certmgr.DNSCredential) string {
	return fmt.Sprintf("%s (%s)", c.Domain, c.Provider)
}

func credLabels(creds []certmgr.DNSCredential) []string {
	labels := make([]string, len(creds))
	for i, c := range creds {
		labels[i] = credLabel(c)
	}
	return labels
}

func credentialNote(vals map[string]string) string {
	if vals["provider"] == certmgr.ProviderAliyun {
		return "Aliyun: enter AccessKeyID:AccessKeySecret (colon-separated)."
	}
	return "Cloudflare: enter an API token with DNS edit permission for the zone."
}

func validateCertField(f field, value string, _ map[string]string) error {
	if f.key == "domain" {
		if _, err := certmgr.NormalizeDomain(value); err != nil {
			return err
		}
	}
	return nil
}

func validateCredField(f field, value string, vals map[string]string) error {
	switch f.key {
	case "domain":
		if _, err := certmgr.NormalizeDomain(value); err != nil {
			return err
		}
	case "credential":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("credential is required")
		}
		if vals["provider"] == certmgr.ProviderAliyun && !strings.Contains(value, ":") {
			return fmt.Errorf("Aliyun credential must be AccessKeyID:AccessKeySecret")
		}
	}
	return nil
}

func looksLikeDomain(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && strings.Contains(s, ".") && !strings.ContainsAny(s, " /")
}
