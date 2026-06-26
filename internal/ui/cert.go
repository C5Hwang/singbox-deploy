package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/ui/form"
)

type certPhase int

const (
	certPhaseAction certPhase = iota
	certPhaseForm
	certPhaseSelect
	certPhaseConfirm
	certPhaseDone
)

type certAction int

const (
	certActionAdd certAction = iota
	certActionEdit
	certActionDelete
)

type certActionItem = actionItem[certAction]

type certManager struct {
	phase  certPhase
	action certAction

	width  int
	height int

	creds   []cluster.DNSCredentials
	loadErr error
	cursor  int

	parameterForm
	selectedRoot string
	fieldErr     string
	runErr       error
	doneMsg      string
}

func newCertManager() *certManager {
	cm := &certManager{
		phase:         certPhaseAction,
		cursor:        1,
		parameterForm: newParameterForm(nil),
	}
	cm.reload()
	return cm
}

func (cm *certManager) reload() {
	store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
	list, err := store.List()
	if err != nil {
		cm.loadErr = err
		return
	}
	cm.loadErr = nil
	cm.creds = list
}

func (cm *certManager) setSize(width, height int) {
	cm.width = width
	cm.height = height
	cm.parameterForm.SetSize(width, height)
}

func (cm *certManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cm.setSize(msg.Width, msg.Height)
	case tea.KeyMsg:
		return cm.handleKey(msg)
	}
	if cm.phase == certPhaseForm && !cm.CurrentFieldHasOptions() {
		return cm.UpdateInput(msg), false
	}
	if cm.phase == certPhaseSelect && !cm.CurrentFieldHasOptions() {
		return cm.UpdateInput(msg), false
	}
	return nil, false
}

func (cm *certManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if cm.loadErr != nil {
		switch {
		case isSelectionCancelKey(msg), isSelectionConfirmKey(msg):
			return nil, true
		}
		return nil, false
	}
	switch cm.phase {
	case certPhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: cm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				cm.activateAction()
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case certPhaseSelect:
		cmd, done, handled := cm.parameterForm.HandleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				root := strings.TrimSpace(cm.Values["target_root"])
				if root == "" {
					cm.fieldErr = "select a root domain"
					return
				}
				cm.selectedRoot = root
				switch cm.action {
				case certActionEdit:
					cm.startEditForm()
				case certActionDelete:
					cm.phase = certPhaseConfirm
				}
			},
			Back: func() {
				if !cm.PreviousField() {
					cm.phase = certPhaseAction
				}
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case certPhaseForm:
		cmd, done, handled := cm.parameterForm.HandleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				cm.phase = certPhaseConfirm
			},
			Back: func() {
				if !cm.PreviousField() {
					cm.phase = certPhaseAction
				}
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case certPhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			if err := cm.apply(); err != nil {
				cm.runErr = err
			}
			cm.phase = certPhaseDone
			cm.reload()
		case msg.String() == "esc", isSelectionNoKey(msg), isSelectionBackKey(msg):
			cm.phase = certPhaseAction
		}
	case certPhaseDone:
		return nil, true
	}
	return nil, false
}

func (cm *certManager) moveAction(delta int) {
	cm.cursor = moveActionCursor(cm.cursor, cm.actions(), delta)
	cm.fieldErr = ""
}

func (cm *certManager) activateAction() {
	cm.fieldErr = ""
	actions := cm.actions()
	idx, ok := selectedIndex(cm.cursor, len(actions))
	if !ok {
		return
	}
	cm.action = actions[idx].action
	switch cm.action {
	case certActionAdd:
		cm.startAddForm()
	case certActionEdit, certActionDelete:
		if len(cm.creds) == 0 {
			cm.fieldErr = "no credentials configured"
			return
		}
		cm.startSelectForm()
	}
}

func (cm *certManager) addFields(existing *cluster.DNSCredentials) []field {
	var rootDef, providerDef, cfTokenDef, aliyunKeyDef, aliyunSecretDef string
	if existing != nil {
		rootDef, providerDef = existing.RootDomain, existing.Provider
		switch existing.Provider {
		case "cloudflare":
			cfTokenDef = existing.APIToken
		case "aliyun":
			aliyunKeyDef = existing.APIToken
			aliyunSecretDef = existing.APISecret
		}
	}
	if providerDef == "" {
		providerDef = "cloudflare"
	}
	return []field{
		{Key: "root_domain", Label: "Root domain", Def: rootDef, Note: "Root zone where the DNS-01 TXT records will be written (e.g. example.com)."},
		{Key: "provider", Label: "DNS provider", Def: providerDef, Options: []string{"cloudflare", "aliyun"}},
		{
			Key:   "cf_token",
			Label: "Cloudflare API Token",
			Def:   cfTokenDef,
			Note:  "API Token with Zone:DNS:Edit permission on the root zone.",
			Skip:  func(vals map[string]string) bool { return vals["provider"] != "cloudflare" },
		},
		{
			Key:   "aliyun_access_key_id",
			Label: "Aliyun AccessKey ID",
			Def:   aliyunKeyDef,
			Skip:  func(vals map[string]string) bool { return vals["provider"] != "aliyun" },
		},
		{
			Key:   "aliyun_access_key_secret",
			Label: "Aliyun AccessKey Secret",
			Def:   aliyunSecretDef,
			Skip:  func(vals map[string]string) bool { return vals["provider"] != "aliyun" },
		},
	}
}

func (cm *certManager) startAddForm() {
	cm.Values = nil
	cm.selectedRoot = ""
	cm.parameterForm.SetFields(cm.addFields(nil))
	cm.parameterForm.Validate = validateCertField
	cm.phase = certPhaseForm
	if cm.parameterForm.AdvanceField() {
		cm.phase = certPhaseConfirm
	}
}

func (cm *certManager) startEditForm() {
	store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
	creds, err := store.Load(cm.selectedRoot)
	if err != nil {
		cm.fieldErr = err.Error()
		cm.phase = certPhaseAction
		return
	}
	cm.parameterForm.SetFields(cm.addFields(&creds))
	cm.parameterForm.Values = map[string]string{
		"root_domain": creds.RootDomain,
		"provider":    creds.Provider,
	}
	switch creds.Provider {
	case "cloudflare":
		cm.parameterForm.Values["cf_token"] = creds.APIToken
	case "aliyun":
		cm.parameterForm.Values["aliyun_access_key_id"] = creds.APIToken
		cm.parameterForm.Values["aliyun_access_key_secret"] = creds.APISecret
	}
	cm.parameterForm.Validate = validateCertField
	cm.phase = certPhaseForm
	if cm.parameterForm.AdvanceField() {
		cm.phase = certPhaseConfirm
	}
}

func (cm *certManager) startSelectForm() {
	opts := make([]string, 0, len(cm.creds))
	for _, c := range cm.creds {
		opts = append(opts, fmt.Sprintf("%s (%s)", c.RootDomain, c.Provider))
	}
	cm.parameterForm.SetFields([]field{
		{Key: "target_root", Label: "Root domain", Options: optionsRootOnly(cm.creds), Note: "Pick the credential set to operate on."},
	})
	cm.parameterForm.Validate = validateCertField
	cm.phase = certPhaseSelect
	if cm.parameterForm.AdvanceField() {
		cm.phase = certPhaseConfirm
	}
}

func optionsRootOnly(creds []cluster.DNSCredentials) []string {
	out := make([]string, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.RootDomain)
	}
	return out
}

func validateCertField(f field, val string, vals map[string]string) error {
	if err := form.ValidateDNSCredentialField(f, val, vals); err != nil {
		return err
	}
	if f.Key == "target_root" && strings.TrimSpace(val) == "" {
		return fmt.Errorf("select a root domain")
	}
	return nil
}

func (cm *certManager) apply() error {
	store := cluster.NewRegistry(paths.DefaultLayout()).DNS()
	switch cm.action {
	case certActionAdd:
		creds := dnsCredentialsFromValues(cm.Values)
		cm.doneMsg = fmt.Sprintf("Saved credentials for %s.", creds.RootDomain)
		return store.Save(creds)
	case certActionEdit:
		creds := dnsCredentialsFromValues(cm.Values)
		cm.doneMsg = fmt.Sprintf("Updated credentials for %s.", creds.RootDomain)
		// Edit may rename root; remove the old entry first.
		if creds.RootDomain != cm.selectedRoot {
			_ = store.Delete(cm.selectedRoot)
		}
		return store.Save(creds)
	case certActionDelete:
		root := cm.selectedRoot
		cm.doneMsg = fmt.Sprintf("Deleted credentials for %s.", root)
		if err := store.Delete(root); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		return nil
	}
	return nil
}

func (cm *certManager) View() string {
	if cm.loadErr != nil {
		return flowTitle.Render("Certificate & site") + "\n\n" + flowErr.Render(cm.loadErr.Error())
	}
	switch cm.phase {
	case certPhaseAction:
		return cm.actionView()
	case certPhaseForm, certPhaseSelect:
		return cm.parameterForm.View("Certificate & site · DNS credentials")
	case certPhaseConfirm:
		return cm.confirmView()
	case certPhaseDone:
		if cm.runErr != nil {
			return flowErr.Render("Operation failed: "+cm.runErr.Error()) + "\n"
		}
		return flowOK.Render(cm.doneMsg) + "\n"
	}
	return ""
}

func (cm *certManager) actionView() string {
	rows := []summaryLine{summaryRow("Configured DNS credentials", fmt.Sprintf("%d", len(cm.creds)))}
	for _, c := range cm.creds {
		rows = append(rows, summaryIndentedRow(2, c.RootDomain, c.Provider))
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render("Certificate & site") + "\n\n")
	b.WriteString(renderSummary(rows) + "\n")
	if cm.fieldErr != "" {
		b.WriteString(flowErr.Render(cm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderActionList(cm.actions(), cm.cursor))
	return b.String()
}

func (cm *certManager) confirmView() string {
	rows := []summaryLine{}
	switch cm.action {
	case certActionAdd:
		rows = append(rows,
			summaryRow("Action", "Add DNS credentials"),
			summaryRow("Root domain", cm.Values["root_domain"]),
			summaryRow("Provider", cm.Values["provider"]),
		)
		rows = append(rows, providerCredRows(cm.Values)...)
	case certActionEdit:
		rows = append(rows,
			summaryRow("Action", "Update DNS credentials"),
			summaryRow("Original root", cm.selectedRoot),
			summaryRow("New root domain", cm.Values["root_domain"]),
			summaryRow("Provider", cm.Values["provider"]),
		)
		rows = append(rows, providerCredRows(cm.Values)...)
	case certActionDelete:
		rows = append(rows,
			summaryRow("Action", "Delete DNS credentials"),
			summaryRow("Root domain", cm.selectedRoot),
		)
	}
	return flowTitle.Render("Certificate & site · Confirm") + "\n\n" + renderSummary(rows)
}

// providerCredRows returns the masked credential rows for the confirm view,
// keyed off the provider selection.
func providerCredRows(vals map[string]string) []summaryLine {
	switch vals["provider"] {
	case "cloudflare":
		return []summaryLine{summaryRow("Cloudflare API Token", maskedSecret(vals["cf_token"]))}
	case "aliyun":
		return []summaryLine{
			summaryRow("Aliyun AccessKey ID", maskedSecret(vals["aliyun_access_key_id"])),
			summaryRow("Aliyun AccessKey Secret", maskedSecret(vals["aliyun_access_key_secret"])),
		}
	}
	return nil
}

func maskedSecret(s string) string {
	if s == "" {
		return "(empty)"
	}
	if len(s) <= 8 {
		return strings.Repeat("•", len(s))
	}
	return s[:2] + strings.Repeat("•", len(s)-4) + s[len(s)-2:]
}

func (cm *certManager) footerHints() []operationHint {
	switch cm.phase {
	case certPhaseAction:
		return actionFooterHints("Select")
	case certPhaseForm, certPhaseSelect:
		return cm.parameterForm.FooterHints()
	case certPhaseConfirm:
		return applyFooterHints("Apply")
	case certPhaseDone:
		return doneFooterHints(cm.runErr != nil)
	}
	return returnFooterHints()
}

func (cm *certManager) actions() []certActionItem {
	return []certActionItem{
		{separator: true, label: "DNS API Credentials"},
		{action: certActionAdd, label: "Add credentials"},
		{action: certActionEdit, label: "Edit credentials"},
		{action: certActionDelete, label: "Delete credentials"},
	}
}
