// ClawEh
// License: MIT

package global

import "time"

// Background-task supervisor tuning. These govern recovery of interrupted
// callback tasks (those with a leftover .run marker and no live worker). Kept as
// plain constants for easy tuning; promote to config later if needed.
const (
	// TaskMaxRestarts is how many times an interrupted task may be relaunched
	// before the supervisor gives up and marks it errored.
	TaskMaxRestarts = 3

	// TaskRetryDelay is the minimum cooldown before a crashed/interrupted task is
	// eligible for relaunch. Written into each task's RetryAfter at launch so a
	// start-crash-start-crash loop cannot burn all attempts instantly.
	TaskRetryDelay = 5 * time.Minute

	// TaskSupervisorInterval is how often the supervisor scans for interrupted
	// tasks to relaunch.
	TaskSupervisorInterval = 1 * time.Minute
)
