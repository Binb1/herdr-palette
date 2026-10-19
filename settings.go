// Per-session settings for saved machines.
//
// config.toml in the plugin config directory says which machines each
// session connects to:
//
//	[sessions.default]
//	machines = ["robins-mac-mini"]
//
//	[sessions.clients]
//	machines = []
//
// Herdr keeps one machine catalog for every window, so the attach
// wrapper enables the session's machines and disables the others before
// each attach. A session without a "machines" list leaves the catalog
// as it is.
//
// Collapsing a machine is a palette view setting, remembered per session
// in the palette state directory.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type paletteConfig struct {
	Sessions map[string]struct {
		Machines *[]string `toml:"machines"`
	} `toml:"sessions"`
}

// paletteConfigPath is Herdr's config directory for this plugin. Herdr
// sets HERDR_PLUGIN_CONFIG_DIR for plugin processes; the attach wrapper
// runs outside Herdr and derives the same path.
func paletteConfigPath() string {
	if dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	return filepath.Join(filepath.Dir(configPath()), "plugins", "config", "binb1.palette", "config.toml")
}

// sessionMachines returns the machines (labels or ids) listed for a
// session, and false when the session has no list.
func sessionMachines(session string) (map[string]bool, bool) {
	var cfg paletteConfig
	if _, err := toml.DecodeFile(paletteConfigPath(), &cfg); err != nil {
		return nil, false
	}
	s, ok := cfg.Sessions[session]
	if !ok || s.Machines == nil {
		return nil, false
	}
	allow := map[string]bool{}
	for _, name := range *s.Machines {
		allow[name] = true
	}
	return allow, true
}

func machineAllowed(m machine, allow map[string]bool) bool {
	return allow[m.ID] || allow[m.Label]
}

// applySessionMachines enables the machines listed for session and
// disables every other saved machine. Open Herdr windows pick up the
// change within a second.
func applySessionMachines(session string) {
	allow, ok := sessionMachines(session)
	if !ok {
		return
	}
	for _, m := range savedMachines() {
		if want := machineAllowed(m, allow); want != m.Enabled {
			verb := "disable"
			if want {
				verb = "enable"
			}
			exec.Command(herdrBin(), "machine", verb, m.ID).Run()
		}
	}
}

// sessionFilter drops the machines a session does not list, so the
// palette does not offer to enable them.
func sessionFilter(ms []machine, session string) []machine {
	allow, ok := sessionMachines(session)
	if !ok {
		return ms
	}
	var out []machine
	for _, m := range ms {
		if machineAllowed(m, allow) {
			out = append(out, m)
		}
	}
	return out
}

// collapsedFile maps session -> collapsed machine ids.
func collapsedFile() string {
	return filepath.Join(filepath.Dir(switchDir()), "collapsed.json")
}

func loadCollapsed(session string) map[string]bool {
	all := map[string][]string{}
	b, _ := os.ReadFile(collapsedFile())
	json.Unmarshal(b, &all)
	out := map[string]bool{}
	for _, id := range all[session] {
		out[id] = true
	}
	return out
}

func saveCollapsed(session string, collapsed map[string]bool) {
	all := map[string][]string{}
	b, _ := os.ReadFile(collapsedFile())
	json.Unmarshal(b, &all)
	ids := []string{}
	for id, on := range collapsed {
		if on {
			ids = append(ids, id)
		}
	}
	all[session] = ids
	if b, err := json.MarshalIndent(all, "", "  "); err == nil {
		os.WriteFile(collapsedFile(), b, 0o644)
	}
}
