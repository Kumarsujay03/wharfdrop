package daemon

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
	"github.com/Kumarsujay03/wharfdrop/internal/update"
)

// ErrRestart is returned by Run after an update was installed; the caller
// should re-execute the binary (see update.Restart).
var ErrRestart = errors.New("restart requested")

const (
	checkEvery  = 6 * time.Hour
	firstCheck  = 1 * time.Minute
	retryIfBusy = 10 * time.Minute
)

type UpdateState struct {
	Current    string    `json:"current"`
	Latest     string    `json:"latest,omitempty"`
	Available  bool      `json:"available"`
	Mode       string    `json:"mode"` // auto | notify | off
	DevBuild   bool      `json:"dev_build"`
	Checked    time.Time `json:"checked,omitempty"`
	Installing bool      `json:"installing"`
	Error      string    `json:"error,omitempty"`
}

func (d *Daemon) updateState() UpdateState {
	d.updMu.Lock()
	s := d.upd
	d.updMu.Unlock()
	s.Current = Version
	s.DevBuild = !update.IsRelease(Version)
	d.Cfg.View(func(c *config.Config) { s.Mode = c.AutoUpdate })
	return s
}

// CheckUpdate asks GitHub for the latest release.
func (d *Daemon) CheckUpdate(ctx context.Context) UpdateState {
	latest, err := update.New(Version).Latest(ctx)
	d.updMu.Lock()
	d.upd.Checked = time.Now()
	if err != nil {
		d.upd.Error = err.Error()
	} else {
		d.upd.Error = ""
		d.upd.Latest = latest
		d.upd.Available = update.Newer(latest, Version)
	}
	d.updMu.Unlock()
	return d.updateState()
}

func (d *Daemon) busy() bool {
	for _, t := range d.transfers.List() {
		if t.Finished.IsZero() {
			return true
		}
	}
	return false
}

// InstallUpdate downloads and verifies the latest release, replaces the
// binary, and asks Run to restart. It refuses while transfers are running.
func (d *Daemon) InstallUpdate(ctx context.Context) error {
	if !update.IsRelease(Version) {
		return errors.New("this is a development build (" + Version + "); install a release from https://github.com/" + update.Repo + "/releases to get updates")
	}
	st := d.CheckUpdate(ctx)
	if st.Error != "" {
		return errors.New(st.Error)
	}
	if !st.Available {
		return errors.New("already up to date (" + Version + ")")
	}
	if d.busy() {
		return errors.New("a transfer is in progress; try again when it finishes")
	}
	d.updMu.Lock()
	if d.upd.Installing {
		d.updMu.Unlock()
		return errors.New("an update is already being installed")
	}
	d.upd.Installing = true
	d.updMu.Unlock()
	defer func() { d.updMu.Lock(); d.upd.Installing = false; d.updMu.Unlock() }()

	exe, err := update.Executable()
	if err == nil {
		log.Printf("installing update %s -> %s", Version, st.Latest)
		err = update.New(Version).Install(ctx, st.Latest, exe)
	}
	if err != nil {
		d.updMu.Lock()
		d.upd.Error = err.Error()
		d.updMu.Unlock()
		return err
	}
	log.Printf("update %s installed", st.Latest)
	go func() {
		time.Sleep(500 * time.Millisecond) // let the API reply go out first
		select {
		case d.restart <- struct{}{}:
		default:
		}
	}()
	return nil
}

func (d *Daemon) updateLoop(ctx context.Context) {
	if !update.IsRelease(Version) {
		return // development builds never update themselves
	}
	wait := firstCheck
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = checkEvery
		st := d.updateState()
		if st.Mode == "off" {
			continue
		}
		st = d.CheckUpdate(ctx)
		if st.Error != "" {
			log.Printf("update check failed: %s", st.Error)
			continue
		}
		if !st.Available {
			continue
		}
		if st.Mode != "auto" {
			log.Printf("update %s is available (run: wharfdrop update)", st.Latest)
			continue
		}
		if err := d.InstallUpdate(ctx); err != nil {
			log.Printf("automatic update postponed: %v", err)
			if strings.Contains(err.Error(), "transfer is in progress") {
				wait = retryIfBusy
			}
		}
	}
}
