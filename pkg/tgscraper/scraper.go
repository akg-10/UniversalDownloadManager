package tgscraper

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// ScrapedMediaItem represents a single downloadable file discovered in a channel.
type ScrapedMediaItem struct {
	MessageID int       `json:"message_id"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	MimeType  string    `json:"mime_type"`
	Date      time.Time `json:"date"`
	PostLink  string    `json:"post_link"`
}

var (
	privateChRegex = regexp.MustCompile(`(?:https?://)?t\.me/c/(\d+)`)
	publicChRegex  = regexp.MustCompile(`(?:https?://)?t\.me/([a-zA-Z0-9_]+)`)
)

// ChannelScraper scans a Telegram channel and extracts all downloadable media.
type ChannelScraper struct {
	raw *tg.Client
}

func NewChannelScraper(raw *tg.Client) *ChannelScraper {
	return &ChannelScraper{raw: raw}
}

// ScrapeChannel extracts all documents and video files from a channel.
func (s *ChannelScraper) ScrapeChannel(ctx context.Context, channelURL string, limit int) ([]ScrapedMediaItem, error) {
	if limit <= 0 {
		limit = 100
	}
	channelURL = strings.TrimSpace(channelURL)

	var inputChannel tg.InputChannelClass
	var channelBaseLink string

	if matches := privateChRegex.FindStringSubmatch(channelURL); len(matches) >= 2 {
		channelID, _ := strconv.ParseInt(matches[1], 10, 64)
		channelBaseLink = fmt.Sprintf("https://t.me/c/%d", channelID)

		// Find access hash from dialogs
		dialogs, err := s.raw.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      100,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to fetch user dialogs: %w", err)
		}

		var accessHash int64
		var found bool
		switch d := dialogs.(type) {
		case *tg.MessagesDialogs:
			for _, chat := range d.Chats {
				if ch, ok := chat.(*tg.Channel); ok && ch.ID == channelID {
					accessHash = ch.AccessHash
					found = true
					break
				}
			}
		case *tg.MessagesDialogsSlice:
			for _, chat := range d.Chats {
				if ch, ok := chat.(*tg.Channel); ok && ch.ID == channelID {
					accessHash = ch.AccessHash
					found = true
					break
				}
			}
		}

		if !found {
			return nil, fmt.Errorf("private channel ID %d not found in your Telegram dialogs. Please join the channel first.", channelID)
		}

		inputChannel = &tg.InputChannel{
			ChannelID:  channelID,
			AccessHash: accessHash,
		}
	} else if matches := publicChRegex.FindStringSubmatch(channelURL); len(matches) >= 2 {
		username := matches[1]
		channelBaseLink = fmt.Sprintf("https://t.me/%s", username)

		resolved, err := s.raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
			Username: username,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to resolve channel @%s: %w", username, err)
		}

		for _, chat := range resolved.Chats {
			if ch, ok := chat.(*tg.Channel); ok {
				inputChannel = &tg.InputChannel{
					ChannelID:  ch.ID,
					AccessHash: ch.AccessHash,
				}
				break
			}
		}

		if inputChannel == nil {
			return nil, fmt.Errorf("target is not a channel or supergroup")
		}
	} else {
		return nil, fmt.Errorf("invalid channel link format: expected https://t.me/channel or https://t.me/c/12345")
	}

	// Fetch message history
	history, err := s.raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: inputChannel.(*tg.InputChannel).ChannelID, AccessHash: inputChannel.(*tg.InputChannel).AccessHash},
		Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch channel history: %w", err)
	}

	var messageList []tg.MessageClass
	switch h := history.(type) {
	case *tg.MessagesMessages:
		messageList = h.Messages
	case *tg.MessagesMessagesSlice:
		messageList = h.Messages
	case *tg.MessagesChannelMessages:
		messageList = h.Messages
	}

	var results []ScrapedMediaItem
	for _, m := range messageList {
		msg, ok := m.(*tg.Message)
		if !ok || msg.Media == nil {
			continue
		}

		docMedia, ok := msg.Media.(*tg.MessageMediaDocument)
		if !ok || docMedia.Document == nil {
			continue
		}

		doc, ok := docMedia.Document.AsNotEmpty()
		if !ok {
			continue
		}

		filename := fmt.Sprintf("file_%d.bin", doc.ID)
		for _, attr := range doc.Attributes {
			if fnAttr, ok := attr.(*tg.DocumentAttributeFilename); ok && fnAttr.FileName != "" {
				filename = fnAttr.FileName
				break
			}
		}

		postLink := fmt.Sprintf("%s/%d", channelBaseLink, msg.ID)
		results = append(results, ScrapedMediaItem{
			MessageID: msg.ID,
			Filename:  filename,
			Size:      doc.Size,
			MimeType:  doc.MimeType,
			Date:      time.Unix(int64(msg.Date), 0),
			PostLink:  postLink,
		})
	}

	return results, nil
}
