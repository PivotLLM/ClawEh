// ClawEh
// License: MIT

package config

import (
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// AlwaysReadableWorkspaceDirs are the workspace folders an agent may always
// read when workspace_read_subdirs limits its reads: tasks/ (sub-agent
// results), tmp/ (inbound attachments), the forums folder (its forums'
// results) and the classifiers folder (its classifier definitions). Writes
// stay confined to the write area.
var AlwaysReadableWorkspaceDirs = []string{"tasks", "tmp", forumfs.BaseDirName, classifierfs.BaseDirName}
