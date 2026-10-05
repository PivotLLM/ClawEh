// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

// Seam (c): JSON Pointer (RFC 6901) support for `share` and `paths` (rev 3
// §4 projection rules). The standard library has no pointer implementation
// and no dependency is added for one.

// ValidPointer reports whether p is a syntactically valid JSON Pointer:
// empty (the whole document) or a sequence of "/"-prefixed tokens in which
// "~" appears only as "~0" or "~1".
func ValidPointer(p string) bool {
	return false
}

// CheckProjection validates a `share` or `paths` allowlist statically (rev
// 3 §4): every pointer is valid and nonempty (the root cannot be a
// projection member), and no pointer is a prefix of another (overlap). It
// returns one message per offending pointer; an empty list is valid. Used
// by ValidateStatic. Array traversal cannot be told from an object member
// named by digits until a document is present, so Project checks it.
func CheckProjection(pointers []string) []string {
	return nil
}

// Resolve returns the value p points to within doc (a value produced by
// encoding/json into any). found is false when the path does not exist;
// err reports an invalid pointer or a token applied to an array (array
// traversal is not allowed in projections).
func Resolve(doc any, p string) (value any, found bool, err error) {
	return nil, false, errNotImplemented
}

// Project builds the projection of the JSON document doc: a fresh object
// holding only the object members the pointers select, with the enclosing
// object structure kept (a pointer "/a/b" yields {"a":{"b":...}}); a
// selected member may contain whole arrays or objects. An empty pointer
// list yields {}. A pointer that resolves to nothing, or that traverses
// an array, is an error (rev 3 §4: "missing share paths fail output
// validation", "invalid/missing paths fail"). The result is canonical
// encoding/json output.
func Project(doc []byte, pointers []string) ([]byte, error) {
	return nil, errNotImplemented
}
