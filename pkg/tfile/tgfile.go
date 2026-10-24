package tfile

import (
	"errors"
	"fmt"
	"strconv"
	"sync"

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
	// FileUniqueID identifies the source Telegram media across forwards and
	// renames ("doc:<id>" / "photo:<id>"), playing the role of the Bot API's
	// file_unique_id. Empty for files built without Telegram media behind
	// them (e.g. constructed directly from a reader).
	FileUniqueID() string
}

type TGFileMessage interface {
	TGFile
	Message() *tg.Message
	RefreshLocation(media tg.MessageMediaClass) error
}

type tgFile struct {
	location     tg.InputFileLocationClass
	size         int64
	name         string
	message      *tg.Message
	dler         downloader.Client
	fileUniqueID string

	// locationMu guards location: a resumable download refreshes the file
	// reference while sibling chunk goroutines read it concurrently.
	locationMu sync.RWMutex
}

func (f *tgFile) SetName(name string) {
	f.name = SanitizeFileName(name)
}

func (f *tgFile) Location() tg.InputFileLocationClass {
	f.locationMu.RLock()
	defer f.locationMu.RUnlock()
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
	f.locationMu.Lock()
	f.location = location
	f.locationMu.Unlock()
	return nil
}

func (f *tgFile) Dler() downloader.Client {
	return f.dler
}

func (f *tgFile) FileUniqueID() string {
	return f.fileUniqueID
}

// newTGFile is the single construction path for tgFile: every public
// constructor funnels through it so no field (file_unique_id in particular)
// can be silently dropped by a hand-rolled struct literal.
func newTGFile(
	location tg.InputFileLocationClass,
	dler downloader.Client,
	size int64,
	name string,
	message *tg.Message,
	fileUniqueID string,
	opts ...TGFileOption,
) *tgFile {
	f := &tgFile{
		location:     location,
		dler:         dler,
		size:         size,
		name:         SanitizeFileName(name),
		message:      message,
		fileUniqueID: fileUniqueID,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func NewTGFile(
	location tg.InputFileLocationClass,
	dler downloader.Client,
	size int64,
	name string,
	opts ...TGFileOption,
) TGFile {
	return newTGFile(location, dler, size, name, nil, "", opts...)
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

// fileUniqueIDFromMedia extracts the stable content identifier of the media
// ("doc:<id>" / "photo:<id>"). Document and photo IDs live in separate
// namespaces, hence the type prefix.
func fileUniqueIDFromMedia(media tg.MessageMediaClass) string {
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		if document, ok := m.Document.AsNotEmpty(); ok {
			return "doc:" + strconv.FormatInt(document.ID, 10)
		}
	case *tg.MessageMediaPhoto:
		if photo, ok := m.Photo.AsNotEmpty(); ok {
			return "photo:" + strconv.FormatInt(photo.ID, 10)
		}
	}
	return ""
}

func FromMedia(media tg.MessageMediaClass, client downloader.Client, opts ...TGFileOption) (TGFile, error) {
	return buildFromMedia(media, client, nil, opts...)
}

func FromMediaMessage(media tg.MessageMediaClass, client downloader.Client, msg *tg.Message, opts ...TGFileOption) (TGFileMessage, error) {
	return buildFromMedia(media, client, msg, opts...)
}

// buildFromMedia turns media into a file, attaching the source message when
// one is given. Both public constructors funnel through here so the built
// file always carries its file_unique_id.
func buildFromMedia(media tg.MessageMediaClass, client downloader.Client, msg *tg.Message, opts ...TGFileOption) (*tgFile, error) {
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
		return newTGFile(
			location,
			client,
			document.Size,
			fileName,
			msg,
			fileUniqueIDFromMedia(m),
			opts...,
		), nil
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
		return newTGFile(
			location,
			client,
			0, // Photo size is not available in InputPhotoFileLocation
			fileName,
			msg,
			fileUniqueIDFromMedia(m),
			opts...,
		), nil
	}
	return nil, fmt.Errorf("unsupported media type: %T", media)
}
