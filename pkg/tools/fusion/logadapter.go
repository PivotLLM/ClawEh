// ClawEh
// License: MIT

package fusion

import (
	"fmt"

	"github.com/tenebris-tech/mlogger"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// logComponent tags Fusion-originated lines in the unified ClawEh log.
const logComponent = "fusion"

// fusionLogAdapter bridges MCPFusion's mlogger.Logger interface onto ClawEh's
// central logger, so the embedded engine's events land in the main ClawEh log
// (component "fusion") rather than a separate fusion.log file.
//
// Two deliberate level mappings: ClawEh has no dedicated Notice level, so
// Notice* logs at Info; and Fatal*/FatalExit log at Error and NEVER exit — the
// engine is embedded, so a library-level "fatal" must not tear down the whole
// ClawEh process (Fusion emits Fatal for ordinary request failures).
type fusionLogAdapter struct{}

// Compile-time proof the adapter satisfies the interface WithLogger expects
// (global.Logger is a type alias for mlogger.Logger).
var _ mlogger.Logger = fusionLogAdapter{}

func newFusionLogAdapter() fusionLogAdapter { return fusionLogAdapter{} }

func (fusionLogAdapter) Debug(m string)   { logger.DebugCF(logComponent, m, nil) }
func (fusionLogAdapter) Info(m string)    { logger.InfoCF(logComponent, m, nil) }
func (fusionLogAdapter) Notice(m string)  { logger.InfoCF(logComponent, m, nil) }
func (fusionLogAdapter) Warning(m string) { logger.WarnCF(logComponent, m, nil) }
func (fusionLogAdapter) Error(m string)   { logger.ErrorCF(logComponent, m, nil) }
func (fusionLogAdapter) Fatal(m string)   { logger.ErrorCF(logComponent, m, nil) }

func (fusionLogAdapter) Debugf(f string, a ...any) {
	logger.DebugCF(logComponent, fmt.Sprintf(f, a...), nil)
}
func (fusionLogAdapter) Infof(f string, a ...any) {
	logger.InfoCF(logComponent, fmt.Sprintf(f, a...), nil)
}
func (fusionLogAdapter) Noticef(f string, a ...any) {
	logger.InfoCF(logComponent, fmt.Sprintf(f, a...), nil)
}
func (fusionLogAdapter) Warningf(f string, a ...any) {
	logger.WarnCF(logComponent, fmt.Sprintf(f, a...), nil)
}
func (fusionLogAdapter) Errorf(f string, a ...any) {
	logger.ErrorCF(logComponent, fmt.Sprintf(f, a...), nil)
}
func (fusionLogAdapter) Fatalf(f string, a ...any) {
	logger.ErrorCF(logComponent, fmt.Sprintf(f, a...), nil)
}

func (fusionLogAdapter) DebugFields(a ...any)   { logger.DebugCF(logComponent, "", fieldsToMap(a)) }
func (fusionLogAdapter) InfoFields(a ...any)    { logger.InfoCF(logComponent, "", fieldsToMap(a)) }
func (fusionLogAdapter) NoticeFields(a ...any)  { logger.InfoCF(logComponent, "", fieldsToMap(a)) }
func (fusionLogAdapter) WarningFields(a ...any) { logger.WarnCF(logComponent, "", fieldsToMap(a)) }
func (fusionLogAdapter) ErrorFields(a ...any)   { logger.ErrorCF(logComponent, "", fieldsToMap(a)) }
func (fusionLogAdapter) FatalFields(a ...any)   { logger.ErrorCF(logComponent, "", fieldsToMap(a)) }

// FatalExit must not terminate the process: the engine is embedded in ClawEh.
func (fusionLogAdapter) FatalExit() {
	logger.ErrorCF(logComponent, "fusion requested process exit (suppressed; engine is embedded)", nil)
}

// Close is a no-op: ClawEh owns the underlying logger's lifecycle.
func (fusionLogAdapter) Close() {}

// fieldsToMap converts mlogger's alternating key/value args into a fields map,
// mirroring mlogger.FormatFields: a trailing key with no value gets "MISSING".
func fieldsToMap(args []any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	fields := make(map[string]any, (len(args)+1)/2)
	for i := 0; i < len(args); i += 2 {
		key := fmt.Sprintf("%v", args[i])
		if i+1 < len(args) {
			fields[key] = args[i+1]
		} else {
			fields[key] = "MISSING"
		}
	}
	return fields
}
