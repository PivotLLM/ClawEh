// ClawEh
// License: MIT

package audit

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/utils"
)

// deviceStoreTimeout bounds the read of the pairing database so a locked
// file cannot stall the report.
const deviceStoreTimeout = 5 * time.Second

func msTime(ms int64) string {
	if ms == 0 {
		return unknown
	}
	return time.UnixMilli(ms).Format(timeFormat)
}

// deviceRows reads the paired and pending devices from the gateway pairing
// database. The database is opened only when it already exists, so a fresh
// install is reported as unavailable rather than created by the audit.
func deviceRows(dd string) (paired, pending [][]string) {
	path := filepath.Join(dd, "state", "gateway.db")
	if _, err := os.Stat(path); err != nil {
		msg := "unavailable: no device store at " + path
		if !os.IsNotExist(err) {
			msg = "unavailable: " + err.Error()
		}
		return [][]string{row(msg, "", "", "")}, [][]string{row(msg, "", "", "")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), deviceStoreTimeout)
	defer cancel()
	store, err := device.OpenStore(ctx, path)
	if err != nil {
		msg := "unavailable: " + err.Error()
		return [][]string{row(msg, "", "", "")}, [][]string{row(msg, "", "", "")}
	}
	defer utils.CloseQuietly(store)

	if list, err := store.ListPaired(ctx); err != nil {
		paired = append(paired, row("unavailable: "+err.Error(), "", "", ""))
	} else {
		for _, d := range list {
			paired = append(paired, row(orValue(d.DisplayName, unknown)+" ("+orValue(d.Platform, unknown)+")",
				d.DeviceID, orValue(d.AgentID, "(gateway default)"), msTime(d.ApprovedAtMs)))
		}
		if len(paired) == 0 {
			paired = append(paired, row(none, "", "", ""))
		}
	}
	if list, err := store.ListPending(ctx); err != nil {
		pending = append(pending, row("unavailable: "+err.Error(), "", "", ""))
	} else {
		for _, p := range list {
			pending = append(pending, row(orValue(p.DisplayName, unknown)+" ("+orValue(p.Platform, unknown)+")",
				p.DeviceID, orValue(p.RemoteIP, unknown), msTime(p.CreatedAtMs)))
		}
		if len(pending) == 0 {
			pending = append(pending, row(none, "", "", ""))
		}
	}
	return paired, pending
}

func collectDevices(cfg *config.Config, env Environment) Section {
	paired, pending := deviceRows(dataDir(cfg, env))
	dev := cfg.Channels.Device
	return Section{
		Title: "Devices",
		Notes: []string{"Device tokens are never listed. Paired devices come from the pairing database under the data directory."},
		Tables: []Table{
			pairs("Settings",
				row("USB device monitor (devices.enabled)", onOff(cfg.Devices.Enabled)),
				row("Monitor USB hot-plug", onOff(cfg.Devices.MonitorUSB)),
				row("Device gateway (channels.device)", onOff(dev.Enabled)),
				row("Auto-approve pairings", onOff(dev.AutoApprove)),
			),
			{Caption: "Paired devices", Columns: []string{"Device", "Device ID", "Agent", "Approved"}, Rows: paired},
			{Caption: "Pending pairings", Columns: []string{"Device", "Device ID", "From", "Requested"}, Rows: pending},
		},
	}
}
