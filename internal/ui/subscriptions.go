package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/account"
	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

type subscriptionPhase int

const (
	subscriptionPhaseAction subscriptionPhase = iota
	subscriptionPhaseForm
	subscriptionPhaseConfirm
	subscriptionPhaseRunning
	subscriptionPhaseDone
)

type subscriptionAction int

const (
	subscriptionActionDisplayName subscriptionAction = iota
	subscriptionActionLocal
	subscriptionActionRefresh
)

var (
	subscriptionUILayout   = paths.DefaultLayout
	detectSubscriptionHost = system.DetectHost
	updateSubscriptionsRun = subscription.Update
	updateDisplayNameRun   = account.Update
)

type subscriptionActionItem = actionItem[subscriptionAction]

type subscriptionManager struct {
	phase  subscriptionPhase
	action subscriptionAction

	width  int
	height int

	host    system.Host
	hostErr error
	cfg     deploy.Config
	loadErr error

	cursor int
	parameterForm
	commandRun
	result deploy.Config
}

func newSubscriptionManager() *subscriptionManager {
	sm := &subscriptionManager{
		phase:         subscriptionPhaseAction,
		cursor:        1,
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	host, err := detectSubscriptionHost()
	sm.host = host
	sm.hostErr = err
	layout := subscriptionUILayout()
	cfg, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		sm.loadErr = err
		return sm
	}
	sm.cfg = cfg
	return sm
}

func (sm *subscriptionManager) setSize(width, height int) {
	sm.width = width
	sm.height = height
	sm.parameterForm.setSize(width, height)
	sm.commandRun.setSize(width, height)
}

func (sm *subscriptionManager) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		sm.setSize(msg.Width, msg.Height)
	case runMsg:
		return sm.handleRun(msg), false
	case tea.KeyMsg:
		return sm.handleKey(msg)
	case tea.MouseMsg:
		return sm.handleMouse(msg), false
	}
	if sm.phase == subscriptionPhaseForm && !sm.currentFieldHasOptions() {
		return sm.updateInput(msg), false
	}
	return nil, false
}

func (sm *subscriptionManager) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if sm.loadErr != nil {
		switch {
		case isSelectionCancelKey(msg), isSelectionConfirmKey(msg):
			return nil, true
		}
		return nil, false
	}
	switch sm.phase {
	case subscriptionPhaseAction:
		cmd, done, handled := handleSelectionKey(msg, selectionKeyHandlers{
			Move: sm.moveAction,
			Confirm: func() (tea.Cmd, bool) {
				sm.activateAction()
				return nil, false
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case subscriptionPhaseForm:
		cmd, done, handled := sm.parameterForm.handleKey(msg, parameterFormKeyHandlers{
			Complete: func() {
				sm.phase = subscriptionPhaseConfirm
			},
			Back: func() {
				if !sm.previousField() {
					sm.phase = subscriptionPhaseAction
				}
			},
			Cancel: func() (tea.Cmd, bool) { return nil, true },
		})
		if handled {
			return cmd, done
		}
	case subscriptionPhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return sm.startRun(), false
		case isSelectionBackKey(msg):
			if len(sm.fields) > 0 {
				sm.phase = subscriptionPhaseForm
				sm.backToLastField()
			} else {
				sm.phase = subscriptionPhaseAction
			}
		case msg.String() == "esc", isSelectionNoKey(msg):
			return nil, true
		}
	case subscriptionPhaseRunning:
		if msg.String() == "enter" && sm.runComplete {
			sm.phase = subscriptionPhaseDone
		}
	case subscriptionPhaseDone:
		return nil, true
	}
	return nil, false
}

func (sm *subscriptionManager) handleMouse(_ tea.MouseMsg) tea.Cmd {
	return nil
}

func (sm *subscriptionManager) moveAction(delta int) {
	sm.cursor = moveActionCursor(sm.cursor, sm.actions(), delta)
	sm.fieldErr = ""
}

func (sm *subscriptionManager) activateAction() {
	sm.fieldErr = ""
	actions := sm.actions()
	idx, ok := selectedIndex(sm.cursor, len(actions))
	if !ok {
		return
	}
	sm.action = actions[idx].action
	switch sm.action {
	case subscriptionActionDisplayName:
		sm.startForm(sm.displayNameFields())
	case subscriptionActionLocal:
		sm.startForm(sm.localFields())
	case subscriptionActionRefresh:
		sm.phase = subscriptionPhaseConfirm
	}
}

func (sm *subscriptionManager) startForm(fields []field) {
	sm.parameterForm.setFields(fields)
	sm.parameterForm.validate = validateSubscriptionField
	sm.phase = subscriptionPhaseForm
	if sm.parameterForm.advanceField() {
		sm.phase = subscriptionPhaseConfirm
	}
}

func (sm *subscriptionManager) displayNameFields() []field {
	return []field{fieldFromParameter(uiparams.SubscriptionDisplayNameField(sm.cfg))}
}

func (sm *subscriptionManager) localFields() []field {
	return fieldsFromParameters(uiparams.SubscriptionLocalFields(sm.cfg))
}

func validateSubscriptionField(f field, val string, _ map[string]string) error {
	return uiparams.ValidateSubscriptionParameterValue(f.key, val)
}

func (sm *subscriptionManager) canApply() bool {
	return sm.hostErr == nil && sm.host.IsRoot && sm.host.Supported() && !sm.host.SELinux
}

func (sm *subscriptionManager) applyBlocker() string {
	if sm.hostErr != nil {
		return "failed to detect host: " + sm.hostErr.Error()
	}
	if !sm.host.IsRoot {
		return "subscription changes must be run as root"
	}
	if !sm.host.Supported() {
		return fmt.Sprintf("unsupported system: family=%q arch=%q", sm.host.OS.Family, sm.host.Arch)
	}
	if sm.host.SELinux {
		return "SELinux is enforcing; subscription changes are blocked"
	}
	return "cannot apply subscription changes"
}

func (sm *subscriptionManager) startRun() tea.Cmd {
	if !sm.canApply() {
		sm.fieldErr = sm.applyBlocker()
		sm.phase = subscriptionPhaseAction
		return nil
	}
	sm.phase = subscriptionPhaseRunning
	sm.resetRun(make(chan runMsg, 64))
	ch := sm.ch
	logs := &logWriter{ch: ch}
	if sm.action == subscriptionActionDisplayName {
		opts := account.UpdateOptions{
			Layout:      subscriptionUILayout(),
			Runner:      system.NewExecRunner(logs),
			DisplayName: sm.values["display_name"],
			Progress: func(e deploy.Event) {
				ev := e
				ch <- runMsg{event: &ev}
			},
		}
		go func() {
			_, err := updateDisplayNameRun(context.Background(), opts)
			ch <- runMsg{done: true, err: err}
		}()
		return sm.waitForRun()
	}
	opts := sm.buildSubscriptionUpdateOptions()
	opts.Layout = subscriptionUILayout()
	opts.Runner = system.NewExecRunner(logs)
	opts.Firewall = sm.host.Firewall
	opts.Progress = func(e subscription.Event) {
		ch <- runMsg{event: &deploy.Event{Index: e.Index, Total: e.Total, Label: e.Label, Detail: e.Detail, Status: e.Status, Err: e.Err}}
	}
	go func() {
		_, err := updateSubscriptionsRun(context.Background(), opts)
		ch <- runMsg{done: true, err: err}
	}()
	return sm.waitForRun()
}

func (sm *subscriptionManager) buildSubscriptionUpdateOptions() subscription.UpdateOptions {
	registry := cluster.NewRegistry(subscriptionUILayout())
	opts := subscription.UpdateOptions{
		Salt:          sm.cfg.Salt,
		SubscribePort: sm.cfg.SubscribePort,
		LoadConfig: func(l paths.Layout) (subscription.Config, error) {
			cfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return subscription.Config{}, err
			}
			return subscription.Config{Domain: cfg.Domain, Salt: cfg.Salt, SubscribePort: cfg.SubscribePort}, nil
		},
		WriteState: func(stateDir string, cfg subscription.Config) error {
			full, err := deploy.LoadProtocolConfig(subscriptionUILayout())
			if err != nil {
				return err
			}
			full.Salt = cfg.Salt
			full.SubscribePort = cfg.SubscribePort
			return deploy.WriteInstallState(stateDir, full)
		},
		WriteNginxConfig: func(l paths.Layout, cfg subscription.Config, confPath string) error {
			full, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return err
			}
			full.Salt = cfg.Salt
			full.SubscribePort = cfg.SubscribePort
			return deploy.WriteManagedNginxConfig(l, full, confPath)
		},
		WriteSubscriptions: func(_ context.Context, l paths.Layout, cfg subscription.Config) error {
			full, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return err
			}
			full.Salt = cfg.Salt
			full.SubscribePort = cfg.SubscribePort
			return registry.WriteFleetSubscriptions(l, full)
		},
		RunCommands: deploy.RunCommands,
		CheckPorts: func(ctx context.Context, domain string, port int) error {
			return system.CheckPorts(ctx, domain, []system.Port{{Number: port, Proto: "tcp", Label: "subscription/Nginx", Public: true}})
		},
	}
	if sm.action == subscriptionActionLocal {
		opts.Salt = strings.TrimSpace(sm.values["subscribe_salt"])
		if port, err := strconv.Atoi(strings.TrimSpace(sm.values["subscribe_port"])); err == nil {
			opts.SubscribePort = port
		}
	}
	return opts
}

func (sm *subscriptionManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(sm, msg) }

func (sm *subscriptionManager) runState() *commandRun { return &sm.commandRun }

func (sm *subscriptionManager) markRunFailed() { sm.phase = subscriptionPhaseDone }

func (sm *subscriptionManager) View() string {
	if sm.loadErr != nil {
		return flowTitle.Render("Manage Subscriptions") + "\n\n" + flowErr.Render(sm.loadErr.Error()) + "\n\n" + dimStyle.Render("Run install first.")
	}
	switch sm.phase {
	case subscriptionPhaseAction:
		return sm.actionView()
	case subscriptionPhaseForm:
		return sm.parameterForm.View("Manage Subscriptions · Parameters")
	case subscriptionPhaseConfirm:
		return sm.confirmView()
	case subscriptionPhaseRunning:
		return commandRunningView(sm, "Manage Subscriptions · Running")
	case subscriptionPhaseDone:
		if sm.runErr != nil {
			return commandFailedView(sm, "Subscription update failed")
		}
		return flowOK.Render("Subscriptions updated") + "\n\n" + sm.doneSummary()
	default:
		return ""
	}
}

func (sm *subscriptionManager) actionView() string {
	registry := cluster.NewRegistry(subscriptionUILayout())
	nodes, _ := registry.List()
	rows := []summaryLine{
		summaryRow("Subscription port", strconv.Itoa(sm.cfg.SubscribePort)),
		summaryRow("Subscription salt", sm.cfg.Salt),
		summaryRow("Cluster nodes", strconv.Itoa(len(nodes))),
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render("Manage Subscriptions") + "\n\n")
	b.WriteString(renderSummary(rows) + "\n")
	if !sm.canApply() {
		b.WriteString(flowErr.Render(sm.applyBlocker()) + "\n")
	}
	if sm.fieldErr != "" {
		b.WriteString(flowErr.Render(sm.fieldErr) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderActionList(sm.actions(), sm.cursor))
	return b.String()
}

func (sm *subscriptionManager) confirmView() string {
	var rows []summaryLine
	switch sm.action {
	case subscriptionActionDisplayName:
		rows = append(rows,
			summaryRow("Current display name", sm.cfg.DisplayName),
			summaryRow("New display name", sm.values["display_name"]),
		)
	case subscriptionActionLocal:
		rows = append(rows,
			summaryRow("Current salt", sm.cfg.Salt),
			summaryRow("New salt", sm.values["subscribe_salt"]),
			summaryRow("Current port", strconv.Itoa(sm.cfg.SubscribePort)),
			summaryRow("New port", sm.values["subscribe_port"]),
		)
	case subscriptionActionRefresh:
		registry := cluster.NewRegistry(subscriptionUILayout())
		nodes, _ := registry.List()
		rows = append(rows, summaryRow("Refresh subscriptions including nodes", strconv.Itoa(len(nodes))))
	}
	rows = append(rows, summaryBlank())
	if sm.action == subscriptionActionDisplayName {
		rows = append(rows, summaryText("This will regenerate sing-box config and subscription files."))
	} else {
		rows = append(rows, summaryText("This will regenerate subscription files for the master and every cluster node."))
	}
	return flowTitle.Render("Manage Subscriptions · Confirm") + "\n\n" + renderSummary(rows)
}

func (sm *subscriptionManager) doneSummary() string {
	cfg := sm.result
	if cfg.Domain == "" {
		cfg = sm.cfg
	}
	registry := cluster.NewRegistry(subscriptionUILayout())
	nodes, _ := registry.List()
	return renderSummary([]summaryLine{
		summaryRow("Display name", cfg.DisplayName),
		summaryRow("Subscription port", strconv.Itoa(cfg.SubscribePort)),
		summaryRow("Cluster nodes", strconv.Itoa(len(nodes))),
		summaryRow("Subscriptions", "refreshed"),
	})
}

func (sm *subscriptionManager) footerHints() []operationHint {
	if sm.loadErr != nil {
		return returnFooterHints()
	}
	switch sm.phase {
	case subscriptionPhaseAction:
		return actionFooterHints("Select")
	case subscriptionPhaseForm:
		return sm.parameterForm.footerHints()
	case subscriptionPhaseConfirm:
		return applyFooterHints("Apply")
	case subscriptionPhaseRunning:
		return runningFooterHints(sm.runComplete)
	case subscriptionPhaseDone:
		return doneFooterHints(sm.runErr != nil)
	default:
		return nil
	}
}

func (sm *subscriptionManager) actions() []subscriptionActionItem {
	return []subscriptionActionItem{
		{separator: true, label: "Settings"},
		{action: subscriptionActionDisplayName, label: "Edit display name"},
		{action: subscriptionActionLocal, label: "Edit subscription salt & port"},
		{separator: true, label: "Manage"},
		{action: subscriptionActionRefresh, label: "Refresh subscriptions"},
	}
}
