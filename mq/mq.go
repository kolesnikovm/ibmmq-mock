package mq

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"log"
	"reflect"
	"strings"
	"sync"
)

type tshm struct {
	StructID  []byte `length:"4"`
	MQSegmLen []byte `length:"4"`
	ConversID []byte `length:"4"`
	RequestID []byte `length:"4"`
	ByteOrder []byte `length:"1"`
	SegmType  []byte `length:"1"`
	CtlFlag1  []byte `length:"1"`
	CtlFlag2  []byte `length:"1"`
	LUWIdent  []byte `length:"8"`
	Encoding  []byte `length:"4"`
	CCSID     []byte `length:"2"`
	Reserved  []byte `length:"2"`
}

type apiHeader struct {
	ReplyLen   []byte `length:"4"`
	ComplCode  []byte `length:"4"`
	ReasonCode []byte `length:"4"`
	ObjectHdl  []byte `length:"4"`
}

type context struct {
	userID   []byte
	sessions map[uint32]*session
	mux      sync.RWMutex
}

type session struct {
	qMgr    []byte
	appName []byte
	lastHdl uint32
	hdls    map[uint32]*queue
}

type hdl struct {
	producer int
	consumer int
}

type queue struct {
	name     []byte
	role     []byte //producer/consumer
	messages []message
}

type message struct {
}

const (
	INITIAL_DATA           = byte(0x01)
	USER_DATA              = byte(0x08)
	MQCONN, MQCONN_REPLY   = byte(0x81), byte(0x91)
	MQOPEN, MQOPEN_REPLY   = byte(0x83), byte(0x93)
	MQINQ, MQINQ_REPLY     = byte(0x89), byte(0x99)
	MQCLOSE, MQCLOSE_REPLY = byte(0x84), byte(0x94)
	SOCKET_ACTION          = byte(0x0c)
	SPI, SPI_REPLY         = byte(0x8c), byte(0x9c)
	MQPUT, MQPUT_REPLY     = byte(0x86), byte(0x96)
	REQUEST_MSGS           = byte(0x0e)
	ASYNC_MESSAGE          = byte(0x0d)
	NOTIFICATION           = byte(0x0f)
	MQCMIT, MQCMIT_REPLY   = byte(0x8a), byte(0x9a)
	MQDISC, MQDISC_REPLY   = byte(0x82), byte(0x92)

	TSH  = "TSH "
	TSHM = "TSHM"
	TSHC = "TSHC"
)

var (
	msgTypes = map[byte]byte{
		INITIAL_DATA: INITIAL_DATA,
		MQCONN:       MQCONN_REPLY,
		MQOPEN:       MQOPEN_REPLY,
		MQINQ:        MQINQ_REPLY,
		MQCLOSE:      MQCLOSE_REPLY,
		SPI:          SPI_REPLY,
		MQPUT:        MQPUT_REPLY,
		MQCMIT:       MQCMIT_REPLY,
		MQDISC:       MQDISC_REPLY,
	}

	userID, appType, appName, qMgr []byte //перенести в контекст

	MQCC_OK   = []byte{0x00, 0x00, 0x00, 0x00}
	MQRC_NONE = []byte{0x00, 0x00, 0x00, 0x00}
	ZERO_HDL  = []byte{0x00, 0x00, 0x00, 0x00}

	ctx = context{
		sessions: make(map[uint32]*session),
	}
)

func HandleMessage(msg []byte) (response []byte) {
	tshType := msg[:4]

	var msgType byte
	var tshmRs tshm

	switch string(tshType) {
	case TSH:
		msgType = msg[9]
	case TSHM:
		tshmRs = tshm{
			StructID:  []byte(TSHM),
			ConversID: msg[8:12],
			RequestID: msg[12:16],
			ByteOrder: msg[16:17],
			MQSegmLen: make([]byte, 4),
			SegmType:  []byte{msgTypes[msg[17]]},
			CtlFlag1:  msg[18:19],
			CtlFlag2:  msg[19:20],
			LUWIdent:  msg[20:28],
			Encoding:  msg[28:32],
			CCSID:     msg[32:34],
			Reserved:  msg[34:36],
		}
		msgType = msg[17]
	case TSHC:
		msgType = msg[9]
	default:
		log.Printf("[WARN] Unknown TSH type: %s\n", tshType)
		return nil
	}

	switch msgType {
	case INITIAL_DATA:
		response = handleInitialData(msg, tshType)
	case USER_DATA:
		userID = msg[40:52]
	case MQCONN:
		tshmRs.MQSegmLen = []byte{0x00, 0x00, 0x01, 0x80}

		appType = msg[128:132]
		appName = msg[100:128]
		qMgr = msg[52:100]

		cid := binary.BigEndian.Uint32(msg[8:12])
		log.Printf("[INFO] created new session with patameters: C: %d\n", cid)
		ctx.mux.Lock()
		ctx.sessions[cid] = &session{
			qMgr:    qMgr,
			appName: appName,
			hdls:    make(map[uint32]*queue),
		}
		ctx.mux.Unlock()

		apiHeader := apiHeader{
			ReplyLen:   []byte{0x00, 0x00, 0x01, 0x78},
			ComplCode:  MQCC_OK,
			ReasonCode: MQRC_NONE,
			ObjectHdl:  ZERO_HDL,
		}

		mqConn := handleMqConn(msg)

		response = append(response, getBytes(tshmRs)...)
		response = append(response, getBytes(apiHeader)...)
		response = append(response, mqConn...)
	case MQOPEN:
		tshmRs.MQSegmLen = []byte{0x00, 0x00, 0x01, 0x54}

		apiHeader := apiHeader{
			ReplyLen:   []byte{0x00, 0x00, 0x01, 0x4c},
			ComplCode:  MQCC_OK,
			ReasonCode: MQRC_NONE,
			ObjectHdl:  []byte{0x02, 0x00, 0x00, 0x00},
		}

		mqOpen := handleMqOpen(msg)

		response = append(response, getBytes(tshmRs)...)
		response = append(response, getBytes(apiHeader)...)
		response = append(response, mqOpen...)
	case MQINQ:
		mqInc := handleMqInc(msg)

		segmLen := 36 + 16 + len(mqInc)
		segmLenBytes := getByteLength(segmLen)
		replyLenBytes := getByteLength(segmLen - 8)

		tshmRs.MQSegmLen = segmLenBytes
		apiHeader := apiHeader{
			ReplyLen:   replyLenBytes,
			ComplCode:  MQCC_OK,
			ReasonCode: MQRC_NONE,
			ObjectHdl:  msg[48:52],
		}
		response = append(response, getBytes(tshmRs)...)
		response = append(response, getBytes(apiHeader)...)
		response = append(response, mqInc...)
	case MQCLOSE:
		tshmRs.MQSegmLen = []byte{0x00, 0x00, 0x00, 0x34}

		mqClose := handleMqClose(msg)

		response = append(response, getBytes(tshmRs)...)
		response = append(response, mqClose...)
	case SOCKET_ACTION:
		if bytes.Compare(msg[36:40], []byte{0x02, 0x00, 0x00, 0x00}) == 0 {
			return nil
		}

		response = handleSocketAction(msg)
	case SPI:
		spi := handleSpi(msg)

		segmLen := 36 + 16 + len(spi)
		segmLenBytes := getByteLength(segmLen)
		replyLenBytes := getByteLength(segmLen - 8)

		tshmRs.MQSegmLen = segmLenBytes

		objectHdl := ZERO_HDL

		spiVerb := msg[52:56]
		if bytes.Compare(spiVerb, OPEN) == 0 {
			objectHdl = getHandler(msg)
		}

		apiHeader := apiHeader{
			ReplyLen:   replyLenBytes,
			ComplCode:  MQCC_OK,
			ReasonCode: MQRC_NONE,
			ObjectHdl:  objectHdl,
		}

		response = append(response, getBytes(tshmRs)...)
		response = append(response, getBytes(apiHeader)...)
		response = append(response, spi...)
	case MQPUT:
		mqPut := handleMqPut(msg, userID, appType, appName, qMgr)

		segmLen := 36 + 16 + len(mqPut)
		segmLenBytes := getByteLength(segmLen)
		replyLenBytes := getByteLength(segmLen - 8)

		tshmRs.MQSegmLen = segmLenBytes

		apiHeader := apiHeader{
			ReplyLen:   replyLenBytes,
			ComplCode:  MQCC_OK,
			ReasonCode: MQRC_NONE,
			ObjectHdl:  msg[48:52],
		}

		response = append(response, getBytes(tshmRs)...)
		response = append(response, getBytes(apiHeader)...)
		response = append(response, mqPut...)
	case REQUEST_MSGS:
		asyncMsg := handleRequestMsg(msg, userID, appType, appName, qMgr)

		segmLen := 36 + len(asyncMsg)
		segmLenBytes := getByteLength(segmLen)

		tshmRs.MQSegmLen = segmLenBytes
		tshmRs.RequestID = []byte{0x00, 0x00, 0x00, 0x01}
		tshmRs.SegmType = []byte{ASYNC_MESSAGE}
		tshmRs.CtlFlag1 = []byte{0x30}
		tshmRs.Encoding = []byte{0x22, 0x02, 0x00, 0x01}

		response = append(response, getBytes(tshmRs)...)
		response = append(response, asyncMsg...)
	case MQCMIT:
		log.Printf("[INFO] M: MQCMIT, C: %d, R: %d\n", binary.BigEndian.Uint32(msg[8:12]), binary.BigEndian.Uint32(msg[12:16]))

		tshmRs.MQSegmLen = []byte{0x00, 0x00, 0x00, 0x34}
		apiHeader := apiHeader{
			ReplyLen:   []byte{0x00, 0x00, 0x00, 0x2c},
			ComplCode:  MQCC_OK,
			ReasonCode: MQRC_NONE,
			ObjectHdl:  ZERO_HDL,
		}

		response = append(response, getBytes(tshmRs)...)
		response = append(response, getBytes(apiHeader)...)
	case MQDISC:
		log.Printf("[INFO] M: MQDISC, C: %d, R: %d\n", binary.BigEndian.Uint32(msg[8:12]), binary.BigEndian.Uint32(msg[12:16]))

		tshmRs.MQSegmLen = []byte{0x00, 0x00, 0x00, 0x34}
		apiHeader := apiHeader{
			ReplyLen:   []byte{0x00, 0x00, 0x00, 0x2c},
			ComplCode:  MQCC_OK,
			ReasonCode: MQRC_NONE,
			ObjectHdl:  ZERO_HDL,
		}

		response = append(response, getBytes(tshmRs)...)
		response = append(response, getBytes(apiHeader)...)
	}

	return response
}

func decodeString(stg string) []byte {
	bytes, _ := hex.DecodeString(stg)
	return bytes
}

func getBytes(msgPart interface{}) (bytes []byte) {
	v := reflect.ValueOf(msgPart)

	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsNil() {
			continue
		}

		bytes = append(bytes, v.Field(i).Bytes()...)
	}

	return bytes
}

func getByteLength(length int) []byte {
	byteLen := make([]byte, 4)
	binary.BigEndian.PutUint32(byteLen, uint32(length))

	return byteLen
}

func getHandler(msg []byte) []byte {
	objectHdl := make([]byte, 4)

	spiVerb := msg[52:56]
	if bytes.Compare(spiVerb, OPEN) == 0 {
		cid := binary.BigEndian.Uint32(msg[8:12])
		log.Printf("[INFO] serving handler for session with id %d\n", cid)
		role := msg[96:100]
		name := msg[188:236]

		ctx.mux.RLock()
		session := ctx.sessions[cid]
		ctx.mux.RUnlock()

		for hdl, q := range session.hdls {
			if bytes.Equal(q.name, name) && bytes.Equal(q.role, role) {
				binary.BigEndian.PutUint32(objectHdl, hdl)
				log.Printf("[INFO] return existing handler %d for %s - %x\n", hdl, strings.TrimSpace(string(q.name)), q.role)
				return objectHdl
			}
		}

		session.lastHdl += 2
		session.hdls[session.lastHdl] = &queue{
			name: name,
			role: role,
		}

		log.Printf("[INFO] return new handler %d for %s - %x\n", session.lastHdl, strings.TrimSpace(string(name)), role)
		binary.LittleEndian.PutUint32(objectHdl, session.lastHdl)
	}

	return objectHdl
}
