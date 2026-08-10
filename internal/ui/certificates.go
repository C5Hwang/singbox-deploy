package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
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
	certPhaseRenewPick
	certPhaseRenewConfirm
	certPhaseCertPick
	certPhaseCredPick
	certPhaseRunning
	certPhaseDone
)

type certOperation int

const (
	certOperationAdd certOperation = iota
	certOperationRenew
)

// certManager is the Certificate & DNS-credential management page. It exposes
// the certmgr inventory: add a new certificate, force-renew an existing one,
// delete one, and manage the DNS zones whose names authorize issuance by
// suffix match. Entering a new domain not covered by any zone redirects to the
// zone form and resumes issuance once a covering zone is added.
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
	operation        certOperation
	pendingRenew     certmgr.CertInfo

	// Issuance continuation after adding a credential mid-flow.
	pendingDomain        string
	resumeIssueAfterCred bool
	// returnAfterIssue is set when another form (hub install / add spoke)
	// redirected here because its domain was not managed yet. After issuance,
	// the next key returns directly to the suspended caller.
	returnAfterIssue bool

	notice   transientNotice
	loadErr  error
	startCmd tea.Cmd // waitForRun command produced when a run starts inside a callback

	// Per-instance hooks keep certificate-flow tests deterministic while the
	// production defaults below remain the concrete certmgr/hubctl operations.
	issueCertificate      func(context.Context, string, io.Writer) error
	distributeCertificate func(context.Context, string, io.Writer, func(deploy.Event)) error
	// countDistributionTargets reports how many activation steps the
	// distribution stage will report, so the bar is sized before issuance.
	countDistributionTargets func(string) int
}

var certActions = []string{"Add certificate", "Renew certificate", "Delete certificate", "Manage DNS zones"}
var credActions = []string{"Add DNS zone", "Delete DNS zone"}

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

func newCertManagerForDomain(domain string) *certManager {
	m := newCertManager()
	m.returnAfterIssue = true
	m.beginCertFormWithSeed(domain)
	return m
}

func (m *certManager) reload() {
	m.loadErr = nil
	if err := certmgr.SeedLegacyCredentials(m.layout); err != nil {
		m.loadErr = err
		m.notice.setError("load certificate state failed: " + err.Error())
	}
	inv, err := certmgr.Inventory(m.layout)
	if err != nil {
		m.loadErr = err
		m.notice.setError("load certificate inventory failed: " + err.Error())
	}
	m.inventory = inv
	creds, err := certmgr.LoadCredentials(m.layout)
	if err != nil {
		m.loadErr = err
		m.notice.setError("load DNS zones failed: " + err.Error())
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
	m.notice.clearForUserAction(msg)
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
	case certPhaseRenewPick:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateRenewPick(key)
		}
	case certPhaseRenewConfirm:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateRenewConfirm(key)
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
				if m.operation == certOperationRenew {
					m.notice.setInfo("certificate renewed")
				} else {
					m.notice.setInfo("certificate added")
				}
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
					m.notice.setError("no certificates to renew")
					return nil, false
				}
				m.pickCursor = 0
				m.pendingRenew = certmgr.CertInfo{}
				m.phase = certPhaseRenewPick
			case 2:
				if len(m.inventory) == 0 {
					m.notice.setError("no certificates to delete")
					return nil, false
				}
				m.pickCursor = 0
				m.phase = certPhaseCertPick
			case 3:
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
					m.notice.setError("no credentials to delete")
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

func (m *certManager) updateRenewPick(key tea.KeyMsg) (tea.Cmd, bool) {
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.pickCursor = moveSelection(m.pickCursor, len(m.inventory), d) },
		Confirm: func() (tea.Cmd, bool) {
			if idx, ok := selectedIndex(m.pickCursor, len(m.inventory)); ok {
				m.pendingRenew = m.inventory[idx]
				m.phase = certPhaseRenewConfirm
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) {
			m.pendingRenew = certmgr.CertInfo{}
			m.phase = certPhaseList
			return nil, false
		},
	})
	return nil, false
}

func (m *certManager) updateRenewConfirm(key tea.KeyMsg) (tea.Cmd, bool) {
	switch strings.ToLower(key.String()) {
	case "y":
		target := m.pendingRenew
		m.pendingRenew = certmgr.CertInfo{}
		m.startCertificateRun(certOperationRenew, target.Domain)
		return m.startCmd, false
	case "n", "esc":
		m.pendingRenew = certmgr.CertInfo{}
		m.phase = certPhaseList
	}
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
					m.notice.setError("delete failed: " + err.Error())
				} else if len(consumers) > 0 {
					m.notice.setError(fmt.Sprintf("cannot delete %s: certificate is used by %s", domain, strings.Join(consumers.Labels(), ", ")))
				} else if err := certmgr.Deregister(m.layout, domain); err != nil {
					m.notice.setError("delete failed: " + err.Error())
				} else {
					m.notice.setInfo("deleted certificate " + domain)
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
					m.notice.setError("delete failed: " + err.Error())
				} else {
					m.notice.setInfo("deleted DNS zone")
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
	m.beginCertFormWithSeed("")
}

func (m *certManager) beginCertFormWithSeed(domain string) {
	m.operation = certOperationAdd
	seed := map[string]string{}
	if domain != "" {
		seed["domain"] = domain
	}
	m.form.begin([]field{
		{key: "domain", label: "Certificate domain", note: "Needs a covering DNS zone. To renew a domain already listed, use Renew certificate."},
	}, seed, validateCertField)
	m.phase = certPhaseForm
}

func (m *certManager) beginCredForm(seedDomain string) {
	seed := map[string]string{}
	if seedDomain != "" {
		seed["domain"] = seedDomain
	}
	m.form.begin([]field{
		{key: "domain", label: "DNS zone", note: "The zone you manage at your DNS provider. Authorizes this domain and every subdomain (e.g. example.com covers a.example.com)."},
		{key: "provider", label: "DNS provider", def: certmgr.ProviderCloudflare, options: []string{certmgr.ProviderCloudflare, certmgr.ProviderAliyun}},
		{key: "credential", label: "API token", secret: true, noteFunc: credentialNote},
	}, seed, validateCredField)
	m.phase = certPhaseCredForm
}

func (m *certManager) completeForm() {
	if m.phase == certPhaseCredForm {
		m.completeCredForm()
		return
	}
	m.continueAdd(strings.TrimSpace(m.form.values["domain"]))
}

func (m *certManager) continueAdd(domain string) {
	managed, err := certmgr.IsManaged(m.layout, domain)
	if managed {
		m.notice.setError(fmt.Sprintf("%s is already managed; use Renew certificate instead", domain))
		m.phase = certPhaseList
		return
	}
	var unmanaged *certmgr.UnmanagedDomainError
	if err != nil && !errors.As(err, &unmanaged) {
		m.notice.setError("cannot add certificate: " + err.Error())
		m.phase = certPhaseList
		return
	}
	if !certmgr.CredentialCovers(m.creds, domain) {
		// Redirect to add a covering credential, then resume issuance.
		m.pendingDomain = domain
		m.resumeIssueAfterCred = true
		m.notice.setError("no DNS zone covers " + domain + "; add one to continue")
		m.beginCredForm(domain)
		return
	}
	m.startCertificateRun(certOperationAdd, domain)
}

func (m *certManager) completeCredForm() {
	cred := certmgr.DNSCredential{
		Domain:     strings.TrimSpace(m.form.values["domain"]),
		Provider:   strings.TrimSpace(m.form.values["provider"]),
		Credential: strings.TrimSpace(m.form.values["credential"]),
	}
	if err := certmgr.UpsertCredential(m.layout, cred); err != nil {
		m.notice.setError("save credential failed: " + err.Error())
		m.reload()
		m.phase = certPhaseCredList
		return
	}
	m.reload()
	if m.resumeIssueAfterCred {
		m.resumeIssueAfterCred = false
		if certmgr.CredentialCovers(m.creds, m.pendingDomain) {
			m.continueAdd(m.pendingDomain)
			return
		}
	}
	m.notice.setInfo("saved DNS zone " + cred.Domain)
	m.phase = certPhaseCredList
}

func (m *certManager) startCertificateRun(operation certOperation, domain string) {
	m.operation = operation
	m.notice.clear()
	m.phase = certPhaseRunning
	ch := make(chan runMsg, 64)
	m.run.resetRun(ch)
	logs := &logWriter{ch: ch}
	progress := runProgressSender(ch)

	// The ACME order is step one and each activation target — the hub reload
	// plus every spoke holding this domain — is a step of its own. Emitting
	// them is what drives the progress bar: with no events at all it reports
	// 0% for the entire run. The target count is taken before issuance to size
	// the bar; distribution then reports the authoritative total, so a node
	// added or removed in between recalibrates it instead of overflowing it.
	issue := deploy.Event{Index: 1, Total: 1 + m.distributionTargets(domain), Label: "Issue certificate", Detail: domain}
	if operation == certOperationRenew {
		issue.Label = "Renew certificate"
	}
	go func() {
		if operation == certOperationRenew {
			fmt.Fprintf(logs, "force renewing certificate for %s via DNS-01...\n", domain)
		} else {
			fmt.Fprintf(logs, "adding certificate for %s via DNS-01...\n", domain)
		}
		issue.Status = "running"
		deploy.EmitProgress(progress, issue)
		if err := m.issue(context.Background(), domain, logs); err != nil {
			issue.Status, issue.Err = "fail", err
			deploy.EmitProgress(progress, issue)
			ch <- runMsg{done: true, err: err}
			return
		}
		issue.Status = "ok"
		deploy.EmitProgress(progress, issue)
		err := m.distribute(context.Background(), domain, logs, shiftRunProgress(progress, 1))
		ch <- runMsg{done: true, err: err}
	}()
	m.startCmd = m.run.waitForRun()
}

// distributionTargets counts the activation steps distribution will report. A
// lookup failure is deliberately not surfaced: it only costs bar precision
// during issuance, and distribution reports the real count once it starts.
func (m *certManager) distributionTargets(domain string) int {
	if m.countDistributionTargets != nil {
		return m.countDistributionTargets(domain)
	}
	consumers, err := (&hubctl.Controller{Layout: m.layout}).CertificateConsumers(domain)
	if err != nil {
		return 0
	}
	return len(consumers)
}

func (m *certManager) issue(ctx context.Context, domain string, log io.Writer) error {
	if m.issueCertificate != nil {
		return m.issueCertificate(ctx, domain, log)
	}
	mgr := &certmgr.Manager{Layout: m.layout, Output: log}
	_, err := mgr.Issue(ctx, domain)
	return err
}

func (m *certManager) distribute(ctx context.Context, domain string, log io.Writer, progress func(deploy.Event)) error {
	if m.distributeCertificate != nil {
		return m.distributeCertificate(ctx, domain, log, progress)
	}
	ctrl := &hubctl.Controller{Layout: m.layout, Runner: system.NewExecRunner(log), ExpectedVersion: toolVersion}
	return ctrl.DistributeCertificate(ctx, domain, log, progress)
}

func (m *certManager) View() string {
	switch m.phase {
	case certPhaseRunning:
		if m.operation == certOperationRenew {
			return commandRunningView(m, "Renewing certificate")
		}
		return commandRunningView(m, "Adding certificate")
	case certPhaseDone:
		if m.run.runErr != nil {
			if m.operation == certOperationRenew {
				return commandFailedView(m, "Certificate renewal failed")
			}
			return commandFailedView(m, "Certificate addition failed")
		}
		if m.operation == certOperationRenew {
			return flowTitle.Render("Certificate renewed") + "\n\n" + flowOK.Render("Press any key to return")
		}
		return flowTitle.Render("Certificate added") + "\n\n" + flowOK.Render("Press any key to return")
	case certPhaseForm, certPhaseCredForm:
		title := "Add certificate"
		if m.phase == certPhaseCredForm {
			title = "Add DNS zone"
		}
		return m.form.View(title)
	case certPhaseCredList:
		return m.credListView()
	case certPhaseRenewPick:
		return m.pickView("Renew certificate", certInfoLabels(m.inventory))
	case certPhaseRenewConfirm:
		return m.renewConfirmView()
	case certPhaseCertPick:
		return m.pickView("Delete certificate", certInfoLabels(m.inventory))
	case certPhaseCredPick:
		return m.pickView("Delete DNS zone", credLabels(m.creds))
	default:
		return m.listView()
	}
}

func (m *certManager) listView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Certificate management") + "\n\n")
	if notice := m.notice.view(); notice != "" {
		b.WriteString(notice + "\n\n")
	}
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
	b.WriteString(dimStyle.Render(fmt.Sprintf("DNS zones: %d", len(m.creds))) + "\n\n")
	b.WriteString(renderActionMenu(certActions, m.actionCursor))
	return b.String()
}

func (m *certManager) credListView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("DNS zones") + "\n\n")
	if notice := m.notice.view(); notice != "" {
		b.WriteString(notice + "\n\n")
	}
	if len(m.creds) == 0 {
		b.WriteString(dimStyle.Render("No DNS zones yet. Add one to authorize certificate issuance.") + "\n\n")
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

func (m *certManager) renewConfirmView() string {
	return flowTitle.Render("Renew certificate · Confirm") + "\n\n" +
		statusWarn.Render("Forces a new ACME DNS-01 order now, even if the current certificate is still valid.") + "\n" +
		"Repeated renewal is subject to Let's Encrypt rate limits.\n\n" +
		"Domain: " + m.pendingRenew.Domain + "\n\n" +
		"On success, the renewed certificate is distributed to every node that uses it.\n\n" +
		"Press y to force renew, or n/Esc to cancel."
}

func (m *certManager) footerHints() []operationHint {
	switch m.phase {
	case certPhaseRunning:
		return runningFooterHints(false)
	case certPhaseDone:
		return doneFooterHints(m.run.runErr != nil)
	case certPhaseForm, certPhaseCredForm:
		return m.form.footerHints()
	case certPhaseRenewPick:
		return actionFooterHints("Choose")
	case certPhaseRenewConfirm:
		return []operationHint{{key: "Y", action: "Force renew"}, {key: "N/Esc", action: "Cancel"}}
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
		return c.Domain + "  " + statusWarn.Render("needs a DNS zone")
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
		label = "invalid"
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
			return fmt.Errorf("API token is required")
		}
		if vals["provider"] == certmgr.ProviderAliyun && !strings.Contains(value, ":") {
			return fmt.Errorf("Aliyun API token must be AccessKeyID:AccessKeySecret")
		}
	}
	return nil
}

func looksLikeDomain(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && strings.Contains(s, ".") && !strings.ContainsAny(s, " /")
}
