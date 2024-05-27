package client

import (
	"context"
	"fmt"
	"github.com/netbirdio/netbird/relay/client/dialer/udp"
	"io"
	"net"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/netbirdio/netbird/relay/messages"
)

const (
	bufferSize            = 1500 // optimise the buffer size
	serverResponseTimeout = 8 * time.Second
)

var (
	reconnectingTimeout = 5 * time.Second
)

type Msg struct {
	buf []byte
}

type connContainer struct {
	conn     *Conn
	messages chan Msg
}

type Client struct {
	log           *log.Entry
	ctx           context.Context
	ctxCancel     context.CancelFunc
	serverAddress string
	hashedID      []byte

	relayConnIsEstablished bool
	conns                  map[string]*connContainer
	connsMutext            sync.Mutex // protect conns and relayConnIsEstablished bool

	relayConn        net.Conn
	serviceIsRunning bool
	wgRelayConn      sync.WaitGroup
	mu               sync.Mutex
	onDisconnected   chan struct{}
}

func NewClient(ctx context.Context, serverAddress, peerID string) *Client {
	ctx, ctxCancel := context.WithCancel(ctx)
	hashedID, hashedStringId := messages.HashID(peerID)
	return &Client{
		log:            log.WithField("client_id", hashedStringId),
		ctx:            ctx,
		ctxCancel:      ctxCancel,
		serverAddress:  serverAddress,
		hashedID:       hashedID,
		conns:          make(map[string]*connContainer),
		onDisconnected: make(chan struct{}),
	}
}

func (c *Client) Connect() error {
	c.mu.Lock()
	if c.serviceIsRunning {
		c.mu.Unlock()
		return nil
	}

	err := c.connect()
	if err != nil {
		c.mu.Unlock()
		return err
	}

	c.serviceIsRunning = true

	c.wgRelayConn.Add(1)
	go c.readLoop()

	c.mu.Unlock()

	go func() {
		<-c.ctx.Done()
		cErr := c.close()
		if cErr != nil {
			log.Errorf("failed to close relay connection: %s", cErr)
		}
	}()

	go c.reconnectGuard()

	return nil
}

func (c *Client) reconnectGuard() {
	for {
		c.wgRelayConn.Wait()

		c.mu.Lock()
		if !c.serviceIsRunning {
			c.mu.Unlock()
			return
		}

		log.Infof("reconnecting to relay server")
		err := c.connect()
		if err != nil {
			log.Errorf("failed to reconnect to relay server: %s", err)
			c.mu.Unlock()
			time.Sleep(reconnectingTimeout)
			continue
		}
		log.Infof("reconnected to relay server")
		c.wgRelayConn.Add(1)
		go c.readLoop()

		c.mu.Unlock()

	}
}

func (c *Client) OpenConn(dstPeerID string) (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.connsMutext.Lock()
	defer c.connsMutext.Unlock()

	if !c.relayConnIsEstablished {
		return nil, fmt.Errorf("relay connection is not established")
	}

	if !c.serviceIsRunning {
		return nil, fmt.Errorf("relay connection is not established")
	}

	hashedID, hashedStringID := messages.HashID(dstPeerID)
	log.Infof("open connection to peer: %s", hashedStringID)
	messageBuffer := make(chan Msg, 2)
	conn := NewConn(c, hashedID, hashedStringID, c.generateConnReaderFN(messageBuffer))

	c.conns[hashedStringID] = &connContainer{
		conn,
		messageBuffer,
	}
	return conn, nil
}

func (c *Client) Close() error {
	c.ctxCancel()
	return c.close()
}

func (c *Client) connect() error {
	conn, err := udp.Dial(c.serverAddress)
	if err != nil {
		return err
	}
	c.relayConn = conn

	err = c.handShake()
	if err != nil {
		cErr := conn.Close()
		if cErr != nil {
			log.Errorf("failed to close connection: %s", cErr)
		}
		c.relayConn = nil
		return err
	}

	c.relayConnIsEstablished = true
	return nil
}

func (c *Client) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.serviceIsRunning {
		return nil
	}

	c.serviceIsRunning = false

	err := c.relayConn.Close()

	c.wgRelayConn.Wait()

	return err
}

func (c *Client) handShake() error {
	defer func() {
		err := c.relayConn.SetReadDeadline(time.Time{})
		if err != nil {
			log.Errorf("failed to reset read deadline: %s", err)
		}
	}()

	msg, err := messages.MarshalHelloMsg(c.hashedID)
	if err != nil {
		log.Errorf("failed to marshal hello message: %s", err)
		return err
	}
	_, err = c.relayConn.Write(msg)
	if err != nil {
		log.Errorf("failed to send hello message: %s", err)
		return err
	}

	err = c.relayConn.SetReadDeadline(time.Now().Add(serverResponseTimeout))
	if err != nil {
		log.Errorf("failed to set read deadline: %s", err)
		return err
	}

	buf := make([]byte, 1500) // todo: optimise buffer size
	n, err := c.relayConn.Read(buf)
	if err != nil {
		log.Errorf("failed to read hello response: %s", err)
		return err
	}

	msgType, err := messages.DetermineServerMsgType(buf[:n])
	if err != nil {
		log.Errorf("failed to determine message type: %s", err)
		return err
	}

	if msgType != messages.MsgTypeHelloResponse {
		log.Errorf("unexpected message type: %s", msgType)
		return fmt.Errorf("unexpected message type")
	}
	return nil
}

func (c *Client) readLoop() {
	var errExit error
	var n int
	for {
		buf := make([]byte, bufferSize)
		n, errExit = c.relayConn.Read(buf)
		if errExit != nil {
			if c.serviceIsRunning {
				c.log.Debugf("failed to read message from relay server: %s", errExit)
			}
			break
		}

		msgType, err := messages.DetermineServerMsgType(buf[:n])
		if err != nil {
			c.log.Errorf("failed to determine message type: %s", err)
			continue
		}

		switch msgType {
		case messages.MsgTypeTransport:
			peerID, err := messages.UnmarshalTransportID(buf[:n])
			if err != nil {
				c.log.Errorf("failed to parse transport message: %v", err)
				continue
			}
			stringID := messages.HashIDToString(peerID)

			container, ok := c.conns[stringID]
			if !ok {
				c.log.Errorf("peer not found: %s", stringID)
				continue
			}

			container.messages <- Msg{
				buf[:n],
			}
		}
	}

	if c.serviceIsRunning {
		_ = c.relayConn.Close()
	}

	c.connsMutext.Lock()
	c.relayConnIsEstablished = false
	for _, container := range c.conns {
		close(container.messages)
	}
	c.conns = make(map[string]*connContainer)
	c.connsMutext.Unlock()

	c.log.Tracef("exit from read loop")
	c.wgRelayConn.Done()
}

func (c *Client) writeTo(id string, dstID []byte, payload []byte) (int, error) {
	c.mu.Lock()
	_, ok := c.conns[id]
	if !ok {
		c.mu.Unlock()
		return 0, io.EOF
	}
	c.mu.Unlock()
	msg := messages.MarshalTransportMsg(dstID, payload)
	n, err := c.relayConn.Write(msg)
	if err != nil {
		log.Errorf("failed to write transport message: %s", err)
	}
	return n, err
}

func (c *Client) generateConnReaderFN(msgChannel chan Msg) func(b []byte) (n int, err error) {
	return func(b []byte) (n int, err error) {
		msg, ok := <-msgChannel
		if !ok {
			return 0, io.EOF
		}

		payload, err := messages.UnmarshalTransportPayload(msg.buf)
		if err != nil {
			return 0, err
		}

		n = copy(b, payload)
		return n, nil
	}
}

func (c *Client) closeConn(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.connsMutext.Lock()
	defer c.connsMutext.Unlock()

	conn, ok := c.conns[id]
	if !ok {
		return fmt.Errorf("connection already closed")
	}
	close(conn.messages)
	delete(c.conns, id)

	return nil
}
