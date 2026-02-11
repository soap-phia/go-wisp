package wisp

import (
	"encoding/binary"
	"strconv"
	"sync"

	"github.com/lxzan/gws"
)

type wispConnection struct {
	wsConn *gws.Conn

	streamsMu sync.RWMutex
	streams   map[uint32]*wispStream

	config *Config
}

func (c *wispConnection) init() {
	c.sendContinuePacket(0, c.config.BufferRemainingLength)
}

func (c *wispConnection) close() {
	c.wsConn.NetConn().Close()
}

func (c *wispConnection) handlePacket(packetType uint8, streamId uint32, payload []byte) {
	switch packetType {
	case packetTypeConnect:
		c.handleConnectPacket(streamId, payload)
	case packetTypeData:
		c.handleDataPacket(streamId, payload)
	case packetTypeClose:
		c.handleClosePacket(streamId, payload)
	default:
		return
	}
}

func (c *wispConnection) handleConnectPacket(streamId uint32, payload []byte) {
	if len(payload) < 3 {
		return
	}
	streamType := payload[0]
	port := strconv.FormatUint(uint64(binary.LittleEndian.Uint16(payload[1:3])), 10)
	hostname := string(payload[3:])

	stream := &wispStream{
		wispConn:  c,
		streamId:  streamId,
		dataQueue: make(chan []byte, c.config.BufferRemainingLength),
		isOpen:    true,
	}

	c.streamsMu.Lock()
	if _, exists := c.streams[streamId]; exists {
		c.streamsMu.Unlock()
		return
	}
	c.streams[streamId] = stream
	c.streamsMu.Unlock()

	go stream.handleConnect(streamType, port, hostname)
}

func (c *wispConnection) handleDataPacket(streamId uint32, payload []byte) {
	c.streamsMu.RLock()
	stream, exists := c.streams[streamId]
	c.streamsMu.RUnlock()
	if !exists {
		go c.sendClosePacket(streamId, closeReasonInvalidInfo)
		return
	}

	stream.isOpenMutex.RLock()
	defer stream.isOpenMutex.RUnlock()
	if !stream.isOpen {
		return
	}

	select {
	case stream.dataQueue <- payload:
	default:
	}
}

func (c *wispConnection) handleClosePacket(streamId uint32, payload []byte) {
	if len(payload) < 1 {
		return
	}
	closeReason := payload[0]

	c.streamsMu.RLock()
	stream, exists := c.streams[streamId]
	c.streamsMu.RUnlock()
	if !exists {
		return
	}

	go stream.handleClose(closeReason)
}

func (c *wispConnection) sendPacket(packetType uint8, streamId uint32, payload []byte) {
	var header [5]byte
	header[0] = packetType
	binary.LittleEndian.PutUint32(header[1:5], streamId)

	if err := c.wsConn.Writev(gws.OpcodeBinary, header[:], payload); err != nil {
		c.close()
	}
}

func (c *wispConnection) sendDataPacket(streamId uint32, data []byte) {
	c.sendPacket(packetTypeData, streamId, data)
}

func (c *wispConnection) sendContinuePacket(streamId uint32, bufferRemaining uint32) {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, bufferRemaining)
	c.sendPacket(packetTypeContinue, streamId, payload)
}

func (c *wispConnection) sendClosePacket(streamId uint32, reason uint8) {
	payload := []byte{reason}
	c.sendPacket(packetTypeClose, streamId, payload)
}

func (c *wispConnection) deleteWispStream(streamId uint32) {
	c.streamsMu.Lock()
	delete(c.streams, streamId)
	c.streamsMu.Unlock()
}

func (c *wispConnection) deleteAllWispStreams() {
	c.streamsMu.RLock()
	streams := make([]*wispStream, 0, len(c.streams))
	for _, stream := range c.streams {
		streams = append(streams, stream)
	}
	c.streamsMu.RUnlock()

	for _, stream := range streams {
		stream.close(closeReasonUnspecified)
	}
}
