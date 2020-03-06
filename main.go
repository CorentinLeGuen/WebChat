package main

import (
	"bytes"
	"flag"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"time"
)

type Server struct {
	clients     map[*Client]bool
	broadcast   chan []byte
	subscribe   chan *Client
	unsubscribe chan *Client
}

type Client struct {
	server *Server
	conn   *websocket.Conn
	address string
	send   chan []byte
}

func newServer() *Server {
	return &Server{
		clients:     make(map[*Client]bool),
		broadcast:   make(chan []byte),
		subscribe:   make(chan *Client),
		unsubscribe: make(chan *Client),
	}
}

func (s *Server) run() {
	for {
		select {
		case client := <-s.subscribe:
			s.clients[client] = true
		case client := <-s.unsubscribe:
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				close(client.send)
			}
		case message := <-s.broadcast:
			for client := range s.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(s.clients, client)
				}
			}
		}
	}
}

var serverConf = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

func (c *Client) readMessages() {
	defer func() {
		c.server.unsubscribe <- c
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		message = []byte(c.address + " :\t" + string(message))
		message = bytes.TrimSpace(bytes.Replace(message, []byte{'\n'}, []byte{' '}, -1))
		c.server.broadcast <- message
	}
}

func (c *Client) sendMessages() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			_, _ = w.Write(message)

			n := len(c.send)
			for i := 0; i < n; i++ {
				_, _ = w.Write([]byte{'\n'})
				_, _ = w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func serveWs(server *Server, w http.ResponseWriter, r *http.Request) {
	conn, err := serverConf.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{server: server, conn: conn, address: r.RemoteAddr, send: make(chan []byte, 256)}
	client.server.subscribe <- client

	server.broadcast <- []byte("Server message :\t" + client.address + " just joigned.")

	go client.sendMessages()
	go client.readMessages()
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	if r.URL.Path != "/" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.ServeFile(w, r, "index.html")
}

func main() {
	flag.Parse()
	server := newServer()
	go server.run()
	http.HandleFunc("/", serveHome)
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(server, w, r)
	})
	err := http.ListenAndServe(*flag.String("addr", ":80", "http service address"), nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
