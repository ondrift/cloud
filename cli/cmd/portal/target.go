package portal

// target.go — which slice the dashboard acts on.
//
// Nothing is stored. The dashboard's choice is this process's DRIFT_SLICE, which
// every command it starts for you inherits, and it ends when the dashboard does.

import (
	"os"

	"github.com/ondrift/cloud/cli/common"
)

// selectSlice makes name the slice the dashboard acts on.
func (m *model) selectSlice(name string) error {
	if err := os.Setenv(common.SliceEnv, name); err != nil {
		return err
	}
	m.active = name
	return nil
}

// adoptSliceOf selects the slice the Driftfile in dir declares, after a command
// created or changed it, so the dashboard follows what was just made.
func (m *model) adoptSliceOf(dir string) {
	if common.DriftfileSlice != nil {
		if name, _, found, err := common.DriftfileSlice(dir); found && err == nil {
			_ = m.selectSlice(name)
		}
	}
	m.loadSlices()
}
