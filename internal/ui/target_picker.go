package ui

import (
	"fmt"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

type targetKind int

const (
	targetKindLocal targetKind = iota
	targetKindNode
	targetKindAll
)

type target struct {
	kind targetKind
	node cluster.Node
}

func (t target) isLocal() bool { return t.kind == targetKindLocal }
func (t target) isNode() bool  { return t.kind == targetKindNode }
func (t target) isAll() bool   { return t.kind == targetKindAll }

func (t target) label() string {
	switch t.kind {
	case targetKindLocal:
		return "Local (master)"
	case targetKindAll:
		return "All (master + every node)"
	case targetKindNode:
		alias := t.node.Alias
		if alias == "" {
			alias = t.node.Domain
		}
		if alias == "" {
			alias = t.node.ID
		}
		return fmt.Sprintf("Node %s [%s] (%s)", t.node.ID, alias, t.node.WGIP)
	}
	return "unknown"
}

func (t target) badge() string {
	switch t.kind {
	case targetKindLocal:
		return "Target: Local"
	case targetKindAll:
		return "Target: All (master + every node)"
	case targetKindNode:
		alias := t.node.Alias
		if alias == "" {
			alias = t.node.Domain
		}
		if alias == "" {
			alias = t.node.ID
		}
		return fmt.Sprintf("Target: %s (%s)", alias, t.node.WGIP)
	}
	return "Target: unknown"
}

type targetPicker struct {
	targets []target
	cursor  int
	loadErr error
}

func newTargetPicker(layout paths.Layout) targetPicker {
	registry := cluster.NewRegistry(layout)
	nodes, err := registry.List()
	tp := targetPicker{loadErr: err}
	tp.targets = []target{{kind: targetKindLocal}}
	for _, n := range nodes {
		tp.targets = append(tp.targets, target{kind: targetKindNode, node: n})
	}
	if len(nodes) > 0 {
		tp.targets = append(tp.targets, target{kind: targetKindAll})
	}
	return tp
}

func (tp targetPicker) hasNodes() bool { return len(tp.targets) > 1 }

func (tp targetPicker) selected() target {
	idx, ok := selectedIndex(tp.cursor, len(tp.targets))
	if !ok {
		return target{kind: targetKindLocal}
	}
	return tp.targets[idx]
}

func (tp *targetPicker) move(delta int) {
	tp.cursor = moveSelection(tp.cursor, len(tp.targets), delta)
}

func renderTargetPicker(title string, tp targetPicker) string {
	var b strings.Builder
	b.WriteString(flowTitle.Render(title) + "\n\n")
	b.WriteString(dimStyle.Render("Choose where to apply this action.") + "\n\n")
	for i, t := range tp.targets {
		row := "  " + t.label()
		if i == tp.cursor {
			row = selStyle.Render("> " + t.label())
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func renderTargetBadge(t target) string {
	return dimStyle.Render(t.badge())
}

// agentNodes returns the nodes to call when the target is All. The master is
// handled separately by the caller.
func agentNodes(tp targetPicker) []cluster.Node {
	out := make([]cluster.Node, 0, len(tp.targets))
	for _, t := range tp.targets {
		if t.kind == targetKindNode {
			out = append(out, t.node)
		}
	}
	return out
}
