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
	text     string // the memory's text, so the document can say what it belongs to
	ref      string
	pending  bool // memory is unconfirmed: name the document, do not load it
}

// collectRef appends a memory's file reference to sites when it has one. The
// memory line itself carries no marker — a filename sitting next to a memory,
// far above the text it names, reads as a citation rather than an inclusion.
// The path appears once, in the attachments section, attached to its content.
func collectRef(sites []refSite, m memoryRef) []refSite {
	if ref := strings.TrimSpace(m.ref); ref != "" {
		m.ref = ref
		sites = append(sites, refSite(m))
	}
	return sites
}

// memoryRef is the collectRef argument set, named so call sites read clearly.
type memoryRef struct {
	memoryID string
	text     string
	ref      string
	pending  bool
}

// memoryHeadline shortens a memory's text to a single quotable line for the
// document header.
func memoryHeadline(s string) string {
	s = oneLine(s)
	if len(s) > maxHeadlineChars {
		s = strings.TrimSpace(s[:maxHeadlineChars]) + "…"
	}
	return s
}

// provenance names the memory (or memories) a document belongs to. With a
// single owner it quotes the memory text, which is what actually answers "why
// am I being shown this document?" — an opaque id alone does not.
func provenance(owners []refSite) string {
	if len(owners) == 1 {
		if t := memoryHeadline(owners[0].text); t != "" {
			return fmt.Sprintf("From memory %s (%q)", owners[0].memoryID, t)
		}
		return "From memory " + owners[0].memoryID
	}
	ids := make([]string, 0, len(owners))
	for _, o := range owners {
		ids = append(ids, o.memoryID)
	}
	return "From memories " + strings.Join(ids, ", ")
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

	// Dedup by path, keeping first-referenced order and merging every memory that
	// points at it (one shared doc, one copy in the prompt). A document is only
	// withheld as pending when every memory naming it is unconfirmed — one
	// confirmed owner is reason enough to load it.
	order := make([]string, 0, len(sites))
	byRef := make(map[string][]refSite, len(sites))
	for _, s := range sites {
		if _, seen := byRef[s.ref]; !seen {
			order = append(order, s.ref)
		}
		byRef[s.ref] = append(byRef[s.ref], s)
	}

	remaining := c.opt.fileTotalMaxBytes
	var b strings.Builder
	for _, ref := range order {
		owners := byRef[ref]
		sort.SliceStable(owners, func(i, j int) bool { return owners[i].memoryID < owners[j].memoryID })
		pending := true
		for _, o := range owners {
			if !o.pending {
				pending = false
				break
			}
		}

		fmt.Fprintf(&b, "### Attached: %s\n", ref)

		// An unconfirmed memory names its document but does not spend context on
		// it: one line saying so, and nothing else.
		if pending {
			fmt.Fprintf(&b, "%s — pending confirmation, so its contents are not loaded. Confirm the memory to include this document.\n\n", provenance(owners))
			continue
		}

		if remaining <= 0 {
			fmt.Fprintf(&b, "%s — not included: the per-turn attachment budget (%d bytes) is exhausted. Read the file directly if you need it.\n\n",
				provenance(owners), c.opt.fileTotalMaxBytes)
			continue
		}
		limit := c.opt.fileMaxBytes
		if limit > remaining {
			limit = remaining
		}

		att, err := c.opt.loadAttachment(ref, limit)
		if err != nil {
			fmt.Fprintf(&b, "%s — not included: %s\n\n", provenance(owners), oneLine(err.Error()))
			continue
		}
		remaining -= len(att.Content)

		fmt.Fprintf(&b, "%s, %d bytes, current as of this turn.\n\n", provenance(owners), att.Size)
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
