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
	section string
	title   string   // text shown and matched against
	dot     string   // agent status ("working", ...) or "" for no dot
	args    []string // herdr arguments to run on Enter
	rename  bool     // true for "Rename workspace" (asks for a name first)
}

type snapshot struct {
	Result struct {
		Snapshot struct {
			FocusedWorkspaceID string `json:"focused_workspace_id"`
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
func loadItems() ([]item, string) {
	var items []item
	focusedWs := ""

	var snap snapshot
	if herdrJSON(&snap, "api", "snapshot") == nil {
		s := snap.Result.Snapshot
		focusedWs = s.FocusedWorkspaceID
		agentTitle := map[string]string{} // workspace -> first agent title
		for _, a := range s.Agents {
			if agentTitle[a.WorkspaceID] == "" {
				agentTitle[a.WorkspaceID] = a.Title
			}
		}
		for _, w := range s.Workspaces {
			// Labels already carry the workspace number ("1. Fodmap").
			title := w.Label
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
				section: secJump, title: a.Title, dot: a.AgentStatus,
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
				section: secActions, title: a.Title,
				args: []string{"plugin", "action", "invoke", "--plugin", a.PluginID, a.ActionID},
			})
		}
	}

	items = append(items,
		item{section: secHerdr, title: "New workspace", args: []string{"workspace", "create"}},
		item{section: secHerdr, title: "Rename workspace", rename: true},
		item{section: secHerdr, title: "Reload config", args: []string{"server", "reload-config"}},
	)
	return items, focusedWs
}

// ---- theme ----

type theme struct {
	text, dim, header, accent, selBg lipgloss.AdaptiveColor
	status                           map[string]lipgloss.Color
}

// Catppuccin Mocha (dark) / Latte (light). Status dots match Robin's SwiftBar plugin.
var th = theme{
	text:   lipgloss.AdaptiveColor{Dark: "#CDD6F4", Light: "#4C4F69"},
	dim:    lipgloss.AdaptiveColor{Dark: "#6C7086", Light: "#8C8FA1"},
	header: lipgloss.AdaptiveColor{Dark: "#CBA6F7", Light: "#8839EF"},
	accent: lipgloss.AdaptiveColor{Dark: "#F8BC82", Light: "#FE640B"},
	selBg:  lipgloss.AdaptiveColor{Dark: "#313244", Light: "#CCD0DA"},
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
	items     []item
	focusedWs string
	query     string
	matches   []fuzzy.Match // over titles of items, grouped by section
	cursor    int           // index into matches
	scroll    int
	width     int
	height    int
	renaming  bool
	newName   string
}

func newModel() model {
	items, ws := loadItems()
	m := model{items: items, focusedWs: ws, width: 80, height: 20}
	m.filter()
	return m
}

type titles []item

func (t titles) String(i int) string { return t[i].title }
func (t titles) Len() int            { return len(t) }

func (m *model) filter() {
	if m.query == "" {
		m.matches = nil
		for i := range m.items {
			m.matches = append(m.matches, fuzzy.Match{Index: i})
		}
	} else {
		m.matches = fuzzy.FindFrom(m.query, titles(m.items))
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

func (m model) run(it item) tea.Cmd {
	args := it.args
	if it.rename {
		args = append([]string{"workspace", "rename", m.focusedWs}, strings.Fields(m.newName)...)
	}
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
		if m.renaming {
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
				if it.rename {
					m.renaming = true
					return m, nil
				}
				return m, m.run(it)
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
		m.renaming = false
		m.newName = ""
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		if strings.TrimSpace(m.newName) != "" && m.focusedWs != "" {
			return m, m.run(item{rename: true})
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
				return m, m.run(m.items[m.matches[idx].Index])
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
	selSt := lipgloss.NewStyle().Background(th.selBg)

	var b strings.Builder

	if m.renaming {
		b.WriteString(accSt.Render("Rename to: ") + textSt.Render(m.newName) + accSt.Render("▌"))
	} else {
		b.WriteString(accSt.Render("› ") + textSt.Render(m.query) + accSt.Render("▌"))
	}
	b.WriteString("\n")

	rows := m.rows()
	end := m.scroll + m.listHeight()
	if end > len(rows) {
		end = len(rows)
	}
	for i := m.scroll; i < end; i++ {
		r := rows[i]
		if r.itemIdx < 0 {
			b.WriteString(headSt.Render(r.text) + "\n")
			continue
		}
		match := m.matches[r.itemIdx]
		it := m.items[match.Index]
		line := "  "
		if it.dot != "" {
			c, ok := th.status[it.dot]
			if !ok {
				c = th.status["idle"]
			}
			line = lipgloss.NewStyle().Foreground(c).Render("●") + " "
		}
		line += highlight(it.title, match.MatchedIndexes, textSt, accSt.Bold(true))
		if r.itemIdx == m.cursor {
			line = selSt.Render(lipgloss.NewStyle().Inline(true).MaxWidth(w).Render(line + strings.Repeat(" ", w)))
		}
		b.WriteString(line + "\n")
	}
	if len(m.matches) == 0 {
		b.WriteString(dimSt.Render("  no matches") + "\n")
	}
	for i := end - m.scroll; i < m.listHeight(); i++ {
		b.WriteString("\n")
	}

	footer := dimSt.Render("🐑") + strings.Repeat(" ", max(1, w-24)) + dimSt.Render("↑↓ move · ↵ run · esc")
	b.WriteString(footer)

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
