// ClawEh
// License: MIT

package utils

import "testing"

// Telegram voice notes arrive as .oga (Ogg Opus); they must count as audio so
// transcription runs on them.
func TestIsAudioFile(t *testing.T) {
	for _, name := range []string{"file_3.oga", "clip.opus", "song.mp3", "VOICE.OGG"} {
		if !IsAudioFile(name, "") {
			t.Errorf("%s not recognised as audio", name)
		}
	}
	for _, name := range []string{"photo.jpg", "notes.txt", "file_9"} {
		if IsAudioFile(name, "") {
			t.Errorf("%s recognised as audio", name)
		}
	}
	if !IsAudioFile("blob", "audio/ogg") || !IsAudioFile("blob", "application/ogg") {
		t.Error("audio content type not recognised")
	}
}
