// Phone mode: flatten every multi-pane tab into one pane per tab, so a
// narrow client (a phone over mosh) gets full-width panes. The palette
// invokes this as "herdr-palette phone on|off". A state file records the
// original position of every moved pane, so "off" restores the splits.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

type phoneMove struct {
	PaneID string  `json:"pane_id"`
	TabID  string  `json:"tab_id"`
	Target string  `json:"target_pane"`
	Split  string  `json:"split"`
	Ratio  float64 `json:"ratio"`
}

type phoneState struct {
	Auto  bool        `json:"auto"` // engaged by the layout hook, not by hand
	Moves []phoneMove `json:"moves"`
}

// Below this layout width the auto hook engages phone mode. Phones over
// mosh sit near 50 columns; a desktop window stays well above this.
const phoneWidth = 60

type paneRect struct {
	X, Y, Width, Height int
}

type layoutPane struct {
	PaneID string   `json:"pane_id"`
	Rect   paneRect `json:"rect"`
}

type layoutSnap struct {
	Result struct {
		Snapshot struct {
			FocusedWorkspaceID string `json:"focused_workspace_id"`
			FocusedTabID       string `json:"focused_tab_id"`
			FocusedPaneID      string `json:"focused_pane_id"`
			Panes              []struct {
				PaneID string `json:"pane_id"`
			} `json:"panes"`
			Layouts []struct {
				TabID string `json:"tab_id"`
				Area  struct {
					Width int `json:"width"`
				} `json:"area"`
				Panes []layoutPane `json:"panes"`
			} `json:"layouts"`
		} `json:"snapshot"`
	} `json:"result"`
}

func stateFile() string {
	dir := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state", "herdr-palette")
	}
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "phone-mode.json")
}

func phoneModeActive() bool {
	_, err := os.Stat(stateFile())
	return err == nil
}

// A manual exit sets a suppress marker so the auto hook does not
// re-engage while a narrow client is still attached (a mosh phone stays
// attached when backgrounded). A wide layout clears it, so a later
// narrow client engages again.
func suppressFile() string { return stateFile() + ".suppress" }

func setSuppress()   { os.WriteFile(suppressFile(), []byte("1"), 0o644) }
func clearSuppress() { os.Remove(suppressFile()) }
func suppressed() bool {
	_, err := os.Stat(suppressFile())
	return err == nil
}

// splitFor derives the split to recreate p beside keep on restore.
func splitFor(keep, p paneRect) (string, float64) {
	if p.Y != keep.Y {
		return "down", float64(p.Height) / float64(keep.Height+p.Height)
	}
	return "right", float64(p.Width) / float64(keep.Width+p.Width)
}

// The lock keeps the layout hook from re-entering while a transition is
// in flight: every pane move fires more layout events. A stale lock
// (a crashed transition) expires after one minute.
func acquireLock() bool {
	lock := stateFile() + ".lock"
	if fi, err := os.Stat(lock); err == nil && time.Since(fi.ModTime()) > time.Minute {
		os.Remove(lock)
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func releaseLock() { os.Remove(stateFile() + ".lock") }

func phoneOn(auto bool) error {
	if !acquireLock() {
		return fmt.Errorf("a phone mode transition is already running")
	}
	defer releaseLock()
	if phoneModeActive() {
		return fmt.Errorf("phone mode is already on")
	}
	var s layoutSnap
	if err := herdrJSON(&s, "api", "snapshot"); err != nil {
		return err
	}
	// Mark phone mode active before the first move, so concurrent hook
	// runs and the palette see the transition immediately.
	writeState := func(moves []phoneMove) error {
		data, _ := json.MarshalIndent(phoneState{Auto: auto, Moves: moves}, "", " ")
		return os.WriteFile(stateFile(), data, 0o644)
	}
	if err := writeState(nil); err != nil {
		return err
	}
	var moves []phoneMove
	for _, l := range s.Result.Snapshot.Layouts {
		if len(l.Panes) < 2 {
			continue
		}
		// Order panes left-to-right, top-to-bottom. The first stays as
		// the anchor; every other pane is to its right or below, so the
		// restore can rebuild the row by chaining each onto the previous.
		ordered := append([]layoutPane(nil), l.Panes...)
		sort.Slice(ordered, func(a, b int) bool {
			if ordered[a].Rect.X != ordered[b].Rect.X {
				return ordered[a].Rect.X < ordered[b].Rect.X
			}
			return ordered[a].Rect.Y < ordered[b].Rect.Y
		})
		prev := ordered[0]
		for _, p := range ordered[1:] {
			if exec.Command(herdrBin(), "pane", "move", p.PaneID, "--new-tab").Run() != nil {
				continue
			}
			dir, ratio := splitFor(prev.Rect, p.Rect)
			moves = append(moves, phoneMove{
				PaneID: p.PaneID, TabID: l.TabID, Target: prev.PaneID, Split: dir, Ratio: ratio,
			})
			prev = p
		}
	}
	if len(moves) == 0 {
		os.Remove(stateFile())
		return fmt.Errorf("no split panes to flatten")
	}
	return writeState(moves)
}

func phoneOff() error {
	if !acquireLock() {
		return fmt.Errorf("a phone mode transition is already running")
	}
	defer releaseLock()
	data, err := os.ReadFile(stateFile())
	if err != nil {
		return fmt.Errorf("phone mode is not on")
	}
	var st phoneState
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	moves := st.Moves
	var s layoutSnap
	if err := herdrJSON(&s, "api", "snapshot"); err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, p := range s.Result.Snapshot.Panes {
		exists[p.PaneID] = true
	}
	// Remember what is focused now, so the moves do not steal focus.
	focusWs := s.Result.Snapshot.FocusedWorkspaceID
	focusTab := s.Result.Snapshot.FocusedTabID
	focusPane := s.Result.Snapshot.FocusedPaneID
	// Forward order: each pane attaches to the previous pane in the row,
	// which is already back in place, so the original order is preserved.
	for _, mv := range moves {
		if !exists[mv.PaneID] {
			continue // the pane closed while phone mode was on
		}
		err := exec.Command(herdrBin(), "pane", "move", mv.PaneID,
			"--tab", mv.TabID, "--target-pane", mv.Target,
			"--split", mv.Split, "--ratio", fmt.Sprintf("%.3f", mv.Ratio)).Run()
		if err != nil {
			// The target pane or tab is gone: settle for the tab alone.
			exec.Command(herdrBin(), "pane", "move", mv.PaneID, "--tab", mv.TabID).Run()
		}
	}
	if err := os.Remove(stateFile()); err != nil {
		return err
	}
	// Restore the focus the moves disturbed.
	if focusWs != "" {
		exec.Command(herdrBin(), "workspace", "focus", focusWs).Run()
	}
	if focusTab != "" {
		exec.Command(herdrBin(), "tab", "focus", focusTab).Run()
	}
	if focusPane != "" {
		exec.Command(herdrBin(), "pane", "focus", "--pane", focusPane).Run()
	}
	return nil
}

// phoneAuto runs from the layout.updated event hook. A narrow layout
// engages phone mode; a wide layout releases it, but only when phone
// mode was engaged automatically — a manual choice stays until the user
// exits it. Anything else is a quiet no-op.
func phoneAuto() error {
	var s layoutSnap
	if err := herdrJSON(&s, "api", "snapshot"); err != nil {
		return err
	}
	width := 0
	for _, l := range s.Result.Snapshot.Layouts {
		if l.Area.Width > width {
			width = l.Area.Width
		}
	}
	if width == 0 {
		return nil
	}
	if width >= phoneWidth {
		// A wide layout means the narrow client is gone. Clear any manual
		// suppression and undo an automatic engage.
		clearSuppress()
		if phoneModeActive() {
			data, err := os.ReadFile(stateFile())
			if err != nil {
				return nil
			}
			var st phoneState
			if json.Unmarshal(data, &st) == nil && st.Auto {
				return phoneOff()
			}
		}
		return nil
	}
	// Narrow layout: engage unless already on or manually suppressed.
	if !phoneModeActive() && !suppressed() {
		return phoneOn(true)
	}
	return nil
}

func phoneMain(mode string) {
	var err error
	switch mode {
	case "on":
		clearSuppress()
		err = phoneOn(false)
	case "off":
		setSuppress()
		err = phoneOff()
	case "toggle":
		if phoneModeActive() {
			setSuppress()
			err = phoneOff()
		} else {
			clearSuppress()
			err = phoneOn(false)
		}
	case "auto":
		// Hook runs are frequent and racing ones are expected: stay quiet.
		if err := phoneAuto(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	default:
		err = fmt.Errorf("usage: herdr-palette phone <on|off|toggle|auto>")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
