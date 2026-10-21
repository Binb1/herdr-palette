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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// ---- data ----

const (
	secJump     = "Jump"
	secActions  = "Actions"
	secHerdr    = "Herdr"
	secSessions = "Sessions"
	secMachines = "Machines"
)

var sectionOrder = map[string]int{secSessions: 0, secActions: 1, secHerdr: 2, secMachines: 3, secJump: 4}

type item struct {
	section    string
	title      string   // text shown and matched against
	dot        string   // agent status ("working", ...) or "" for no dot
	args       []string // herdr arguments to run on Enter
	selfArgs   []string // if set, run this binary with these arguments instead
	renameArgs []string // if set, ask for a name first and append it to these
	session    string   // if set, switch to this Herdr session
	newSession bool     // ask for a name, then switch to that session
	kill       string   // if set, stop this session after a second Enter
	reconnect  string   // if set, reconnect this saved machine in a new tab
	remote     bool     // built from saved machines; replaced on each refresh
	next       bool     // the "Next blocked agent" entry
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

	// The three herdr calls are independent; run them side by side.
	var (
		snap             snapshot
		acts             actionList
		sessions         []item
		snapErr, actsErr error
		wg               sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); snapErr = herdrJSON(&snap, "api", "snapshot") }()
	go func() { defer wg.Done(); actsErr = herdrJSON(&acts, "plugin", "action", "list") }()
	go func() { defer wg.Done(); sessions = sessionItems() }()
	wg.Wait()

	if snapErr == nil {
		s := snap.Result.Snapshot
		focusedWs = s.FocusedWorkspaceID
		focusedTab = s.FocusedTabID
		for _, a := range s.Agents {
			if a.AgentStatus == "blocked" && !a.Focused {
				items = append(items, item{
					section: secJump, title: "Next blocked agent · " + clean(a.Title), dot: "blocked", next: true,
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

	if actsErr == nil {
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
	if phoneModeActive() {
		items = append(items, item{section: secHerdr, title: "Exit phone mode",
			selfArgs: []string{"phone", "off"}})
	} else {
		items = append(items, item{section: secHerdr, title: "Phone mode",
			selfArgs: []string{"phone", "on"}})
	}
	return append(items, sessions...)
}

// loadStatus maps "workspace focus <id>" and "agent focus <pane>" item
// arguments to the live agent status, for the refresh while open.
func loadStatus() map[string]string {
	var snap snapshot
	if herdrJSON(&snap, "api", "snapshot") != nil {
		return nil
	}
	s := snap.Result.Snapshot
	out := map[string]string{}
	for _, w := range s.Workspaces {
		out["workspace focus "+w.WorkspaceID] = w.AgentStatus
	}
	for _, a := range s.Agents {
		out["agent focus "+a.PaneID] = a.AgentStatus
	}
	return out
}

// ---- theme ----

type theme struct {
	text, dim, header, accent lipgloss.AdaptiveColor
	status                    map[string]lipgloss.AdaptiveColor
}

// th follows the Herdr theme; see theme.go.
var th = loadTheme()

// ---- model ----

type row struct {
	text    string
	itemIdx int // index into filtered matches, or -1 for a section header
}

type model struct {
	base       []item // loaded entries
	items      []item // base plus suggestions built from the query
	query      string
	matches    []fuzzy.Match // over titles of items, grouped by section
	cursor     int           // index into matches
	scroll     int
	width      int
	height     int
	renameBase []string // non-nil while asking for a name
	naming     bool     // asking for a new session name
	newName    string
	err        string // shown in the footer after a failed switch
	confirm    string // session awaiting a second Enter to be stopped
	frame      int    // animation frame, advanced by tick
	typedAt    int    // frame of the last key, keeps the cursor solid
	machines   []machine
	remoteBusy bool // a remote refresh is in flight
}

// ---- animation ----

// One tick drives every animation: the working spinner, the blocked
// pulse and the cursor blink. Every second it also refreshes statuses.
const tickEvery = 100 * time.Millisecond

type tickMsg struct{}
type statusMsg map[string]string

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func refreshStatus() tea.Msg { return statusMsg(loadStatus()) }

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// glyph is the status mark for a dot at the current frame.
func (m model) glyph(dot string) string {
	switch dot {
	case "working":
		return spinner[m.frame%len(spinner)]
	case "blocked":
		if m.frame/4%2 == 1 {
			return "○"
		}
	}
	return "●"
}

// caret blinks, but stays solid for half a second after a key.
func (m model) caret() string {
	if (m.frame-m.typedAt)/5%2 == 1 {
		return " "
	}
	return "▌"
}

func newModel() model {
	var (
		items []item
		ms    []machine
		wg    sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); items = loadItems() }()
	go func() { defer wg.Done(); ms = savedMachines() }()
	wg.Wait()
	m := model{base: append(items, machineItems(ms, nil, nil)...), width: 80, height: 20,
		machines: ms, remoteBusy: len(ms) > 0}
	m.filter()
	return m
}

// setRemote replaces the remote rows and keeps the selected entry
// selected. A remote "Next blocked agent" only shows when no local
// agent is blocked; it goes first so it leads the Jump group.
func (m *model) setRemote(remote []item) {
	key := func(it item) string { return it.section + "\x00" + it.title + "\x00" + strings.Join(it.args, " ") }
	sel := ""
	if m.cursor < len(m.matches) {
		sel = key(m.items[m.matches[m.cursor].Index])
	}
	var local []item
	localNext := false
	for _, it := range m.base {
		if !it.remote {
			local = append(local, it)
			localNext = localNext || it.next
		}
	}
	var base []item
	for _, it := range remote {
		if it.next && !localNext {
			base = append(base, it)
		}
	}
	base = append(base, local...)
	for _, it := range remote {
		if !it.next {
			base = append(base, it)
		}
	}
	m.base = base
	m.filter()
	for i, match := range m.matches {
		if key(m.items[match.Index]) == sel {
			m.cursor = i
			break
		}
	}
	m.ensureVisible()
}

type titles []item

func (t titles) String(i int) string { return t[i].title }
func (t titles) Len() int            { return len(t) }

func (m *model) filter() {
	// A leading ">" restricts the search to commands (Actions + Herdr),
	// like the VS Code palette. A leading "@" restricts it to sessions.
	q := m.query
	commandsOnly := strings.HasPrefix(q, ">")
	sessionsOnly := strings.HasPrefix(q, "@")
	if commandsOnly || sessionsOnly {
		q = strings.TrimSpace(q[1:])
	}
	m.items = m.base
	var extra []item
	if q != "" && !sessionsOnly {
		extra = m.suggestions(q)
		m.items = append(m.base[:len(m.base):len(m.base)], extra...)
	}
	m.matches = nil
	if q == "" {
		for i := range m.base {
			m.matches = append(m.matches, fuzzy.Match{Index: i})
		}
	} else {
		m.matches = fuzzy.FindFrom(q, titles(m.base))
	}
	// Suggestions always show, below the real matches of their group.
	for i := range extra {
		m.matches = append(m.matches, fuzzy.Match{Index: len(m.base) + i, Score: -1})
	}
	kept := m.matches[:0]
	for _, match := range m.matches {
		sec := m.items[match.Index].section
		jump := sec == secJump || sec == secSessions || sec == secMachines
		if (!sessionsOnly || sec == secSessions) && !(commandsOnly && jump) {
			kept = append(kept, match)
		}
	}
	m.matches = kept
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

// suggestions turns the query into commands that take it as a name:
// rename the workspace or tab, or create a workspace, tab or session.
func (m model) suggestions(q string) []item {
	quoted := " \"" + q + "\""
	var out []item
	for _, it := range m.base {
		if it.renameArgs != nil {
			out = append(out, item{section: secHerdr, title: it.title + quoted,
				args: append(append([]string{}, it.renameArgs...), q)})
		}
	}
	return append(out,
		item{section: secHerdr, title: "New workspace" + quoted,
			args: []string{"workspace", "create", "--label", q}},
		item{section: secHerdr, title: "New tab" + quoted,
			args: []string{"tab", "create", "--label", q}},
		item{section: secHerdr, title: "New session" + quoted,
			session: strings.Join(strings.Fields(q), "-")},
	)
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
	return m.runBin(herdrBin(), args)
}

func (m model) runBin(bin string, args []string) tea.Cmd {
	c := exec.Command(bin, args...)
	c.Run()
	return tea.Quit
}

// switchTo moves the client to another session, or shows why it cannot.
func (m model) switchTo(name string) (tea.Model, tea.Cmd) {
	if err := switchSession(name); err != nil {
		m.err = err.Error()
		return m, nil
	}
	return m, tea.Quit
}

// choose activates an entry: enter rename mode, switch session, run this
// binary, or run herdr.
func (m model) choose(it item) (tea.Model, tea.Cmd) {
	if it.renameArgs != nil {
		m.renameBase = it.renameArgs
		return m, nil
	}
	if it.kill != "" {
		if m.confirm != it.kill {
			m.confirm = it.kill
			m.err = "Enter again to kill " + it.kill + " and all its panes"
			return m, nil
		}
		// Stopping this session also ends the palette, so detach the
		// command from its process group.
		// "default" cannot be deleted; other sessions go away entirely.
		script := `"$0" session stop "$1" && [ "$1" != default ] && "$0" session delete "$1"`
		c := exec.Command("sh", "-c", script, herdrBin(), it.kill)
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		c.Start()
		return m, tea.Quit
	}
	if it.reconnect != "" {
		if err := reconnectMachine(it.reconnect); err != nil {
			m.err = "reconnect: " + err.Error()
			return m, nil
		}
		return m, tea.Quit
	}
	if it.newSession {
		m.naming = true
		return m, nil
	}
	if it.session != "" {
		return m.switchTo(it.session)
	}
	if it.selfArgs != nil {
		self, err := os.Executable()
		if err != nil {
			return m, tea.Quit
		}
		return m, m.runBin(self, it.selfArgs)
	}
	return m, m.run(it.args)
}

func (m model) Init() tea.Cmd {
	if m.remoteBusy {
		return tea.Batch(tick(), loadRemote(m.machines))
	}
	return tick()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.frame++
		cmds := []tea.Cmd{tick()}
		if m.frame%10 == 0 {
			cmds = append(cmds, refreshStatus)
		}
		if m.frame%remoteEvery == 0 && !m.remoteBusy && len(m.machines) > 0 {
			m.remoteBusy = true
			cmds = append(cmds, loadRemote(m.machines))
		}
		return m, tea.Batch(cmds...)
	case remoteMsg:
		m.remoteBusy = false
		m.setRemote(msg)
	case statusMsg:
		for _, list := range [][]item{m.base, m.items} {
			for i := range list {
				if st, ok := msg[strings.Join(list[i].args, " ")]; ok && list[i].dot != "current" {
					list[i].dot = st
				}
			}
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		m.typedAt = m.frame
		if m.renameBase != nil || m.naming {
			return m.updateRename(msg)
		}
		if msg.String() != "enter" && m.confirm != "" {
			m.confirm, m.err = "", ""
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
				return m.choose(m.items[m.matches[m.cursor].Index])
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
		m.naming = false
		m.newName = ""
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		if name := strings.TrimSpace(m.newName); name != "" {
			if m.naming {
				// Session names become directory names: no spaces.
				return m.switchTo(strings.Join(strings.Fields(name), "-"))
			}
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
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		m.moveCursor(-1)
	case msg.Button == tea.MouseButtonWheelDown:
		m.moveCursor(1)
	case msg.Action == tea.MouseActionMotion:
		// Hover moves the selection, so the palette tracks the pointer.
		if idx := m.itemAt(msg.Y); idx >= 0 {
			m.cursor = idx
		}
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		if idx := m.itemAt(msg.Y); idx >= 0 {
			return m.choose(m.items[m.matches[idx].Index])
		}
	}
	return m, nil
}

// itemAt maps a terminal row to a filtered-match index, or -1 for
// the prompt line, a section header, or empty space.
func (m model) itemAt(y int) int {
	line := y - 1 + m.scroll // row 0 is the prompt line
	rows := m.rows()
	if line >= 0 && line < len(rows) {
		return rows[line].itemIdx
	}
	return -1
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

	if m.naming {
		b.WriteString(accSt.Render("New session: ") + textSt.Render(m.newName) + accSt.Render(m.caret()))
	} else if m.renameBase != nil {
		b.WriteString(accSt.Render("Rename to: ") + textSt.Render(m.newName) + accSt.Render(m.caret()))
	} else if m.query == "" {
		b.WriteString(accSt.Render(m.caret()) + dimSt.Render(" search · > commands · @ sessions"))
	} else {
		b.WriteString(textSt.Render(m.query) + accSt.Render(m.caret()))
	}
	b.WriteString("\n")

	rows := m.rows()
	if m.naming {
		rows = nil // the name prompt has no list
	}
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
			line += lipgloss.NewStyle().Foreground(c).Render(m.glyph(it.dot)) + " "
		} else {
			line += "  "
		}
		line += highlight(it.title, match.MatchedIndexes, textSt, accSt.Bold(true))
		b.WriteString(line + "\n")
	}
	if len(m.matches) == 0 && !m.naming {
		b.WriteString(dimSt.Render("  no matches") + "\n")
		lines++
	}
	for ; lines < m.listHeight(); lines++ {
		b.WriteString("\n")
	}

	if m.err != "" {
		b.WriteString(" " + lipgloss.NewStyle().Foreground(th.status["blocked"]).Render(m.err))
	} else {
		hints := "↑↓ move · ↵ run · esc"
		b.WriteString(strings.Repeat(" ", max(1, w-len([]rune(hints)))) + dimSt.Render(hints))
	}

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
	linkStableBin()
	if len(os.Args) > 1 && os.Args[1] == "phone" {
		mode := ""
		if len(os.Args) > 2 {
			mode = os.Args[2]
		}
		phoneMain(mode)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		name := "default"
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		attachMain(name)
		return
	}
	// The toggle action reads this file to close an open palette.
	pid := pidFile()
	os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0o644)
	p := tea.NewProgram(newModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	os.Remove(pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pidFile() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "state", "herdr-palette")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "palette.pid")
}
