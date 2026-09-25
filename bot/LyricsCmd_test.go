package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestLyricsReplacementAttachments(t *testing.T) {
	if got := lyricsReplacementAttachments(nil); got == nil || len(got) != 0 {
		t.Fatalf("text mode attachments = %#v, want explicit empty array", got)
	}
	files := []*discordgo.File{{Name: "lyrics.jpg", Reader: bytes.NewReader([]byte("jpeg"))}}
	attachments := lyricsReplacementAttachments(files)
	if len(attachments) != 1 || attachments[0].ID != "0" || attachments[0].Filename != "lyrics.jpg" {
		t.Fatalf("image mode attachments = %#v", attachments)
	}
	components := buildLyricsComponentsV2("song", "artist", "", "footer", true, true, nil)
	payload, err := json.Marshal(WebhookEditLyricsV2{
		Components:  &components,
		Flags:       discordgo.MessageFlagsIsComponentsV2,
		Attachments: &attachments,
	})
	if err != nil {
		t.Fatal(err)
	}
	var encoded struct {
		Content     *string `json:"content"`
		Attachments []struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(payload, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.Content != nil || len(encoded.Attachments) != 1 || encoded.Attachments[0].ID != "0" || encoded.Attachments[0].Filename != "lyrics.jpg" {
		t.Fatalf("invalid image payload: %s", payload)
	}
}

func TestLyricsMultipartFallbackKeepsImageBytes(t *testing.T) {
	image := []byte{0xff, 0xd8, 0xff, 0xd9}
	files := []*discordgo.File{{Name: "lyrics.jpg", ContentType: "image/jpeg", Reader: bytes.NewReader(image)}}
	attachments := lyricsReplacementAttachments(files)
	components := buildLyricsComponentsV2("song", "artist", "", "footer", true, true, nil)
	payload := WebhookEditLyricsV2{
		Components:  &components,
		Flags:       discordgo.MessageFlagsIsComponentsV2,
		Attachments: &attachments,
	}

	for attempt := 0; attempt < 3; attempt++ {
		if err := rewindLyricsFiles(files); err != nil {
			t.Fatal(err)
		}
		contentType, body, err := discordgo.MultipartBodyWithJSON(payload, files)
		if err != nil {
			t.Fatal(err)
		}
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			t.Fatal(err)
		}
		parts := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		var fileBytes []byte
		var payloadBytes []byte
		for {
			part, err := parts.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			switch part.FormName() {
			case "payload_json":
				payloadBytes = data
			case "files[0]":
				fileBytes = data
			}
		}
		if !bytes.Equal(fileBytes, image) {
			t.Fatalf("attempt %d uploaded %d bytes, want %d", attempt, len(fileBytes), len(image))
		}
		if !strings.Contains(string(payloadBytes), `"attachments":[{"id":"0"`) {
			t.Fatalf("attempt %d omitted new attachment: %s", attempt, payloadBytes)
		}
	}
}

func TestLyricsExistingPanelDoesNotUseNewInteractionWebhook(t *testing.T) {
	inter := &discordgo.Interaction{Token: "new-command-token"}
	if got := lyricsPanelInteraction("existing-message", inter); got != nil {
		t.Fatal("existing panel must be edited by its channel message ID")
	}
	if got := lyricsPanelInteraction("", inter); got != inter {
		t.Fatal("new panel should retain its original interaction webhook")
	}
}
