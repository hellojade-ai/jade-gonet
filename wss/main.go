package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// =============================================================================
// 1. Echo — Basic request/response over WebSocket
// =============================================================================

func handleEcho(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("echo upgrade error: %v", err)
		return
	}
	defer conn.Close()

	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			break
		}
		response := fmt.Sprintf("[echo] %s", message)
		if err := conn.WriteMessage(mt, []byte(response)); err != nil {
			break
		}
	}
}

// =============================================================================
// 2. Broadcast — One-to-many: every connected client receives every message
// =============================================================================

type BroadcastHub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
}

var broadcast = &BroadcastHub{clients: make(map[*websocket.Conn]bool)}

func (h *BroadcastHub) add(conn *websocket.Conn) {
	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()
}

func (h *BroadcastHub) remove(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
}

func (h *BroadcastHub) send(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for conn := range h.clients {
		conn.WriteMessage(websocket.TextMessage, msg)
	}
}

func handleBroadcast(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		broadcast.remove(conn)
		conn.Close()
	}()

	broadcast.add(conn)
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}
		broadcast.send([]byte(fmt.Sprintf("[broadcast] %s", message)))
	}
}

// =============================================================================
// 3. Pub/Sub — Topic-based channels: clients subscribe and publish to topics
// =============================================================================

type PubSubHub struct {
	mu     sync.RWMutex
	topics map[string]map[*websocket.Conn]bool
}

var pubsub = &PubSubHub{topics: make(map[string]map[*websocket.Conn]bool)}

func (ps *PubSubHub) subscribe(topic string, conn *websocket.Conn) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.topics[topic] == nil {
		ps.topics[topic] = make(map[*websocket.Conn]bool)
	}
	ps.topics[topic][conn] = true
}

func (ps *PubSubHub) unsubscribeAll(conn *websocket.Conn) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for topic, clients := range ps.topics {
		delete(clients, conn)
		if len(clients) == 0 {
			delete(ps.topics, topic)
		}
	}
}

func (ps *PubSubHub) publish(topic string, msg []byte) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for conn := range ps.topics[topic] {
		conn.WriteMessage(websocket.TextMessage, msg)
	}
}

type PubSubMessage struct {
	Action  string `json:"action"`  // "subscribe", "publish"
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
}

func handlePubSub(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		pubsub.unsubscribeAll(conn)
		conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg PubSubMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"invalid json, expected {action, topic, payload}"}`))
			continue
		}
		switch msg.Action {
		case "subscribe":
			pubsub.subscribe(msg.Topic, conn)
			conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"subscribed":"%s"}`, msg.Topic)))
		case "publish":
			pubsub.publish(msg.Topic, []byte(fmt.Sprintf(`{"topic":"%s","payload":"%s"}`, msg.Topic, msg.Payload)))
		default:
			conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"action must be subscribe or publish"}`))
		}
	}
}

// =============================================================================
// 4. Stream — Server continuously pushes data to the client (e.g. metrics)
// =============================================================================

func handleStream(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	seq := 0
	for {
		select {
		case <-done:
			return
		case t := <-ticker.C:
			seq++
			msg := fmt.Sprintf(`{"seq":%d,"timestamp":"%s","metric":%.2f}`, seq, t.Format(time.RFC3339), float64(seq)*1.5)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				return
			}
		}
	}
}

// =============================================================================
// 5. Chat — Multi-room chat with named users
// =============================================================================

type ChatHub struct {
	mu    sync.RWMutex
	rooms map[string]map[*websocket.Conn]string // room -> conn -> username
}

var chat = &ChatHub{rooms: make(map[string]map[*websocket.Conn]string)}

type ChatMessage struct {
	Action   string `json:"action"`   // "join", "message", "leave"
	Room     string `json:"room"`
	Username string `json:"username"`
	Text     string `json:"text"`
}

func (ch *ChatHub) join(room, username string, conn *websocket.Conn) {
	ch.mu.Lock()
	if ch.rooms[room] == nil {
		ch.rooms[room] = make(map[*websocket.Conn]string)
	}
	ch.rooms[room][conn] = username
	ch.mu.Unlock()
	ch.broadcastToRoom(room, fmt.Sprintf(`{"event":"joined","room":"%s","user":"%s"}`, room, username))
}

func (ch *ChatHub) leave(conn *websocket.Conn) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	for room, members := range ch.rooms {
		if username, ok := members[conn]; ok {
			delete(members, conn)
			if len(members) == 0 {
				delete(ch.rooms, room)
			} else {
				for c := range members {
					c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"event":"left","room":"%s","user":"%s"}`, room, username)))
				}
			}
		}
	}
}

func (ch *ChatHub) broadcastToRoom(room string, msg string) {
	ch.mu.RLock()
	defer ch.mu.RUnlock()
	for conn := range ch.rooms[room] {
		conn.WriteMessage(websocket.TextMessage, []byte(msg))
	}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() {
		chat.leave(conn)
		conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg ChatMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"invalid json"}`))
			continue
		}
		switch msg.Action {
		case "join":
			chat.join(msg.Room, msg.Username, conn)
		case "message":
			chat.broadcastToRoom(msg.Room, fmt.Sprintf(`{"event":"message","room":"%s","user":"%s","text":"%s"}`, msg.Room, msg.Username, msg.Text))
		}
	}
}

// =============================================================================
// 6. Jade Chat — AI chat relay: validates token, streams from Jade API
// =============================================================================

type JadeChatRequest struct {
	Action   string            `json:"action"`
	Token    string            `json:"token"`
	Messages []JadeChatMessage `json:"messages"`
}

type JadeChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type JadeChatResponse struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	nestjsBaseURL = getEnv("NESTJS_BASE_URL", "http://localhost:3000/api")
	vllmBaseURL   = getEnv("VLLM_BASE_URL", "https://vllm.hellojade.ai")
	vllmModel     = getEnv("VLLM_MODEL", "gemma-3-27b-it")
)

// OpenAI streaming response types
type OpenAIChatRequest struct {
	Model     string            `json:"model"`
	Messages  []JadeChatMessage `json:"messages"`
	Stream    bool              `json:"stream"`
	MaxTokens int               `json:"max_tokens"`
}

type OpenAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func sendJadeEvent(conn *websocket.Conn, event string, data interface{}) {
	payload, _ := json.Marshal(data)
	msg := JadeChatResponse{Event: event, Data: payload}
	raw, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, raw)
}

func sendJadeError(conn *websocket.Conn, message string) {
	sendJadeEvent(conn, "error", map[string]string{"message": message})
}

func handleJadeChat(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("jade-chat upgrade error: %v", err)
		return
	}
	defer conn.Close()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var req JadeChatRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			sendJadeError(conn, "invalid JSON")
			continue
		}

		if req.Action != "chat" {
			sendJadeError(conn, "unknown action, expected 'chat'")
			continue
		}

		if req.Token == "" {
			sendJadeError(conn, "token is required")
			continue
		}

		// Validate stream token with NestJS
		validResp, err := http.Get(fmt.Sprintf("%s/conversations/validate-token/%s", nestjsBaseURL, req.Token))
		if err != nil {
			sendJadeError(conn, "token validation failed: service unreachable")
			continue
		}
		validResp.Body.Close()

		if validResp.StatusCode != 200 {
			sendJadeError(conn, "invalid or expired token")
			continue
		}

		// Build OpenAI chat completion request with full message history
		openaiReq := OpenAIChatRequest{
			Model:     vllmModel,
			Messages:  req.Messages,
			Stream:    true,
			MaxTokens: 1024,
		}
		reqBody, _ := json.Marshal(openaiReq)

		vllmResp, err := http.Post(
			fmt.Sprintf("%s/v1/chat/completions", vllmBaseURL),
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			sendJadeError(conn, "AI service unreachable")
			continue
		}

		// Stream OpenAI SSE response back as WebSocket frames
		fullContent, tokenCount := streamOpenAIResponse(conn, vllmResp)
		vllmResp.Body.Close()

		if fullContent != "" {
			sendJadeEvent(conn, "done", map[string]interface{}{
				"content":    fullContent,
				"tokenCount": tokenCount,
			})
		}
	}
}

// streamOpenAIResponse reads OpenAI SSE events and relays tokens via WebSocket.
func streamOpenAIResponse(conn *websocket.Conn, resp *http.Response) (string, int) {
	scanner := bufio.NewScanner(resp.Body)
	var fullContent strings.Builder
	tokenCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := line[6:]

		// OpenAI signals end of stream with [DONE]
		if data == "[DONE]" {
			break
		}

		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
			if content != "" {
				fullContent.WriteString(content)
				sendJadeEvent(conn, "token", content)
			}

			if chunk.Choices[0].FinishReason != nil {
				break
			}
		}

		if chunk.Usage != nil {
			tokenCount = chunk.Usage.CompletionTokens
		}
	}

	// Fallback token count if not provided by API
	if tokenCount == 0 {
		tokenCount = len(strings.Fields(fullContent.String()))
	}

	return fullContent.String(), tokenCount
}

// =============================================================================
// Main
// =============================================================================

func main() {
	http.HandleFunc("/ws/echo", handleEcho)
	http.HandleFunc("/ws/broadcast", handleBroadcast)
	http.HandleFunc("/ws/pubsub", handlePubSub)
	http.HandleFunc("/ws/stream", handleStream)
	http.HandleFunc("/ws/chat", handleChat)
	http.HandleFunc("/ws/jade-chat", handleJadeChat)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"wss","endpoints":["/ws/echo","/ws/broadcast","/ws/pubsub","/ws/stream","/ws/chat","/ws/jade-chat"]}`))
	})

	log.Println("WSS server starting on :8000")
	log.Fatal(http.ListenAndServe(":8000", nil))
}
