package polygon

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/common"
	"github.com/gorilla/websocket"
)

const (
	MinuteAggs = "AM"
	SecondAggs = "A"
	Trades     = "T"
	Quotes     = "Q"
)

var (
	once sync.Once
	str  *Stream
)

type Stream struct {
	sync.Mutex
	sync.Once
	conn                  *websocket.Conn
	authenticated, closed atomic.Value
	handlers              sync.Map
}

// Subscribe to the specified Polygon stream channel.
func (s *Stream) Subscribe(channel string, handler func(msg interface{})) (err error) {
	if s.conn == nil {
		s.conn = openSocket()
	}

	if err = s.auth(); err != nil {
		return
	}

	s.Do(func() {
		go s.start()
	})

	s.handlers.Store(channel, handler)

	if err = s.sub(channel); err != nil {
		return
	}

	return
}

// Close gracefully closes the Polygon stream.
func (s *Stream) Close() error {
	s.Lock()
	defer s.Unlock()

	if s.conn == nil {
		return nil
	}

	if err := s.conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	); err != nil {
		return err
	}

	// so we know it was gracefully closed
	s.closed.Store(true)

	return s.conn.Close()
}

func (s *Stream) handleError(err error) {
	if websocket.IsCloseError(err) {
		// if this was a graceful closure, don't reconnect
		if s.closed.Load().(bool) {
			return
		}
	} else {
		log.Printf("polygon stream read error (%v)", err)
	}

	s.conn = openSocket()
}

func (s *Stream) start() {
	for {
		if _, bytes, err := s.conn.ReadMessage(); err == nil {
			msgArray := []PolgyonServerMsg{}
			if err := json.Unmarshal(bytes, msgArray); err == nil {
				for _, msg := range msgArray {
					if v, ok := s.handlers.Load(msg.Event); ok {
						switch msg.Event {
						case SecondAggs:
							fallthrough
						case MinuteAggs:
							var minuteAgg StreamAggregate
							if err := json.Unmarshal(bytes, minuteAgg); err == nil {
								h := v.(func(msg interface{}))
								h(minuteAgg)
							} else {
								s.handleError(err)
							}
						case Quotes:
							var quoteUpdate StreamQuote
							if err := json.Unmarshal(bytes, quoteUpdate); err == nil {
								h := v.(func(msg interface{}))
								h(quoteUpdate)
							} else {
								s.handleError(err)
							}
						case Trades:
							var tradeUpdate StreamTrade
							if err := json.Unmarshal(bytes, tradeUpdate); err == nil {
								h := v.(func(msg interface{}))
								h(tradeUpdate)
							} else {
								s.handleError(err)
							}
						}
					}
				}
			} else {
				s.handleError(err)
			}
		} else {
			s.handleError(err)
		}
	}
}

func (s *Stream) sub(channel string) (err error) {
	s.Lock()
	defer s.Unlock()

	subReq := PolygonClientMsg{
		Action: "subscribe",
		Params: channel,
	}

	if err = s.conn.WriteJSON(subReq); err != nil {
		return
	}

	return
}

func (s *Stream) isAuthenticated() bool {
	return s.authenticated.Load().(bool)
}

func (s *Stream) auth() (err error) {
	s.Lock()
	defer s.Unlock()

	if s.isAuthenticated() {
		return
	}

	authRequest := PolygonClientMsg{
		Action: "auth",
		Params: common.Credentials().ID,
	}

	if err = s.conn.WriteJSON(authRequest); err != nil {
		return
	}

	msg := PolygonAuthMsg{}

	// ensure the auth response comes in a timely manner
	s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer s.conn.SetReadDeadline(time.Time{})

	if err = s.conn.ReadJSON(&msg); err != nil {
		return
	}

	if !strings.EqualFold(msg.Status, "success") {
		return fmt.Errorf("failed to authorize alpaca stream")
	}

	return
}

// GetStream returns the singleton Polygon stream structure.
func GetStream() *Stream {
	once.Do(func() {
		str = &Stream{
			authenticated: atomic.Value{},
			handlers:      sync.Map{},
		}

		str.authenticated.Store(false)
		str.closed.Store(false)
	})

	return str
}

func openSocket() *websocket.Conn {
	scheme := "wss"
	polygonStreamEndpoint, ok := os.LookupEnv("POLYGON_WS_URL")
	if !ok {
		polygonStreamEndpoint = "alpaca.socket.polygon.io"
	}
	ub, _ := url.Parse(polygonStreamEndpoint)
	if ub.Scheme == "http" {
		scheme = "ws"
	}
	u := url.URL{Scheme: scheme, Host: ub.Host, Path: "/stocks"}
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		panic(err)
	}
	return c
}
