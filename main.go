// herdr-palette: a command palette for Herdr, rendered in a popup pane.
//
// The program reads the Herdr session snapshot and the plugin action list,
// shows a fuzzy-filterable list, and runs the selected command on Enter.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// ---- data ----

const (
	secJump    = "Jump"
	secActions = "Actions"
	secHerdr   = "Herdr"
)

var sectionOrder = map[string]int{secJump: 0, secActions: 1, secHerdr: 2}

type item struct {
	section    string
	title      string   // text shown and matched against
	dot        string   // agent status ("working", ...) or "" for no dot
	args       []string // herdr arguments to run on Enter
	renameArgs []string // if set, ask for a name first and append it to these
}

type snapshot struct {
	Result struct {
		Snapshot struct {
			FocusedWorkspaceID string `json:"focused_workspace_id"`
			FocusedTabID       string `json:"focused_tab_id"`
			Workspaces         []struct {
				WorkspaceID string `json:"workspace_id"`
				Number      int    `json:"number"`
				Label       string `json:"label"`
				AgentStatus string `json:"agent_status"`
				Focused     bool   `json:"focused"`
			} `json:"workspaces"`
			Agents []struct {
				AgentStatus string `json:"agent_status"`
				PaneID      string `json:"pane_id"`
				WorkspaceID string `json:"workspace_id"`
				Title       string `json:"terminal_title_stripped"`
				Focused     bool   `json:"focused"`
			} `json:"agents"`
		} `json:"snapshot"`
	} `json:"result"`
}

type actionList struct {
	Result struct {
		Actions []struct {
			ActionID string `json:"action_id"`
			PluginID string `json:"plugin_id"`
			Title    string `json:"title"`
		} `json:"actions"`
	} `json:"result"`
}

// clean removes control characters so snapshot-derived text cannot
// carry terminal escape sequences into the rendered view.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func herdrBin() string {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p
	}
	return "herdr"
}

func herdrJSON(v any, args ...string) error {
	out, err := exec.Command(herdrBin(), args...).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// loadItems builds the full item list from the live Herdr state.
func loadItems() []item {
	var items []item
	focusedWs, focusedTab := "", ""

	var snap snapshot
	if herdrJSON(&snap, "api", "snapshot") == nil {
		s := snap.Result.Snapshot
		focusedWs = s.FocusedWorkspaceID
		focusedTab = s.FocusedTabID
		for _, a := range s.Agents {
			if a.AgentStatus == "blocked" && !a.Focused {
				items = append(items, item{
					section: secJump, title: "Next blocked agent · " + clean(a.Title), dot: "blocked",
					args: []string{"agent", "focus", a.PaneID},
				})
				break
			}
		}
		agentTitle := map[string]string{} // workspace -> first agent title
		for _, a := range s.Agents {
			if agentTitle[a.WorkspaceID] == "" {
				agentTitle[a.WorkspaceID] = clean(a.Title)
			}
		}
		for _, w := range s.Workspaces {
			// Labels already carry the workspace number ("1. Fodmap").
			title := clean(w.Label)
			if title == "" {
				title = fmt.Sprintf("%d.", w.Number)
			}
			if t := agentTitle[w.WorkspaceID]; t != "" && t != w.Label {
				title += " · " + t
			}
			items = append(items, item{
				section: secJump, title: title, dot: w.AgentStatus,
				args: []string{"workspace", "focus", w.WorkspaceID},
			})
		}
		for _, a := range s.Agents {
			items = append(items, item{
				section: secJump, title: clean(a.Title), dot: a.AgentStatus,
				args: []string{"agent", "focus", a.PaneID},
			})
		}
	}

	var acts actionList
	if herdrJSON(&acts, "plugin", "action", "list") == nil {
		for _, a := range acts.Result.Actions {
			if a.PluginID == "binb1.palette" {
				continue
			}
			items = append(items, item{
				section: secActions, title: clean(a.Title),
				args: []string{"plugin", "action", "invoke", "--plugin", a.PluginID, a.ActionID},
			})
		}
	}

	items = append(items,
		item{section: secHerdr, title: "New workspace", args: []string{"workspace", "create"}},
		item{section: secHerdr, title: "New tab", args: []string{"tab", "create"}},
		item{section: secHerdr, title: "Reload config", args: []string{"server", "reload-config"}},
	)
	if focusedWs != "" {
		items = append(items, item{section: secHerdr, title: "Rename workspace",
			renameArgs: []string{"workspace", "rename", focusedWs}})
	}
	if focusedTab != "" {
		items = append(items, item{section: secHerdr, title: "Rename tab",
			renameArgs: []string{"tab", "rename", focusedTab}})
	}
	return items
}

// ---- theme ----

type theme struct {
	text, dim, header, accent lipgloss.AdaptiveColor
	status                    map[string]lipgloss.Color
}

// Catppuccin Mocha (dark) / Latte (light). Status dots match Robin's SwiftBar plugin.
var th = theme{
	text:   lipgloss.AdaptiveColor{Dark: "#CDD6F4", Light: "#4C4F69"},
	dim:    lipgloss.AdaptiveColor{Dark: "#6C7086", Light: "#8C8FA1"},
	header: lipgloss.AdaptiveColor{Dark: "#CBA6F7", Light: "#8839EF"},
	accent: lipgloss.AdaptiveColor{Dark: "#F8BC82", Light: "#FE640B"},
	status: map[string]lipgloss.Color{
		"working": "#E8A33D", "blocked": "#E35D6A", "done": "#5BB974", "idle": "#98989D",
	},
}

// ---- model ----

type row struct {
	text    string
	itemIdx int // index into filtered matches, or -1 for a section header
}

type model struct {
	items      []item
	query      string
	matches    []fuzzy.Match // over titles of items, grouped by section
	cursor     int           // index into matches
	scroll     int
	width      int
	height     int
	renameBase []string // non-nil while asking for a name
	newName    string
}

func newModel() model {
	m := model{items: loadItems(), width: 80, height: 20}
	m.filter()
	return m
}

type titles []item

func (t titles) String(i int) string { return t[i].title }
func (t titles) Len() int            { return len(t) }

func (m *model) filter() {
	// A leading ">" restricts the search to commands (Actions + Herdr),
	// like the VS Code palette.
	q := m.query
	commandsOnly := strings.HasPrefix(q, ">")
	if commandsOnly {
		q = strings.TrimSpace(q[1:])
	}
	m.matches = nil
	if q == "" {
		for i := range m.items {
			m.matches = append(m.matches, fuzzy.Match{Index: i})
		}
	} else {
		m.matches = fuzzy.FindFrom(q, titles(m.items))
	}
	if commandsOnly {
		kept := m.matches[:0]
		for _, match := range m.matches {
			if m.items[match.Index].section != secJump {
				kept = append(kept, match)
			}
		}
		m.matches = kept
	}
	sort.SliceStable(m.matches, func(a, b int) bool {
		sa := sectionOrder[m.items[m.matches[a].Index].section]
		sb := sectionOrder[m.items[m.matches[b].Index].section]
		if sa != sb {
			return sa < sb
		}
		return m.matches[a].Score > m.matches[b].Score
	})
	m.cursor = 0
	m.scroll = 0
}

// rows lays out headers and items in display order.
func (m model) rows() []row {
	var out []row
	last := ""
	for i, match := range m.matches {
		sec := m.items[match.Index].section
		if sec != last {
			out = append(out, row{text: sec, itemIdx: -1})
			last = sec
		}
		out = append(out, row{itemIdx: i})
	}
	return out
}

// ---- layout constants ----

// Herdr draws the popup border and title. The app fills the pane:
// one prompt line, the list, one footer line, with a 1-column margin.

func (m model) panelWidth() int {
	return m.width - 2
}

func (m model) listHeight() int {
	return m.height - 2 // prompt, footer
}

func (m model) run(args []string) tea.Cmd {
	c := exec.Command(herdrBin(), args...)
	c.Run()
	return tea.Quit
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		if m.renameBase != nil {
			return m.updateRename(msg)
		}
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, tea.Quit
		case "up", "ctrl+p":
			m.moveCursor(-1)
		case "down", "ctrl+n":
			m.moveCursor(1)
		case "enter":
			if m.cursor < len(m.matches) {
				it := m.items[m.matches[m.cursor].Index]
				if it.renameArgs != nil {
					m.renameBase = it.renameArgs
					return m, nil
				}
				return m, m.run(it.args)
			}
		case "backspace":
			if m.query != "" {
				m.query = m.query[:len(m.query)-1]
				m.filter()
			}
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.query += string(msg.Runes)
				m.filter()
			}
		}
	}
	return m, nil
}

func (m model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.renameBase = nil
		m.newName = ""
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		if name := strings.TrimSpace(m.newName); name != "" {
			return m, m.run(append(m.renameBase, name))
		}
	case "backspace":
		if m.newName != "" {
			m.newName = m.newName[:len(m.newName)-1]
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.newName += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.MouseWheelUp:
		m.moveCursor(-1)
	case tea.MouseWheelDown:
		m.moveCursor(1)
	case tea.MouseLeft:
		// Row 0 is the prompt line, the list starts on row 1.
		line := msg.Y - 1 + m.scroll
		rows := m.rows()
		if line >= 0 && line < len(rows) {
			if idx := rows[line].itemIdx; idx >= 0 {
				it := m.items[m.matches[idx].Index]
				if it.renameArgs != nil {
					m.renameBase = it.renameArgs
					return m, nil
				}
				return m, m.run(it.args)
			}
		}
	}
	return m, nil
}

func (m *model) moveCursor(delta int) {
	next := m.cursor + delta
	if next >= 0 && next < len(m.matches) {
		m.cursor = next
	}
	m.ensureVisible()
}

// ensureVisible scrolls the row window so the cursor's row stays on screen.
func (m *model) ensureVisible() {
	rows := m.rows()
	line := 0
	for i, r := range rows {
		if r.itemIdx == m.cursor {
			line = i
			break
		}
	}
	if line < m.scroll {
		m.scroll = line
	}
	if line >= m.scroll+m.listHeight() {
		m.scroll = line - m.listHeight() + 1
	}
}

// ---- view ----

func (m model) View() string {
	w := m.panelWidth()
	textSt := lipgloss.NewStyle().Foreground(th.text)
	dimSt := lipgloss.NewStyle().Foreground(th.dim)
	headSt := lipgloss.NewStyle().Foreground(th.header).Bold(true)
	accSt := lipgloss.NewStyle().Foreground(th.accent)

	var b strings.Builder

	if m.renameBase != nil {
		b.WriteString(accSt.Render("Rename to: ") + textSt.Render(m.newName) + accSt.Render("▌"))
	} else if m.query == "" {
		b.WriteString(accSt.Render("▌") + dimSt.Render(" search · > commands"))
	} else {
		b.WriteString(textSt.Render(m.query) + accSt.Render("▌"))
	}
	b.WriteString("\n")

	rows := m.rows()
	end := m.scroll + m.listHeight()
	if end > len(rows) {
		end = len(rows)
	}
	lines := 0
	for i := m.scroll; i < end; i++ {
		lines++
		r := rows[i]
		if r.itemIdx < 0 {
			b.WriteString(headSt.Render(r.text) + "\n")
			continue
		}
		match := m.matches[r.itemIdx]
		it := m.items[match.Index]
		line := "  "
		if r.itemIdx == m.cursor {
			line = accSt.Render("› ")
		}
		if it.dot != "" {
			c, ok := th.status[it.dot]
			if !ok {
				c = th.status["idle"]
			}
			line += lipgloss.NewStyle().Foreground(c).Render("●") + " "
		} else {
			line += "  "
		}
		line += highlight(it.title, match.MatchedIndexes, textSt, accSt.Bold(true))
		b.WriteString(line + "\n")
	}
	if len(m.matches) == 0 {
		b.WriteString(dimSt.Render("  no matches") + "\n")
		lines++
	}
	for ; lines < m.listHeight(); lines++ {
		b.WriteString("\n")
	}

	hints := "↑↓ move · ↵ run · esc"
	b.WriteString(strings.Repeat(" ", max(1, w-len([]rune(hints)))) + dimSt.Render(hints))

	// Indent every line by one column so text clears Herdr's border.
	return " " + strings.ReplaceAll(b.String(), "\n", "\n ")
}

// highlight renders s with the matched byte indexes in the accent style.
func highlight(s string, matched []int, base, hit lipgloss.Style) string {
	set := map[int]bool{}
	for _, i := range matched {
		set[i] = true
	}
	var b strings.Builder
	for i, r := range s {
		if set[i] {
			b.WriteString(hit.Render(string(r)))
		} else {
			b.WriteString(base.Render(string(r)))
		}
	}
	return b.String()
}

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
