// Session switching. Herdr has no API to move a client to another
// session, so "herdr-palette attach <name>" wraps the client in a loop:
// the palette writes the target session to a file named after the
// wrapper's pid, then sends SIGTERM to the wrapped client. The client
// detaches cleanly (the session keeps running) and the loop attaches to
// the target. A plain detach (prefix+q) writes no file, so the loop ends.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type sessionList struct {
	Sessions []struct {
		Name       string `json:"name"`
		Running    bool   `json:"running"`
		SocketPath string `json:"socket_path"`
	} `json:"sessions"`
}

// switchDir is shared by the wrapper (a normal shell) and the palette
// (a plugin pane), so it does not use HERDR_PLUGIN_STATE_DIR.
func switchDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "state", "herdr-palette", "switch")
	os.MkdirAll(dir, 0o755)
	return dir
}

func switchFile(wrapperPid int) string {
	return filepath.Join(switchDir(), strconv.Itoa(wrapperPid))
}

// attachArgs returns the herdr arguments that attach to a session.
func attachArgs(name string) []string {
	if name == "default" {
		return nil
	}
	return []string{"--session", name}
}

// attachMain runs the client in a loop until a detach that the palette
// did not request.
func attachMain(name string) {
	// Handle SIGINT/SIGTERM in the wrapper without passing SIG_IGN on to
	// the client: the client owns the terminal while it runs.
	signal.Notify(make(chan os.Signal, 1), os.Interrupt, syscall.SIGTERM)
	file := switchFile(os.Getpid())
	defer os.Remove(file)
	for {
		os.Remove(file)
		c := exec.Command(herdrBin(), attachArgs(name)...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := c.Run()
		next, _ := os.ReadFile(file)
		name = strings.TrimSpace(string(next))
		if name == "" {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			return
		}
	}
}

// currentSession names the session this palette runs in.
func currentSession(list sessionList) string {
	sock := os.Getenv("HERDR_SOCKET_PATH")
	for _, s := range list.Sessions {
		if sock != "" && s.SocketPath == sock {
			return s.Name
		}
	}
	return "default"
}

// sessionStatus sums up the agents of a running session as one dot:
// blocked wins over working, working over done, done over idle.
func sessionStatus(socket string) string {
	c := exec.Command(herdrBin(), "api", "snapshot")
	c.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+socket)
	out, err := c.Output()
	if err != nil {
		return ""
	}
	var snap snapshot
	if json.Unmarshal(out, &snap) != nil {
		return ""
	}
	rank := map[string]int{"idle": 1, "done": 2, "working": 3, "blocked": 4}
	best := ""
	for _, a := range snap.Result.Snapshot.Agents {
		if rank[a.AgentStatus] > rank[best] {
			best = a.AgentStatus
		}
	}
	return best
}

// sessionItems lists every session, current one first.
func sessionItems() []item {
	var list sessionList
	if herdrJSON(&list, "session", "list", "--json") != nil {
		return nil
	}
	cur := currentSession(list)
	items := []item{{section: secSessions, title: cur + " · current", session: cur}}
	for _, s := range list.Sessions {
		if s.Name == cur {
			continue
		}
		it := item{section: secSessions, title: s.Name, session: s.Name}
		if s.Running {
			it.dot = sessionStatus(s.SocketPath)
		} else {
			it.title += " · stopped"
		}
		items = append(items, it)
	}
	return append(items, item{section: secSessions, title: "New session…", newSession: true})
}

// wrappedClients returns, for each "herdr-palette attach" wrapper whose
// client is attached to session, the wrapper pid and the client pid.
func wrappedClients(session string) (wrappers, clients []int) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,args=").Output()
	if err != nil {
		return nil, nil
	}
	type proc struct {
		ppid int
		args []string
	}
	procs := map[int]proc{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		procs[pid] = proc{ppid, f[2:]}
	}
	for pid, p := range procs {
		parent, ok := procs[p.ppid]
		if !ok || len(parent.args) < 2 || filepath.Base(parent.args[0]) != "herdr-palette" || parent.args[1] != "attach" {
			continue
		}
		name := "default"
		for i, a := range p.args {
			if a == "--session" && i+1 < len(p.args) {
				name = p.args[i+1]
			}
		}
		if name == session {
			wrappers = append(wrappers, p.ppid)
			clients = append(clients, pid)
		}
	}
	return wrappers, clients
}

// switchSession moves every wrapped client of the current session to
// target. It fails when no client runs under "herdr-palette attach".
func switchSession(target string) error {
	var list sessionList
	if err := herdrJSON(&list, "session", "list", "--json"); err != nil {
		return err
	}
	cur := currentSession(list)
	if target == cur {
		return nil
	}
	wrappers, clients := wrappedClients(cur)
	if len(clients) == 0 {
		return fmt.Errorf("this window was not started with herdr-palette attach (h)")
	}
	for i, w := range wrappers {
		os.WriteFile(switchFile(w), []byte(target), 0o644)
		syscall.Kill(clients[i], syscall.SIGTERM)
	}
	return nil
}
