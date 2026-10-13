// Colors come from the Herdr theme in config.toml: the base theme for
// dark and light (theme.dark_name / theme.light_name, or theme.name),
// then the [theme.custom], [theme.custom.dark] and [theme.custom.light]
// overrides. Only the Catppuccin palettes are built in; other themes
// fall back to Catppuccin, and their custom overrides still apply.
package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/lipgloss"
)

type palette map[string]string

var (
	mocha = palette{
		"text": "#CDD6F4", "overlay0": "#6C7086", "mauve": "#CBA6F7",
		"peach": "#FAB387", "red": "#F38BA8", "green": "#A6E3A1",
	}
	latte = palette{
		"text": "#4C4F69", "overlay0": "#9CA0B0", "mauve": "#8839EF",
		"peach": "#FE640B", "red": "#D20F39", "green": "#40A02B",
	}
)

type herdrConfig struct {
	Theme struct {
		Name      string         `toml:"name"`
		DarkName  string         `toml:"dark_name"`
		LightName string         `toml:"light_name"`
		Custom    map[string]any `toml:"custom"`
	} `toml:"theme"`
}

func configPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "herdr", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr", "config.toml")
}

// basePalette picks the built-in palette for a theme name.
func basePalette(name string, dark bool) palette {
	if strings.Contains(name, "latte") || (name == "" && !dark) {
		return latte
	}
	if !dark && !strings.HasPrefix(name, "catppuccin") {
		return latte
	}
	return mocha
}

// resolve applies the custom overrides in order: [theme.custom], then
// the appearance-specific table.
func resolve(base palette, custom map[string]any, appearance string) palette {
	p := palette{}
	for k, v := range base {
		p[k] = v
	}
	apply := func(t map[string]any) {
		for k, v := range t {
			if s, ok := v.(string); ok && strings.HasPrefix(s, "#") {
				p[k] = s
			}
		}
	}
	apply(custom)
	if t, ok := custom[appearance].(map[string]any); ok {
		apply(t)
	}
	return p
}

func loadTheme() theme {
	var cfg herdrConfig
	toml.DecodeFile(configPath(), &cfg)
	t := cfg.Theme
	darkName, lightName := t.DarkName, t.LightName
	if darkName == "" {
		darkName = t.Name
	}
	if lightName == "" {
		lightName = t.Name
	}
	d := resolve(basePalette(darkName, true), t.Custom, "dark")
	l := resolve(basePalette(lightName, false), t.Custom, "light")
	c := func(key string) lipgloss.AdaptiveColor {
		return lipgloss.AdaptiveColor{Dark: d[key], Light: l[key]}
	}
	// "accent" overrides the orange when the config sets it.
	accent := func(p palette) string {
		if a := p["accent"]; a != "" {
			return a
		}
		return p["peach"]
	}
	acc := lipgloss.AdaptiveColor{Dark: accent(d), Light: accent(l)}
	return theme{
		text: c("text"), dim: c("overlay0"), header: c("mauve"), accent: acc,
		status: map[string]lipgloss.AdaptiveColor{
			"working": c("peach"), "blocked": c("red"), "done": c("green"),
			"idle": c("overlay0"), "current": acc,
		},
	}
}
