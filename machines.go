// Saved SSH machines (herdr machine add). The Machines group lists each
// machine with its health, and Jump gains the workspaces and agents of
// every enabled machine, read with "herdr --machine <id> api snapshot".
// SSH can be slow or hang, so all remote reads run in the background
// with a timeout: the palette opens with local state and fills in the
// remote rows when they arrive.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	remoteTimeout = 5 * time.Second
	remoteEvery   = 50 // ticks between remote refreshes (5 s)
)

type machine struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Target  string `json:"target"`
	Session string `json:"session"`
	Enabled bool   `json:"enabled"`
}

type machineStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "reachable", "auth required", "error", ...
	Error  string `json:"error"`
}

// remoteData is one background read: machine health and the snapshot
// of every machine that answered, keyed by machine id.
type remoteData struct {
	status map[string]machineStatus
	snaps  map[string]snapshot
}

type remoteMsg *remoteData

// savedMachines reads the local machine catalog. It does not touch SSH.
func savedMachines() []machine {
	var ms []machine
	if herdrJSON(&ms, "machine", "list", "--json") != nil {
		return nil
	}
	return ms
}

// herdrJSONTimeout is herdrJSON with a deadline. Herdr runs ssh as a
// child, so the command gets its own process group and the whole group
// is killed on timeout.
func herdrJSONTimeout(v any, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, herdrBin(), args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	// "machine status" exits 1 when a machine is unhealthy but still
	// prints its report, so parse stdout before giving up on the error.
	out, err := c.Output()
	if jerr := json.Unmarshal(out, v); jerr != nil {
		if err != nil {
			return err
		}
		return jerr
	}
	return nil
}

// machineDot maps a machine health to a status dot color.
func machineDot(m machine, st machineStatus, checked bool) string {
	switch {
	case !m.Enabled:
		return "idle"
	case !checked:
		return "unknown"
	case st.Status == "reachable":
		return "done"
	}
	return "blocked"
}

// agentCounts summarizes a snapshot for a collapsed machine row.
func agentCounts(snap snapshot) string {
	agents, blocked := 0, 0
	for _, a := range snap.Result.Snapshot.Agents {
		agents++
		if a.AgentStatus == "blocked" {
			blocked++
		}
	}
	out := fmt.Sprintf(" · %d agents", agents)
	if agents == 1 {
		out = " · 1 agent"
	}
	if blocked > 0 {
		out += fmt.Sprintf(" · %d blocked", blocked)
	}
	return out
}

// machineItems builds the Machines group. Without data, the rows show a
// grey dot until the background check returns. Enter on a reachable
// machine focuses its focused workspace; on any other machine it opens
// a reconnect tab. A collapsed machine sums up its agents in its row.
func machineItems(ms []machine, data *remoteData, collapsed map[string]bool) []item {
	var items []item
	for _, m := range ms {
		var st machineStatus
		checked := false
		if data != nil {
			st, checked = data.status[m.ID]
		}
		snap, hasSnap := snapshot{}, false
		if data != nil {
			snap, hasSnap = data.snaps[m.ID]
		}
		it := item{section: secMachines, title: clean(m.Label), remote: true,
			dot: machineDot(m, st, checked)}
		switch {
		case !m.Enabled:
			it.title += " · disabled"
			it.args = []string{"machine", "enable", m.ID}
		case !checked:
			it.title += " · checking…"
			it.reconnect = m.ID
		case st.Status != "reachable":
			it.title += " · " + clean(st.Status) + " · reconnect"
			it.reconnect = m.ID
		case hasSnap && snap.Result.Snapshot.FocusedWorkspaceID != "":
			it.args = []string{"--machine", m.ID, "workspace", "focus", snap.Result.Snapshot.FocusedWorkspaceID}
		default:
			it.reconnect = m.ID
		}
		if collapsed[m.ID] && hasSnap {
			it.title += agentCounts(snap)
		}
		items = append(items, it)
		if !m.Enabled {
			continue
		}
		fold := item{section: secMachines, remote: true, collapse: m.ID,
			title: "Collapse " + clean(m.Label) + " in this session"}
		if collapsed[m.ID] {
			fold.title = "Expand " + clean(m.Label) + " in this session"
		}
		items = append(items, fold, item{section: secMachines, remote: true,
			title: "Disable " + clean(m.Label), args: []string{"machine", "disable", m.ID}})
	}
	return items
}

// remoteItems builds every remote row: the Machines group, then the Jump
// rows of each expanded machine, led by a remote "Next blocked agent".
func remoteItems(ms []machine, data *remoteData, collapsed map[string]bool) []item {
	items := machineItems(ms, data, collapsed)
	if data == nil {
		return items
	}
	var next *item
	for _, m := range ms {
		snap, ok := data.snaps[m.ID]
		if !ok || collapsed[m.ID] {
			continue
		}
		n, jump := remoteJumpItems(m, snap)
		if next == nil {
			next = n
		}
		items = append(items, jump...)
	}
	if next != nil {
		items = append([]item{*next}, items...)
	}
	return items
}

// remoteJumpItems turns one machine's snapshot into Jump rows. Every
// command carries the --machine prefix, because ids are per server.
func remoteJumpItems(m machine, snap snapshot) (next *item, items []item) {
	s := snap.Result.Snapshot
	prefix := clean(m.Label) + " · "
	at := []string{"--machine", m.ID}
	for _, a := range s.Agents {
		if a.AgentStatus == "blocked" {
			next = &item{section: secJump, remote: true, next: true, dot: "blocked",
				title: "Next blocked agent · " + prefix + clean(a.Title),
				args:  append(at[:2:2], "agent", "focus", a.PaneID)}
			break
		}
	}
	agentTitle := map[string]string{}
	for _, a := range s.Agents {
		if agentTitle[a.WorkspaceID] == "" {
			agentTitle[a.WorkspaceID] = clean(a.Title)
		}
	}
	for _, w := range s.Workspaces {
		title := clean(w.Label)
		if title == "" {
			title = fmt.Sprintf("%d.", w.Number)
		}
		if t := agentTitle[w.WorkspaceID]; t != "" && t != w.Label {
			title += " · " + t
		}
		items = append(items, item{section: secJump, remote: true, dot: w.AgentStatus,
			title: prefix + title, args: append(at[:2:2], "workspace", "focus", w.WorkspaceID)})
	}
	for _, a := range s.Agents {
		items = append(items, item{section: secJump, remote: true, dot: a.AgentStatus,
			title: prefix + clean(a.Title), args: append(at[:2:2], "agent", "focus", a.PaneID)})
	}
	return next, items
}

// loadRemote checks every machine and reads every enabled machine's
// snapshot, side by side. Rows are built from the result by remoteItems,
// so collapsing a machine needs no new read.
func loadRemote(ms []machine) tea.Cmd {
	return func() tea.Msg {
		var (
			statuses []machineStatus
			statusOK bool
			snaps    = make([]*snapshot, len(ms))
			wg       sync.WaitGroup
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			statusOK = herdrJSONTimeout(&statuses, "machine", "status", "--json") == nil
		}()
		for i, m := range ms {
			if !m.Enabled {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				var s snapshot
				if herdrJSONTimeout(&s, "--machine", m.ID, "api", "snapshot") == nil {
					snaps[i] = &s
				}
			}()
		}
		wg.Wait()

		data := &remoteData{status: map[string]machineStatus{}, snaps: map[string]snapshot{}}
		for _, st := range statuses {
			data.status[st.ID] = st
		}
		for i, m := range ms {
			// A snapshot that came back proves the machine is reachable;
			// a failed status check without one marks it unreachable.
			if snaps[i] != nil {
				data.snaps[m.ID] = *snaps[i]
				data.status[m.ID] = machineStatus{ID: m.ID, Status: "reachable"}
			} else if _, ok := data.status[m.ID]; !ok && m.Enabled {
				reason := "unreachable"
				if !statusOK {
					reason = "no answer"
				}
				data.status[m.ID] = machineStatus{ID: m.ID, Status: reason}
			}
		}
		return remoteMsg(data)
	}
}

// reconnectMachine opens a new tab and runs "herdr machine reconnect"
// there: SSH authentication needs an interactive terminal.
func reconnectMachine(id string) error {
	var created struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := herdrJSON(&created, "tab", "create", "--label", "reconnect", "--focus"); err != nil {
		return err
	}
	cmd := shellQuote(herdrBin()) + " machine reconnect " + shellQuote(id)
	return exec.Command(herdrBin(), "pane", "run", created.Result.RootPane.PaneID, cmd).Run()
}

func shellQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
		} else {
			out += string(r)
		}
	}
	return out + "'"
}
