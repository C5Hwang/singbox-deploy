package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/account"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
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
	subscriptionPhaseReorder
)

type subscriptionAction int

const (
	subscriptionActionDisplayName subscriptionAction = iota
	subscriptionActionLocal
	subscriptionActionEditSpoke
	subscriptionActionReorder
	subscriptionActionRefresh
)

var (
	subscriptionUILayout      = paths.DefaultLayout
	detectSubscriptionHost    = system.DetectHost
	updateSubscriptionsRun    = subscription.Update
	updateDisplayNameRun      = account.Update
	applySpokeSubscriptionRun = (*subscriptionManager).applySpokeSubscription
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
	nodes   []nodes.Node
	loadErr error

	cursor        int
	editNodeIndex int
	localPosition int
	reorder       reorderForm
	parameterForm
	commandRun
	result deploy.Config
}

func newSubscriptionManager() *subscriptionManager {
	sm := &subscriptionManager{
		phase:         subscriptionPhaseAction,
		cursor:        1,
		editNodeIndex: -1,
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
	list, err := nodes.Load(layout)
	if err != nil {
		sm.loadErr = err
		return sm
	}
	sm.nodes = list
	sm.localPosition = deploy.LoadLocalSubscriptionPosition(layout)
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
				if sm.action == subscriptionActionEditSpoke && sm.editNodeIndex < 0 {
					selectedLabel := sm.values["edit_spoke_select"]
					for i, node := range sm.nodes {
						if spokeOptionLabel(node) == selectedLabel {
							sm.editNodeIndex = i
							break
						}
					}
					sm.startEditSpokeForm()
					return
				}
				sm.phase = subscriptionPhaseConfirm
			},
			Back: func() {
				if !sm.previousField() {
					if sm.action == subscriptionActionEditSpoke && sm.editNodeIndex >= 0 {
						sm.editNodeIndex = -1
						sm.startForm(sm.editSpokeSelectField())
						return
					}
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
	case subscriptionPhaseReorder:
		confirm, cancel := sm.reorder.handleKey(msg)
		if confirm {
			sm.phase = subscriptionPhaseConfirm
			return nil, false
		}
		if cancel {
			return nil, true
		}
	case subscriptionPhaseConfirm:
		switch {
		case isSelectionConfirmKey(msg), isSelectionYesKey(msg):
			return sm.startRun(), false
		case isSelectionBackKey(msg):
			if sm.action == subscriptionActionReorder {
				sm.phase = subscriptionPhaseReorder
			} else if len(sm.fields) > 0 {
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
			layout := subscriptionUILayout()
			if cfg, err := deploy.LoadProtocolConfig(layout); err == nil {
				sm.cfg = cfg
				sm.result = cfg
			}
			if list, err := nodes.Load(layout); err == nil {
				sm.nodes = list
			}
			sm.localPosition = deploy.LoadLocalSubscriptionPosition(layout)
			sm.phase = subscriptionPhaseDone
		} else {
			sm.handleScrollKey(msg.String(), sm.logViewportHeight())
		}
	case subscriptionPhaseDone:
		return sm.handleDoneKey(msg.String())
	}
	return nil, false
}

func (sm *subscriptionManager) handleMouse(msg tea.MouseMsg) tea.Cmd {
	sm.handleLogWheel(msg.Button, sm.phase == subscriptionPhaseRunning || (sm.phase == subscriptionPhaseDone && sm.runErr != nil))
	return nil
}

func (sm *subscriptionManager) moveAction(delta int) {
	sm.cursor = moveActionCursor(sm.cursor, sm.actions(), delta)
	sm.fieldErr = ""
}

func (sm *subscriptionManager) activateAction() {
	sm.fieldErr = ""
	sm.editNodeIndex = -1
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
	case subscriptionActionEditSpoke:
		if len(sm.nodes) == 0 {
			sm.fieldErr = "no spoke nodes are registered; add one under Spoke → Spoke nodes"
			return
		}
		sm.startForm(sm.editSpokeSelectField())
	case subscriptionActionReorder:
		sm.reorder = newReorderForm(sm.buildReorderItems())
		sm.phase = subscriptionPhaseReorder
	case subscriptionActionRefresh:
		// Refresh has no form; clear any fields left over from a previous
		// form action so Back from the confirm page returns to the action
		// list instead of resurrecting that stale form.
		sm.parameterForm.setFields(nil)
		sm.phase = subscriptionPhaseConfirm
	}
}

func (sm *subscriptionManager) startForm(fields []field) {
	sm.phase = subscriptionPhaseForm
	if sm.parameterForm.begin(fields, nil, validateSubscriptionField) {
		sm.phase = subscriptionPhaseConfirm
	}
}

func (sm *subscriptionManager) displayNameFields() []field {
	return []field{fieldFromParameter(uiparams.SubscriptionDisplayNameField(sm.cfg))}
}

func (sm *subscriptionManager) localFields() []field {
	return fieldsFromParameters(uiparams.SubscriptionLocalFields(sm.cfg))
}

func (sm *subscriptionManager) editSpokeSelectField() []field {
	options := spokeLabels(sm.nodes)
	return []field{{
		key:     "edit_spoke_select",
		label:   "Spoke subscription settings to edit",
		options: options,
		note:    noteSpokeTransport,
	}}
}

func (sm *subscriptionManager) startEditSpokeForm() {
	if sm.editNodeIndex < 0 || sm.editNodeIndex >= len(sm.nodes) {
		return
	}
	node := sm.nodes[sm.editNodeIndex]
	fields := []field{
		{key: "spoke_alias", label: labelSpokeSubscriptionAlias + " (optional)", note: noteSpokeSubscriptionAlias},
		{key: "include_subscription", label: "Include in hub subscription", options: []string{"yes", "no"}, note: "Choose no to drop this spoke's nodes from the hub subscription."},
	}
	sm.phase = subscriptionPhaseForm
	if sm.parameterForm.begin(fields, map[string]string{
		"spoke_alias":          node.SubscriptionAlias,
		"include_subscription": yesNoString(node.IncludeInSubscription),
	}, sm.validateSpokeField) {
		sm.phase = subscriptionPhaseConfirm
	}
}

func (sm *subscriptionManager) buildReorderItems() []reorderItem {
	included := sm.includedNodes()
	total := 1 + len(included)
	items := make([]reorderItem, 0, total)
	localPos := deploy.ClampLocalPosition(sm.localPosition, len(included))
	localLabel := "Local"
	if sm.cfg.DisplayName != "" && sm.cfg.DisplayName != deploy.DefaultDisplayName {
		localLabel = "Local (" + sm.cfg.DisplayName + ")"
	}
	nodeIdx := 0
	for i := 0; i < total; i++ {
		if i == localPos {
			items = append(items, reorderItem{key: "local", label: localLabel})
		} else {
			node := included[nodeIdx]
			items = append(items, reorderItem{key: node.ID, label: spokeOptionLabel(node)})
			nodeIdx++
		}
	}
	return items
}

func (sm *subscriptionManager) targetLocalPosition() int {
	if sm.action == subscriptionActionReorder {
		for i, item := range sm.reorder.items {
			if item.key == "local" {
				return i
			}
		}
	}
	return sm.localPosition
}

// validateSpokeField adds registry-wide subscription-alias uniqueness to the
// shared validation. A duplicate has to be caught before the reconfigure is
// pushed over WireGuard.
func (sm *subscriptionManager) validateSpokeField(f field, val string, vals map[string]string) error {
	if f.key == "spoke_alias" {
		exempt := ""
		fallback := ""
		if sm.editNodeIndex >= 0 && sm.editNodeIndex < len(sm.nodes) {
			exempt = sm.nodes[sm.editNodeIndex].ID
			fallback = sm.nodes[sm.editNodeIndex].EffectiveAlias()
		}
		alias := strings.TrimSpace(val)
		if alias == "" {
			alias = fallback
		}
		if existing, clash := nodes.SubscriptionAliasConflict(sm.nodes, alias, exempt); clash {
			return fmt.Errorf("subscription alias is already used by %s", existing.EffectiveAlias())
		}
	}
	return validateSubscriptionField(f, val, vals)
}

func validateSubscriptionField(f field, val string, vals map[string]string) error {
	if err := uiparams.ValidateSubscriptionParameterValue(f.key, val); err != nil {
		return err
	}
	if err := uiparams.ValidateSharedParameterValue(f.key, val); err != nil {
		return err
	}
	return validateInstallPortConflict(f.key, val, vals)
}

func (sm *subscriptionManager) canApply() bool { return hostCanApply(sm.host, sm.hostErr) }

func (sm *subscriptionManager) applyBlocker() string {
	return hostApplyBlocker(sm.host, sm.hostErr,
		"subscription changes must be run as root",
		"SELinux is enforcing; subscription changes are blocked",
		"cannot apply subscription changes")
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
	progress := runProgressSender(ch)
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
			if err == nil {
				refreshHubSubscriptions(logs)
			}
			ch <- runMsg{done: true, err: err}
		}()
		return sm.waitForRun()
	}
	if sm.action == subscriptionActionEditSpoke {
		go func() {
			err := applySpokeSubscriptionRun(sm, context.Background(), logs, progress)
			ch <- runMsg{done: true, err: err}
		}()
		return sm.waitForRun()
	}
	if sm.action == subscriptionActionReorder {
		go func() {
			err := sm.applySourceOrder(context.Background(), logs)
			ch <- runMsg{done: true, err: err}
		}()
		return sm.waitForRun()
	}
	if sm.action == subscriptionActionRefresh {
		go func() {
			// Remove obsolete public remote definitions before publishing only
			// hub + registered spoke data fetched through WireGuard.
			err := deploy.SaveRemoteSubscriptions(subscriptionUILayout(), nil)
			if err == nil {
				err = (&hubctl.Controller{Layout: subscriptionUILayout(), ExpectedVersion: toolVersion}).RefreshSubscriptions(context.Background())
			}
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
		if err == nil {
			refreshHubSubscriptions(logs)
		}
		ch <- runMsg{done: true, err: err}
	}()
	return sm.waitForRun()
}

func (sm *subscriptionManager) applySpokeSubscription(ctx context.Context, logs *logWriter, progress func(deploy.Event)) error {
	if sm.editNodeIndex < 0 || sm.editNodeIndex >= len(sm.nodes) {
		return fmt.Errorf("selected spoke no longer exists")
	}
	selected := sm.nodes[sm.editNodeIndex]
	layout := subscriptionUILayout()
	ctrl := &hubctl.Controller{
		Layout: layout, Runner: system.NewExecRunner(logs), ExpectedVersion: toolVersion,
		Progress: offsetRunProgress(progress, 1, 5),
	}
	rollbackCtrl := *ctrl
	rollbackCtrl.Progress = nil
	return applySpokeRegistryReconfigure(
		ctx, layout, selected.ID, logs, progress,
		spokeRegistryChange{
			Detail:     "save the requested spoke subscription settings",
			Generation: spokeRegistryGenerationSubscription,
			Apply: func(current *nodes.Node) error {
				current.SubscriptionAlias = strings.TrimSpace(sm.values["spoke_alias"])
				current.IncludeInSubscription = sm.values["include_subscription"] != "no"
				return nil
			},
			Restore: func(current *nodes.Node, original, applied nodes.Node) {
				if current.SubscriptionAlias == applied.SubscriptionAlias {
					current.SubscriptionAlias = original.SubscriptionAlias
				}
				if current.IncludeInSubscription == applied.IncludeInSubscription {
					current.IncludeInSubscription = original.IncludeInSubscription
				}
			},
		},
		ctrl.Reconfigure,
		rollbackCtrl.Reconfigure,
	)
}

func (sm *subscriptionManager) applySourceOrder(ctx context.Context, logs *logWriter) error {
	byID := make(map[string]nodes.Node, len(sm.nodes))
	for _, node := range sm.nodes {
		byID[node.ID] = node
	}
	ordered := make([]nodes.Node, 0, len(sm.nodes))
	seen := map[string]bool{}
	localPosition := 0
	for i, item := range sm.reorder.items {
		if item.key == "local" {
			localPosition = i
			continue
		}
		if node, ok := byID[item.key]; ok {
			ordered = append(ordered, node)
			seen[node.ID] = true
		}
	}
	// Excluded and newly-added nodes keep their registry order after the
	// explicitly ordered subscription sources.
	for _, node := range sm.nodes {
		if !seen[node.ID] {
			ordered = append(ordered, node)
		}
	}
	layout := subscriptionUILayout()
	orderedIDs := make([]string, len(ordered))
	for i := range ordered {
		orderedIDs[i] = ordered[i].ID
	}
	if err := nodes.Reorder(layout, orderedIDs); err != nil {
		return err
	}
	if err := deploy.SaveLocalSubscriptionPosition(layout, localPosition); err != nil {
		return err
	}
	return (&hubctl.Controller{Layout: layout, ExpectedVersion: toolVersion}).RefreshSubscriptions(ctx)
}

func (sm *subscriptionManager) buildSubscriptionUpdateOptions() subscription.UpdateOptions {
	opts := subscription.UpdateOptions{
		Salt:          sm.cfg.Salt,
		SubscribePort: sm.cfg.SubscribePort,
		Remotes:       nil,
		SetRemotes:    true,
		Fetch:         deploy.DefaultSubscriptionFetch,
		LoadConfig: func(l paths.Layout) (subscription.Config, error) {
			cfg, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return subscription.Config{}, err
			}
			return subscription.Config{Domain: cfg.Domain, Salt: cfg.Salt, SubscribePort: cfg.SubscribePort}, nil
		},
		LoadRemotes:     func(paths.Layout) ([]subscription.Remote, error) { return nil, nil },
		ValidateRemotes: func([]subscription.Remote) error { return nil },
		WriteState: func(stateDir string, cfg subscription.Config) error {
			full, err := deploy.LoadProtocolConfig(subscriptionUILayout())
			if err != nil {
				return err
			}
			full.Salt = cfg.Salt
			full.SubscribePort = cfg.SubscribePort
			return deploy.WriteInstallState(stateDir, full)
		},
		SaveRemotes: func(l paths.Layout, _ []subscription.Remote) error {
			if err := deploy.SaveRemoteSubscriptions(l, nil); err != nil {
				return err
			}
			return nil
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
		WriteWithRemotes: func(ctx context.Context, l paths.Layout, cfg subscription.Config, _ []subscription.Remote, _ subscription.Fetcher) error {
			full, err := deploy.LoadProtocolConfig(l)
			if err != nil {
				return err
			}
			full.Salt = cfg.Salt
			full.SubscribePort = cfg.SubscribePort
			return deploy.WriteSubscriptionsWithRemotes(ctx, l, full, nil, nil, sm.localPosition)
		},
		RunCommands: deploy.RunCommands,
		CheckPorts: func(ctx context.Context, domain string, port int) error {
			return system.CheckPorts(ctx, domain, []system.Port{{Number: port, Proto: "tcp", Label: "subscription", Public: true}})
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

func (sm *subscriptionManager) includedNodes() []nodes.Node {
	out := make([]nodes.Node, 0, len(sm.nodes))
	for _, node := range sm.nodes {
		if node.IncludeInSubscription && node.Installed {
			out = append(out, node)
		}
	}
	return out
}

func spokeOptionLabel(node nodes.Node) string {
	id := node.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("%s (%s · %s)", node.EffectiveAlias(), node.WGIP, id)
}

func spokeLabels(list []nodes.Node) []string {
	labels := make([]string, len(list))
	for i, node := range list {
		labels[i] = spokeOptionLabel(node)
	}
	return labels
}

func (sm *subscriptionManager) handleRun(msg runMsg) tea.Cmd { return handleCommandRun(sm, msg) }

func (sm *subscriptionManager) runState() *commandRun { return &sm.commandRun }

func (sm *subscriptionManager) markRunFailed() { sm.phase = subscriptionPhaseDone }

func (sm *subscriptionManager) View() string {
	if sm.loadErr != nil {
		return flowTitle.Render(titleSubscriptions) + "\n\n" + flowErr.Render(sm.loadErr.Error()) + "\n\n" + dimStyle.Render("Run Setup first.")
	}
	switch sm.phase {
	case subscriptionPhaseAction:
		return sm.actionView()
	case subscriptionPhaseForm:
		return sm.parameterForm.View(titleSubscriptions + " · Parameters")
	case subscriptionPhaseReorder:
		return sm.reorder.View(titleSubscriptions + " · Reorder")
	case subscriptionPhaseConfirm:
		return sm.confirmView()
	case subscriptionPhaseRunning:
		return commandRunningView(sm, titleSubscriptions+" · Running")
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
		summaryRow(uiparams.LabelSubscribePort, strconv.Itoa(sm.cfg.SubscribePort)),
		summaryRow(uiparams.LabelSubscribeSalt, sm.cfg.Salt),
		summaryRow("Spoke nodes", strconv.Itoa(len(sm.nodes))),
		summaryRow("Included spokes", strconv.Itoa(len(sm.includedNodes()))),
		summaryRow("Control path", "WireGuard only"),
	}
	var b strings.Builder
	b.WriteString(flowTitle.Render(titleSubscriptions) + "\n\n")
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
	case subscriptionActionEditSpoke:
		if sm.editNodeIndex >= 0 && sm.editNodeIndex < len(sm.nodes) {
			old := sm.nodes[sm.editNodeIndex]
			rows = append(rows,
				summaryRow("Spoke", spokeOptionLabel(old)),
				summaryRow("Management alias", old.EffectiveAlias()),
				summaryRow("Current subscription alias", old.EffectiveSubscriptionAlias()),
				summaryRow("New subscription alias", or(sm.values["spoke_alias"], old.EffectiveAlias())),
				summaryRow("Include in hub subscription", sm.values["include_subscription"]),
			)
		}
	case subscriptionActionReorder:
		rows = append(rows, summaryRow("New order", ""))
		for i, item := range sm.reorder.items {
			rows = append(rows, summaryIndentedRow(2, strconv.Itoa(i+1), item.label))
		}
	case subscriptionActionRefresh:
		rows = append(rows,
			summaryRow("Refresh spoke subscriptions", strconv.Itoa(len(sm.includedNodes()))),
			summaryRow("Transport", "WireGuard overlay"),
		)
	}
	rows = append(rows, summaryBlank())
	if sm.action == subscriptionActionDisplayName {
		rows = append(rows, summaryText("Regenerates the sing-box config and subscription files."))
	} else {
		rows = append(rows, summaryText("Regenerates the subscription files."))
	}
	return flowTitle.Render(titleSubscriptions+" · Confirm") + "\n\n" + renderSummary(rows)
}

func (sm *subscriptionManager) doneSummary() string {
	cfg := sm.result
	if cfg.Domain == "" {
		cfg = sm.cfg
	}
	return renderSummary([]summaryLine{
		summaryRow(uiparams.LabelDisplayName, cfg.DisplayName),
		summaryRow(uiparams.LabelSubscribePort, strconv.Itoa(cfg.SubscribePort)),
		summaryRow("Included spokes", strconv.Itoa(len(sm.includedNodes()))),
		summaryRow("Spoke transport", "WireGuard"),
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
	case subscriptionPhaseReorder:
		return sm.reorder.footerHints()
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
		{separator: true, label: "Hub"},
		{action: subscriptionActionDisplayName, label: "Edit hub display name"},
		{action: subscriptionActionLocal, label: "Edit hub subscription settings"},
		{separator: true, label: "Spokes"},
		{action: subscriptionActionEditSpoke, label: "Edit spoke subscription settings"},
		{separator: true, label: "Sources"},
		{action: subscriptionActionReorder, label: "Reorder nodes"},
		{action: subscriptionActionRefresh, label: "Refresh from spokes"},
	}
}
