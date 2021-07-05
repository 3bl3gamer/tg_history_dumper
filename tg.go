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
	"github.com/ansel1/merry"
	"golang.org/x/net/proxy"
)

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
	ID            int32
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
		AppVersion:     "0.0.1",
		DeviceModel:    "Unknown",
		SystemVersion:  runtime.GOOS + "/" + runtime.GOARCH,
		SystemLangCode: "en",
		LangPack:       "",
		LangCode:       "en",
	}

	sessStore := &mtproto.SessFileStore{FPath: config.SessionFilePath}

	var dialer proxy.Dialer
	if config.Socks5ProxyAddr != "" {
		var err error
		dialer, err = proxy.SOCKS5("tcp", config.Socks5ProxyAddr, nil, proxy.Direct)
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

	log.Info("logged in as \033[32;1m%s (%s)\033[0m #%d", strings.TrimSpace(me.FirstName+" "+me.LastName), me.Username, me.ID)
	return tg, &me, nil
}

func tgGetMessageStamp(msgTL mtproto.TL) (int32, error) {
	switch msg := msgTL.(type) {
	case mtproto.TL_message:
		return msg.Date, nil
	case mtproto.TL_messageService:
		return msg.Date, nil
	default:
		return 0, merry.Wrap(mtproto.WrongRespError(msg))
	}
}

func tgExtractDialogsData(dialogs []mtproto.TL, chats []mtproto.TL, users []mtproto.TL) ([]*Chat, error) {
	chatsByID := make(map[int32]mtproto.TL_chat)
	channelsByID := make(map[int32]mtproto.TL_channel)
	for _, chatTL := range chats {
		switch chat := chatTL.(type) {
		case mtproto.TL_chat:
			chatsByID[chat.ID] = chat
		case mtproto.TL_chatForbidden:
			chatsByID[chat.ID] = mtproto.TL_chat{ID: chat.ID, Title: chat.Title}
		case mtproto.TL_channel:
			channelsByID[chat.ID] = chat
		case mtproto.TL_channelForbidden:
			channelsByID[chat.ID] = mtproto.TL_channel{ID: chat.ID, Title: chat.Title, AccessHash: chat.AccessHash, Megagroup: chat.Megagroup}
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(chatTL))
		}
	}
	usersByID := make(map[int32]mtproto.TL_user)
	for _, userTL := range users {
		user := userTL.(mtproto.TL_user)
		usersByID[user.ID] = user
	}
	extractedChats := make([]*Chat, len(dialogs))
	for i, chatTL := range dialogs {
		dialog := chatTL.(mtproto.TL_dialog)
		ext := &Chat{LastMessageID: dialog.TopMessage}
		switch peer := dialog.Peer.(type) {
		case mtproto.TL_peerUser:
			user := usersByID[peer.UserID]
			ext.ID = user.ID
			ext.Title = strings.TrimSpace(user.FirstName + " " + user.LastName)
			ext.Username = user.Username
			ext.Type = ChatUser
			ext.Obj = user
		case mtproto.TL_peerChat:
			chat := chatsByID[peer.ChatID]
			ext.ID = chat.ID
			ext.Title = chat.Title
			ext.Type = ChatGroup
			ext.Obj = chat
		case mtproto.TL_peerChannel:
			channel := channelsByID[peer.ChannelID]
			ext.ID = channel.ID
			ext.Title = channel.Title
			ext.Username = channel.Username
			ext.Type = ChatChannel
			if channel.Megagroup {
				ext.Type = ChatGroup
			}
			ext.Obj = channel
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(dialog.Peer))
		}
		extractedChats[i] = ext
	}
	return extractedChats, nil
}

func tgLoadChats(tg *tgclient.TGClient) ([]*Chat, error) {
	chats := make([]*Chat, 0)
	offsetDate := int32(0)
	for {
		res := tg.SendSyncRetry(mtproto.TL_messages_getDialogs{
			OffsetPeer: mtproto.TL_inputPeerEmpty{},
			OffsetDate: offsetDate,
			Limit:      100,
		}, time.Second, 0, 30*time.Second)

		switch slice := res.(type) {
		case mtproto.TL_messages_dialogs:
			chats, err := tgExtractDialogsData(slice.Dialogs, slice.Chats, slice.Users)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			return chats, nil
		case mtproto.TL_messages_dialogsSlice:
			group, err := tgExtractDialogsData(slice.Dialogs, slice.Chats, slice.Users)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			for _, d := range group {
				chats = append(chats, d) //TODO: check duplicates
			}

			offsetDate, err = tgGetMessageStamp(slice.Messages[len(slice.Messages)-1])
			if err != nil {
				return nil, merry.Wrap(err)
			}

			if len(chats) == int(slice.Count) {
				return chats, nil
			}
			if len(slice.Dialogs) < 100 {
				log.Warn("some chats seem missing: got %d in the end, expected %d; retrying from start", len(chats), slice.Count)
				offsetDate = 0
			}
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(res))
		}
	}
}

func tgLoadContacts(tg *tgclient.TGClient) (mtproto.TL, error) {

	var stri int32 = 0
	res := tg.SendSyncRetry(mtproto.TL_contacts_getContacts{
		Hash: stri,
	}, time.Second, 0, 30*time.Second)

	//fmt.Println("contacts response:", slog.StringifyIndent(res, "  "))
	return res, nil
}

func tgLoadAuths(tg *tgclient.TGClient) (mtproto.TL, error) {

	res := tg.SendSyncRetry(mtproto.TL_account_getAuthorizations{}, time.Second, 0, 30*time.Second)
	//fmt.Println("contacts response:", slog.StringifyIndent(res, "  "))
	return res, nil
}

func tgLoadMessages(
	tg *tgclient.TGClient, peerTL mtproto.TL, limit, lastMsgID int32,
) ([]mtproto.TL, []mtproto.TL, []mtproto.TL, error) {
	var inputPeer mtproto.TL
	switch peer := peerTL.(type) {
	case mtproto.TL_user:
		inputPeer = mtproto.TL_inputPeerUser{UserID: peer.ID, AccessHash: peer.AccessHash}
	case mtproto.TL_chat:
		inputPeer = mtproto.TL_inputPeerChat{ChatID: peer.ID}
	case mtproto.TL_channel:
		inputPeer = mtproto.TL_inputPeerChannel{ChannelID: peer.ID, AccessHash: peer.AccessHash}
	default:
		return nil, nil, nil, merry.Wrap(mtproto.WrongRespError(peerTL))
	}

	res := tg.SendSyncRetry(mtproto.TL_messages_getHistory{
		Peer:      inputPeer,
		Limit:     limit,
		OffsetID:  lastMsgID + 1,
		AddOffset: -limit,
	}, time.Second, 0, 30*time.Second)

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

func tgObjToMap(obj mtproto.TL) map[string]interface{} {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
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
	DcID          int32
	Size          int32
	FName         string
}

// findBestPhotoSize returns largest photo size of images.
// Usually it is the last size-object. But SOME TIMES Sizes aray is reversed.
func findBestPhotoSize(photo mtproto.TL_photo) *mtproto.TL_photoSize {
	var bestSize *mtproto.TL_photoSize
	for _, sizeTL := range photo.Sizes {
		if size, ok := sizeTL.(mtproto.TL_photoSize); ok {
			if bestSize == nil || size.Size > bestSize.Size {
				bestSize = &size
			}
		}
	}
	return bestSize
}

func tgGetMessageMediaFileInfo(msgTL mtproto.TL) *TGFileInfo {
	msg, ok := msgTL.(mtproto.TL_message)
	if !ok {
		return nil
	}
	switch media := msg.Media.(type) {
	case mtproto.TL_messageMediaPhoto:
		if _, ok := media.Photo.(mtproto.TL_photoEmpty); ok {
			log.Error(nil, "got 'photoEmpty' in media of message #%d", msg.ID)
			return nil
		}
		photo := media.Photo.(mtproto.TL_photo)
		size := findBestPhotoSize(photo)
		if size == nil {
			log.Error(nil, "could not found suitable image size of message #%d", msg.ID)
			panic("image size search failed")
		}
		return &TGFileInfo{
			InputLocation: mtproto.TL_inputPhotoFileLocation{
				ID:            photo.ID,
				AccessHash:    photo.AccessHash,
				FileReference: photo.FileReference,
				ThumbSize:     size.Type,
			},
			Size:  size.Size,
			DcID:  photo.DcID,
			FName: "photo.jpg",
		}
	case mtproto.TL_messageMediaDocument:
		doc := media.Document.(mtproto.TL_document)
		fname := ""
		for _, attrTL := range doc.Attributes {
			if nameAttr, ok := attrTL.(mtproto.TL_documentAttributeFilename); ok {
				fname = nameAttr.FileName
				break
			}
		}
		return &TGFileInfo{
			InputLocation: mtproto.TL_inputDocumentFileLocation{
				ID:            doc.ID,
				AccessHash:    doc.AccessHash,
				FileReference: doc.FileReference,
			},
			Size:  doc.Size,
			DcID:  doc.DcID,
			FName: fname,
		}
	default:
		return nil
	}
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

func tgGetMessageID(messageTL mtproto.TL) (int32, error) {
	switch message := messageTL.(type) {
	case mtproto.TL_message:
		return message.ID, nil
	case mtproto.TL_messageService:
		return message.ID, nil
	default:
		return 0, merry.Wrap(mtproto.WrongRespError(messageTL))
	}
}
