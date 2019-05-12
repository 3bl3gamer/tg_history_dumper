package main

import (
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/3bl3gamer/tgclient"
	"github.com/3bl3gamer/tgclient/mtproto"
	"github.com/ansel1/merry"
	"golang.org/x/net/proxy"
)

func tgConnect(appID int, appHash string) (*tgclient.TGClient, error) {
	cfg := &mtproto.AppConfig{
		AppID:          int32(appID),
		AppHash:        appHash,
		AppVersion:     "0.0.1",
		DeviceModel:    "Unknown",
		SystemVersion:  runtime.GOOS + "/" + runtime.GOARCH,
		SystemLangCode: "en",
		LangPack:       "",
		LangCode:       "en",
	}

	sessStore := &mtproto.SessFileStore{FPath: "./tg.session"}

	dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:9050", nil, proxy.Direct)
	if err != nil {
		return nil, merry.Wrap(err)
	}

	tg := tgclient.NewTGClientExt(cfg, sessStore, tgLogHandler, dialer)

	if err := tg.InitAndConnect(); err != nil {
		return nil, merry.Wrap(err)
	}

	res, err := tg.AuthExt(mtproto.ScanfAuthDataProvider{}, mtproto.TL_users_getUsers{ID: []mtproto.TL{mtproto.TL_inputUserSelf{}}})
	if err != nil {
		return nil, merry.Wrap(err)
	}
	users, ok := res.(mtproto.VectorObject)
	if !ok {
		return nil, merry.Wrap(mtproto.WrongRespError(res))
	}
	me := users[0].(mtproto.TL_user)
	log.Info("logged in as \033[32;1m%s (%s)\033[0m #%d", strings.TrimSpace(me.FirstName+" "+me.LastName), me.Username, me.ID)
	return tg, nil
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

func tgExtractDialogsData(dialogs []mtproto.TL, chats []mtproto.TL, users []mtproto.TL) ([]*Dialog, error) {
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
	extractedDialogs := make([]*Dialog, len(dialogs))
	for i, dialogTL := range dialogs {
		dialog := dialogTL.(mtproto.TL_dialog)
		ext := &Dialog{LastMessageID: dialog.TopMessage}
		switch peer := dialog.Peer.(type) {
		case mtproto.TL_peerUser:
			user := usersByID[peer.UserID]
			ext.ID = user.ID
			ext.Title = strings.TrimSpace(user.FirstName + " " + user.LastName)
			ext.Username = user.Username
			ext.Obj = user
		case mtproto.TL_peerChat:
			chat := chatsByID[peer.ChatID]
			ext.ID = chat.ID
			ext.Title = chat.Title
			ext.Obj = chat
		case mtproto.TL_peerChannel:
			channel := channelsByID[peer.ChannelID]
			ext.ID = channel.ID
			ext.Title = channel.Title
			ext.Username = channel.Username
			ext.Obj = channel
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(dialog.Peer))
		}
		extractedDialogs[i] = ext
	}
	return extractedDialogs, nil
}

func tgLoadDialogs(tg *tgclient.TGClient) ([]*Dialog, error) {
	dialogs := make([]*Dialog, 0)
	offsetDate := int32(0)
	for {
		res := tg.SendSync(mtproto.TL_messages_getDialogs{
			OffsetPeer: mtproto.TL_inputPeerEmpty{},
			OffsetDate: offsetDate,
			Limit:      100,
		})
		switch slice := res.(type) {
		case mtproto.TL_messages_dialogs:
			dialogs, err := tgExtractDialogsData(slice.Dialogs, slice.Chats, slice.Users)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			return dialogs, nil
		case mtproto.TL_messages_dialogsSlice:
			group, err := tgExtractDialogsData(slice.Dialogs, slice.Chats, slice.Users)
			if err != nil {
				return nil, merry.Wrap(err)
			}
			for _, d := range group {
				dialogs = append(dialogs, d) //TODO: check duplicates
			}

			offsetDate, err = tgGetMessageStamp(slice.Messages[len(slice.Messages)-1])
			if err != nil {
				return nil, merry.Wrap(err)
			}

			if len(dialogs) == int(slice.Count) {
				return dialogs, nil
			}
			if len(slice.Dialogs) < 100 {
				log.Warn("some dialogs seem missing: got %d in the end, expected %d; retrying from start", len(dialogs), slice.Count)
				offsetDate = 0
			}
		default:
			return nil, merry.Wrap(mtproto.WrongRespError(res))
		}
	}
}

func tgLoadMessages(tg *tgclient.TGClient, peerTL mtproto.TL, limit, lastMsgID int32) ([]mtproto.TL, error) {
	var inputPeer mtproto.TL
	switch peer := peerTL.(type) {
	case mtproto.TL_user:
		inputPeer = mtproto.TL_inputPeerUser{UserID: peer.ID, AccessHash: peer.AccessHash}
	case mtproto.TL_channel:
		inputPeer = mtproto.TL_inputPeerChannel{ChannelID: peer.ID, AccessHash: peer.AccessHash}
	default:
		return nil, merry.Wrap(mtproto.WrongRespError(peerTL))
	}

	res := tg.SendSync(mtproto.TL_messages_getHistory{
		Peer:      inputPeer,
		Limit:     limit,
		OffsetID:  lastMsgID + 1,
		AddOffset: -limit,
	})

	switch messages := res.(type) {
	case mtproto.TL_messages_messagesSlice:
		return messages.Messages, nil
	case mtproto.TL_messages_channelMessages:
		return messages.Messages, nil
	default:
		return nil, merry.Wrap(mtproto.WrongRespError(res))
	}
}

func tgObjToMap(obj mtproto.TL) map[string]interface{} {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	typ := v.Type()
	res := make(map[string]interface{})
	for i := 0; i < v.NumField(); i++ {
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
