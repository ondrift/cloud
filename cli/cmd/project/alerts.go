package project

// alerts.go — a function's alerts, planned and applied with the rest of the file.
//
// An alert is a condition the slice watches (a function failing `threshold` times in
// `window`) and a webhook it tells when it holds. The file declares the first;
// the webhook is write-only, read from the environment, and compared with what
// the slice holds without being printed.
//
// The slice keeps its alerts by name, one spec each, and a POST under an existing
// name replaces it. So apply sets what is new or different and deletes what the
// file does not name, and `drift plan` shows that deletion as a removal the
// person is asked about.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// liveAlert is one alert as the slice stores it.
type liveAlert struct {
	Name         string `json:"name"`
	Function     string `json:"function"`
	Trigger      string `json:"trigger"`
	Threshold    int    `json:"threshold"`
	WindowSec    int    `json:"window_seconds"`
	NotifyType   string `json:"notify_type"`
	NotifyTarget string `json:"notify_target"`
}

// fetchLiveAlerts is a variable so a test can answer it without a platform.
var fetchLiveAlerts = readLiveAlerts

func readLiveAlerts() ([]liveAlert, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/alert", nil)
	if err != nil {
		return nil, common.TransportError("list alerts", err)
	}
	defer resp.Body.Close()
	raw, err := common.CheckResponse(resp, "list alerts")
	if err != nil {
		return nil, err
	}
	var alerts []liveAlert
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &alerts); err != nil {
			return nil, fmt.Errorf("list alerts: the platform sent a reply this CLI cannot read")
		}
	}
	return alerts, nil
}

// alertWindow says a window the way the file writes it.
func alertWindow(sec int) string {
	switch {
	case sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%ds", sec)
}

// webhookIsURL is whether an environment value could be a webhook at all. It is
// checked without being shown.
func webhookIsURL(v string) bool {
	return strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://")
}

// alertDiff names what differs between a declared alert and the one the slice
// holds, and never the webhook itself.
func alertDiff(a DeclaredAlert, live liveAlert) []string {
	var changes []string
	if a.Function != live.Function {
		changes = append(changes, fmt.Sprintf("watches %s → %s", live.Function, a.Function))
	}
	if a.On != live.Trigger {
		changes = append(changes, fmt.Sprintf("on %s → %s", live.Trigger, a.On))
	}
	if a.Threshold != live.Threshold {
		changes = append(changes, fmt.Sprintf("threshold %d → %d", live.Threshold, a.Threshold))
	}
	if a.WindowSec != live.WindowSec {
		changes = append(changes, fmt.Sprintf("window %s → %s", alertWindow(live.WindowSec), alertWindow(a.WindowSec)))
	}
	if os.Getenv(a.Env) != live.NotifyTarget {
		changes = append(changes, fmt.Sprintf("webhook changes to $%s", a.Env))
	}
	return changes
}

func (p *planState) diffAlerts(d *planDiff, creating bool) {
	declared := map[string]bool{}
	liveByName := map[string]liveAlert{}
	for _, l := range p.alerts {
		liveByName[l.Name] = l
	}
	for _, a := range p.c.Alerts {
		declared[a.Name] = true
		addr := "alert " + a.Name
		what := fmt.Sprintf("%s fails %d× in %s, tells $%s", a.Function, a.Threshold, alertWindow(a.WindowSec), a.Env)
		switch live, held := liveByName[a.Name]; {
		case !envSet(a.Env):
			d.add("!", addr, "$%s is not set here: apply would refuse", a.Env)
		case !webhookIsURL(os.Getenv(a.Env)):
			d.add("!", addr, "$%s is not an http(s) URL: apply would refuse", a.Env)
		case creating:
			d.add("+", addr, "%s", what)
		case p.alertsErr != nil:
			d.add(" ", addr, "live alerts unknown (%v)", p.alertsErr)
		case !held:
			d.add("+", addr, "%s", what)
		default:
			if changes := alertDiff(a, live); len(changes) > 0 {
				d.add("~", addr, "%s", strings.Join(changes, "; "))
			} else {
				d.add(" ", addr, "unchanged")
			}
		}
	}
	if creating || p.alertsErr != nil {
		return
	}
	names := make([]string, 0, len(liveByName))
	for n := range liveByName {
		if !declared[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		d.add("-", "alert "+n, "deleted: the file does not name it")
		d.removes = append(d.removes, "alert "+n)
	}
}

// applyAlerts sets each alert the file declares where the slice holds none or a
// different one, and deletes the ones it does not name. By the time it runs the
// removals have been agreed to.
func applyAlerts(c *Compiled, p *planState, out io.Writer) error {
	wrote := false
	say := func(format string, args ...any) {
		if !wrote {
			fmt.Fprintf(out, "  %s\n", common.AtomicHeader())
			wrote = true
		}
		fmt.Fprintf(out, "    %s %s\n", common.Check(), fmt.Sprintf(format, args...))
	}

	liveByName := map[string]liveAlert{}
	for _, l := range p.alerts {
		liveByName[l.Name] = l
	}
	declared := map[string]bool{}
	for _, a := range c.Alerts {
		declared[a.Name] = true
		if live, held := liveByName[a.Name]; held && p.alertsErr == nil && len(alertDiff(a, live)) == 0 {
			continue
		}
		body, err := json.Marshal(liveAlert{
			Name: a.Name, Function: a.Function, Trigger: a.On, Threshold: a.Threshold,
			WindowSec: a.WindowSec, NotifyType: "webhook", NotifyTarget: os.Getenv(a.Env),
		})
		if err != nil {
			return err
		}
		resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/atomic/alert", bytes.NewReader(body))
		if err != nil {
			return common.TransportError("set alert "+a.Name, err)
		}
		_, cerr := common.CheckResponse(resp, "set alert "+a.Name)
		resp.Body.Close() // #nosec G104 -- the body has been read in full
		if cerr != nil {
			return cerr
		}
		say("alert %s set on %s", a.Name, a.Function)
	}

	if p.alertsErr != nil {
		return nil // what the slice holds is unknown, so nothing is deleted on a guess
	}
	var gone []string
	for n := range liveByName {
		if !declared[n] {
			gone = append(gone, n)
		}
	}
	sort.Strings(gone)
	for _, n := range gone {
		resp, err := common.DoRequest(http.MethodDelete, common.APIBaseURL+"/ops/atomic/alert?name="+url.QueryEscape(n), nil)
		if err != nil {
			return common.TransportError("delete alert "+n, err)
		}
		_, cerr := common.CheckResponse(resp, "delete alert "+n)
		resp.Body.Close() // #nosec G104 -- the body has been read in full
		if cerr != nil {
			return cerr
		}
		say("alert %s deleted", n)
	}
	return nil
}
