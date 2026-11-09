package tgbridge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gotd/td/tg"
)

// FileMetadata holds extracted info about a Telegram file.
type FileMetadata struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
	Location *tg.InputDocumentFileLocation `json:"-"`
	DocID    int64  `json:"doc_id"`
}

var (
	// Matches: https://t.me/c/1234567890/123 or t.me/c/1234567890/123
	privateChannelRegex = regexp.MustCompile(`(?:https?://)?t\.me/c/(\d+)/(\d+)`)
	// Matches: https://t.me/channelname/123 or t.me/channelname/123
	publicChannelRegex  = regexp.MustCompile(`(?:https?://)?t\.me/([a-zA-Z0-9_]+)/(\d+)`)
)

// Resolver resolves Telegram links to documents using the raw MTProto client.
type Resolver struct {
	raw         *tg.Client
	channelHash map[int64]int64 // channelID -> accessHash cache
	mu          sync.RWMutex
}

// NewResolver creates a new link resolver.
func NewResolver(raw *tg.Client) *Resolver {
	return &Resolver{
		raw:         raw,
		channelHash: make(map[int64]int64),
	}
}

// PopulateDialogs builds the internal channel access hash cache.
func (r *Resolver) PopulateDialogs(ctx context.Context) error {
	dialogs, err := r.raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	switch d := dialogs.(type) {
	case *tg.MessagesDialogs:
		for _, chat := range d.Chats {
			if ch, ok := chat.(*tg.Channel); ok {
				r.channelHash[ch.ID] = ch.AccessHash
			}
		}
	case *tg.MessagesDialogsSlice:
		for _, chat := range d.Chats {
			if ch, ok := chat.(*tg.Channel); ok {
				r.channelHash[ch.ID] = ch.AccessHash
			}
		}
	}
	return nil
}

// ResolveLink extracts file metadata from a Telegram post link.
func (r *Resolver) ResolveLink(ctx context.Context, rawLink string) (*FileMetadata, error) {
	rawLink = strings.TrimSpace(rawLink)

	// Check if private channel link: /c/<channel_id>/<msg_id>
	if matches := privateChannelRegex.FindStringSubmatch(rawLink); len(matches) == 3 {
		channelID, _ := strconv.ParseInt(matches[1], 10, 64)
		msgID, _ := strconv.Atoi(matches[2])
		return r.resolvePrivateChannelMessage(ctx, channelID, msgID)
	}

	// Check if public channel link: /<username>/<msg_id>
	if matches := publicChannelRegex.FindStringSubmatch(rawLink); len(matches) == 3 {
		username := matches[1]
		msgID, _ := strconv.Atoi(matches[2])
		return r.resolvePublicChannelMessage(ctx, username, msgID)
	}

	return nil, fmt.Errorf("unsupported Telegram link format. Expected https://t.me/channel/123 or https://t.me/c/12345/123")
}

func (r *Resolver) resolvePublicChannelMessage(ctx context.Context, username string, msgID int) (*FileMetadata, error) {
	resolved, err := r.raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: username,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve username @%s: %w", username, err)
	}

	var inputChannel *tg.InputChannel
	for _, chat := range resolved.Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			inputChannel = &tg.InputChannel{
				ChannelID:  ch.ID,
				AccessHash: ch.AccessHash,
			}
			r.mu.Lock()
			r.channelHash[ch.ID] = ch.AccessHash
			r.mu.Unlock()
			break
		}
	}

	if inputChannel == nil {
		return nil, fmt.Errorf("resolved peer is not a channel")
	}

	msgs, err := r.raw.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: inputChannel,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get channel message: %w", err)
	}

	return r.extractDocumentFromMessages(msgs)
}

func (r *Resolver) resolvePrivateChannelMessage(ctx context.Context, channelID int64, msgID int) (*FileMetadata, error) {
	r.mu.RLock()
	accessHash, found := r.channelHash[channelID]
	r.mu.RUnlock()

	if !found {
		// Attempt refreshing dialogs
		_ = r.PopulateDialogs(ctx)
		r.mu.RLock()
		accessHash, found = r.channelHash[channelID]
		r.mu.RUnlock()
	}

	if !found {
		return nil, fmt.Errorf("channel ID %d not found in your Telegram dialogs. Make sure you have joined this channel.", channelID)
	}

	msgs, err := r.raw.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{
			ChannelID:  channelID,
			AccessHash: accessHash,
		},
		ID: []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch message: %w", err)
	}

	return r.extractDocumentFromMessages(msgs)
}

func (r *Resolver) extractDocumentFromMessages(msgs tg.MessagesMessagesClass) (*FileMetadata, error) {
	var messageList []tg.MessageClass
	switch m := msgs.(type) {
	case *tg.MessagesMessages:
		messageList = m.Messages
	case *tg.MessagesMessagesSlice:
		messageList = m.Messages
	case *tg.MessagesChannelMessages:
		messageList = m.Messages
	default:
		return nil, errors.New("unrecognized messages response type")
	}

	if len(messageList) == 0 {
		return nil, errors.New("message not found (may have been deleted)")
	}

	msg, ok := messageList[0].(*tg.Message)
	if !ok || msg.Media == nil {
		return nil, errors.New("message does not contain any downloadable media")
	}

	docMedia, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok || docMedia.Document == nil {
		return nil, errors.New("message media is not a document or video file")
	}

	doc, ok := docMedia.Document.AsNotEmpty()
	if !ok {
		return nil, errors.New("document is empty")
	}

	meta := &FileMetadata{
		DocID:    doc.ID,
		Size:     doc.Size,
		MimeType: doc.MimeType,
		Filename: fmt.Sprintf("telegram_file_%d.bin", doc.ID),
		Location: &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
		},
	}

	// Extract filename from attributes
	for _, attr := range doc.Attributes {
		if fnAttr, ok := attr.(*tg.DocumentAttributeFilename); ok {
			if fnAttr.FileName != "" {
				meta.Filename = fnAttr.FileName
				break
			}
		}
	}

	return meta, nil
}
