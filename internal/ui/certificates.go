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
	certPhaseZonePick
	certPhaseHostForm
	certPhaseZoneList
	certPhaseZoneForm
	certPhaseRenewPick
	certPhaseRenewConfirm
	certPhaseCertPick
	certPhaseZoneDeletePick
	certPhaseRunning
	certPhaseDone
)

type certOperation int

const (
	certOperationAdd certOperation = iota
	certOperationRenew
)

// certManager is the Certificate & DNS-zone management page. It exposes the
// certmgr inventory: add a new certificate, force-renew an existing one, delete
// one, and manage the DNS zones whose names authorize issuance by suffix match.
//
// Adding a certificate is zone-first: the operator picks the DNS zone that will
// issue it and then types only the hostname under that zone, so the containment
// the suffix match relies on is a structure on screen rather than a rule to
// remember. Choosing "add a new zone" from the picker collects the zone and its
// API token and comes straight back to the hostname step.
type certManager struct {
	run  commandRun
	form parameterForm

	layout    paths.Layout
	width     int
	phase     certPhase
	inventory []certmgr.CertInfo
	zones     []certmgr.DNSCredential

	actionCursor     int
	zoneActionCursor int
	pickCursor       int
	zoneCursor       int
	operation        certOperation
	pendingRenew     certmgr.CertInfo

	// pendingZone is the zone the hostname step composes its domain under.
	pendingZone string
	// Issuance continuation after adding a zone mid-flow. pendingDomain resumes
	// a domain another screen asked for; pickHostAfterZone returns to the
	// hostname step for a zone just added from the picker.
	pendingDomain        string
	resumeIssueAfterZone bool
	pickHostAfterZone    bool
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
var zoneActions = []string{"Add DNS zone", "Delete DNS zone"}

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
	m.beginAddForDomain(domain)
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
	zones, err := certmgr.LoadCredentials(m.layout)
	if err != nil {
		m.loadErr = err
		m.notice.setError("load DNS zones failed: " + err.Error())
	}
	m.zones = zones
}

func (m *certManager) runState() *commandRun { return &m.run }
func (m *certManager) markRunFailed()        { m.phase = certPhaseDone }

func (m *certManager) setSize(w, h int) {
	m.width = w
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
	case certPhaseHostForm, certPhaseZoneForm:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateForm(key)
		}
		return nil, false
	case certPhaseList:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateList(key)
		}
	case certPhaseZonePick:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateZonePick(key)
		}
	case certPhaseZoneList:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateZoneList(key)
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
	case certPhaseZoneDeletePick:
		if key, ok := msg.(tea.KeyMsg); ok {
			return m.updateZoneDeletePick(key)
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
				m.beginAddCertificate()
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
				m.zoneActionCursor = 0
				m.phase = certPhaseZoneList
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { return nil, true },
	})
	return nil, done
}

func (m *certManager) updateZoneList(key tea.KeyMsg) (tea.Cmd, bool) {
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.zoneActionCursor = moveSelection(m.zoneActionCursor, len(zoneActions), d) },
		Confirm: func() (tea.Cmd, bool) {
			switch m.zoneActionCursor {
			case 0:
				m.beginZoneForm("")
			case 1:
				if len(m.zones) == 0 {
					m.notice.setError("no DNS zones to delete")
					return nil, false
				}
				m.pickCursor = 0
				m.phase = certPhaseZoneDeletePick
			}
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { m.phase = certPhaseList; return nil, false },
	})
	return nil, false
}

// updateZonePick drives the first step of adding a certificate. The row past
// the last zone adds a new one and then continues to the hostname step, so an
// operator with no zones yet is walked through the prerequisite instead of
// being rejected by it.
func (m *certManager) updateZonePick(key tea.KeyMsg) (tea.Cmd, bool) {
	rows := len(m.zones) + 1
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.zoneCursor = moveSelection(m.zoneCursor, rows, d) },
		Confirm: func() (tea.Cmd, bool) {
			idx, ok := selectedIndex(m.zoneCursor, rows)
			if !ok || idx == len(m.zones) {
				m.pickHostAfterZone = true
				m.beginZoneForm("")
				return nil, false
			}
			m.beginHostForm(m.zones[idx].Domain, "")
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) {
			if m.returnAfterIssue {
				return nil, true
			}
			m.phase = certPhaseList
			return nil, false
		},
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

func (m *certManager) updateZoneDeletePick(key tea.KeyMsg) (tea.Cmd, bool) {
	handleSelectionKey(key, selectionKeyHandlers{
		Move: func(d int) { m.pickCursor = moveSelection(m.pickCursor, len(m.zones), d) },
		Confirm: func() (tea.Cmd, bool) {
			if idx, ok := selectedIndex(m.pickCursor, len(m.zones)); ok {
				if err := certmgr.DeleteCredential(m.layout, m.zones[idx].Domain); err != nil {
					m.notice.setError("delete failed: " + err.Error())
				} else {
					m.notice.setInfo("deleted DNS zone")
				}
				m.reload()
			}
			m.phase = certPhaseZoneList
			return nil, false
		},
		Cancel: func() (tea.Cmd, bool) { m.phase = certPhaseZoneList; return nil, false },
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
			m.cancelForm()
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

// cancelForm returns from a form to whichever screen opened it, dropping the
// continuations that only make sense while that form is on screen.
func (m *certManager) cancelForm() {
	switch {
	case m.phase == certPhaseZoneForm && m.pickHostAfterZone:
		m.pickHostAfterZone = false
		m.phase = certPhaseZonePick
	case m.phase == certPhaseZoneForm:
		m.resumeIssueAfterZone = false
		m.pendingDomain = ""
		m.phase = certPhaseZoneList
	case m.phase == certPhaseHostForm:
		m.phase = certPhaseZonePick
	default:
		m.phase = certPhaseList
	}
}

// beginAddCertificate opens the add flow at its first question: which zone
// issues this certificate. The picker is shown even with no zones stored, where
// its only row is the one that adds the first.
func (m *certManager) beginAddCertificate() {
	m.operation = certOperationAdd
	m.pendingZone = ""
	m.pendingDomain = ""
	m.zoneCursor = 0
	m.phase = certPhaseZonePick
}

// beginAddForDomain opens the add flow already aimed at a domain another screen
// could not accept. A covering zone lands directly on the hostname step with
// the name filled in, so the operator confirms rather than retypes it; without
// one, the zone comes first and issuance resumes as soon as it is saved.
func (m *certManager) beginAddForDomain(domain string) {
	m.operation = certOperationAdd
	m.pendingDomain = domain
	if zone, ok := certmgr.SelectCredential(m.zones, domain); ok {
		m.beginHostForm(zone.Domain, hostWithinZone(zone.Domain, domain))
		return
	}
	m.resumeIssueAfterZone = true
	m.beginZoneForm(zoneSeedFor(domain))
}

// beginHostForm collects the hostname to issue under zone. Only the part below
// the zone is asked for, and the badge shows the domain it composes so the
// operator sees the result before committing to it.
func (m *certManager) beginHostForm(zone, host string) {
	m.operation = certOperationAdd
	m.pendingZone = zone
	seed := map[string]string{}
	if host != "" {
		seed["host"] = host
	}
	m.form.begin([]field{{
		key:   "host",
		label: "Hostname",
		note: "The name below " + zone + ". Leave it empty to issue for " + zone +
			" itself. To renew a domain already listed, use Renew certificate instead.",
		badgeFunc: func(vals map[string]string) string {
			return "will issue: " + certificateDomainForHost(zone, vals["host"])
		},
	}}, seed, m.validateHostField)
	m.phase = certPhaseHostForm
}

func (m *certManager) beginZoneForm(seedDomain string) {
	seed := map[string]string{}
	if seedDomain != "" {
		seed["domain"] = seedDomain
	}
	m.form.begin([]field{
		{key: "domain", label: "DNS zone", note: "The zone you manage at your DNS provider. Authorizes this domain and every subdomain (e.g. example.com covers a.example.com)."},
		{key: "provider", label: "DNS provider", def: certmgr.ProviderCloudflare, options: []string{certmgr.ProviderCloudflare, certmgr.ProviderAliyun}},
		{key: "credential", label: "API token", secret: true, noteFunc: apiTokenNote},
	}, seed, validateZoneField)
	m.phase = certPhaseZoneForm
}

func (m *certManager) completeForm() {
	if m.phase == certPhaseZoneForm {
		m.completeZoneForm()
		return
	}
	m.continueAdd(certificateDomainForHost(m.pendingZone, m.form.values["host"]))
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
	if !certmgr.CredentialCovers(m.zones, domain) {
		// Redirect to add a covering zone, then resume issuance. The seed is the
		// registrable parent, not the certificate domain: a zone scoped to the
		// single host would work once and then demand the same API token again
		// for every sibling.
		m.pendingDomain = domain
		m.resumeIssueAfterZone = true
		m.notice.setError("no DNS zone covers " + domain + "; add one to continue")
		m.beginZoneForm(zoneSeedFor(domain))
		return
	}
	m.startCertificateRun(certOperationAdd, domain)
}

func (m *certManager) completeZoneForm() {
	cred := certmgr.DNSCredential{
		Domain:     strings.TrimSpace(m.form.values["domain"]),
		Provider:   strings.TrimSpace(m.form.values["provider"]),
		Credential: strings.TrimSpace(m.form.values["credential"]),
	}
	if err := certmgr.UpsertCredential(m.layout, cred); err != nil {
		m.notice.setError("save DNS zone failed: " + err.Error())
		m.reload()
		m.phase = certPhaseZoneList
		return
	}
	m.reload()
	pickHost := m.pickHostAfterZone
	m.pickHostAfterZone = false
	if m.resumeIssueAfterZone {
		m.resumeIssueAfterZone = false
		if certmgr.CredentialCovers(m.zones, m.pendingDomain) {
			m.continueAdd(m.pendingDomain)
			return
		}
	}
	m.notice.setInfo("saved DNS zone " + cred.Domain)
	if pickHost {
		// The zone was added from the add-certificate picker, so carry on with
		// the certificate that prompted it.
		m.beginHostForm(cred.Domain, "")
		return
	}
	m.phase = certPhaseZoneList
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
	case certPhaseZonePick:
		return m.zonePickView()
	case certPhaseHostForm:
		return m.form.View("Add certificate · " + m.pendingZone)
	case certPhaseZoneForm:
		return m.form.View("Add DNS zone")
	case certPhaseZoneList:
		return m.zoneListView()
	case certPhaseRenewPick:
		return m.pickView("Renew certificate", certInfoLabels(m.inventory))
	case certPhaseRenewConfirm:
		return m.renewConfirmView()
	case certPhaseCertPick:
		return m.pickView("Delete certificate", certInfoLabels(m.inventory))
	case certPhaseZoneDeletePick:
		return m.pickView("Delete DNS zone", zoneLabels(m.zones))
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
	b.WriteString(dimStyle.Render(fmt.Sprintf("DNS zones: %d", len(m.zones))) + "\n\n")
	b.WriteString(renderActionMenu(certActions, m.actionCursor))
	return b.String()
}

func (m *certManager) zoneListView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("DNS zones") + "\n\n")
	if notice := m.notice.view(); notice != "" {
		b.WriteString(notice + "\n\n")
	}
	if len(m.zones) == 0 {
		b.WriteString(dimStyle.Render("No DNS zones yet. Add one to authorize certificate issuance.") + "\n\n")
	} else {
		for _, c := range m.zones {
			b.WriteString("  " + zoneLabel(c) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(renderActionMenu(zoneActions, m.zoneActionCursor))
	return b.String()
}

// addZoneRow is the trailing row of the zone picker, so the prerequisite is
// always one keystroke away from the step that needs it.
const addZoneRow = "+ Add a new DNS zone…"

func (m *certManager) zonePickView() string {
	var b strings.Builder
	b.WriteString(flowTitle.Render("Add certificate") + "\n\n")
	b.WriteString("Which DNS zone issues this certificate?\n")
	hint := "The certificate is issued for a hostname under the zone you pick."
	if len(m.zones) == 0 {
		hint = "No DNS zones yet. Add the zone you manage at your DNS provider; it then covers every hostname under it."
	}
	for _, line := range wrapFieldNote(hint, m.width) {
		b.WriteString(dimStyle.Render(line) + "\n")
	}
	b.WriteString("\n")
	for i, label := range append(zoneLabels(m.zones), addZoneRow) {
		row := "  " + label
		if i == m.zoneCursor {
			row = selStyle.Render("> " + label)
		}
		b.WriteString(row + "\n")
	}
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
	case certPhaseZonePick:
		return actionFooterHints("Select")
	case certPhaseHostForm, certPhaseZoneForm:
		return m.form.footerHints()
	case certPhaseRenewPick:
		return actionFooterHints("Choose")
	case certPhaseRenewConfirm:
		return []operationHint{{key: "Y", action: "Force renew"}, {key: "N/Esc", action: "Cancel"}}
	case certPhaseCertPick, certPhaseZoneDeletePick:
		return actionFooterHints("Delete")
	case certPhaseZoneList:
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

func zoneLabel(c certmgr.DNSCredential) string {
	return fmt.Sprintf("%s (%s)", c.Domain, c.Provider)
}

func zoneLabels(creds []certmgr.DNSCredential) []string {
	labels := make([]string, len(creds))
	for i, c := range creds {
		labels[i] = zoneLabel(c)
	}
	return labels
}

func apiTokenNote(vals map[string]string) string {
	if vals["provider"] == certmgr.ProviderAliyun {
		return "Aliyun: enter AccessKeyID:AccessKeySecret (colon-separated)."
	}
	return "Cloudflare: enter an API token with DNS edit permission for the zone."
}

// validateHostField validates the composed certificate domain rather than the
// hostname on its own, so an entry that is only invalid once joined to the zone
// is caught at the field instead of at issuance.
func (m *certManager) validateHostField(f field, value string, _ map[string]string) error {
	if f.key != "host" {
		return nil
	}
	_, err := certmgr.NormalizeDomain(certificateDomainForHost(m.pendingZone, value))
	return err
}

// certificateDomainForHost composes the certificate domain from a hostname
// entered under zone. An empty hostname means the zone apex, and a hostname that
// already carries the zone suffix — a pasted fully-qualified name — is taken as
// it stands rather than nested under the zone a second time.
func certificateDomainForHost(zone, host string) string {
	zone = strings.ToLower(strings.Trim(strings.TrimSpace(zone), "."))
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "."))
	switch {
	case zone == "":
		return host
	case host == "" || host == zone:
		return zone
	case strings.HasSuffix(host, "."+zone):
		return host
	default:
		return host + "." + zone
	}
}

// hostWithinZone is the inverse: the part of domain below zone, empty when
// domain is the zone apex.
func hostWithinZone(zone, domain string) string {
	zone = strings.ToLower(strings.Trim(strings.TrimSpace(zone), "."))
	domain = strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
	if zone == "" || domain == zone {
		return ""
	}
	return strings.TrimSuffix(domain, "."+zone)
}

func validateZoneField(f field, value string, vals map[string]string) error {
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

// zoneSeedFor proposes the DNS zone to prefill when a certificate domain has no
// covering zone yet. An unsplittable name falls back to itself, which is what
// the operator typed and can still correct.
func zoneSeedFor(domain string) string {
	zone, err := certmgr.ZoneOf(domain)
	if err != nil {
		return strings.TrimSpace(domain)
	}
	return zone
}

func looksLikeDomain(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && strings.Contains(s, ".") && !strings.ContainsAny(s, " /")
}
