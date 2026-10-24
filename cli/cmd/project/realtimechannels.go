// realtimechannels.go — `realtime { client_channels }`, which Realtime channels
// a visitor's browser may publish to.
//
// A frame a client sends on a channel the list does not name is dropped by the
// slice, so a visitor cannot speak to the other visitors on a channel the owner
// never opened. A Pulse publishes whatever this says: the list governs clients
// only.
//
// The list is the whole truth, as `edge.allow` is: present, it replaces the
// slice's set, and an empty one closes every channel. Unlike `edge.allow`, a
// file with no block is also a declaration of nothing open, because a channel
// the file no longer names must not stay open on the strength of an earlier
// apply. That write is made only when the slice reports something open, so a
// slice that holds nothing, or whose runtime predates the route, is left alone.
//
// The route is the slice's own, reached through the api's Backbone passthrough,
// which attaches the slice's credential; the slice refuses the call without it.
package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ondrift/cloud/cli/common"
)

// clientChannelsRoute is the api path the slice's client-channel route is
// forwarded on.
const clientChannelsRoute = "/ops/backbone/realtime/channels"

// declaredClientChannels reads `backbone.realtime.client_channels`: whether the
// file declares the list at all, and the list as written.
func declaredClientChannels(m *Manifest) (bool, []string, error) {
	if !m.Slice().Has("backbone", "realtime") {
		return false, nil, nil
	}
	raw, ok := m.Slice().Get("backbone", "realtime", "client_channels").([]any)
	if !ok {
		return false, nil, fmt.Errorf(`realtime.client_channels must be a list of channel names, such as ["chat", "room:*"]`)
	}
	channels := make([]string, 0, len(raw))
	for i, entry := range raw {
		s, ok := entry.(string)
		if !ok {
			return false, nil, fmt.Errorf("realtime.client_channels[%d]: %v is not a channel name", i, entry)
		}
		channels = append(channels, s)
	}
	return true, channels, nil
}

// preflightClientChannels runs before the first deploy step, so a list that
// cannot be sent refuses the file before any of it is applied. The grammar of
// each name is the schema's, checked when the file compiles.
func preflightClientChannels(m *Manifest) error {
	_, _, err := declaredClientChannels(m)
	return err
}

// liveClientChannels is what the slice's route reports.
type liveClientChannels struct {
	Declared bool     `json:"declared"`
	Channels []string `json:"channels"`
}

// fetchClientChannels reads the channels a slice's clients may publish to,
// sorted. The slice is named on the request, because `drift plan` reads before
// an apply makes this file's slice the active one.
func fetchClientChannels(m *Manifest) ([]string, error) {
	resp, err := common.DoRequestWithHeaders(http.MethodGet, common.APIBaseURL+clientChannelsRoute, nil,
		map[string]string{"X-Slice": m.Name()})
	if err != nil {
		return nil, common.TransportError("read the client channels", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "read the client channels")
	if err != nil {
		return nil, err
	}
	var live liveClientChannels
	if err := json.Unmarshal(body, &live); err != nil {
		return nil, fmt.Errorf("read the client channels: the platform sent a reply this CLI cannot read")
	}
	sort.Strings(live.Channels)
	return live.Channels, nil
}

// setClientChannels replaces the slice's set.
func setClientChannels(m *Manifest, channels []string) error {
	raw, err := json.Marshal(map[string][]string{"channels": channels})
	if err != nil {
		return err
	}
	resp, err := common.DoRequestWithHeaders(http.MethodPost, common.APIBaseURL+clientChannelsRoute, bytes.NewReader(raw),
		map[string]string{"X-Slice": m.Name(), "Content-Type": "application/json"})
	if err != nil {
		return common.TransportError("set the client channels", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "set the client channels")
	return err
}

// applyClientChannels makes the slice's client channels the file's.
func applyClientChannels(m *Manifest, out io.Writer) error {
	declared, channels, err := declaredClientChannels(m)
	if err != nil {
		return err
	}
	if !declared {
		open, rerr := fetchClientChannels(m)
		if rerr != nil || len(open) == 0 {
			return nil
		}
		if err := setClientChannels(m, []string{}); err != nil {
			return fmt.Errorf("realtime.client_channels: %w", err)
		}
		fmt.Fprintf(out, "    %s Realtime: client publishing closed on %s\n", common.Check(), strings.Join(open, ", "))
		return nil
	}
	if err := setClientChannels(m, channels); err != nil {
		return fmt.Errorf("realtime.client_channels: %w", err)
	}
	if len(channels) == 0 {
		fmt.Fprintf(out, "    %s Realtime: client publishing closed on every channel\n", common.Check())
		return nil
	}
	fmt.Fprintf(out, "    %s Realtime: clients may publish to %s\n", common.Check(), strings.Join(channels, ", "))
	return nil
}

// diffClientChannels adds the plan's rows for the client channels. A channel
// the file stops naming is shown closed, and needs no answer: nothing is lost
// by closing it.
func (p *planState) diffClientChannels(d *planDiff, creating bool) {
	declared, want, err := declaredClientChannels(p.c.Manifest)
	if err != nil {
		d.notes = append(d.notes, err.Error())
		return
	}
	switch {
	case creating && !declared:
		return
	case !creating && p.channelsErr != nil:
		// Said when the file declares the list. A file that does not is not
		// asking anything of a slice that cannot report.
		if declared {
			d.add(" ", "realtime", "live client channels unknown (%v)", p.channelsErr)
		}
		return
	}
	have := map[string]bool{}
	if !creating {
		for _, h := range p.channels {
			have[h] = true
		}
	}
	wantSet := map[string]bool{}
	sort.Strings(want)
	for _, w := range want {
		wantSet[w] = true
		if have[w] {
			d.add(" ", "realtime "+w, "clients may publish (unchanged)")
		} else {
			d.add("+", "realtime "+w, "clients may publish")
		}
	}
	var closing []string
	for h := range have {
		if !wantSet[h] {
			closing = append(closing, h)
		}
	}
	sort.Strings(closing)
	for _, h := range closing {
		d.add("-", "realtime "+h, "closed to client publishing")
	}
}
