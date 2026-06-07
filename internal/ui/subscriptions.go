package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/account"
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
	subscriptionActionAddRemote
	subscriptionActionDeleteRemotes
	subscriptionActionRefresh
)

var (
	subscriptionUILayout   = paths.DefaultLayout
	detectSubscriptionHost = system.DetectHost
	updateSubscriptionsRun = subscription.Update
	updateDisplayNameRun   = account.Update
)

type subscriptionActionItem struct {
	action subscriptionAction
	label  string
}

type subscriptionManager struct {
	phase  subscriptionPhase
	action subscriptionAction

	width  int
	height int

	host    system.Host
	hostErr error
	cfg     deploy.Config
	remotes []deploy.RemoteSubscription
	loadErr error

	cursor int
	parameterForm
	commandRun
	result deploy.Config
}

func newSubscriptionManager() *subscriptionManager {
	sm := &subscriptionManager{
		phase:         subscriptionPhaseAction,
		parameterForm: newParameterForm(nil),
		commandRun:    newCommandRun(),
	}
	host, err := detectSubscriptionHost()
	sm.host = host
	sm.hostErr = err
	cfg, err := deploy.LoadProtocolConfig(subscriptionUILayout())
	if err != nil {
		sm.loadErr = err
		return sm
	}
	sm.cfg = cfg
	remotes, err := deploy.LoadRemoteSubscriptions(subscriptionUILayout())
	if err != nil {
		sm.loadErr = err
		return sm
	}
	sm.remotes = remotes
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
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
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
			Cancel: func() (tea.Cmd, bool) {
				return nil, true
			},
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
		switch msg.String() {
		case "enter":
			if sm.runComplete {
				if cfg, err := deploy.LoadProtocolConfig(subscriptionUILayout()); err == nil {
					sm.cfg = cfg
					sm.result = cfg
				}
				if remotes, err := deploy.LoadRemoteSubscriptions(subscriptionUILayout()); err == nil {
					sm.remotes = remotes
				}
				sm.phase = subscriptionPhaseDone
			}
		case "up", "k":
			sm.scrollLog(1, sm.logViewportHeight())
		case "down", "j":
			sm.scrollLog(-1, sm.logViewportHeight())
		case "pgup":
			sm.scrollLog(sm.logViewportHeight(), sm.logViewportHeight())
		case "pgdown":
			sm.scrollLog(-sm.logViewportHeight(), sm.logViewportHeight())
		case "home":
			sm.logScroll = sm.maxLogScroll(sm.logViewportHeight())
		case "end":
			sm.logScroll = 0
		}
	case subscriptionPhaseDone:
		if sm.runErr != nil {
			switch msg.String() {
			case "up", "k":
				sm.scrollLog(1, sm.doneLogHeight())
				return nil, false
			case "down", "j":
				sm.scrollLog(-1, sm.doneLogHeight())
				return nil, false
			}
		}
		return nil, true
	}
	return nil, false
}

func (sm *subscriptionManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if sm.phase == subscriptionPhaseRunning || (sm.phase == subscriptionPhaseDone && sm.runErr != nil) {
			sm.scrollLog(3, sm.logViewportHeight())
		}
	case tea.MouseButtonWheelDown:
		if sm.phase == subscriptionPhaseRunning || (sm.phase == subscriptionPhaseDone && sm.runErr != nil) {
			sm.scrollLog(-3, sm.logViewportHeight())
		}
	}
	return nil
}

func (sm *subscriptionManager) moveAction(delta int) {
	actions := sm.actions()
	sm.cursor = moveSelection(sm.cursor, len(actions), delta)
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
	case subscriptionActionAddRemote:
		sm.startForm(sm.remoteFields())
	case subscriptionActionDeleteRemotes:
		if len(sm.remotes) == 0 {
			sm.fieldErr = "no remote subscriptions to delete"
			return
		}
		sm.startForm(sm.deleteRemoteFields())
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

func (sm *subscriptionManager) remoteFields() []field {
	return []field{
		{key: "remote_domain", label: "Remote domain", note: "Domain name of the remote singbox-deploy server, for example node.example.com. Used to build remote subscription URLs."},
		{key: "remote_alias", label: "Remote alias", note: "Alias used to rename aggregated remote nodes and display remote traffic. The node-name prefix (e.g. JP in JP-01) is replaced with this alias while preserving the numbering suffix, and the corresponding country flag emoji is prepended (e.g. JP-01 → 🇺🇸 US-vps1-01 when alias is US-vps1)."},
		{key: "remote_subscribe_port", label: "Remote subscription HTTPS port", def: strconv.Itoa(sm.cfg.SubscribePort)},
		{key: "remote_salt", label: "Remote subscription salt"},
	}
}

func (sm *subscriptionManager) deleteRemoteFields() []field {
	options := make([]string, 0, len(sm.remotes))
	for _, remote := range sm.remotes {
		options = append(options, remoteOptionLabel(remote))
	}
	return []field{{
		key:     "delete_remotes",
		label:   "Remote subscriptions to delete",
		options: options,
		multi:   true,
		note:    "Select one or more configured remote subscriptions to delete.",
	}}
}

func validateSubscriptionField(f field, val string, _ map[string]string) error {
	switch f.key {
	case "remote_domain":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("remote domain is required")
		}
	case "remote_alias":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("remote alias is required")
		}
	case "delete_remotes":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("select at least one remote entry to delete")
		}
	}
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
	opts := subscription.UpdateOptions{
		Salt:          sm.cfg.Salt,
		SubscribePort: sm.cfg.SubscribePort,
		Remotes:       toSubscriptionRemotes(sm.targetRemotes()),
		SetRemotes:    true,
		Fetch:         deploy.DefaultSubscriptionFetch,
		LoadConfig: func(l paths.Layout) (subscription.Config, error) {
			cfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return subscription.Config{}, err
			}
			return subscription.Config{Domain: cfg.Domain, Salt: cfg.Salt, SubscribePort: cfg.SubscribePort}, nil
		},
		LoadRemotes: func(l paths.Layout) ([]subscription.Remote, error) {
			remotes, err := deploy.LoadRemoteSubscriptions(l)
			if err != nil {
				return nil, err
			}
			return toSubscriptionRemotes(remotes), nil
		},
		ValidateRemotes: func(remotes []subscription.Remote) error {
			return deploy.ValidateRemoteSubscriptions(toDeployRemotes(remotes))
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
		SaveRemotes: func(l paths.Layout, remotes []subscription.Remote) error {
			return deploy.SaveRemoteSubscriptions(l, toDeployRemotes(remotes))
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
		WriteWithRemotes: func(ctx context.Context, l paths.Layout, cfg subscription.Config, remotes []subscription.Remote, fetch subscription.Fetcher) error {
			full, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return err
			}
			full.Salt = cfg.Salt
			full.SubscribePort = cfg.SubscribePort
			return deploy.WriteSubscriptionsWithRemotes(ctx, l, full, toDeployRemotes(remotes), deploy.SubscriptionFetcher(fetch))
		},
		RefreshMonitor: func(ctx context.Context, l paths.Layout, fetch subscription.Fetcher) error {
			sources, err := deploy.LoadMonitorSources(l)
			if err != nil {
				return err
			}
			return deploy.RefreshRemoteMonitor(ctx, l, sources, deploy.SubscriptionFetcher(fetch))
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

func toSubscriptionRemotes(remotes []deploy.RemoteSubscription) []subscription.Remote {
	out := make([]subscription.Remote, len(remotes))
	for i, r := range remotes {
		out[i] = subscription.Remote{Domain: r.Domain, Port: r.Port, Alias: r.Alias, Salt: r.Salt}
	}
	return out
}

func toDeployRemotes(remotes []subscription.Remote) []deploy.RemoteSubscription {
	out := make([]deploy.RemoteSubscription, len(remotes))
	for i, r := range remotes {
		out[i] = deploy.RemoteSubscription{Domain: r.Domain, Port: r.Port, Alias: r.Alias, Salt: r.Salt}
	}
	return out
}

func (sm *subscriptionManager) targetRemotes() []deploy.RemoteSubscription {
	switch sm.action {
	case subscriptionActionAddRemote:
		remotes := append([]deploy.RemoteSubscription(nil), sm.remotes...)
		port, _ := strconv.Atoi(strings.TrimSpace(sm.values["remote_subscribe_port"]))
		remotes = append(remotes, deploy.RemoteSubscription{
			Domain: strings.TrimSpace(sm.values["remote_domain"]),
			Port:   port,
			Alias:  strings.TrimSpace(sm.values["remote_alias"]),
			Salt:   strings.TrimSpace(sm.values["remote_salt"]),
		})
		return remotes
	case subscriptionActionDeleteRemotes:
		deleted := selectedOptions(sm.values["delete_remotes"])
		remotes := make([]deploy.RemoteSubscription, 0, len(sm.remotes))
		for _, remote := range sm.remotes {
			if deleted[remoteOptionLabel(remote)] {
				continue
			}
			remotes = append(remotes, remote)
		}
		return remotes
	default:
		return append([]deploy.RemoteSubscription(nil), sm.remotes...)
	}
}

func remoteOptionLabel(remote deploy.RemoteSubscription) string {
	alias := strings.TrimSpace(remote.Alias)
	if alias == "" {
		alias = strings.TrimSpace(remote.Domain)
	}
	return fmt.Sprintf("%s (%s:%d)", alias, strings.TrimSpace(remote.Domain), remote.Port)
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
	rows := []summaryLine{
		summaryRow("Subscription port", strconv.Itoa(sm.cfg.SubscribePort)),
		summaryRow("Subscription salt", sm.cfg.Salt),
		summaryRow("Remote subscriptions", strconv.Itoa(len(sm.remotes))),
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
	for i, action := range sm.actions() {
		row := "  " + action.label
		if i == sm.cursor {
			row = selStyle.Render("> " + action.label)
		}
		b.WriteString(row + "\n")
	}
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
	case subscriptionActionAddRemote:
		rows = append(rows,
			summaryRow("Add remote domain", sm.values["remote_domain"]),
			summaryRow("Remote subscription port", sm.values["remote_subscribe_port"]),
			summaryRow("Remote alias", sm.values["remote_alias"]),
		)
	case subscriptionActionDeleteRemotes:
		selected := sm.selectedRemoteDeleteLabels()
		remaining := remoteLabels(sm.targetRemotes())
		rows = append(rows,
			summaryRow("Delete remote subscriptions", strconv.Itoa(len(selected))),
		)
		for _, label := range selected {
			rows = append(rows, summaryIndentedRow(2, "Delete", label))
		}
		rows = append(rows, summaryRow("Remaining remote subscriptions", strconv.Itoa(len(remaining))))
		if len(remaining) == 0 {
			rows = append(rows, summaryIndentedRow(2, "Keep", "none"))
		}
		for _, label := range remaining {
			rows = append(rows, summaryIndentedRow(2, "Keep", label))
		}
	case subscriptionActionRefresh:
		rows = append(rows, summaryRow("Refresh remote subscriptions", strconv.Itoa(len(sm.remotes))))
	}
	rows = append(rows, summaryBlank())
	if sm.action == subscriptionActionDisplayName {
		rows = append(rows, summaryText("This will regenerate sing-box config and subscription files."))
	} else {
		rows = append(rows, summaryText("This will regenerate subscription files."))
	}
	return flowTitle.Render("Manage Subscriptions · Confirm") + "\n\n" + renderSummary(rows)
}

func (sm *subscriptionManager) selectedRemoteDeleteLabels() []string {
	selected := selectedOptions(sm.values["delete_remotes"])
	labels := make([]string, 0, len(selected))
	for _, remote := range sm.remotes {
		label := remoteOptionLabel(remote)
		if selected[label] {
			labels = append(labels, label)
		}
	}
	return labels
}

func remoteLabels(remotes []deploy.RemoteSubscription) []string {
	labels := make([]string, 0, len(remotes))
	for _, remote := range remotes {
		labels = append(labels, remoteOptionLabel(remote))
	}
	return labels
}

func (sm *subscriptionManager) doneSummary() string {
	cfg := sm.result
	if cfg.Domain == "" {
		cfg = sm.cfg
	}
	return renderSummary([]summaryLine{
		summaryRow("Display name", cfg.DisplayName),
		summaryRow("Subscription port", strconv.Itoa(cfg.SubscribePort)),
		summaryRow("Remote subscriptions", strconv.Itoa(len(sm.remotes))),
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
		{action: subscriptionActionDisplayName, label: "Edit display name"},
		{action: subscriptionActionLocal, label: "Edit subscription salt & port"},
		{action: subscriptionActionAddRemote, label: "Add remote subscription"},
		{action: subscriptionActionDeleteRemotes, label: "Delete remote subscription"},
		{action: subscriptionActionRefresh, label: "Refresh subscriptions"},
	}
}
