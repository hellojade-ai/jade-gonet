package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// =============================================================================
// 1. REST JSON — Standard JSON request/response API
//    The most common HTTP API pattern. Accepts JSON body, returns JSON.
//    Use case: CRUD APIs, microservice communication, mobile backends.
// =============================================================================

type Item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Data string `json:"data"`
}

var store = map[string]Item{
	"1": {ID: "1", Name: "alpha", Data: "first item"},
	"2": {ID: "2", Name: "beta", Data: "second item"},
	"3": {ID: "3", Name: "gamma", Data: "third item"},
}

func handleJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if id != "" {
			item, ok := store[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
				return
			}
			json.NewEncoder(w).Encode(item)
			return
		}
		items := make([]Item, 0, len(store))
		for _, item := range store {
			items = append(items, item)
		}
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		var item Item
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if item.ID == "" {
			item.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		store[item.ID] = item
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(item)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
	}
}

// =============================================================================
// 2. Server-Sent Events (SSE) — Streaming push from server to client
//    Server holds the connection open and pushes events as they happen.
//    Use case: live dashboards, notifications, real-time feeds.
// =============================================================================

func handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	seq := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case t := <-ticker.C:
			seq++
			fmt.Fprintf(w, "id: %d\n", seq)
			fmt.Fprintf(w, "event: tick\n")
			fmt.Fprintf(w, "data: {\"seq\":%d,\"time\":\"%s\"}\n\n", seq, t.Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

// =============================================================================
// 3. Chunked Streaming — Progressive response delivery
//    Server sends response in chunks as data becomes available.
//    Use case: large file generation, AI text streaming, log tailing.
// =============================================================================

func handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Transfer-Encoding", "chunked")

	steps := []string{
		"initializing pipeline",
		"loading model weights",
		"tokenizing input",
		"running inference",
		"decoding output",
		"formatting response",
		"complete",
	}

	for i, step := range steps {
		chunk := map[string]interface{}{
			"step":     i + 1,
			"total":    len(steps),
			"status":   step,
			"progress": float64(i+1) / float64(len(steps)) * 100,
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "%s\n", data)
		flusher.Flush()
		if i < len(steps)-1 {
			time.Sleep(800 * time.Millisecond)
		}
	}
}

// =============================================================================
// 4. Multipart Upload — File and form data handling
//    Accept file uploads with associated metadata via multipart/form-data.
//    Use case: file uploads, image processing, document ingestion.
// =============================================================================

func handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST only"})
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB max
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to parse multipart: " + err.Error()})
		return
	}

	results := []map[string]interface{}{}

	for key, files := range r.MultipartForm.File {
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			size, _ := io.Copy(io.Discard, f)
			f.Close()

			results = append(results, map[string]interface{}{
				"field":    key,
				"filename": fh.Filename,
				"size":     size,
				"type":     fh.Header.Get("Content-Type"),
			})
		}
	}

	metadata := map[string]string{}
	for key, values := range r.MultipartForm.Value {
		if len(values) > 0 {
			metadata[key] = values[0]
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"files":    results,
		"metadata": metadata,
		"status":   "uploaded",
	})
}

// =============================================================================
// Main
// =============================================================================

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/json", handleJSON)
	mux.HandleFunc("/api/sse", handleSSE)
	mux.HandleFunc("/api/stream", handleStream)
	mux.HandleFunc("/api/upload", handleUpload)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"http","endpoints":["/api/json","/api/sse","/api/stream","/api/upload"]}`))
	})

	log.Println("HTTP server starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
