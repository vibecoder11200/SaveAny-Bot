package tfile

import (
	"testing"

	"github.com/gotd/td/tg"
)

func photoMedia(ref []byte) *tg.MessageMediaPhoto {
	return &tg.MessageMediaPhoto{
		Photo: &tg.Photo{
			ID:            7,
			AccessHash:    9,
			FileReference: ref,
			Sizes: []tg.PhotoSizeClass{
				&tg.PhotoSize{Type: "x", W: 1, H: 1, Size: 10},
			},
		},
	}
}

func documentMedia(ref []byte) *tg.MessageMediaDocument {
	return &tg.MessageMediaDocument{
		Document: &tg.Document{
			ID:            1,
			AccessHash:    2,
			FileReference: ref,
			Size:          123,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: "a.mp4"},
			},
		},
	}
}

func TestRefreshLocationSwapsReferenceKeepsIdentity(t *testing.T) {
	msg := &tg.Message{ID: 42}
	client := &tg.Client{}

	file, err := FromMediaMessage(photoMedia([]byte("old-ref")), client, msg, WithName("keep.jpg"))
	if err != nil {
		t.Fatalf("FromMediaMessage: %v", err)
	}
	if err := file.RefreshLocation(photoMedia([]byte("new-ref"))); err != nil {
		t.Fatalf("RefreshLocation: %v", err)
	}
	loc, ok := file.Location().(*tg.InputPhotoFileLocation)
	if !ok {
		t.Fatalf("location type: %T", file.Location())
	}
	if string(loc.FileReference) != "new-ref" {
		t.Fatalf("file reference = %q, want %q", loc.FileReference, "new-ref")
	}
	if file.Name() != "keep.jpg" {
		t.Fatalf("name = %q, want keep.jpg", file.Name())
	}
	if file.Message() != msg {
		t.Fatal("message pointer changed")
	}
	if file.Dler() != client {
		t.Fatal("dler changed")
	}
}

func TestRefreshLocationDocument(t *testing.T) {
	file, err := FromMedia(documentMedia([]byte("old-ref")), &tg.Client{})
	if err != nil {
		t.Fatalf("FromMedia: %v", err)
	}
	if file.Name() != "a.mp4" {
		t.Fatalf("name from filename attribute = %q, want a.mp4", file.Name())
	}
	msgFile, ok := file.(TGFileMessage)
	if !ok {
		t.Fatal("plain file does not implement TGFileMessage")
	}
	if err := msgFile.RefreshLocation(documentMedia([]byte("fresh"))); err != nil {
		t.Fatalf("RefreshLocation: %v", err)
	}
	loc, ok := file.Location().(*tg.InputDocumentFileLocation)
	if !ok {
		t.Fatalf("location type: %T", file.Location())
	}
	if string(loc.FileReference) != "fresh" {
		t.Fatalf("file reference = %q, want fresh", loc.FileReference)
	}
	if file.Size() != 123 {
		t.Fatalf("size = %d, want 123", file.Size())
	}
}

func TestRefreshLocationUnsupportedMedia(t *testing.T) {
	file, err := FromMediaMessage(photoMedia([]byte("old")), &tg.Client{}, &tg.Message{ID: 1})
	if err != nil {
		t.Fatalf("FromMediaMessage: %v", err)
	}
	if err := file.RefreshLocation(&tg.MessageMediaContact{}); err == nil {
		t.Fatal("expected error for unsupported media, got nil")
	}
}
