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
)

type phoneMove struct {
	PaneID string  `json:"pane_id"`
	TabID  string  `json:"tab_id"`
	Target string  `json:"target_pane"`
	Split  string  `json:"split"`
	Ratio  float64 `json:"ratio"`
}

type paneRect struct {
	X, Y, Width, Height int
}

type layoutSnap struct {
	Result struct {
		Snapshot struct {
			Panes []struct {
				PaneID string `json:"pane_id"`
			} `json:"panes"`
			Layouts []struct {
				TabID         string `json:"tab_id"`
				FocusedPaneID string `json:"focused_pane_id"`
				Panes         []struct {
					PaneID string   `json:"pane_id"`
					Rect   paneRect `json:"rect"`
				} `json:"panes"`
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

// splitFor derives the split to recreate p beside keep on restore.
func splitFor(keep, p paneRect) (string, float64) {
	if p.Y != keep.Y {
		return "down", float64(p.Height) / float64(keep.Height+p.Height)
	}
	return "right", float64(p.Width) / float64(keep.Width+p.Width)
}

func phoneOn() error {
	if phoneModeActive() {
		return fmt.Errorf("phone mode is already on")
	}
	var s layoutSnap
	if err := herdrJSON(&s, "api", "snapshot"); err != nil {
		return err
	}
	var moves []phoneMove
	for _, l := range s.Result.Snapshot.Layouts {
		if len(l.Panes) < 2 {
			continue
		}
		keep := l.FocusedPaneID
		if keep == "" {
			keep = l.Panes[0].PaneID
		}
		var keepRect paneRect
		for _, p := range l.Panes {
			if p.PaneID == keep {
				keepRect = p.Rect
			}
		}
		for _, p := range l.Panes {
			if p.PaneID == keep {
				continue
			}
			if exec.Command(herdrBin(), "pane", "move", p.PaneID, "--new-tab").Run() != nil {
				continue
			}
			dir, ratio := splitFor(keepRect, p.Rect)
			moves = append(moves, phoneMove{
				PaneID: p.PaneID, TabID: l.TabID, Target: keep, Split: dir, Ratio: ratio,
			})
		}
	}
	if len(moves) == 0 {
		return fmt.Errorf("no split panes to flatten")
	}
	data, _ := json.MarshalIndent(moves, "", " ")
	return os.WriteFile(stateFile(), data, 0o644)
}

func phoneOff() error {
	data, err := os.ReadFile(stateFile())
	if err != nil {
		return fmt.Errorf("phone mode is not on")
	}
	var moves []phoneMove
	if err := json.Unmarshal(data, &moves); err != nil {
		return err
	}
	var s layoutSnap
	if err := herdrJSON(&s, "api", "snapshot"); err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, p := range s.Result.Snapshot.Panes {
		exists[p.PaneID] = true
	}
	// Reverse order rebuilds nested splits closest to the original.
	for i := len(moves) - 1; i >= 0; i-- {
		mv := moves[i]
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
	return os.Remove(stateFile())
}

func phoneMain(mode string) {
	var err error
	switch mode {
	case "on":
		err = phoneOn()
	case "off":
		err = phoneOff()
	default:
		err = fmt.Errorf("usage: herdr-palette phone <on|off>")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
