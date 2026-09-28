// ClawEh
// License: MIT

package telegram

import "testing"

// Telegram's file path already carries the real extension; the type-based
// extension is only a fallback. Appending it blindly produced file_3.oga.ogg
// and file_1.jpg.jpg in the media directory.
func TestLocalFilename(t *testing.T) {
	cases := []struct{ path, ext, want string }{
		{"voice/file_3.oga", ".ogg", "voice/file_3.oga"},
		{"photos/file_1.jpg", ".jpg", "photos/file_1.jpg"},
		{"music/file_8.mp3", ".mp3", "music/file_8.mp3"},
		{"documents/file_2.txt", "", "documents/file_2.txt"},
		{"voice/file_9", ".ogg", "voice/file_9.ogg"},
		{"documents/file_4", "", "documents/file_4"},
	}
	for _, c := range cases {
		if got := localFilename(c.path, c.ext); got != c.want {
			t.Errorf("localFilename(%q, %q) = %q, want %q", c.path, c.ext, got, c.want)
		}
	}
}
