package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/3bl3gamer/tgclient"
	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry/v2"
	"golang.org/x/net/proxy"
)

type FileInfosExtractorFunc = func(item mtproto.TL) ([]TGFileInfo, error)

var skipPendingWebpagePhotos bool = false

const skipPendingWebpagePhotosHelp = "Sometimes there are preview images for links in chats.\n" +
	"Sometimes (rarely) these previews have status 'pending'.\n" +
	"This status means that preview will be ready soon.\n" +
	"But the dumper can not (yet) re-fetch same messages again.\n" +
	"So, as a temporary workaround, the dumper will abort with error\n" +
	"and expect you or outer script to restart with a short delay.\n" +
	"But, if you prefer to skip some link previews (instead of aborting with error),\n" +
	"add -skip-pending-webpage-photos flag."

const videoCoverFileSuffix = "video_cover.jpg"

type ChatType int8

const (
	ChatUser ChatType = iota
	ChatGroup
	ChatChannel
)

func (t ChatType) String() string {
	switch t {
	case ChatUser:
		return "user"
	case ChatGroup:
		return "group"
	case ChatChannel:
		return "channel"
	default:
		return fmt.Sprintf("??%d??", t)
	}
}

func (t *ChatType) UnmarshalJSON(buf []byte) error {
	var s string
	if err := json.Unmarshal(buf, &s); err != nil {
		return merry.Wrap(err)
	}
	switch s {
	case "user":
		*t = ChatUser
	case "group":
		*t = ChatGroup
	case "channel":
		*t = ChatChannel
	default:
		return merry.New("wrong chat type: " + s)
	}
	return nil
}

type Chat struct {
	ID            int64
	Title         string
	Username      string
	LastMessageID int32
	Type          ChatType
	Obj           mtproto.TL
}

func tgConnect(config *Config, logHandler *LogHandler) (*tgclient.TGClient, *mtproto.TL_user, error) {
	cfg := &mtproto.AppConfig{
		AppID:          config.AppID,
		AppHash:        config.AppHash,
		AppVersion:     "0." + strconv.Itoa(mtproto.TL_Layer),
		DeviceModel:    "TG History Dumper",
		SystemVersion:  runtime.GOOS + "/" + runtime.GOARCH,
		SystemLangCode: "en",
		LangPack:       "",
		LangCode:       "en",
	}

	sessStore := &mtproto.SessFileStore{FPath: config.SessionFilePath}

	var dialer proxy.Dialer
	if config.Socks5ProxyAddr != "" {
		var auth *proxy.Auth
		if config.Socks5ProxyUser != "" || config.Socks5ProxyPassword != "" {
			auth = &proxy.Auth{
				User:     config.Socks5ProxyUser,
				Password: config.Socks5ProxyPassword,
			}
		}
		var err error
		dialer, err = proxy.SOCKS5("tcp", config.Socks5ProxyAddr, auth, proxy.Direct)
		if err != nil {
			return nil, nil, merry.Wrap(err)
		}
	}

	tg := tgclient.NewTGClientExt(cfg, sessStore, logHandler, dialer)

	if err := tg.InitAndConnect(); err != nil {
		return nil, nil, merry.Wrap(err)
	}

	res, err := tg.AuthExt(mtproto.ScanfAuthDataProvider{}, mtproto.TL_users_getUsers{ID: []mtproto.TL{mtproto.TL_inputUserSelf{}}})
	if err != nil {
		return nil, nil, merry.Wrap(err)
	}
	users, ok := res.(mtproto.VectorObject)
	if !ok {
		return nil, nil, merry.Wrap(mtproto.WrongRespError(res))
	}
	me := users[0].(mtproto.TL_user)
	return tg, &me, nil
}

func tgGetMessageID(messageTL mtproto.TL) (int32, error) {
	switch message := messageTL.(type) {
	case mtproto.TL_message:
		return message.ID, nil
	case mtproto.TL_messageService:
		return message.ID, nil
	case mtproto.TL_messageEmpty:
		// Sometimes the first message (#1) in channel is TL_messageEmpty
		// (instead of service message with action:TL_messageActionChannelCreate).
		// Maybe it was somehow deleted? For example, in @rutheniumos
		// the first visible mesage is https://t.me/rutheniumos/7 and #1 is "empty".
		return message.ID, nil
	default:
		return 0, merry.Wrap(mtproto.WrongRespError(messageTL))
	}
}

func tgGetMessageIDStampPeer(msgTL mtproto.TL) (int32, int32, mtproto.TL, error) {
	switch msg := msgTL.(type) {
	case mtproto.TL_message:
		return msg.ID, msg.Date, msg.PeerID, nil
	case mtproto.TL_messageService:
		return msg.ID, msg.Date, msg.PeerID, nil
	case mtproto.TL_messageEmpty:
		return 0, 0, nil, nil
	default:
		return 0, 0, nil, merry.Wrap(mtproto.WrongRespError(msg))
	}
}

func tgGetStoryID(storyTL mtproto.TL) (int32, error) {
	switch story := storyTL.(type) {
	case mtproto.TL_storyItemDeleted:
		return story.ID, nil
	case mtproto.TL_storyItemSkipped:
		return story.ID, nil
	case mtproto.TL_storyItem:
		return story.ID, nil
	default:
		return 0, merry.Wrap(mtproto.WrongRespError(story))
	}
}

func tgExtractDialogsData(dialogs []mtproto.TL, chats []mtproto.TL, users []mtproto.TL) ([]*Chat, error) {
	chatsByID := make(map[int64]mtproto.TL_chat)
	channelsByID := make(map[int64]mtproto.TL_channel)
	for _, chatTL := range chats {
		switch chat := chatTL.(type) {
		case mtproto.TL_chat:
			chatsByID[chat.ID] = chat
		case mtproto.TL_chatForbidden:
			chatsByID[chat.ID] = mtproto.TL_chat{ID: chat.ID, Title: chat.Title}
		case mtproto.TL_channel:
			channelsByID[chat.ID] = chat
		case mtproto.TL_channelForbidden:
			channelsByID[chat.ID] = mtproto.TL_channel{ID: chat.ID, Title: chat.Title, AccessHash: &chat.AccessHash, Megagroup: chat.Megagroup}
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(chatTL))
		}
	}
	usersByID := make(map[int64]mtproto.TL_user)
	for _, userTL := range users {
		user := userTL.(mtproto.TL_user)
		usersByID[user.ID] = user
	}
	extractedChats := make([]*Chat, len(dialogs))
	for i, chatTL := range dialogs {
		dialog := chatTL.(mtproto.TL_dialog)
		switch peer := dialog.Peer.(type) {
		case mtproto.TL_peerUser:
			extractedChats[i] = tgExtractUserData(usersByID[peer.UserID], dialog.TopMessage)
		case mtproto.TL_peerChat:
			extractedChats[i] = tgExtractChatData(chatsByID[peer.ChatID], dialog.TopMessage)
		case mtproto.TL_peerChannel:
			extractedChats[i] = tgExtractChannelData(channelsByID[peer.ChannelID], dialog.TopMessage)
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(dialog.Peer))
		}
	}
	return extractedChats, nil
}
func tgExtractUserData(user mtproto.TL_user, lastMessageID int32) *Chat {
	return &Chat{
		ID:            user.ID,
		Title:         strings.TrimSpace(mtproto.DerefOr(user.FirstName, "") + " " + mtproto.DerefOr(user.LastName, "")),
		Username:      mtproto.DerefOr(user.Username, ""),
		Type:          ChatUser,
		Obj:           user,
		LastMessageID: lastMessageID,
	}
}
func tgExtractChatData(chat mtproto.TL_chat, lastMessageID int32) *Chat {
	return &Chat{
		ID:            chat.ID,
		Title:         chat.Title,
		Type:          ChatGroup,
		Obj:           chat,
		LastMessageID: lastMessageID,
	}
}
func tgExtractChannelData(channel mtproto.TL_channel, lastMessageID int32) *Chat {
	chatType := ChatChannel
	if channel.Megagroup {
		chatType = ChatGroup
	}
	return &Chat{
		ID:            channel.ID,
		Title:         channel.Title,
		Username:      mtproto.DerefOr(channel.Username, ""),
		Type:          chatType,
		Obj:           channel,
		LastMessageID: lastMessageID,
	}
}

func tgLoadChats(tg *tgclient.TGClient) ([]*Chat, error) {
	chats := make([]*Chat, 0)
	// For deduplication. Chat duplicated may be encountered not only on second and subsequent iterations,
	// but also when user has pinned chats: these chats will be send in the begining of the first chunk (i.e. on "top")
	// and __also__ the same chats may be send in subsequent chunks as if these chats were not pinned.
	chatIDs := make(map[int64]bool)

	iteration := 0
	maxIterations := 2

	// It is 'min' limit. Because TG can actually send __more__ chats in response.
	// This happens for small limits and pinned chats: TG always adds some regualr chats after pinned ones.
	// Though it's not clear how many regular chats there will be. For example,
	// when there are 3 pinned chats and limit is 1, there will be 7 chats in response: 3 pinned and 4 regular.
	// If limit is 5, the response will contain 8 chats. If limit is 10 (and 3 still pinned), response will match request (10 chats).
	minChatsPerSlice := int32(100)

	offsetMessageDate := int32(0)
	offsetMessageID := int32(0)
	offsetPeer := mtproto.TL(mtproto.TL_inputPeerEmpty{})

	for {
		resTL := tg.SendSyncRetry(mtproto.TL_messages_getDialogs{
			OffsetDate: offsetMessageDate,
			OffsetID:   offsetMessageID,
			OffsetPeer: offsetPeer,
			Limit:      minChatsPerSlice,
		}, time.Second, 0, 30*time.Second)

		var res mtproto.TL_messages_dialogs
		switch d := resTL.(type) {
		case mtproto.TL_messages_dialogs:
			res = d
		case mtproto.TL_messages_dialogsSlice:
			res.Dialogs, res.Chats, res.Users, res.Messages = d.Dialogs, d.Chats, d.Users, d.Messages
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(resTL))
		}

		{
			s, _ := resTL.(mtproto.TL_messages_dialogsSlice)
			log.Debug("dialogs chunk, size=%d/%d, messages=%d, iteration=%d",
				len(res.Dialogs), s.Count, len(res.Messages), iteration)
		}

		slice, err := tgExtractDialogsData(res.Dialogs, res.Chats, res.Users)
		if err != nil {
			return nil, merry.Wrap(err)
		}

		for _, chat := range slice {
			if !chatIDs[chat.ID] {
				if iteration == 0 {
					chats = append(chats, chat)
				} else {
					// On the second and subsequent iterations (if any) we will add only chats with the most recent messages.
					// It is better to put them at the start of the list (prepend).
					// It will not match 100% with the sorting on official clients, but such order accuracy is not needed for the dumper.
					chats = append([]*Chat{chat}, chats...)
				}
				chatIDs[chat.ID] = true
			}
		}

		if _, ok := resTL.(mtproto.TL_messages_dialogs); ok {
			break //messages.dialogs contains complete list of all user dialogs
		}
		if s, ok := resTL.(mtproto.TL_messages_dialogsSlice); ok && len(chats) == int(s.Count) {
			break //all dialogs are fetched
		}
		if len(res.Dialogs) < int(minChatsPerSlice) {
			s, _ := resTL.(mtproto.TL_messages_dialogsSlice)
			log.Debug("last dialog slice size is %d, expexted %d. Looks like we've reached the bottom of dialogs list. "+
				"But dialogs list is not complete (got only %d of total %d dialogs)",
				len(res.Dialogs), int(minChatsPerSlice), len(chats), s.Count)

			if iteration < maxIterations-1 {
				log.Info("looks like dialog list has updated while loading, will retry one more time from the start (check debug log for more info)")
				offsetMessageDate = 0
				offsetMessageID = 0
				offsetPeer = mtproto.TL_inputPeerEmpty{}
				iteration += 1
				continue
			} else {
				log.Error(nil, "can not load all dialogs: got only %d of total %d; will continue as is but the chat list will be INCOMPLETE",
					len(chats), s.Count)
				break
			}
		}

		// Making offset values for the next chunk (aka slice).
		// These values are taken from the last chat in this chunk and from the last message of that chat.
		// `res.Messages` should contain the last messages, one message for each chat.
		// If for some reason such message was not found, trying next chat (i.e. iterating from last to first).
		// Items in `res.Messages` seems always sorted by message date.
		// But chats are not! Most of them are sorted by their last message date too, __except pinned ones__.
		// Pinned chats are always returned first in the first chunk.
		// So `res.Dialogs` and `res.Messages` sorting is dufferent and we can't just take the last `res.Messages` item for offset.
		offsetMessageFound := false
		for i := len(slice) - 1; i >= 0; i-- {
			chat := slice[i]

			msg, found, err := tgFindMessageByChat(res.Messages, chat.Obj)
			if err != nil {
				return nil, merry.Wrap(err)
			}

			if found {
				id, date, _, err := tgGetMessageIDStampPeer(msg)
				if err != nil {
					return nil, merry.Wrap(err)
				}
				inputPeer, err := tgMakeInputPeer(chat.Obj)
				if err != nil {
					return nil, merry.Wrap(err)
				}
				offsetMessageDate = date
				offsetMessageID = id
				offsetPeer = inputPeer
				offsetMessageFound = true
				break
			} else {
				log.Warn("dialogs: could not find last message for '%s' #%d, trying previous dialog", chat.Title, chat.ID)
			}
		}
		if !offsetMessageFound {
			log.Error(nil, "could not find last message for any of dialogs in dialog slice; will continue as is but the chat list will be INCOMPLETE")
			break
		}
	}
	return chats, nil
}
func tgFindMessageByChat(messages []mtproto.TL, chatTL mtproto.TL) (mtproto.TL, bool, error) {
	for _, msg := range messages {
		_, _, msgPeer, err := tgGetMessageIDStampPeer(msg)
		if err != nil {
			return nil, false, merry.Wrap(err)
		}

		switch chat := chatTL.(type) {
		case mtproto.TL_user:
			if m, ok := msgPeer.(mtproto.TL_peerUser); ok && m.UserID == chat.ID {
				return msg, true, nil
			}
		case mtproto.TL_chat:
			if m, ok := msgPeer.(mtproto.TL_peerChat); ok && m.ChatID == chat.ID {
				return msg, true, nil
			}
		case mtproto.TL_channel:
			if m, ok := msgPeer.(mtproto.TL_peerChannel); ok && m.ChannelID == chat.ID {
				return msg, true, nil
			}
		default:
			return nil, false, merry.Wrap(mtproto.WrongRespError(chatTL))
		}
	}
	return nil, false, nil
}

func tgLoadContacts(tg *tgclient.TGClient) (*mtproto.TL_contacts_contacts, error) {
	res := tg.SendSyncRetry(mtproto.TL_contacts_getContacts{}, time.Second, 0, 30*time.Second)

	contacts, ok := res.(mtproto.TL_contacts_contacts)
	if !ok {
		return nil, merry.Wrap(mtproto.WrongRespError(res))
	}
	return &contacts, nil
}

func tgLogout(tg *tgclient.TGClient) error {
	res := tg.SendSyncRetry(mtproto.TL_auth_logOut{}, time.Second, 0, 30*time.Second)
	if _, ok := res.(mtproto.TL_auth_loggedOut); !ok {
		return merry.New(mtproto.UnexpectedTL("logout", res))
	}
	return nil
}

func tgLoadAuths(tg *tgclient.TGClient) ([]mtproto.TL_authorization, error) {
	res := tg.SendSyncRetry(mtproto.TL_account_getAuthorizations{}, time.Second, 0, 30*time.Second)

	auths, ok := res.(mtproto.TL_account_authorizations)
	if !ok {
		return nil, merry.Wrap(mtproto.WrongRespError(res))
	}
	return auths.Authorizations, nil
}

func tgMakeInputPeer(peerTL mtproto.TL) (mtproto.TL, error) {
	switch peer := peerTL.(type) {
	case mtproto.TL_user:
		if peer.AccessHash == nil {
			return nil, merry.Errorf("user #%d has no access_hash", peer.ID)
		}
		return mtproto.TL_inputPeerUser{UserID: peer.ID, AccessHash: *peer.AccessHash}, nil
	case mtproto.TL_chat:
		return mtproto.TL_inputPeerChat{ChatID: peer.ID}, nil
	case mtproto.TL_channel:
		if peer.AccessHash == nil {
			return nil, merry.Errorf("channel #%d has no access_hash", peer.ID)
		}
		return mtproto.TL_inputPeerChannel{ChannelID: peer.ID, AccessHash: *peer.AccessHash}, nil
	default:
		return nil, merry.Wrap(mtproto.WrongRespError(peerTL))
	}
}

// Works in two modes:
//  1. when recentOffset <= 0:
//     requests `limit` messages newer than `lastMsgID`
//  2. when recentOffset > 0:
//     requests `limit` oldest messages of `recentOffset` most recent messages
func tgLoadMessages(
	tg *tgclient.TGClient, peerTL mtproto.TL, limit, lastMsgID, recentOffset int32,
) ([]mtproto.TL, []mtproto.TL, []mtproto.TL, error) {
	inputPeer, err := tgMakeInputPeer(peerTL)
	if err != nil {
		return nil, nil, nil, merry.Wrap(err)
	}

	params := mtproto.TL_messages_getHistory{
		Peer:  inputPeer,
		Limit: limit,
	}
	if recentOffset <= 0 {
		params.OffsetID = lastMsgID + 1
		params.AddOffset = -limit
	} else {
		params.AddOffset = recentOffset - limit
	}
	res := tg.SendSyncRetry(params, time.Second, 0, 30*time.Second)

	switch messages := res.(type) {
	case mtproto.TL_messages_messages:
		return messages.Messages, messages.Users, messages.Chats, nil
	case mtproto.TL_messages_messagesSlice:
		return messages.Messages, messages.Users, messages.Chats, nil
	case mtproto.TL_messages_channelMessages:
		return messages.Messages, messages.Users, messages.Chats, nil
	default:
		return nil, nil, nil, merry.Wrap(mtproto.WrongRespError(res))
	}
}

func tgLoadMissingMessageMediaStory(tg *tgclient.TGClient, chat mtproto.TL, msgTL mtproto.TL, relatedChats []mtproto.TL) (mtproto.TL, error) {
	if msg, ok := msgTL.(mtproto.TL_message); ok {
		if media, ok := msg.Media.(mtproto.TL_messageMediaStory); ok {
			if media.Story == nil {
				chatInputPeer, err := tgMakeInputPeer(chat)
				if err != nil {
					return nil, merry.Wrap(err)
				}

				var inputPeer mtproto.TL
				switch mediaPeer := media.Peer.(type) {
				case mtproto.TL_peerUser:
					inputPeer = mtproto.TL_inputPeerUserFromMessage{Peer: chatInputPeer, MsgID: msg.ID, UserID: mediaPeer.UserID}
				case mtproto.TL_peerChannel:
					// For some reason TL_inputPeerChannelFromMessage does not work here: TL_stories_getStoriesByID always returns CHANNEL_INVALID.
					// (similar TL_inputPeerUserFromMessage works as expected though)
					// So trying first to get channel from related chats (likely "Chats" from response to TL_messages_getHistory).
					for _, chatTL := range relatedChats {
						if channel, ok := chatTL.(mtproto.TL_channel); ok && channel.ID == mediaPeer.ChannelID && channel.AccessHash != nil {
							inputPeer = mtproto.TL_inputPeerChannel{ChannelID: channel.ID, AccessHash: *channel.AccessHash}
							break
						}
					}
					if inputPeer == nil {
						inputPeer = mtproto.TL_inputPeerChannelFromMessage{Peer: chatInputPeer, MsgID: msg.ID, ChannelID: mediaPeer.ChannelID}
					}
				default:
					return nil, merry.Wrap(mtproto.WrongRespError(media.Peer))
				}

				res := tg.SendSyncRetry(mtproto.TL_stories_getStoriesByID{
					Peer: inputPeer,
					ID:   []int32{media.ID},
				}, time.Second, 0, 5*60*time.Second) //need more time here: once got FLOOD_WAIT_54

				if mtproto.IsError(res, "CHANNEL_PRIVATE") {
					return msgTL, nil //UI shows such message as "This story has expired."
				}
				stories, ok := res.(mtproto.TL_stories_stories)
				if !ok {
					return nil, merry.Wrap(mtproto.WrongRespError(res))
				}
				if len(stories.Stories) == 0 {
					return msgTL, nil //story is not available (expired/hidden/removed), shown as "This story has expired."
				}
				if len(stories.Stories) != 1 {
					return nil, merry.Errorf("unexpected stories count: %d != 1", len(stories.Stories))
				}

				story, ok := stories.Stories[0].(mtproto.TL_storyItem)
				if !ok {
					return nil, merry.Wrap(mtproto.WrongRespError(res))
				}
				media.Story = story
				msg.Media = media
				return msg, nil
			}
		}
	}
	return msgTL, nil
}

func tgLoadPinnedStories(tg *tgclient.TGClient, peerTL mtproto.TL, limit, offsetID int32) ([]mtproto.TL, []mtproto.TL, []mtproto.TL, error) {
	inputPeer, err := tgMakeInputPeer(peerTL)
	if err != nil {
		return nil, nil, nil, merry.Wrap(err)
	}

	res := tg.SendSyncRetry(mtproto.TL_stories_getPinnedStories{
		Peer:     inputPeer,
		OffsetID: offsetID,
		Limit:    limit,
	}, time.Second, 0, 5*60*time.Second)
	stories, ok := res.(mtproto.TL_stories_stories)
	if !ok {
		return nil, nil, nil, merry.Wrap(mtproto.WrongRespError(res))
	}
	return stories.Stories, stories.Users, stories.Chats, nil
}

func tgLoadArchivedStories(tg *tgclient.TGClient, peerTL mtproto.TL, limit, offsetID int32) ([]mtproto.TL, []mtproto.TL, []mtproto.TL, error) {
	inputPeer, err := tgMakeInputPeer(peerTL)
	if err != nil {
		return nil, nil, nil, merry.Wrap(err)
	}

	res := tg.SendSyncRetry(mtproto.TL_stories_getStoriesArchive{
		Peer:     inputPeer,
		OffsetID: offsetID,
		Limit:    limit,
	}, time.Second, 0, 30*time.Second)
	stories, ok := res.(mtproto.TL_stories_stories)
	if !ok {
		return nil, nil, nil, merry.Wrap(mtproto.WrongRespError(res))
	}
	return stories.Stories, stories.Users, stories.Chats, nil
}

func tgObjToMap(obj mtproto.TL) map[string]interface{} {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}

	typ := v.Type()
	res := make(map[string]interface{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		var val interface{}
		switch value := v.Field(i).Interface().(type) {
		case int64:
			val = strconv.FormatInt(value, 10)
		case mtproto.TL:
			val = tgObjToMap(value)
		case []mtproto.TL:
			vals := make([]interface{}, len(value))
			for i, item := range value {
				vals[i] = tgObjToMap(item)
			}
			val = vals
		default:
			val = value
		}
		res[field.Name] = val
	}
	res["_"] = typ.Name()
	return res
}

type TGFileInfo struct {
	InputLocation mtproto.TL
	DCID          int32
	Size          int64
	FName         string
	IndexInMsg    int64 //message with paid content may have multiple media files inside
}

// getBestPhotoSize returns largest photo size of images.
// Usually it is the last size-object. But SOMETIMES Sizes aray is reversed.
func getBestPhotoSize(photo mtproto.TL_photo) (sizeType string, sizeBytes int32, err error) {
	maxResolution := int32(0)
	for _, sizeTL := range photo.Sizes {
		switch size := sizeTL.(type) {
		case mtproto.TL_photoSize:
			if size.W*size.H > maxResolution {
				maxResolution = size.W * size.H
				sizeType = size.Type
				sizeBytes = size.Size
			}
		case mtproto.TL_photoSizeProgressive:
			if size.W*size.H > maxResolution {
				maxResolution = size.W * size.H
				sizeType = size.Type
				if len(size.Sizes) > 0 {
					sizeBytes = size.Sizes[len(size.Sizes)-1]
				}
			}
		case mtproto.TL_photoStrippedSize:
			// not needed
		default:
			err = merry.Errorf(mtproto.UnexpectedTL("photoSize", sizeTL))
			return
		}
	}
	if maxResolution == 0 {
		err = merry.New("could not find suitable image size")
		return
	}
	return
}

func tgFindPhotoFileInfo(photoTL mtproto.TL, fname string, indexInMsg int64, ctxLocationInObj, ctxObjName string, ctxObjID int32) (TGFileInfo, bool, error) {
	if _, ok := photoTL.(mtproto.TL_photoEmpty); ok {
		log.Error(nil, "got 'photoEmpty' in %s of %s #%d item #%d", ctxLocationInObj, ctxObjName, ctxObjID, indexInMsg)
		return TGFileInfo{}, false, nil
	}

	photo := photoTL.(mtproto.TL_photo)
	sizeType, sizeBytes, err := getBestPhotoSize(photo)
	if err != nil {
		return TGFileInfo{}, false, merry.Prependf(err, "image size of %s #%d item #%d", ctxObjName, ctxObjID, indexInMsg)
	}

	return TGFileInfo{
		InputLocation: mtproto.TL_inputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     sizeType,
		},
		Size:       int64(sizeBytes),
		DCID:       photo.DCID,
		FName:      fname,
		IndexInMsg: indexInMsg,
	}, true, nil
}

func tgFindMediaFileInfos(mediaTL mtproto.TL, indexInMsg int64, ctxObjName string, ctxObjID int32) ([]TGFileInfo, error) {
	switch media := mediaTL.(type) {
	case mtproto.TL_messageMediaPhoto:
		fileInfo, found, err := tgFindPhotoFileInfo(media.Photo, "photo.jpg", indexInMsg, "media", ctxObjName, ctxObjID)
		if err != nil {
			return nil, merry.Wrap(err)
		}
		if found {
			return []TGFileInfo{fileInfo}, nil
		}
		return nil, nil
	case mtproto.TL_messageMediaDocument:
		doc := media.Document.(mtproto.TL_document) //has received TL_documentEmpty here once, after restart is has become TL_document
		fname := ""
		for _, attrTL := range doc.Attributes {
			if nameAttr, ok := attrTL.(mtproto.TL_documentAttributeFilename); ok {
				fname = nameAttr.FileName
				break
			}
		}
		var fileInfos []TGFileInfo
		if media.VideoCover != nil {
			fileInfo, found, err := tgFindPhotoFileInfo(media.VideoCover, videoCoverFileSuffix, indexInMsg, "document.VideoCover", ctxObjName, ctxObjID)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			if found {
				fileInfos = append(fileInfos, fileInfo)
			}
		}
		// There may be also a media.AltDocuments array which are used for video quality selection.
		// It *seems* that media.Document is an original and `media.AltDocuments` are re-encoded versions with lower size.
		// AltDocuments are also have very similar exif data and media.Document is a bit different from that.
		// Although *sometimes* there is an entry in AltDocuments which is *bigger* than the media.Document.
		// (for example, here t.me/android_ru/1630915 Document.Size=637176 and AltDocuments[2].Size=744419)
		// Why? ¯\_(ツ)_/¯
		// Saving only the original.
		fileInfos = append(fileInfos, TGFileInfo{
			InputLocation: mtproto.TL_inputDocumentFileLocation{
				ID:            doc.ID,
				AccessHash:    doc.AccessHash,
				FileReference: doc.FileReference,
			},
			Size:       doc.Size,
			DCID:       doc.DCID,
			FName:      fname,
			IndexInMsg: indexInMsg,
		})
		return fileInfos, nil
	case mtproto.TL_messageMediaStory:
		if media.Story == nil {
			return nil, nil
		}
		story, ok := media.Story.(mtproto.TL_storyItem)
		if !ok {
			return nil, merry.Errorf(mtproto.UnexpectedTL("photoSize", media.Story))
		}
		return tgFindMediaFileInfos(story.Media, indexInMsg, ctxObjName, ctxObjID)
	case mtproto.TL_messageMediaPaidMedia:
		var fileInfos []TGFileInfo
		for mediaIndex, extMediaTL := range media.ExtendedMedia {
			switch extMedia := extMediaTL.(type) {
			case mtproto.TL_messageExtendedMedia:
				info, err := tgFindMediaFileInfos(extMedia.Media, int64(mediaIndex), ctxObjName, ctxObjID)
				if err != nil {
					return nil, merry.Wrap(err)
				}
				fileInfos = append(fileInfos, info...)
			case mtproto.TL_messageExtendedMediaPreview:
				// no need to save preview: for accessible files there should be a reference to the full file
				// and for paywall'ed ones there seems only TL_photoStrippedSize with image data
				// embedded into message (https://core.telegram.org/api/files#stripped-thumbnails)
			default:
				log.Error(nil, "unexpected paid media item %#T in media of %s #%d item #%d, skipping",
					extMediaTL, ctxObjName, ctxObjID, mediaIndex)
			}
		}
		return fileInfos, nil
	case mtproto.TL_messageMediaUnsupported:
		log.Error(nil, "media of %s #%d item #%d is insupported, skipping", ctxObjName, ctxObjID, indexInMsg)
		return nil, nil
	case mtproto.TL_messageMediaWebPage:
		switch webPage := media.Webpage.(type) {
		case mtproto.TL_webPageEmpty:
			return nil, nil //no URL preview
		case mtproto.TL_webPagePending:
			// TODO: re-fetch the message somehow
			if skipPendingWebpagePhotos {
				log.Warn("webpage preview image in %s #%d is pending, skipping", ctxObjName, ctxObjID)
				return nil, nil
			} else {
				return nil, merry.Wrap(
					fmt.Errorf(
						"tl;dr: webPage preview image in %s #%d is pending, run with -skip-pending-webpage-photos to skip such images."+
							"\n\nMore details:\n%s",
						ctxObjName, ctxObjID, skipPendingWebpagePhotosHelp),
					merry.NoCaptureStack(),
				)
			}
		case mtproto.TL_webPage:
			if webPage.Photo != nil {
				fileInfo, found, err := tgFindPhotoFileInfo(webPage.Photo, "webpage_photo.jpg", indexInMsg, "media.webPage", ctxObjName, ctxObjID)
				if err != nil {
					return nil, merry.Wrap(err)
				}
				if found {
					return []TGFileInfo{fileInfo}, nil
				}
			}
			return nil, nil
		default:
			log.Error(nil, "unexpected webpage %#T in media of %s #%d, skipping",
				media.Webpage, ctxObjName, ctxObjID)
			return nil, nil
		}
	case mtproto.TL_messageMediaGeo,
		mtproto.TL_messageMediaContact,
		mtproto.TL_messageMediaVenue,
		mtproto.TL_messageMediaGame,
		mtproto.TL_messageMediaInvoice,
		mtproto.TL_messageMediaGeoLive,
		mtproto.TL_messageMediaPoll,
		mtproto.TL_messageMediaDice,
		mtproto.TL_messageMediaGiveaway,
		mtproto.TL_messageMediaGiveawayResults,
		mtproto.TL_messageMediaVideoStream,
		mtproto.TL_messageMediaToDo,
		nil:
		// nothing to save here
		return nil, nil
	default:
		log.Error(nil, "unexpected media %#T of %s #%d item #%d, skipping", mediaTL, ctxObjName, ctxObjID, indexInMsg)
		return nil, nil
	}
}

func tgFindMessageMediaFileInfos(msgTL mtproto.TL) ([]TGFileInfo, error) {
	msg, ok := msgTL.(mtproto.TL_message)
	if !ok {
		return nil, nil
	}
	return tgFindMediaFileInfos(msg.Media, 0, "message", msg.ID)
}

func tgFindStoryMediaFileInfos(storyTL mtproto.TL) ([]TGFileInfo, error) {
	story, ok := storyTL.(mtproto.TL_storyItem)
	if !ok {
		return nil, nil
	}
	return tgFindMediaFileInfos(story.Media, 0, "story", story.ID)
}

/*
type FileRefsItem struct {
	Path []string
	Ref  mtproto.TL
}

func tgGetFileRefs(obj mtproto.TL) []FileRefsItem {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	typ := v.Type()
	res := []FileRefsItem(nil)
	for i := 0; i < typ.NumField(); i++ {
		fieldName := typ.Field(i).Name
		valueTL := v.Field(i).Interface()
		switch value := valueTL.(type) {
		case mtproto.TL:
			for _, ref := range tgGetFileRefs(value) {
				ref.Path = append([]string{fieldName}, ref.Path...)
				res = append(res, ref)
			}
		case []mtproto.TL:
			for i, item := range value {
				for _, ref := range tgGetFileRefs(item) {
					ref.Path = append([]string{fieldName, strconv.Itoa(i)}, ref.Path...)
					res = append(res, ref)
				}
			}
		case mtproto.TL_photo:
			res = append(res, FileRefsItem{[]string{fieldName}})
		}
	}
	return res
}
*/
