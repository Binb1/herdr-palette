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

// remoteMsg carries the rebuilt remote rows: machine rows and remote Jump rows.
type remoteMsg []item

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

// machineItems builds the Machines group. Without status, the rows show
// a grey dot until the background check returns. Enter on a reachable
// machine focuses its focused workspace (focused maps id -> workspace);
// on any other machine it opens a reconnect tab.
func machineItems(ms []machine, status map[string]machineStatus, focused map[string]string) []item {
	var items []item
	for _, m := range ms {
		st, checked := status[m.ID]
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
		case focused[m.ID] != "":
			it.args = []string{"--machine", m.ID, "workspace", "focus", focused[m.ID]}
		default:
			it.reconnect = m.ID
		}
		items = append(items, it)
		if m.Enabled {
			items = append(items, item{section: secMachines, remote: true,
				title: "Disable " + clean(m.Label), args: []string{"machine", "disable", m.ID}})
		}
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
// snapshot, side by side.
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

		status := map[string]machineStatus{}
		for _, st := range statuses {
			status[st.ID] = st
		}
		for i, m := range ms {
			// A snapshot that came back proves the machine is reachable;
			// a failed status check without one marks it unreachable.
			if snaps[i] != nil {
				status[m.ID] = machineStatus{ID: m.ID, Status: "reachable"}
			} else if _, ok := status[m.ID]; !ok && m.Enabled {
				reason := "unreachable"
				if !statusOK {
					reason = "no answer"
				}
				status[m.ID] = machineStatus{ID: m.ID, Status: reason}
			}
		}

		focused := map[string]string{}
		for i, m := range ms {
			if snaps[i] != nil {
				focused[m.ID] = snaps[i].Result.Snapshot.FocusedWorkspaceID
			}
		}
		items := machineItems(ms, status, focused)
		var next *item
		for i, m := range ms {
			if snaps[i] == nil {
				continue
			}
			n, jump := remoteJumpItems(m, *snaps[i])
			if next == nil {
				next = n
			}
			items = append(items, jump...)
		}
		if next != nil {
			items = append([]item{*next}, items...)
		}
		return remoteMsg(items)
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
