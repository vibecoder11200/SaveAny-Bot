package tfile

import (
	"errors"
	"fmt"

	"github.com/celestix/gotgproto/functions"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
)

type TGFile interface {
	Location() tg.InputFileLocationClass
	Dler() downloader.Client // witch client to use for downloading
	Size() int64
	Name() string
	SetName(name string)
}

type TGFileMessage interface {
	TGFile
	Message() *tg.Message
	RefreshLocation(media tg.MessageMediaClass) error
}

type tgFile struct {
	location tg.InputFileLocationClass
	size     int64
	name     string
	message  *tg.Message
	dler     downloader.Client
}

func (f *tgFile) SetName(name string) {
	f.name = SanitizeFileName(name)
}

func (f *tgFile) Location() tg.InputFileLocationClass {
	return f.location
}

func (f *tgFile) Size() int64 {
	return f.size
}

func (f *tgFile) Name() string {
	return f.name
}

func (f *tgFile) Message() *tg.Message {
	return f.message
}

// RefreshLocation swaps in the location built from fresh media — most
// importantly a fresh file_reference — keeping the name, size, message and
// download client untouched, so a download can restart in place after
// Telegram rejected the old reference.
func (f *tgFile) RefreshLocation(media tg.MessageMediaClass) error {
	location, err := locationFromMedia(media)
	if err != nil {
		return err
	}
	f.location = location
	return nil
}

func (f *tgFile) Dler() downloader.Client {
	return f.dler
}

func NewTGFile(
	location tg.InputFileLocationClass,
	dler downloader.Client,
	size int64,
	name string,
	opts ...TGFileOption,
) TGFile {
	f := &tgFile{
		location: location,
		dler:     dler,
		size:     size,
		name:     SanitizeFileName(name),
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// locationFromMedia builds a download location carrying the media's current
// file_reference. It is the single place that turns media into a location, so
// both the initial build and a refresh produce identical locations.
func locationFromMedia(media tg.MessageMediaClass) (tg.InputFileLocationClass, error) {
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		document, ok := m.Document.AsNotEmpty()
		if !ok {
			return nil, errors.New("document is empty")
		}
		return document.AsInputDocumentFileLocation(), nil
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.AsNotEmpty()
		if !ok {
			return nil, errors.New("photo is empty")
		}
		if len(photo.Sizes) == 0 {
			return nil, errors.New("photo sizes are empty")
		}
		photoSize := photo.Sizes[len(photo.Sizes)-1]
		size, ok := photoSize.AsNotEmpty()
		if !ok {
			return nil, errors.New("photo size is empty")
		}
		location := new(tg.InputPhotoFileLocation)
		location.ID = photo.GetID()
		location.AccessHash = photo.GetAccessHash()
		location.FileReference = photo.GetFileReference()
		location.ThumbSize = size.GetType()
		return location, nil
	}
	return nil, fmt.Errorf("unsupported media type: %T", media)
}

func FromMedia(media tg.MessageMediaClass, client downloader.Client, opts ...TGFileOption) (TGFile, error) {
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		location, err := locationFromMedia(m)
		if err != nil {
			return nil, err
		}
		document, ok := m.Document.AsNotEmpty()
		if !ok {
			return nil, errors.New("document is empty")
		}
		fileName := ""
		for _, attribute := range document.Attributes {
			if name, ok := attribute.(*tg.DocumentAttributeFilename); ok {
				fileName = name.GetFileName()
				break
			}
		}
		file := NewTGFile(
			location,
			client,
			document.Size,
			fileName,
			opts...,
		)
		return file, nil
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.AsNotEmpty()
		if !ok {
			return nil, errors.New("photo is empty")
		}
		location, err := locationFromMedia(m)
		if err != nil {
			return nil, err
		}
		fileName, err := functions.GetMediaFileName(m)
		if err != nil {
			fileName = fmt.Sprintf("photo_%d.png", photo.GetID())
		}
		file := NewTGFile(
			location,
			client,
			0, // Photo size is not available in InputPhotoFileLocation
			fileName,
			opts...,
		)
		return file, nil
	}
	return nil, fmt.Errorf("unsupported media type: %T", media)
}

func FromMediaMessage(media tg.MessageMediaClass, client downloader.Client, msg *tg.Message, opts ...TGFileOption) (TGFileMessage, error) {
	file, err := FromMedia(media, client, opts...)
	if err != nil {
		return nil, err
	}
	return &tgFile{
		location: file.Location(),
		dler:     file.Dler(),
		size:     file.Size(),
		name:     file.Name(),
		message:  msg,
	}, nil
}
