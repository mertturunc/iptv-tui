// iptv-tui is a terminal IPTV client for M3U playlists and Xtream Codes
// portals. It launches mpv as the media player and offers live/VOD/series
// browsing, favorites, search, EPG, and staggered playback resume.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/dbg"
	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
	"github.com/mertturunc/iptv-tui/internal/tui"
)

// version is stamped at build time via -ldflags "-X main.version=<ver>".
var version = "dev"

func main() {
	dbg.Init(version, os.Args)
	if dbg.Enabled() {
		fmt.Fprintln(os.Stderr, "debug log cleared: "+dbg.Path())
	}
	printVersion := flag.Bool("version", false, "print version and exit")
	play := flag.String("play", "", "play a URL or file directly with mpv and wait until it exits")
	addSource := flag.String("add-source", "", "add an m3u source URL to the config and exit")
	ascii := flag.Bool("ascii", false, "draw the console in plain ASCII, for terminals whose font lacks the box-drawing and symbol glyphs")
	flag.Parse()

	cfg, err := store.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	if *play != "" {
		os.Exit(runDirect(cfg, *play))
	}
	if *addSource != "" {
		os.Exit(addAndExit(cfg, *addSource))
	}
	if *printVersion {
		fmt.Println("iptv-tui " + version)
		os.Exit(0)
	}

	// The terminal picks its own font and the app can only send characters, so a
	// font that lacks a few marks cannot be fixed from in here. What we can do is
	// give the operator a switch that takes every glyph out of the equation.
	tui.SetASCII(*ascii || os.Getenv("IPTV_TUI_ASCII") == "1")

	// Paint the saved palette before anything derives a style from it.
	tui.ApplyStoredTheme(cfg.Cfg.Settings.Theme)
	// Tint the whole window; restored below so nothing leaks into the shell.
	tui.SetTermBackground()
	defer tui.ResetTermBackground()

	app := tui.New(cfg)
	p := tea.NewProgram(app, tea.WithAltScreen())
	app.SetSend(p.Send)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		os.Exit(1)
	}
}

func runDirect(cfg *store.Store, target string) int {
	if _, _, ok := player.Detect(cfg.Cfg.Settings.MpvPath); !ok {
		fmt.Fprintln(os.Stderr, "no player found (looked for mpv, vlc, mpc-hc)")
		for _, l := range player.InstallHelp() {
			fmt.Fprintln(os.Stderr, "  "+l)
		}
		return 1
	}
	req := player.Request{
		URL:           target,
		Title:         target,
		MpvPath:       cfg.Cfg.Settings.MpvPath,
		UserAgent:     cfg.Cfg.Settings.DefaultUserAgent,
		CustomArgs:    player.SplitArgs(cfg.Cfg.Settings.MpvArgs),
		TrackPosition: true,
	}
	proc, err := player.Launch(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "player:", err)
		return 1
	}
	<-proc.Done()
	fmt.Println(proc.PlayerName() + " closed")
	return 0
}

func addAndExit(cfg *store.Store, url string) int {
	src := store.Source{
		ID:   store.RandomID(),
		Name: url,
		Kind: "m3u",
		URL:  url,
	}
	cfg.AddSource(src)
	if err := cfg.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		return 1
	}
	fmt.Println("added source:", url)
	return 0
}
