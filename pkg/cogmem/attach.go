// ClawEh - Cognitive Memory
// License: MIT

package cogmem

import (
	"fmt"
	"sort"
	"strings"
)

// Attachment is one markdown file pointed at by a memory's FileRef, resolved for
// injection into the prompt. Loading is the caller's job (see AttachmentLoader):
// cogmem never touches the filesystem itself, so the permission check lives with
// the code that owns the agent's file policy.
type Attachment struct {
	Ref       string // path as stored on the memory, e.g. "files/voice.md"
	Content   string // file contents, already capped at the requested limit
	Size      int64  // full size on disk, in bytes (0 when unknown)
	Truncated bool   // Content is a prefix: the file is longer than the cap
}

// AttachmentLoader resolves a memory's file reference into its contents, reading
// at most maxBytes. Implementations MUST enforce the agent's read permissions
// and reject anything the agent may not read; an error is rendered into the
// prompt as an unavailable-attachment note, so the model is never left believing
// it saw a document it did not.
type AttachmentLoader func(ref string, maxBytes int) (Attachment, error)

// WithAttachmentLoader installs the loader used to resolve memory file pointers.
// Without one, file references are rendered as plain markers and no file content
// is injected.
func WithAttachmentLoader(fn AttachmentLoader) Option {
	return func(o *options) { o.loadAttachment = fn }
}

// WithFileMaxBytes caps a single attachment.
func WithFileMaxBytes(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.fileMaxBytes = n
		}
	}
}

// WithFileTotalMaxBytes caps all attachments injected in one turn.
func WithFileTotalMaxBytes(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.fileTotalMaxBytes = n
		}
	}
}

// refSite records that a rendered memory carries a file reference, so the
// attachments block can be assembled after both blocks are composed.
type refSite struct {
	memoryID string
	ref      string
}

// collectRef appends a memory's file reference to sites when it has one.
func collectRef(sites []refSite, memoryID, ref string) []refSite {
	if ref = strings.TrimSpace(ref); ref == "" {
		return sites
	}
	return append(sites, refSite{memoryID: memoryID, ref: ref})
}

// attachmentMarker is the inline tag appended to a memory line that carries a
// file pointer, telling the model the document is present and under which name.
func attachmentMarker(ref string) string {
	if ref = strings.TrimSpace(ref); ref == "" {
		return ""
	}
	return " [attached document: " + ref + "]"
}

// attachmentsBlock renders the full contents of every distinct file referenced
// by the memories in this turn's blocks. Files are loaded in the order they were
// referenced (sticky memories first, then routed), deduped by path, each capped
// at fileMaxBytes and collectively at fileTotalMaxBytes.
//
// Anything not injected is stated explicitly — truncated, unreadable, or budget-
// dropped — so the model knows to reach for the file tools instead of assuming
// it has the whole document.
func (c *Composer) attachmentsBlock(sites []refSite) string {
	if len(sites) == 0 || c.opt.loadAttachment == nil {
		return ""
	}

	// Dedup by path, keeping first-referenced order and collecting every memory
	// id that points at it (one shared doc, one copy in the prompt).
	order := make([]string, 0, len(sites))
	byRef := make(map[string][]string, len(sites))
	for _, s := range sites {
		if _, seen := byRef[s.ref]; !seen {
			order = append(order, s.ref)
		}
		byRef[s.ref] = append(byRef[s.ref], s.memoryID)
	}

	remaining := c.opt.fileTotalMaxBytes
	var b strings.Builder
	for _, ref := range order {
		ids := byRef[ref]
		sort.Strings(ids)
		owner := "memory " + strings.Join(ids, ", ")

		if remaining <= 0 {
			fmt.Fprintf(&b, "## %s (%s)\n\nNot included: the per-turn attachment budget (%d bytes) is exhausted. Read the file directly if you need it.\n\n",
				ref, owner, c.opt.fileTotalMaxBytes)
			continue
		}
		limit := c.opt.fileMaxBytes
		if limit > remaining {
			limit = remaining
		}

		att, err := c.opt.loadAttachment(ref, limit)
		if err != nil {
			fmt.Fprintf(&b, "## %s (%s)\n\nNot included: %s\n\n", ref, owner, oneLine(err.Error()))
			continue
		}
		remaining -= len(att.Content)

		fmt.Fprintf(&b, "## %s (%s", ref, owner)
		if att.Size > 0 {
			fmt.Fprintf(&b, ", %d bytes", att.Size)
		}
		b.WriteString(")\n\n")
		if att.Truncated {
			fmt.Fprintf(&b, "[TRUNCATED: showing the first %d of %d bytes. The rest of this document is NOT below — read the file directly if you need it.]\n\n",
				len(att.Content), att.Size)
		}
		b.WriteString(strings.TrimRight(att.Content, "\n"))
		b.WriteString("\n\n")
	}

	out := strings.TrimRight(b.String(), "\n")
	if out == "" {
		return ""
	}
	return "# Attached Documents\n\n" +
		"Full contents of files attached to memories currently in context, as of this turn. " +
		"Treat them as authoritative reference material for the memory that names them.\n\n" + out
}
