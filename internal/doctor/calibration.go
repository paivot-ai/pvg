package doctor

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/paivot-ai/pvg/internal/settings"
)

// timeNow is a seam for tests to pin the staleness age computation.
var timeNow = time.Now

// checkCalibrationStaleness reports whether tuned gates.*/model.* settings
// still match the toolchain they were calibrated against. Thresholds and
// model overrides are tuned under a specific pvg version; when the toolchain
// changes the calibration silently rots. `pvg settings` stamps
// calibration.stamped/calibration.pvg on every gates.*/model.* change; this
// check compares the stamp against the running version.
//
// Advisory by design: never StatusFail and not Fixable. A blocking check
// would punish upgrades; a silent stamp would hide year-old calibration.
func checkCalibrationStaleness(projectRoot string) Finding {
	const name = "calibration-staleness"
	sett := settings.LoadFile(filepath.Join(projectRoot, ".vault", "knowledge", ".settings.yaml"))

	// An override is any gates.* key, or a model.* key with a non-empty
	// value (an empty model.* line means no override). calibration.* keys
	// never count.
	overrides := 0
	for k, v := range sett {
		if strings.HasPrefix(k, "gates.") || (strings.HasPrefix(k, "model.") && v != "") {
			overrides++
		}
	}
	if overrides == 0 {
		return Finding{Name: name, Status: StatusPass, Message: "no gates.*/model.* overrides; built-in defaults in effect"}
	}

	stamped := sett["calibration.stamped"]
	if stamped == "" {
		return Finding{
			Name:    name,
			Status:  StatusWarn,
			Message: fmt.Sprintf("%d gates.*/model.* override(s) present but never stamped; re-apply one via `pvg settings <key>=<value>` to stamp calibration", overrides),
		}
	}

	stampDate, err := time.Parse("2006-01-02", stamped)
	if err != nil {
		return Finding{
			Name:    name,
			Status:  StatusWarn,
			Message: fmt.Sprintf("malformed calibration.stamped %q (expected YYYY-MM-DD); re-apply a gates.*/model.* setting via `pvg settings` to restamp", stamped),
		}
	}

	current := settings.RunningVersion()
	if stampedVersion := sett["calibration.pvg"]; stampedVersion != current {
		days := int(timeNow().Sub(stampDate).Hours() / 24)
		return Finding{
			Name:    name,
			Status:  StatusWarn,
			Message: fmt.Sprintf("settings calibrated %s (%d day(s) ago) under pvg %s; running %s; re-review gates.*/model.* settings", stamped, days, stampedVersion, current),
		}
	}
	return Finding{Name: name, Status: StatusPass, Message: fmt.Sprintf("gates/model calibration stamped %s under current pvg %s", stamped, current)}
}
